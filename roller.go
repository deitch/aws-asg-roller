package main

import (
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/autoscaling/autoscalingiface"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	"log"
)

const (
	healthy = "Healthy"
)

// adjust runs a single adjustment in the loop to update an ASG in a rolling fashion to latest launch config
func adjust(asgList []string, ec2Svc ec2iface.EC2API, asgSvc autoscalingiface.AutoScalingAPI, readinessHandler readiness, originalDesired map[string]int64) error {
	// get information on all of the groups
	asgs, err := awsDescribeGroups(asgSvc, asgList)
	if err != nil {
		return fmt.Errorf("Unexpected error describing ASGs, skipping: %v", err)
	}
	asgMap := map[string]*autoscaling.Group{}
	// get information on all of the ec2 instances
	instances := make([]*autoscaling.Instance, 0)
	for _, asg := range asgs {
		oldInstances, newInstances, err := groupInstances(asg, ec2Svc)
		if err != nil {
			return fmt.Errorf("unable to group instances into new and old: %v", err)
		}
		// if there are no outdated instances skip updating
		if len(oldInstances) == 0 {
			log.Printf("[%s] ok\n", *asg.AutoScalingGroupName)
			err := ensureNoScaleDownDisabledAnnotation(ec2Svc, mapInstancesIds(asg.Instances))
			if err != nil {
				log.Printf("[%s] Unable to update node annotations: %v\n", *asg.AutoScalingGroupName, err)
			}
			continue
		}

		log.Printf("[%s] need updates: %d\n", *asg.AutoScalingGroupName, len(oldInstances))

		asgMap[*asg.AutoScalingGroupName] = asg
		instances = append(instances, oldInstances...)
		instances = append(instances, newInstances...)

	}
	// no instances no work needed
	if len(instances) == 0 {
		return nil
	}
	ids := mapInstancesIds(instances)
	hostnames, err := awsGetHostnames(ec2Svc, ids)
	if err != nil {
		return fmt.Errorf("Unable to get aws hostnames for ids %v: %v", ids, err)
	}
	hostnameMap := map[string]string{}
	for i, id := range ids {
		hostnameMap[id] = hostnames[i]
	}
	newDesired := map[string]int64{}
	newTerminate := map[string]string{}
	newOriginalDesired := map[string]int64{}
	errors := map[*string]error{}

	// keep keyed references to the ASGs
	for _, asg := range asgMap {
		newDesiredA, newOriginalA, terminateID, err := calculateAdjustment(asg, ec2Svc, hostnameMap, readinessHandler, originalDesired[*asg.AutoScalingGroupName])
		log.Printf("[%s] desired: %d original: %d", *asg.AutoScalingGroupName, newDesiredA, newOriginalA)
		if err != nil {
			log.Printf("[%s] error: %v\n", *asg.AutoScalingGroupName, err)
		}
		newDesired[*asg.AutoScalingGroupName] = newDesiredA
		newOriginalDesired[*asg.AutoScalingGroupName] = newOriginalA
		if terminateID != "" {
			log.Printf("[%s] Scheduled termination: %s", *asg.AutoScalingGroupName, terminateID)
			newTerminate[*asg.AutoScalingGroupName] = terminateID
		}
		errors[asg.AutoScalingGroupName] = err
	}
	// adjust original desired
	for asg, desired := range newOriginalDesired {
		originalDesired[asg] = desired
	}
	// adjust current desired
	for asg, desired := range newDesired {
		log.Printf("[%s] set desired instances: %d\n", asg, desired)
		err = setAsgDesired(asgSvc, asgMap[asg], desired)
		if err != nil {
			return fmt.Errorf("Error setting desired to %d for ASG %s: %v", desired, asg, err)
		}
	}
	// terminate nodes
	for asg, id := range newTerminate {
		log.Printf("[%s] terminating node: %s\n", asg, id)
		// all new config instances are ready, terminate an old one
		err = awsTerminateNode(asgSvc, id)
		if err != nil {
			return fmt.Errorf("Error terminating node %s in ASG %s: %v", id, asg, err)
		}
	}
	return nil
}

// ensureNoScaleDownDisabledAnnotation remove any "cluster-autoscaler.kubernetes.io/scale-down-disabled"
// annotations in the nodes as no update is required anymore.
func ensureNoScaleDownDisabledAnnotation(ec2Svc ec2iface.EC2API, ids []string) error {
	hostnames, err := awsGetHostnames(ec2Svc, ids)
	if err != nil {
		return fmt.Errorf("Unable to get aws hostnames for ids %v: %v", ids, err)
	}
	return removeScaleDownDisabledAnnotation(hostnames)
}

