package main

import (
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/autoscaling/autoscalingiface"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	"log"
)

func setAsgDesired(svc autoscalingiface.AutoScalingAPI, asg *autoscaling.Group, count int64, canIncreaseMax, verbose bool) error {
	if count > *asg.MaxSize {
		if canIncreaseMax {
			err := setAsgMax(svc, asg, count, verbose)
			if err != nil {
				return err
			}
		} else {
			return fmt.Errorf("unable to increase ASG %s desired size to %d as greater than max size %d", *asg.AutoScalingGroupName, count, *asg.MaxSize)
		}
	}
	if verbose {
		log.Printf("increasing ASG %s desired count to %d", *asg.AutoScalingGroupName, count)
	}
	desiredInput := &autoscaling.SetDesiredCapacityInput{
		AutoScalingGroupName: asg.AutoScalingGroupName,
		DesiredCapacity:      aws.Int64(count),
		HonorCooldown:        aws.Bool(true),
	}
	_, err := svc.SetDesiredCapacity(desiredInput)
	if err != nil {
		errMsg := fmt.Sprintf("unable to increase ASG %s desired count to %d", *asg.AutoScalingGroupName, count)
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case autoscaling.ErrCodeScalingActivityInProgressFault:
				return fmt.Errorf("%s - %s %v", errMsg, autoscaling.ErrCodeScalingActivityInProgressFault, aerr.Error())
			case autoscaling.ErrCodeResourceContentionFault:
				return fmt.Errorf("%s - %s %v", errMsg, autoscaling.ErrCodeResourceContentionFault, aerr.Error())
			default:
				return fmt.Errorf("%s - unexpected and unknown AWS error: %v", errMsg, aerr.Error())
			}
		}

		return fmt.Errorf("%s - unexpected and unknown non-AWS error: %v", errMsg, err.Error())
	}
	if verbose {
		log.Printf("increased ASG %s desired count to %d", *asg.AutoScalingGroupName, count)
	}
	return nil
}

func setAsgMax(svc autoscalingiface.AutoScalingAPI, asg *autoscaling.Group, count int64, verbose bool) error {
	if verbose {
		log.Printf("increasing ASG %s max size to %d to accommodate desired count", *asg.AutoScalingGroupName, count)
	}
	_, err := svc.UpdateAutoScalingGroup(&autoscaling.UpdateAutoScalingGroupInput{
		AutoScalingGroupName: asg.AutoScalingGroupName,
		MaxSize:              aws.Int64(count),
	})
	if err != nil {
		errMsg := fmt.Sprintf("unable to increase ASG %s max size to %d", *asg.AutoScalingGroupName, count)
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case autoscaling.ErrCodeScalingActivityInProgressFault:
				return fmt.Errorf("%s - %s %v", errMsg, autoscaling.ErrCodeScalingActivityInProgressFault, aerr.Error())
			case autoscaling.ErrCodeResourceContentionFault:
				return fmt.Errorf("%s - %s %v", errMsg, autoscaling.ErrCodeResourceContentionFault, aerr.Error())
			default:
				return fmt.Errorf("%s - unexpected and unknown AWS error: %v", errMsg, aerr.Error())
			}
		}

		return fmt.Errorf("%s - unexpected and unknown non-AWS error: %v", errMsg, err.Error())
	}
	if verbose {
		log.Printf("increased ASG %s max size to %d to accommodate desired count", *asg.AutoScalingGroupName, count)
	}
	return nil
}

