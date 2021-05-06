package main

import (
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/autoscaling/autoscalingiface"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
)

const (
	healthy = "Healthy"
)

// adjust runs a single adjustment in the loop to update an ASG in a rolling fashion to latest launch config
func adjust(kubernetesEnabled bool, asgList []string, ec2Svc ec2iface.EC2API, asgSvc autoscalingiface.AutoScalingAPI, readinessHandler readiness, originalDesired map[string]int64, storeOriginalDesiredOnTag, canIncreaseMax, verbose, drain, drainForce bool) error {
	// get information on all of the groups
	asgs, err := awsDescribeGroups(asgSvc, asgList)
	if err != nil {
		return fmt.Errorf("Unexpected error describing ASGs, skipping: %v", err)
	}

	// look up and record original desired values
	err = populateOriginalDesired(originalDesired, asgs, asgSvc, storeOriginalDesiredOnTag, verbose)
	if err != nil {
		return fmt.Errorf("unexpected error looking up original desired values for ASGs, skipping: %v", err)
	}

	asgMap := map[string]*autoscaling.Group{}
	// get information on all of the ec2 instances
	instances := make([]*autoscaling.Instance, 0)
	for _, asg := range asgs {
		oldInstances, newInstances, err := groupInstances(asg, ec2Svc, verbose)
		if err != nil {
			return fmt.Errorf("unable to group instances into new and old: %v", err)
		}
		// if there are no outdated instances skip updating
		if len(oldInstances) == 0 && *asg.DesiredCapacity == originalDesired[*asg.AutoScalingGroupName] {
			log.Printf("[%s] ok\n", *asg.AutoScalingGroupName)
			err := ensureNoScaleDownDisabledAnnotation(kubernetesEnabled, ec2Svc, mapInstancesIds(asg.Instances))
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
		return fmt.Errorf("unable to get aws hostnames for ids %v: %v", ids, err)
	}
	hostnameMap := map[string]string{}
	for i, id := range ids {
		hostnameMap[id] = hostnames[i]
	}
	newDesired := map[string]int64{}
	newTerminate := map[string]string{}

	// keep keyed references to the ASGs
	for _, asg := range asgMap {
		newDesiredA, terminateID, err := calculateAdjustment(kubernetesEnabled, asg, ec2Svc, hostnameMap, readinessHandler, originalDesired[*asg.AutoScalingGroupName], verbose, drain, drainForce)
		log.Printf("[%v] desired: %d original: %d", p2v(asg.AutoScalingGroupName), newDesiredA, originalDesired[*asg.AutoScalingGroupName])
		if err != nil {
			log.Printf("[%v] error calculating adjustment - skipping: %v\n", p2v(asg.AutoScalingGroupName), err)
			continue
		}
		if newDesiredA != *asg.DesiredCapacity {
			newDesired[*asg.AutoScalingGroupName] = newDesiredA
		}
		if terminateID != "" {
			log.Printf("[%v] scheduled termination: %s", asg.AutoScalingGroupName, terminateID)
			newTerminate[*asg.AutoScalingGroupName] = terminateID
		}
	}
	// adjust current desired
	for asg, desired := range newDesired {
		log.Printf("[%s] set desired instances: %d\n", asg, desired)
		err = setAsgDesired(asgSvc, asgMap[asg], desired, canIncreaseMax, verbose)
		if err != nil {
			return fmt.Errorf("[%s] error setting desired to %d: %v", asg, desired, err)
		}
	}
	// terminate nodes
	for asg, id := range newTerminate {
		log.Printf("[%s] terminating node: %s\n", asg, id)
		// all new config instances are ready, terminate an old one
		err = awsTerminateNode(asgSvc, id)
		if err != nil {
			return fmt.Errorf("[%s] error terminating node %s: %v", asg, id, err)
		}
	}
	return nil
}

// ensureNoScaleDownDisabledAnnotation remove any "cluster-autoscaler.kubernetes.io/scale-down-disabled"
// annotations in the nodes as no update is required anymore.
func ensureNoScaleDownDisabledAnnotation(kubernetesEnabled bool, ec2Svc ec2iface.EC2API, ids []string) error {
	hostnames, err := awsGetHostnames(ec2Svc, ids)
	if err != nil {
		return fmt.Errorf("unable to get aws hostnames for ids %v: %v", ids, err)
	}
	return removeScaleDownDisabledAnnotation(kubernetesEnabled, hostnames)
}

// calculateAdjustment calculates the new settings for the desired number, and which node (if any) to terminate
// this makes no actual adjustment, only calculates what new settings should be
// returns:
//   what the new desired number of instances should be
//   ID of an instance to terminate, "" if none
//   error
func calculateAdjustment(kubernetesEnabled bool, asg *autoscaling.Group, ec2Svc ec2iface.EC2API, hostnameMap map[string]string, readinessHandler readiness, originalDesired int64, verbose, drain, drainForce bool) (int64, string, error) {
	desired := *asg.DesiredCapacity

	// get instances with old launch config
	oldInstances, newInstances, err := groupInstances(asg, ec2Svc, verbose)
	if err != nil {
		return originalDesired, "", fmt.Errorf("unable to group instances into new and old: %v", err)
	}

	// Possibilities:
	// 1- we have some old ones, but have not started updates yet: set the desired, increment and loop
	// 2- we have no old ones: we must be at end or have no work to do, so finish
	// 3- we have some old ones, but have started updates: run the updates
	if len(oldInstances) == 0 {
		// we are done
		if verbose && desired != originalDesired {
			log.Printf("[%v] returning desired to original value %d", p2v(asg.AutoScalingGroupName), originalDesired)
		}
		return originalDesired, "", nil
	}
	if originalDesired == desired {
		// we have not started updates; raise the desired count
		return originalDesired + 1, "", nil
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
		return desired, "", nil
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
		return desired, "", nil
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
		_, err = setScaleDownDisabledAnnotation(kubernetesEnabled, hostnames)
		if err != nil {
			log.Printf("Unable to set disabled scale down annotations: %v", err)
		}
		unReadyCount, err = readinessHandler.getUnreadyCount(hostnames, ids)
		if err != nil {
			return desired, "", fmt.Errorf("error getting readiness new node status: %v", err)
		}
		if unReadyCount > 0 {
			log.Printf("[%v] Nodes not ready: %d", p2v(asg.AutoScalingGroupName), unReadyCount)
			return desired, "", nil
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
		err = readinessHandler.prepareTermination([]string{hostname}, []string{candidate}, drain, drainForce)
		if err != nil {
			return desired, "", fmt.Errorf("unexpected error readiness handler terminating node %s: %v", hostname, err)
		}
	}

	// all new config instances are ready, terminate an old one
	return desired, candidate, nil
}

// groupInstances handles all of the logic for determining which nodes in the ASG have an old or outdated
// config, and which are up to date. It should do nothing else.
// The entire rest of the code should rely on this for making the determination
func groupInstances(asg *autoscaling.Group, ec2Svc ec2iface.EC2API, verbose bool) ([]*autoscaling.Instance, []*autoscaling.Instance, error) {
	oldInstances := make([]*autoscaling.Instance, 0)
	newInstances := make([]*autoscaling.Instance, 0)
	// we want to be able to handle LaunchTemplate as well
	targetLc := asg.LaunchConfigurationName
	targetLt := asg.LaunchTemplate
	// check for mixed instance policy
	if targetLt == nil && asg.MixedInstancesPolicy != nil && asg.MixedInstancesPolicy.LaunchTemplate != nil {
		if verbose {
			log.Printf("[%v] using mixed instances policy launch template", p2v(asg.AutoScalingGroupName))
		}
		targetLt = asg.MixedInstancesPolicy.LaunchTemplate.LaunchTemplateSpecification
	}
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
				return nil, nil, fmt.Errorf("[%v] error retrieving information about launch template ID %v: %v", p2v(asg.AutoScalingGroupName), p2v(targetLt.LaunchTemplateId), err)
			}
		case targetLt.LaunchTemplateName != nil && *targetLt.LaunchTemplateName != "":
			if targetTemplate, err = awsGetLaunchTemplateByName(ec2Svc, *targetLt.LaunchTemplateName); err != nil {
				return nil, nil, fmt.Errorf("[%v] error retrieving information about launch template name %v: %v", p2v(asg.AutoScalingGroupName), p2v(targetLt.LaunchTemplateName), err)
			}
		default:
			return nil, nil, fmt.Errorf("AutoScaling Group %s had invalid Launch Template", *asg.AutoScalingGroupName)
		}
		// extra safety check
		if targetTemplate == nil {
			return nil, nil, fmt.Errorf("no template found")
		}
		if verbose {
			log.Printf("Grouping instances for ASG named %v with target template name %v, id %v, latest version %v and default version %v", p2v(asg.AutoScalingGroupName), p2v(targetTemplate.LaunchTemplateName), p2v(targetTemplate.LaunchTemplateId), p2v(targetTemplate.LatestVersionNumber), p2v(targetTemplate.DefaultVersionNumber))
		}
		// now we can loop through each node and compare
		for _, i := range asg.Instances {
			switch {
			case i.LaunchTemplate == nil:
				if verbose {
					log.Printf("[%v] adding %v to list of old instances because it does not have a launch template", p2v(asg.AutoScalingGroupName), p2v(i.InstanceId))
				}
				// has no launch template at all
				oldInstances = append(oldInstances, i)
			case aws.StringValue(i.LaunchTemplate.LaunchTemplateName) != aws.StringValue(targetLt.LaunchTemplateName):
				// mismatched name
				if verbose {
					log.Printf("[%v] adding %v to list of old instances because its name is %v and the target template's name is %v", p2v(asg.AutoScalingGroupName), p2v(i.InstanceId), p2v(i.LaunchTemplate.LaunchTemplateName), p2v(targetLt.LaunchTemplateName))
				}
				oldInstances = append(oldInstances, i)
			case aws.StringValue(i.LaunchTemplate.LaunchTemplateId) != aws.StringValue(targetLt.LaunchTemplateId):
				// mismatched ID
				if verbose {
					log.Printf("[%v] adding %v to list of old instances because its template id is %v and the target template's id is %v", p2v(asg.AutoScalingGroupName), p2v(i.InstanceId), p2v(i.LaunchTemplate.LaunchTemplateId), p2v(targetLt.LaunchTemplateId))
				}
				oldInstances = append(oldInstances, i)
			// name and id match, just need to check versions
			case !compareLaunchTemplateVersions(targetTemplate, targetLt, i.LaunchTemplate):
				if verbose {
					log.Printf("[%v] adding %v to list of old instances because the launch template versions do not match (%v!=%v)", p2v(asg.AutoScalingGroupName), p2v(i.InstanceId), p2v(i.LaunchTemplate.Version), p2v(targetLt.Version))
				}
				oldInstances = append(oldInstances, i)
			default:
				if verbose {
					log.Printf("[%v] adding %v to list of new instances because the instance matches the launch template with id %v", p2v(asg.AutoScalingGroupName), p2v(i.InstanceId), p2v(targetLt.LaunchTemplateId))
				}
				newInstances = append(newInstances, i)
			}
		}
	} else if targetLc != nil {
		// go through each instance and find those that are not with the target LC
		for _, i := range asg.Instances {
			if i.LaunchConfigurationName != nil && *i.LaunchConfigurationName == *targetLc {
				newInstances = append(newInstances, i)
			} else {
				if verbose {
					log.Printf("[%v] adding %v to list of old instances because the launch configuration names do not match (%v!=%v)", p2v(asg.AutoScalingGroupName), p2v(i.InstanceId), p2v(i.LaunchConfigurationName), p2v(targetLc))
				}
				oldInstances = append(oldInstances, i)
			}
		}
	} else {
		return nil, nil, fmt.Errorf("[%v] both target launch configuration and launch template are nil", p2v(asg.AutoScalingGroupName))
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
		lt1version = fmt.Sprintf("%d", *targetTemplate.DefaultVersionNumber)
	case "$Latest":
		lt1version = fmt.Sprintf("%d", *targetTemplate.LatestVersionNumber)
	default:
		lt1version = *lt1.Version
	}
	switch *lt2.Version {
	case "$Default":
		lt2version = fmt.Sprintf("%d", *targetTemplate.DefaultVersionNumber)
	case "$Latest":
		lt2version = fmt.Sprintf("%d", *targetTemplate.LatestVersionNumber)
	default:
		lt2version = *lt2.Version
	}
	return lt1version == lt2version
}