// calculateAdjustment calculates the new settings for the desired number, and which node (if any) to terminate
// this makes no actual adjustment, only calculates what new settings should be
// returns:
//   what the new desired number of instances should be
//   what the new original desired should be, primarily if it should be reset
//   ID of an instance to terminate, "" if none
//   error
func calculateAdjustment(asg *autoscaling.Group, ec2Svc ec2iface.EC2API, hostnameMap map[string]string, readinessHandler readiness, originalDesired int64) (int64, int64, string, error) {
	desired := *asg.DesiredCapacity

	// get instances with old launch config
	oldInstances, newInstances, err := groupInstances(asg, ec2Svc)
	if err != nil {
		return originalDesired, 0, "", fmt.Errorf("unable to group instances into new and old: %v", err)
	}

	// Possibilities:
	// 1- we have some old ones, but have not started updates yet: set the desired, increment and loop
	// 2- we have no old ones, but have started updates: we must be at end, so finish
	// 3- we have some old ones, but have started updates: run the updates
	if len(oldInstances) == 0 {
		if originalDesired > 0 {
			return originalDesired, 0, "", nil
		}
	}
	if originalDesired == 0 {
		return desired + 1, desired, "", nil
	}

	// how we determine if we can terminate one
	// we already know we have increased desired capacity
	// check if:
	// a- actual instance count matches our new desired
	// b- all new config instances are in valid state
	// if yes, terminate one old one
	// if not, loop around again - eventually it will be

	// do we have at least one more more ready instances than the original desired? if not, loop again until we do
	readyCount := 0
	for _, i := range asg.Instances {
		if *i.HealthStatus == healthy {
			readyCount++
		}

	}
	if int64(readyCount) < originalDesired+1 {
		return desired, originalDesired, "", nil
	}
	// are any of the updated config instances not ready?
	unReadyCount := 0
	// should check if new node *really* is ready to function
	for _, i := range newInstances {
		if *i.HealthStatus != healthy {
			unReadyCount++
		}
	}
	if unReadyCount > 0 {
		return desired, originalDesired, "", nil
	}
	// do we have additional requirements for readiness?
	if readinessHandler != nil {
		var (
			hostnames []string
			err       error
		)
		// check if the new nodes all are in ready state
		ids := mapInstancesIds(newInstances)
		hostnames = make([]string, 0)
		for _, i := range ids {
			hostnames = append(hostnames, hostnameMap[i])
		}
		_, err = setScaleDownDisabledAnnotation(hostnames)
		if err != nil {
			log.Printf("Unable to set disabled scale down annotations: %v", err)
		}
		unReadyCount, err = readinessHandler.getUnreadyCount(hostnames, ids)
		if err != nil {
			return desired, originalDesired, "", fmt.Errorf("Error getting readiness new node status: %v", err)
		}
		if unReadyCount > 0 {
			log.Printf("[%s] Nodes not ready: %d", *asg.AutoScalingGroupName, unReadyCount)
			return desired, originalDesired, "", nil
		}
	}
	candidate := *oldInstances[0].InstanceId

	if readinessHandler != nil {
		// get the node reference - first need the hostname
		var (
			hostname string
			err      error
		)
		hostname = hostnameMap[candidate]
		err = readinessHandler.prepareTermination([]string{hostname}, []string{candidate})
		if err != nil {
			return desired, originalDesired, "", fmt.Errorf("Unexpected error readiness handler terminating node %s: %v", hostname, err)
		}
	}

	// all new config instances are ready, terminate an old one
	return desired, originalDesired, candidate, nil
}

