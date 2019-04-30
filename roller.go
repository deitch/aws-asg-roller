package main

import (
	"fmt"

	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/autoscaling/autoscalingiface"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
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
	for _, a := range asgs {
		asgMap[*a.AutoScalingGroupName] = a
	}
	// get information on all of the ec2 instances
	instances := make([]*autoscaling.Instance, 0)
	for _, asg := range asgs {
		oldI, newI := groupInstances(asg)
		instances = append(instances, oldI...)
		instances = append(instances, newI...)
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
	for _, asg := range asgs {
		newDesiredA, newOriginalA, terminateID, err := calculateAdjustment(asg, hostnameMap, readinessHandler, originalDesired[*asg.AutoScalingGroupName])
		newDesired[*asg.AutoScalingGroupName] = newDesiredA
		newOriginalDesired[*asg.AutoScalingGroupName] = newOriginalA
		if terminateID != "" {
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
		err = setAsgDesired(asgSvc, asgMap[asg], desired)
		if err != nil {
			return fmt.Errorf("Error setting desired to %d for ASG %s: %v", desired, asg, err)
		}
	}
	// terminate nodes
	for asg, id := range newTerminate {
		// all new config instances are ready, terminate an old one
		err = awsTerminateNode(asgSvc, id)
		if err != nil {
			return fmt.Errorf("Error terminating node %s in ASG %s: %v", id, asg, err)
		}
	}
	return nil
}

// calculateAdjustment calculates the new settings for the desired number, and which node (if any) to terminate
// this makes no actual adjustment, only calculates what new settings should be
// returns:
//   what the new desired number of instances should be
//   what the new original desired should be, primarily if it should be reset
//   ID of an instance to terminate, "" if none
//   error
func calculateAdjustment(asg *autoscaling.Group, hostnameMap map[string]string, readinessHandler readiness, originalDesired int64) (int64, int64, string, error) {
	desired := *asg.DesiredCapacity

	// get instances with old launch config
	oldInstances, newInstances := groupInstances(asg)

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
		unReadyCount, err = readinessHandler.getUnreadyCount(hostnames, ids)
		if err != nil {
			return desired, originalDesired, "", fmt.Errorf("Error getting readiness new node status: %v", err)
		}
		if unReadyCount > 0 {
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

func groupInstances(asg *autoscaling.Group) ([]*autoscaling.Instance, []*autoscaling.Instance) {
	oldInstances := make([]*autoscaling.Instance, 0)
	newInstances := make([]*autoscaling.Instance, 0)
	targetLc := asg.LaunchConfigurationName
	// go through each instance and find those that are not with the target LC
	for _, i := range asg.Instances {
		if i.LaunchConfigurationName != nil && *i.LaunchConfigurationName == *targetLc {
			newInstances = append(newInstances, i)
		} else {
			oldInstances = append(oldInstances, i)
		}
	}
	return oldInstances, newInstances
}

func mapInstancesIds(instances []*autoscaling.Instance) []string {
	ids := make([]string, 0)
	for _, i := range instances {
		ids = append(ids, *i.InstanceId)
	}
	return ids
}
