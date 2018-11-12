package main

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/service/autoscaling"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1"

	drain "github.com/openshift/kubernetes-drain"
)

const (
	asgCheckDelay = 30 // delay between checks of ASG status in seconds
	healthy       = "Healthy"
)

func main() {
	asgList := strings.Split(os.Getenv("ROLLER_ASG"), ",")
	kubeConstraints := os.Getenv("ROLLER_KUBERNETES") == "true"
	if asgList == nil || len(asgList) == 0 {
		log.Fatal("Must supply at least one ASG in ROLLER_ASG environment variable")
	}

	// get a kube connection
	clientset, err := kubeGetClientset()
	if err != nil {
		log.Fatalf("Error getting kubernetes connectionn when required: %v", err)
	}
	if clientset != nil {
		kubeConstraints = true
	}

	// get the AWS sessions
	ec2Svc, asgSvc, err := awsGetServices()
	if err != nil {
		log.Fatalf("Unable to create an AWS session: %v", err)
	}

	// to keep track of original target sizes during rolling updates
	originalDesired := map[*string]int64{}

	// infinite loop
	for {
		// delay with each loop
		log.Printf("Sleeping %d seconds\n", asgCheckDelay)
		time.Sleep(asgCheckDelay * time.Second)
		asgs, err := awsDescribeGroups(asgSvc, asgList)
		if err != nil {
			log.Printf("Unexpected error describing ASGs, skipping: %v", err)
			continue
		}
		// keep keyed references to the ASGs
		for _, asg := range asgs {
			desired := asg.DesiredCapacity

			// get instances with old launch config
			oldInstances, newInstances := groupInstances(asg)

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
			// do we have additional constraints on readiness?
			if kubeConstraints {
				var (
					hostnames []string
				)
				// check if the new nodes all are in ready state
				ids := mapInstancesIds(newInstances)
				hostnames, err = awsGetHostnames(ec2Svc, ids)
				if err != nil {
					log.Printf("Unable to get aws hostnames for ids %v: %v", ids, err)
					continue
				}
				unReadyCount, err = kubeGetUnreadyCount(clientset, hostnames)
				if err != nil {
					log.Printf("Error getting kubernetes new node status: %v", err)
					continue
				}
				if unReadyCount > 0 {
					log.Printf("Cluster has %d unready new nodes", unReadyCount)
					continue
				}
			}
			candidate := *oldInstances[0].InstanceId

			if kubeConstraints {
				// get the node reference - first need the hostname
				var (
					node     *corev1.Node
					hostname string
				)
				hostname, err = awsGetHostname(ec2Svc, candidate)
				if err != nil {
					log.Printf("Unable to get description for node %s: %v", candidate, err)
					continue
				}
				node, err = clientset.CoreV1().Nodes().Get(hostname, v1.GetOptions{})
				if err != nil {
					log.Printf("Unexpected error getting kubernetes node %s: %v", hostname, err)
					continue
				}
				// set options and drain nodes
				err = drain.Drain(clientset, []*corev1.Node{node}, &drain.DrainOptions{
					IgnoreDaemonsets:   true,
					GracePeriodSeconds: -1,
					Force:              true,
				})
				if err != nil {
					log.Printf("Unexpected error draining kubernetes node %s: %v", hostname, err)
					continue
				}
			}

			// all new config instances are ready, terminate an old one
			err = awsTerminateNode(asgSvc, candidate)
			if err != nil {
				log.Printf("Error terminating node %s: %v", candidate, err)
			}
		}
	}
}

func groupInstances(asg *autoscaling.Group) ([]*autoscaling.Instance, []*autoscaling.Instance) {
	oldInstances := make([]*autoscaling.Instance, 0)
	newInstances := make([]*autoscaling.Instance, 0)
	targetLc := asg.LaunchConfigurationName
	// go through each instance and find those that are not with the target LC
	for _, i := range asg.Instances {
		if i.LaunchConfigurationName == targetLc {
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
