package main

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
)

func setAsgDesired(svc *autoscaling.AutoScaling, asg *autoscaling.Group, count int64) {
	// increase the desired capacity by 1
	desiredInput := &autoscaling.SetDesiredCapacityInput{
		AutoScalingGroupName: asg.AutoScalingGroupName,
		DesiredCapacity:      aws.Int64(count),
		HonorCooldown:        aws.Bool(true),
	}

	_, err := svc.SetDesiredCapacity(desiredInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case autoscaling.ErrCodeScalingActivityInProgressFault:
				fmt.Println(autoscaling.ErrCodeScalingActivityInProgressFault, aerr.Error())
			case autoscaling.ErrCodeResourceContentionFault:
				fmt.Println(autoscaling.ErrCodeResourceContentionFault, aerr.Error())
			default:
				fmt.Println(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			fmt.Println(err.Error())
		}
		return
	}
}

func awsGetHostname(svc *ec2.EC2, id string) (string, error) {
	hostnames, err := awsGetHostnames(svc, []string{id})
	if err != nil {
		return "", err
	}
	if len(hostnames) < 1 {
		return "", err
	}
	return hostnames[0], nil
}
func awsGetHostnames(svc *ec2.EC2, ids []string) ([]string, error) {
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

func awsDescribeGroups(svc *autoscaling.AutoScaling, names []string) ([]*autoscaling.Group, error) {
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

func awsTerminateNode(svc *autoscaling.AutoScaling, id string) error {
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

func awsGetServices() (*ec2.EC2, *autoscaling.AutoScaling, error) {
	sess, err := session.NewSession()
	if err != nil {
		return nil, nil, err
	}
	asgSvc := autoscaling.New(sess)
	ec2svc := ec2.New(sess)
	return ec2svc, asgSvc, nil
}
