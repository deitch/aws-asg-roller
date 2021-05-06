package main

import (
	"log"
	"os"
	"strings"
	"time"

	env "github.com/caarlos0/env/v6"
)

func main() {
	configs := getConfigs()

	// get a kube connection
	readinessHandler, err := kubeGetReadinessHandler(configs.KubernetesEnabled, configs.IgnoreDaemonSets, configs.DeleteLocalData)
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
		err := adjust(configs.KubernetesEnabled, configs.ASGS, ec2Svc, asgSvc, readinessHandler, originalDesired, configs.OriginalDesiredOnTag, configs.IncreaseMax, configs.Verbose)
		if err != nil {
			log.Printf("Error adjusting AutoScaling Groups: %v", err)
		}
		// delay with each loop
		log.Printf("Sleeping %d seconds\n", configs.Interval)
		time.Sleep(configs.Interval)
	}
}

func getConfigs() (configs Configs) {
	// Compat helper
	val, ok := os.LookupEnv("ROLLER_CHECK_DELAY")
	if ok {
		// Use value from check delay to set an interval
		if !strings.HasSuffix(val, "s") {
			os.Setenv("ROLLER_INTERVAL", val+"s")
		}
	}

	if err := env.Parse(&configs); err != nil {
		log.Panicf("unexpected error while initializing the config: %v", err)
	}

	return configs
}
