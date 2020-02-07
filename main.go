package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	asgCheckDelay = 30 // Default delay between checks of ASG status in seconds
)

var (
	verbose = os.Getenv("ROLLER_VERBOSE") == "true"
)

func main() {
	asgList := strings.Split(os.Getenv("ROLLER_ASG"), ",")
	if len(asgList) == 0 {
		log.Fatal("Must supply at least one ASG in ROLLER_ASG environment variable")
	}

	// get config env
	ignoreDaemonSets := os.Getenv("ROLLER_IGNORE_DAEMONSETS") != "false"
	deleteLocalData := strings.ToLower(os.Getenv("ROLLER_DELETE_LOCAL_DATA")) == "true"
	// get a kube connection
	readinessHandler, err := kubeGetReadinessHandler(ignoreDaemonSets, deleteLocalData)
	if err != nil {
		log.Fatalf("Error getting kubernetes readiness handler when required: %v", err)
	}

	// get the AWS sessions
	ec2Svc, asgSvc, err := awsGetServices()
	if err != nil {
		log.Fatalf("Unable to create an AWS session: %v", err)
	}

	// to keep track of original target sizes during rolling updates
	originalDesired := map[string]int64{}

	checkDelay, err := getDelay()
	if err != nil {
		log.Fatalf("Unable to get delay: %s", err.Error())
	}

	// infinite loop
	for {
		err := adjust(asgList, ec2Svc, asgSvc, readinessHandler, originalDesired)
		if err != nil {
			log.Printf("Error adjusting AutoScaling Groups: %v", err)
		}
		// delay with each loop
		log.Printf("Sleeping %d seconds\n", checkDelay)
		time.Sleep(time.Duration(checkDelay) * time.Second)
	}
}

// Returns delay value to use in loop. Uses default if not defined.
func getDelay() (int, error) {
	delayOverride, exist := os.LookupEnv("ROLLER_CHECK_DELAY")
	if exist {
		delay, err := strconv.Atoi(delayOverride)
		if err != nil {
			return -1, fmt.Errorf("ROLLER_CHECK_DELAY is not parsable: %v (%s)", delayOverride, err.Error())
		}
		return delay, nil
	}

	return asgCheckDelay, nil
}
