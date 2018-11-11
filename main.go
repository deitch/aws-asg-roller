package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	drain "github.com/openshift/kubernetes-drain"
)

const (
	asgCheckDelay = 30 // delay between checks of ASG status in seconds
	healthy       = "Healthy"
)

func main() {
	asgList := strings.Split(os.Getenv("ROLLER_ASG"), ",")
	kubeDrain := os.Getenv("ROLLER_KUBERNETES") == "true"
	if asgList == nil || len(asgList) == 0 {
		log.Fatal("Must supply at least one ASG in ROLLER_ASG environment variable")
	}
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	// successful? use kubeDrain
	if err == nil {
		kubeDrain = true
	} else if err != rest.ErrNotInCluster {
		// in cluster, but strange error? fatal
		log.Fatal(err.Error())
	} else if kubeDrain {
		config, err = getKubeOutOfCluster()
		if err != nil {
			log.Fatal(err.Error())
		}
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err.Error())
	}
	fmt.Println(clientset)

	// get the AWS sessionn
	sess, err := session.NewSession()
	if err != nil {
		log.Fatalf("Unable to create an AWS session: %v", err)
	}
	asgSvc := autoscaling.New(sess)
	ec2svc := ec2.New(sess)

	// to keep track of original target sizes during rolling updates
	originalDesired := map[*string]int64{}

	// infinite loop
	for {
		// delay with each loop
		log.Printf("Sleeping %d seconds\n", asgCheckDelay)
		time.Sleep(asgCheckDelay * time.Second)
		input := &autoscaling.DescribeAutoScalingGroupsInput{
			AutoScalingGroupNames: aws.StringSlice(asgList),
		}
		result, err := asgSvc.DescribeAutoScalingGroups(input)
		if err != nil {
			if aerr, ok := err.(awserr.Error); ok {
				switch aerr.Code() {
				case autoscaling.ErrCodeInvalidNextToken:
					log.Print("Unexpected AWS NextToken error when doing non-pagination describe, skipping")
				case autoscaling.ErrCodeResourceContentionFault:
					log.Print("Unexpected AWS ResourceContentionFault when doing describe, skipping")
				default:
					log.Printf("Unexpected and unknown AWS error when doing describe: %v", aerr)
				}
			} else {
				// Print the error, cast err to awserr.Error to get the Code and
				// Message from an error.
				log.Printf("Unexpected and unknown non-AWS error when doing describe: %v", err.Error())
			}
			continue
		}
		groups := map[*string]*autoscaling.Group{}
		// keep keyed references to the ASGs
		for _, asg := range result.AutoScalingGroups {
			groups[asg.AutoScalingGroupName] = asg
		}
		for _, asg := range groups {
			desired := asg.DesiredCapacity
			targetLc := asg.LaunchConfigurationName

			// get instances with old launch config
			oldInstances := make([]*autoscaling.Instance, 0)
			newInstances := make([]*autoscaling.Instance, 0)
			// go through each instance and find those that are not with the target LC
			for _, i := range asg.Instances {
				if i.LaunchConfigurationName == targetLc {
					newInstances = append(newInstances, i)
				} else {
					oldInstances = append(oldInstances, i)
				}
			}
			// Possibilities:
			// 1- we have some old ones, but have not started updates yet: set the desired, increment and loop
			// 2- we have no old ones, but have started updates: we must be at end, so finish
			// 3- we have some old ones, but have started updates: run the updates
			if len(oldInstances) == 0 {
				if originalDesired[asg.AutoScalingGroupName] > 0 {
					setAsgDesired(asgSvc, asg, originalDesired[asg.AutoScalingGroupName])
					originalDesired[asg.AutoScalingGroupName] = 0
				}
				continue
			}
			if originalDesired[asg.AutoScalingGroupName] == 0 {
				setAsgDesired(asgSvc, asg, *desired+1)
				originalDesired[asg.AutoScalingGroupName] = *desired
				continue
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
			if int64(readyCount) < originalDesired[asg.AutoScalingGroupName]+1 {
				continue
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
				continue
			}
			candidate := oldInstances[0].InstanceId
			ec2input := &ec2.DescribeInstancesInput{
				InstanceIds: []*string{candidate},
			}
			nodesResult, err := ec2svc.DescribeInstances(ec2input)
			if err != nil {
				log.Printf("Unable to get description for node %s: %v", *candidate, err)
				continue
			}
			if len(nodesResult.Reservations) < 1 || len(nodesResult.Reservations[0].Instances) < 1 {
				log.Printf("Did not get any reservations for node %s", *candidate)
				continue
			}
			hostname := nodesResult.Reservations[0].Instances[0].PrivateDnsName
			// TODO:
			// should check if old node *really* is ready for termination
			if kubeDrain {
				// get the node reference - first need the hostname
				node, err := clientset.CoreV1().Nodes().Get(*hostname, v1.GetOptions{})
				if err != nil {
					log.Printf("Unexpected error getting kubernetes node %s: %v", *hostname, err)
					continue
				}
				// set options and get nodes
				drainOptions := &drain.DrainOptions{
					IgnoreDaemonsets:   true,
					GracePeriodSeconds: -1,
					Force:              true,
				}
				err = drain.Drain(clientset, []*corev1.Node{node}, drainOptions)
				if err != nil {
					log.Printf("Unexpected error draining kubernetes node %s: %v", *hostname, err)
					continue
				}
			}

			// all new config instances are ready, terminate an old one
			input := &autoscaling.TerminateInstanceInAutoScalingGroupInput{
				InstanceId:                     candidate,
				ShouldDecrementDesiredCapacity: aws.Bool(false),
			}

			_, err = asgSvc.TerminateInstanceInAutoScalingGroup(input)
			if err != nil {
				if aerr, ok := err.(awserr.Error); ok {
					switch aerr.Code() {
					case autoscaling.ErrCodeScalingActivityInProgressFault:
						log.Print("Could not terminate instance, autoscaling already in progress, will try next loop")
					case autoscaling.ErrCodeResourceContentionFault:
						log.Print("Could not terminate instance, instance in contention, will try next loop")
					default:
						log.Printf("Unknown aws error when terminating old instance: %v", aerr.Error())
					}
				} else {
					// Print the error, cast err to awserr.Error to get the Code and
					// Message from an error.
					log.Printf("Unknown non-aws error when terminating old instance: %v", err.Error())
				}
				continue
			}
		}
	}
}

func getKubeOutOfCluster() (*rest.Config, error) {
	var kubeconfig string
	kubeconfig = os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		if home := homeDir(); home != "" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		} else {
			return nil, fmt.Errorf("Not KUBECONFIG provided and no home available")
		}
	}

	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}
	return config, nil
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}

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
