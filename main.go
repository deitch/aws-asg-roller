package main

import (
	"log"
	"os"
	"strings"
	"time"
)

const (
	asgCheckDelay = 30 // delay between checks of ASG status in seconds
)

func main() {
	asgList := strings.Split(os.Getenv("ROLLER_ASG"), ",")
	if asgList == nil || len(asgList) == 0 {
		log.Fatal("Must supply at least one ASG in ROLLER_ASG environment variable")
	}

	// get a kube connection
	readinessHandler, err := kubeGetReadinessHandler()
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

	// infinite loop
	for {
		// delay with each loop
		log.Printf("Sleeping %d seconds\n", asgCheckDelay)
		time.Sleep(asgCheckDelay * time.Second)
		err = adjust(asgList, ec2Svc, asgSvc, readinessHandler, originalDesired)
		if err != nil {
			log.Printf("Error adjusting AutoScaling Groups: %v", err)
		}
	}
}