// groupInstances handles all of the logic for determining which nodes in the ASG have an old or outdated
// config, and which are up to date. It should to nothing else.
// The entire rest of the code should rely on this for making the determination
func groupInstances(asg *autoscaling.Group, ec2Svc ec2iface.EC2API) ([]*autoscaling.Instance, []*autoscaling.Instance, error) {
	oldInstances := make([]*autoscaling.Instance, 0)
	newInstances := make([]*autoscaling.Instance, 0)
	// we want to be able to handle LaunchTemplate as well
	targetLc := asg.LaunchConfigurationName
	targetLt := asg.LaunchTemplate
	// prioritize LaunchTemplate over LaunchConfiguration
	if targetLt != nil {
		// we are using LaunchTemplate. Unlike LaunchConfiguration, you can have two nodes in the ASG
		//  with the same LT name, same ID but different versions, so need to check version.
		//  they even can have the same version, if the version is `$Latest` or `$Default`, so need
		//  to get actual versions for each
		var (
			targetTemplate *ec2.LaunchTemplate
			err            error
		)
		switch {
		case targetLt.LaunchTemplateId != nil && *targetLt.LaunchTemplateId != "":
			if targetTemplate, err = awsGetLaunchTemplateByID(ec2Svc, *targetLt.LaunchTemplateId); err != nil {
				return nil, nil, fmt.Errorf("error retrieving information about launch template ID %s: %v", *targetLt.LaunchTemplateId, err)
			}
		case targetLt.LaunchTemplateName != nil && *targetLt.LaunchTemplateName != "":
			if targetTemplate, err = awsGetLaunchTemplateByName(ec2Svc, *targetLt.LaunchTemplateName); err != nil {
				return nil, nil, fmt.Errorf("error retrieving information about launch template name %s: %v", *targetLt.LaunchTemplateName, err)
			}
		default:
			return nil, nil, fmt.Errorf("AutoScaling Group %s had invalid Launch Template", *asg.AutoScalingGroupName)
		}
		// extra safety check
		if targetTemplate == nil {
			return nil, nil, fmt.Errorf("no template found")
		}
		if verbose {
			log.Printf("Grouping instances for ASG named %s with target template name %s, id %s, latest version %d and default version %d", *asg.AutoScalingGroupName, *targetTemplate.LaunchTemplateName, *targetTemplate.LaunchTemplateId, *targetTemplate.LatestVersionNumber, *targetTemplate.DefaultVersionNumber)
		}
		// now we can loop through each node and compare
		for _, i := range asg.Instances {
			switch {
			case i.LaunchTemplate == nil:
				if verbose {
					log.Printf("Adding %s to list of old instances because it does not have a launch template", *i.InstanceId)
				}
				// has no launch template at all
				oldInstances = append(oldInstances, i)
			case aws.StringValue(i.LaunchTemplate.LaunchTemplateName) != aws.StringValue(targetLt.LaunchTemplateName):
				// mismatched name
				if verbose {
					log.Printf("Adding %s to list of old instances because its name is %s and the target template's name is %s", *i.InstanceId, *i.LaunchTemplate.LaunchTemplateName, *targetLt.LaunchTemplateName)
				}
				oldInstances = append(oldInstances, i)
			case aws.StringValue(i.LaunchTemplate.LaunchTemplateId) != aws.StringValue(targetLt.LaunchTemplateId):
				// mismatched ID
				if verbose {
					log.Printf("Adding %s to list of old instances because its template id is %s and the target template's id is %s", *i.InstanceId, *i.LaunchTemplate.LaunchTemplateId, *targetLt.LaunchTemplateId)
				}
				oldInstances = append(oldInstances, i)
			// name and id match, just need to check versions
			case !compareLaunchTemplateVersions(targetTemplate, targetLt, i.LaunchTemplate):
				if verbose {
					log.Printf("Adding %s to list of old instances because the launch template versions do not match (%s!=%s)", *i.InstanceId, *i.LaunchTemplate.Version, *targetLt.Version)
				}
				oldInstances = append(oldInstances, i)
			default:
				if verbose {
					log.Printf("Adding %s to list of new instances because the instance matches the launch template with id %s", *i.InstanceId, *targetLt.LaunchTemplateId)
				}
				newInstances = append(newInstances, i)
			}
		}
	} else {
		// go through each instance and find those that are not with the target LC
		for _, i := range asg.Instances {
			if i.LaunchConfigurationName != nil && *i.LaunchConfigurationName == *targetLc {
				newInstances = append(newInstances, i)
			} else {
				oldInstances = append(oldInstances, i)
			}
		}
	}
	return oldInstances, newInstances, nil
}

func mapInstancesIds(instances []*autoscaling.Instance) []string {
	ids := make([]string, 0)
	for _, i := range instances {
		ids = append(ids, *i.InstanceId)
	}
	return ids
}

// compareLaunchTemplateVersions compare two launch template versions and see if they match
// can handle `$Latest` and `$Default` by resolving to the actual version in use
func compareLaunchTemplateVersions(targetTemplate *ec2.LaunchTemplate, lt1, lt2 *autoscaling.LaunchTemplateSpecification) bool {
	// if both versions do not start with `$`, then just compare
	if lt1 == nil && lt2 == nil {
		return true
	}
	if (lt1 == nil && lt2 != nil) || (lt1 != nil && lt2 == nil) {
		return false
	}
	if lt1.Version == nil && lt2.Version == nil {
		return true
	}
	if (lt1.Version == nil && lt2.Version != nil) || (lt1.Version != nil && lt2.Version == nil) {
		return false
	}
	// if either version starts with `$`, then resolve to actual version from LaunchTemplate
	var lt1version, lt2version string
	switch *lt1.Version {
	case "$Default":
		lt1version = fmt.Sprintf("%d", targetTemplate.DefaultVersionNumber)
	case "$Latest":
		lt1version = fmt.Sprintf("%d", targetTemplate.LatestVersionNumber)
	default:
		lt1version = *lt1.Version
	}
	switch *lt2.Version {
	case "$Default":
		lt2version = fmt.Sprintf("%d", targetTemplate.DefaultVersionNumber)
	case "$Latest":
		lt2version = fmt.Sprintf("%d", targetTemplate.LatestVersionNumber)
	default:
		lt2version = *lt2.Version
	}
	return lt1version == lt2version
}
