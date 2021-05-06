package main

import (
	"fmt"
	"log"
	"strconv"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/autoscaling/autoscalingiface"
)

const asgTagNameOriginalDesired = "aws-asg-roller/OriginalDesired"

// Populates the original desired values for each ASG, based on the current 'desired' value if unkonwn.
// The original desired value is recorded as a tag on the respective ASG. Subsequent runs attempt to
// read the value of the tag to preserve state in the case of the process terminating.
func populateOriginalDesired(originalDesired map[string]int64, asgs []*autoscaling.Group, asgSvc autoscalingiface.AutoScalingAPI, storeOriginalDesiredOnTag bool, verbose bool) error {
	for _, asg := range asgs {
		asgName := *asg.AutoScalingGroupName
		if storeOriginalDesiredOnTag {
			tagOriginalDesired, err := getOriginalDesiredTag(asgSvc, asgName, verbose)
			if err != nil {
				return err
			}
			if tagOriginalDesired >= 0 {
				originalDesired[asgName] = tagOriginalDesired
				continue
			}
		}
		// guess based on the current value
		originalDesired[asgName] = *asg.DesiredCapacity
		if verbose {
			log.Printf("guessed desired value of %d from current desired on ASG: %s", *asg.DesiredCapacity, asgName)
		}
		if storeOriginalDesiredOnTag {
			err := setOriginalDesiredTag(asgSvc, asgName, asg, verbose)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// attempt to read the original desired value from the ASG tag
// returns
//   the original desired value from the tag, if present, otherwise -1
//   error
func getOriginalDesiredTag(asgSvc autoscalingiface.AutoScalingAPI, asgName string, verbose bool) (int64, error) {
	tags, err := asgSvc.DescribeTags(&autoscaling.DescribeTagsInput{
		Filters: []*autoscaling.Filter{
			{
				Name:   aws.String("auto-scaling-group"),
				Values: aws.StringSlice([]string{asgName}),
			},
			{
				Name:   aws.String("key"),
				Values: aws.StringSlice([]string{asgTagNameOriginalDesired}),
			},
		},
	})
	if err != nil {
		return -1, fmt.Errorf("unable to read tag '%s' for ASG %s: %v", asgTagNameOriginalDesired, asgName, err)
	}
	if len(tags.Tags) == 1 {
		if tagOriginalDesired, err := strconv.ParseInt(aws.StringValue(tags.Tags[0].Value), 10, 64); err == nil {
			if verbose {
				log.Printf("read original desired of %d from tag on ASG: %s", tagOriginalDesired, asgName)
			}
			return tagOriginalDesired, nil
		}
		return -1, fmt.Errorf("unable to read tag '%s' for ASG %s: %v", asgTagNameOriginalDesired, asgName, err)
	}
	return -1, nil
}

// record original desired value on a tag, in case of process restart
func setOriginalDesiredTag(asgSvc autoscalingiface.AutoScalingAPI, asgName string, asg *autoscaling.Group, verbose bool) error {
	_, err := asgSvc.CreateOrUpdateTags(&autoscaling.CreateOrUpdateTagsInput{
		Tags: []*autoscaling.Tag{
			{
				Key:               aws.String(asgTagNameOriginalDesired),
				PropagateAtLaunch: aws.Bool(false),
				ResourceId:        aws.String(asgName),
				ResourceType:      aws.String("auto-scaling-group"),
				Value:             aws.String(strconv.FormatInt(*asg.DesiredCapacity, 10)),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("unable to set tag '%s' for ASG %s: %v", asgTagNameOriginalDesired, asgName, err)
	}
	if verbose {
		log.Printf("recorded desired value of %d in tag on ASG: %s", *asg.DesiredCapacity, asgName)
	}
	return nil
}