func awsGetHostname(svc ec2iface.EC2API, id string) (string, error) {
	hostnames, err := awsGetHostnames(svc, []string{id})
	if err != nil {
		return "", err
	}
	if len(hostnames) < 1 {
		return "", err
	}
	return hostnames[0], nil
}
func awsGetLaunchTemplateByID(svc ec2iface.EC2API, id string) (*ec2.LaunchTemplate, error) {
	input := &ec2.DescribeLaunchTemplatesInput{
		LaunchTemplateIds: []*string{
			aws.String(id),
		},
	}
	return awsGetLaunchTemplate(svc, input)
}
func awsGetLaunchTemplateByName(svc ec2iface.EC2API, name string) (*ec2.LaunchTemplate, error) {
	input := &ec2.DescribeLaunchTemplatesInput{
		LaunchTemplateNames: []*string{
			aws.String(name),
		},
	}
	return awsGetLaunchTemplate(svc, input)
}
func awsGetLaunchTemplate(svc ec2iface.EC2API, input *ec2.DescribeLaunchTemplatesInput) (*ec2.LaunchTemplate, error) {
	templatesOutput, err := svc.DescribeLaunchTemplates(input)
	descriptiveMsg := fmt.Sprintf("%v / %v", input.LaunchTemplateIds, input.LaunchTemplateNames)
	if err != nil {
		return nil, fmt.Errorf("Unable to get description for Launch Template %s: %v", descriptiveMsg, err)
	}
	if len(templatesOutput.LaunchTemplates) < 1 {
		return nil, nil
	}
	return templatesOutput.LaunchTemplates[0], nil
}
func awsGetHostnames(svc ec2iface.EC2API, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}
	ec2input := &ec2.DescribeInstancesInput{
		InstanceIds: aws.StringSlice(ids),
	}
	nodesResult, err := svc.DescribeInstances(ec2input)
	if err != nil {
		return nil, fmt.Errorf("Unable to get description for node %v: %v", ids, err)
	}
	if len(nodesResult.Reservations) < 1 {
		return nil, fmt.Errorf("Did not get any reservations for node %v", ids)
	}
	hostnames := make([]string, 0)
	for _, i := range nodesResult.Reservations {
		for _, j := range i.Instances {
			hostnames = append(hostnames, *j.PrivateDnsName)
		}
	}
	return hostnames, nil
}

func awsDescribeGroups(svc autoscalingiface.AutoScalingAPI, names []string) ([]*autoscaling.Group, error) {
	input := &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: aws.StringSlice(names),
	}
	result, err := svc.DescribeAutoScalingGroups(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case autoscaling.ErrCodeInvalidNextToken:
				return nil, fmt.Errorf("Unexpected AWS NextToken error when doing non-pagination describe")
			case autoscaling.ErrCodeResourceContentionFault:
				return nil, fmt.Errorf("Unexpected AWS ResourceContentionFault when doing describe")
			default:
				return nil, fmt.Errorf("Unexpected and unknown AWS error when doing describe: %v", aerr)
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			return nil, fmt.Errorf("Unexpected and unknown non-AWS error when doing describe: %v", err.Error())
		}
	}
	return result.AutoScalingGroups, nil
}

func awsTerminateNode(svc autoscalingiface.AutoScalingAPI, id string) error {
	input := &autoscaling.TerminateInstanceInAutoScalingGroupInput{
		InstanceId:                     aws.String(id),
		ShouldDecrementDesiredCapacity: aws.Bool(false),
	}

	_, err := svc.TerminateInstanceInAutoScalingGroup(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case autoscaling.ErrCodeScalingActivityInProgressFault:
				return fmt.Errorf("Could not terminate instance, autoscaling already in progress, will try next loop")
			case autoscaling.ErrCodeResourceContentionFault:
				return fmt.Errorf("Could not terminate instance, instance in contention, will try next loop")
			default:
				return fmt.Errorf("Unknown aws error when terminating old instance: %v", aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			return fmt.Errorf("Unknown non-aws error when terminating old instance: %v", err.Error())
		}
	}
	return nil
}

func awsGetServices() (ec2iface.EC2API, autoscalingiface.AutoScalingAPI, error) {
	sess, err := session.NewSession()
	if err != nil {
		return nil, nil, err
	}
	asgSvc := autoscaling.New(sess)
	ec2svc := ec2.New(sess)
	return ec2svc, asgSvc, nil
}
