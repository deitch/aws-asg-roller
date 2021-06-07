package main

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
)

// Tests do not talk to a live kubernetes cluster
const kubernetesEnabled = false

type testReadyHandler struct {
	unreadyCount   int
	unreadyError   error
	terminateError error
}

func (t *testReadyHandler) getUnreadyCount(hostnames []string, ids []string) (int, error) {
	return t.unreadyCount, t.unreadyError
}
func (t *testReadyHandler) prepareTermination(hostnames []string, ids []string, drain, drainForce bool) error {
	return t.terminateError
}

func TestCalculateAdjustment(t *testing.T) {
	/*
		 Each test should have:
		 	inputs
		   - number of instances
			 - number with old config
			 - number with new config
			 - desired number of instances
			 - config name
			 - kube enabled (bool)
			 - original desired number
			 - if kube enabled:
			 		- state of each old node
					- state of each new node
			outputs
			 - new desired number
			 - node id to terminated (if any)
			 - errors (if any)
	*/
	unreadyCountHandler := &testReadyHandler{
		unreadyCount: 1,
	}
	unreadyErrorHandler := &testReadyHandler{
		unreadyError: fmt.Errorf("Error"),
	}
	readyHandler := &testReadyHandler{
		unreadyCount: 0,
	}
	terminateHandler := &testReadyHandler{}
	terminateErrorHandler := &testReadyHandler{
		terminateError: fmt.Errorf("Error"),
	}

	tests := []struct {
		oldInstances          []string
		newInstancesHealthy   []string
		newInstancesUnhealthy []string
		desired               int64
		originalDesired       int64
		readiness             readiness
		targetDesired         int64
		targetTerminate       string
		err                   error
		verbose               bool
		drain                 bool
		drainForce            bool
	}{
		// 1 old, 2 new healthy, 0 new unhealthy, should terminate old
		{[]string{"1"}, []string{"2", "3"}, []string{}, 3, 2, nil, 3, "1", nil, false, true, true},
		// 0 old, 2 new healthy, 0 new unhealthy, should indicate end of process
		{[]string{}, []string{"2", "3"}, []string{}, 2, 2, nil, 2, "", nil, false, true, true},
		// 2 old, 0 new healthy, 0 new unhealthy, should indicate start of process
		{[]string{"1", "2"}, []string{}, []string{}, 2, 2, nil, 3, "", nil, false, true, true},
		// 2 old, 0 new healthy, 0 new unhealthy, started, should not do anything until new healthy one
		{[]string{"1", "2"}, []string{}, []string{}, 3, 2, nil, 3, "", nil, false, true, true},
		// 2 old, 1 new healthy, 0 new unhealthy, remove an old one
		{[]string{"1", "2"}, []string{"3"}, []string{}, 3, 2, nil, 3, "1", nil, false, true, true},
		// 2 old, 0 new healthy, 1 new unhealthy, started, should not do anything until new one is healthy
		{[]string{"1", "2"}, []string{}, []string{"3"}, 3, 2, nil, 3, "", nil, false, true, true},

		// 2 old, 1 new healthy, 0 new unhealthy, 1 new unready, should not change anything
		{[]string{"1", "2"}, []string{"3"}, []string{}, 3, 2, unreadyCountHandler, 3, "", nil, false, true, true},
		// 2 old, 1 new healthy, 0 new unhealthy, 0 new unready, 1 error: should not change anything
		{[]string{"1", "2"}, []string{"3"}, []string{}, 3, 2, unreadyErrorHandler, 3, "", fmt.Errorf("error"), false, true, true},
		// 2 old, 1 new healthy, 0 new unhealthy, 0 unready, remove an old one
		{[]string{"1", "2"}, []string{"3"}, []string{}, 3, 2, readyHandler, 3, "1", nil, false, true, true},
		// 2 old, 1 new healthy, 0 new unhealthy, 0 new unready, 1 error: should not change anything
		{[]string{"1", "2"}, []string{"3"}, []string{}, 3, 2, terminateErrorHandler, 3, "", fmt.Errorf("unexpected error"), false, true, true},
		// 2 old, 1 new healthy, 0 new unhealthy, 0 unready, successful terminate: remove an old one
		{[]string{"1", "2"}, []string{"3"}, []string{}, 3, 2, terminateHandler, 3, "1", nil, false, true, true},
	}
	hostnameMap := map[string]string{}
	for i := 0; i < 20; i++ {
		hostnameMap[fmt.Sprintf("%d", i)] = fmt.Sprintf("host%d", i)
	}
	for i, tt := range tests {
		// construct Instances for the group
		lcName := "newconf"
		instances := make([]*autoscaling.Instance, 0)
		lcNameOld := fmt.Sprintf("mod-%s", lcName)
		statusHealthy := "Healthy"
		statusUnhealthy := "Down"
		for _, instance := range tt.oldInstances {
			id := instance
			instances = append(instances, &autoscaling.Instance{
				InstanceId:              &id,
				LaunchConfigurationName: &lcNameOld,
				HealthStatus:            &statusHealthy,
			})
		}
		lcNameNew := lcName
		for _, instance := range tt.newInstancesHealthy {
			id := instance
			instances = append(instances, &autoscaling.Instance{
				InstanceId:              &id,
				LaunchConfigurationName: &lcNameNew,
				HealthStatus:            &statusHealthy,
			})
		}
		for _, instance := range tt.newInstancesUnhealthy {
			id := instance
			instances = append(instances, &autoscaling.Instance{
				InstanceId:              &id,
				LaunchConfigurationName: &lcNameNew,
				HealthStatus:            &statusUnhealthy,
			})
		}
		// construct the Group we will pass
		asg := &autoscaling.Group{
			DesiredCapacity:         &tt.desired,
			LaunchConfigurationName: &lcName,
			Instances:               instances,
			AutoScalingGroupName:    aws.String("myasg"),
		}
		ec2Svc := &mockEc2Svc{
			autodescribe: true,
		}
		desired, terminate, err := calculateAdjustment(kubernetesEnabled, asg, ec2Svc, hostnameMap, tt.readiness, tt.originalDesired, tt.verbose, tt.drain, tt.drainForce)
		switch {
		case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
			t.Errorf("%d: mismatched errors, actual then expected", i)
			t.Logf("%v", err)
			t.Logf("%v", tt.err)
		case desired != tt.targetDesired:
			t.Errorf("%d: Mismatched desired, actual %d expected %d", i, desired, tt.targetDesired)
		case terminate != tt.targetTerminate:
			t.Errorf("%d: Mismatched terminate ID, actual %s expected %s", i, terminate, tt.targetTerminate)
		}
	}
}

func TestAdjust(t *testing.T) {
	tests := []struct {
		desc                        string
		asgs                        []string
		handler                     readiness
		err                         error
		oldIds                      map[string][]string
		newIds                      map[string][]string
		asgCurrentDesired           map[string]int64
		originalDesired             map[string]int64
		newDesired                  map[string]int64
		max                         map[string]int64
		terminate                   []string
		canIncreaseMax              bool
		persistOriginalDesiredOnTag bool
		verbose                     bool
		drain                       bool
		drainForce                  bool
	}{
		{
			"2 asgs adjust first run",
			[]string{"myasg", "anotherasg"},
			nil,
			nil,
			map[string][]string{
				"myasg":      {"1", "2"},
				"anotherasg": {},
			},
			map[string][]string{
				"myasg":      {},
				"anotherasg": {"8", "9", "10"},
			},
			map[string]int64{"myasg": 2, "anotherasg": 3},
			map[string]int64{"myasg": 2},
			map[string]int64{"myasg": 3},
			map[string]int64{"myasg": 3, "anotherasg": 4},
			[]string{},
			false,
			false,
			false,
			true,
			true,
		},
		{
			"2 asgs adjust in progress",
			[]string{"myasg", "anotherasg"},
			nil,
			nil,
			map[string][]string{
				"myasg":      {"1"},
				"anotherasg": {},
			},
			map[string][]string{
				"myasg":      {"2", "3"},
				"anotherasg": {"8", "9", "10"},
			},
			map[string]int64{"myasg": 2, "anotherasg": 10},
			map[string]int64{"myasg": 2, "anotherasg": 10},
			map[string]int64{"myasg": 3},
			map[string]int64{"myasg": 3, "anotherasg": 11},
			[]string{},
			false,
			false,
			false,
			true,
			true,
		},
		{
			"2 asgs adjust in progress with ROLLER_ORIGINAL_DESIRED_ON_TAG set to true",
			[]string{"myasg", "anotherasg"},
			nil,
			nil,
			map[string][]string{
				"myasg":      {"1"},
				"anotherasg": {},
			},
			map[string][]string{
				"myasg":      {"2", "3"},
				"anotherasg": {"8", "9", "10"},
			},
			map[string]int64{"myasg": 3, "anotherasg": 3},
			map[string]int64{"myasg": 2, "anotherasg": 3},
			map[string]int64{},
			map[string]int64{"myasg": 3, "anotherasg": 4},
			[]string{"1"},
			false,
			true,
			false,
			true,
			true,
		},
		{
			"2 asgs adjust complete",
			[]string{"myasg", "anotherasg"},
			nil,
			nil,
			map[string][]string{
				"myasg":      {},
				"anotherasg": {},
			},
			map[string][]string{
				"myasg":      {"1", "2", "3"},
				"anotherasg": {"8", "9", "10"},
			},
			map[string]int64{"myasg": 2},
			map[string]int64{"myasg": 2},
			map[string]int64{},
			map[string]int64{"myasg": 3},
			[]string{},
			false,
			false,
			false,
			true,
			true,
		},
		{
			"2 asgs adjust increase max fail",
			[]string{"myasg", "anotherasg"},
			nil,
			fmt.Errorf("[myasg] error setting desired to 3: unable to increase ASG myasg desired size to 3 as greater than max size 2"),
			map[string][]string{
				"myasg":      {"1"},
				"anotherasg": {},
			},
			map[string][]string{
				"myasg":      {"2", "3"},
				"anotherasg": {"8", "9", "10"},
			},
			map[string]int64{"myasg": 2},
			map[string]int64{"myasg": 2},
			map[string]int64{},
			map[string]int64{"myasg": 2},
			[]string{},
			false,
			false,
			false,
			true,
			true,
		},
		{
			"2 asgs adjust increase max succeed",
			[]string{"myasg", "anotherasg"},
			nil,
			nil,
			map[string][]string{
				"myasg":      {"1"},
				"anotherasg": {},
			},
			map[string][]string{
				"myasg":      {"2", "3"},
				"anotherasg": {"8", "9", "10"},
			},
			map[string]int64{"myasg": 2},
			map[string]int64{"myasg": 2},
			map[string]int64{"myasg": 3},
			map[string]int64{"myasg": 2},
			[]string{},
			true,
			false,
			false,
			true,
			true,
		},
	}

	for i, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			validGroups := map[string]*autoscaling.Group{}
			for _, n := range tt.asgs {
				name := n
				lcName := "lconfig"
				oldLcName := fmt.Sprintf("old%s", lcName)
				myHealthy := healthy
				desired := tt.asgCurrentDesired[name]
				max := tt.max[name]
				instances := make([]*autoscaling.Instance, 0)
				for _, id := range tt.oldIds[name] {
					idd := id
					instances = append(instances, &autoscaling.Instance{
						InstanceId:              &idd,
						LaunchConfigurationName: &oldLcName,
						HealthStatus:            &myHealthy,
					})
				}
				for _, id := range tt.newIds[name] {
					idd := id
					instances = append(instances, &autoscaling.Instance{
						InstanceId:              &idd,
						LaunchConfigurationName: &lcName,
						HealthStatus:            &myHealthy,
					})
				}
				// construct the Group we will pass
				validGroup := &autoscaling.Group{
					AutoScalingGroupName:    &name,
					DesiredCapacity:         &desired,
					Instances:               instances,
					LaunchConfigurationName: &lcName,
					MaxSize:                 &max,
				}

				if tt.persistOriginalDesiredOnTag {
					if originalDesired, ok := tt.originalDesired[name]; ok {
						validGroup.Tags = []*autoscaling.TagDescription{
							{
								Key:               aws.String(asgTagNameOriginalDesired),
								PropagateAtLaunch: aws.Bool(false),
								ResourceId:        &name,
								ResourceType:      aws.String("auto-scaling-group"),
								Value:             aws.String(strconv.FormatInt(originalDesired, 10)),
							},
						}
					}
				}
				validGroups[n] = validGroup
			}
			asgSvc := &mockAsgSvc{
				groups: validGroups,
			}
			ec2Svc := &mockEc2Svc{
				autodescribe: true,
			}
			// convert maps from map[string] to map[*string]
			originalDesiredPtr := map[*string]int64{}
			for k, v := range tt.originalDesired {
				ks := k
				originalDesiredPtr[&ks] = v
			}
			newDesiredPtr := map[*string]int64{}
			for k, v := range tt.newDesired {
				ks := k
				newDesiredPtr[&ks] = v
			}
			err := adjust(kubernetesEnabled, tt.asgs, ec2Svc, asgSvc, tt.handler, tt.originalDesired, tt.persistOriginalDesiredOnTag, tt.canIncreaseMax, tt.verbose, tt.drain, tt.drainForce)
			// what were our last calls to each?
			switch {
			case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
				t.Errorf("%d: mismatched errors, actual then expected", i)
				t.Logf("%v", err)
				t.Logf("%v", tt.err)
			}

			// check each svc with its correct calls
			desiredCalls := asgSvc.counter.filterByName("SetDesiredCapacity")
			if len(desiredCalls) != len(tt.newDesired) {
				t.Errorf("%d: Expected %d SetDesiredCapacity calls but had %d", i, len(tt.newDesired), len(desiredCalls))
			}
			// sort through by the relevant inputs
			for _, d := range desiredCalls {
				asg := d.params[0].(*autoscaling.SetDesiredCapacityInput)
				name := asg.AutoScalingGroupName
				if *asg.DesiredCapacity != tt.newDesired[*name] {
					t.Errorf("%d: Mismatched call to set capacity for ASG '%s': actual %d, expected %d", i, *name, *asg.DesiredCapacity, tt.newDesired[*name])
				}
			}
			// convert list of terminations into map
			ids := map[string]bool{}
			for _, id := range tt.terminate {
				ids[id] = true
			}
			terminateCalls := asgSvc.counter.filterByName("TerminateInstanceInAutoScalingGroup")
			if len(terminateCalls) != len(tt.terminate) {
				t.Errorf("%d: Expected %d Terminate calls but had %d", i, len(tt.terminate), len(terminateCalls))
			}
			for _, d := range terminateCalls {
				in := d.params[0].(*autoscaling.TerminateInstanceInAutoScalingGroupInput)
				id := in.InstanceId
				if _, ok := ids[*id]; !ok {
					t.Errorf("%d: Requested call to terminate instance %s, unexpected", i, *id)
				}
			}
			// check for calls to update the group (e.g. to raise max)
			updateGroupCalls := asgSvc.counter.filterByName("UpdateAutoScalingGroup")
			for k, desired := range tt.newDesired {
				if desired > tt.max[k] && len(updateGroupCalls) == 0 {
					t.Errorf("%d: Expected call to UpdateAutoScalingGroup to set max but there was none", i)
				}
			}
		})
	}
}

func TestGroupInstances(t *testing.T) {
	runTest := func(t *testing.T, asg *autoscaling.Group, i int, oldIds, newIds []string) {
		ec2Svc := &mockEc2Svc{
			autodescribe: true,
		}
		oldInstances, newInstances, err := groupInstances(asg, ec2Svc, false)
		if err != nil {
			t.Errorf("unexpected error grouping instances: %v", err)
			return
		}
		oldList := make([]string, 0)
		newList := make([]string, 0)
		for _, i := range oldInstances {
			oldList = append(oldList, *i.InstanceId)
		}
		for _, i := range newInstances {
			newList = append(newList, *i.InstanceId)
		}
		if !testStringEq(oldList, oldIds) {
			t.Errorf("%d: mismatched old Ids. Actual %v, expected %v", i, oldList, oldIds)
		}
		if !testStringEq(newList, newIds) {
			t.Errorf("%d: mismatched new Ids. Actual %v, expected %v", i, newList, newIds)
		}
	}
	tests := []struct {
		oldIds []string
		newIds []string
	}{
		{[]string{"1", "2"}, []string{"3"}},
		{[]string{"1", "2", "3"}, []string{}},
		{[]string{}, []string{"1", "2", "3"}},
		{[]string{}, []string{"1", "2", "$D"}},
	}
	t.Run("launchconfiguration", func(t *testing.T) {
		for i, tt := range tests {
			instances := make([]*autoscaling.Instance, 0)
			lcName := "lcname"
			lcNameNew := lcName
			lcNameOld := fmt.Sprintf("old-%s", lcName)
			for _, instance := range tt.oldIds {
				id := instance
				instances = append(instances, &autoscaling.Instance{
					InstanceId:              &id,
					LaunchConfigurationName: &lcNameOld,
				})
			}
			for _, instance := range tt.newIds {
				id := instance
				instances = append(instances, &autoscaling.Instance{
					InstanceId:              &id,
					LaunchConfigurationName: &lcNameNew,
				})
			}
			// construct the Group we will pass
			asg := &autoscaling.Group{
				LaunchConfigurationName: &lcName,
				Instances:               instances,
			}
			runTest(t, asg, i, tt.oldIds, tt.newIds)
		}
	})
	t.Run("launchtemplate", func(t *testing.T) {
		for i, tt := range tests {
			instances := make([]*autoscaling.Instance, 0)
			ltName := "lt1"
			ltNameNew := ltName
			ltNameOld := fmt.Sprintf("old-%s", ltName)
			for _, instance := range tt.oldIds {
				id := instance
				instances = append(instances, &autoscaling.Instance{
					InstanceId:     &id,
					LaunchTemplate: &autoscaling.LaunchTemplateSpecification{LaunchTemplateName: &ltNameOld},
				})
			}
			for _, instance := range tt.newIds {
				id := instance
				instances = append(instances, &autoscaling.Instance{
					InstanceId:     &id,
					LaunchTemplate: &autoscaling.LaunchTemplateSpecification{LaunchTemplateName: &ltNameNew},
				})
			}
			// construct the Group we will pass
			asg := &autoscaling.Group{
				LaunchTemplate: &autoscaling.LaunchTemplateSpecification{LaunchTemplateName: &ltName},
				Instances:      instances,
			}
			runTest(t, asg, i, tt.oldIds, tt.newIds)
		}
	})
	t.Run("launchtemplatemixedinstances", func(t *testing.T) {
		for i, tt := range tests {
			instances := make([]*autoscaling.Instance, 0)
			ltName := "lt1"
			ltNameNew := ltName
			ltNameOld := fmt.Sprintf("old-%s", ltName)
			for _, instance := range tt.oldIds {
				id := instance
				instances = append(instances, &autoscaling.Instance{
					InstanceId:     &id,
					LaunchTemplate: &autoscaling.LaunchTemplateSpecification{LaunchTemplateName: &ltNameOld},
				})
			}
			for _, instance := range tt.newIds {
				id := instance
				instances = append(instances, &autoscaling.Instance{
					InstanceId:     &id,
					LaunchTemplate: &autoscaling.LaunchTemplateSpecification{LaunchTemplateName: &ltNameNew},
				})
			}
			// construct the Group we will pass
			asg := &autoscaling.Group{
				MixedInstancesPolicy: &autoscaling.MixedInstancesPolicy{
					LaunchTemplate: &autoscaling.LaunchTemplate{
						LaunchTemplateSpecification: &autoscaling.LaunchTemplateSpecification{LaunchTemplateName: &ltName},
					},
				},
				Instances: instances,
			}
			runTest(t, asg, i, tt.oldIds, tt.newIds)
		}
	})

}

func TestMapInstanceIds(t *testing.T) {
	ids := []string{"1", "2", "10"}
	instances := make([]*autoscaling.Instance, 0)
	for _, i := range ids {
		id := i
		instances = append(instances, &autoscaling.Instance{
			InstanceId: &id,
		})
	}
	m := mapInstancesIds(instances)
	if !testStringEq(m, ids) {
		t.Errorf("mismatched ids. Actual %v, expected %v", m, ids)
	}
}

func TestCompareLaunchTemplateVersions(t *testing.T) {
	template := &ec2.LaunchTemplate{
		DefaultVersionNumber: aws.Int64(25),
		LatestVersionNumber:  aws.Int64(64),
	}
	tests := []struct {
		lt1      *autoscaling.LaunchTemplateSpecification
		lt2      *autoscaling.LaunchTemplateSpecification
		expected bool
	}{
		{nil, nil, true},
		{nil, &autoscaling.LaunchTemplateSpecification{}, false},
		{&autoscaling.LaunchTemplateSpecification{}, nil, false},
		{&autoscaling.LaunchTemplateSpecification{}, &autoscaling.LaunchTemplateSpecification{}, true},
		{&autoscaling.LaunchTemplateSpecification{Version: aws.String("25")}, &autoscaling.LaunchTemplateSpecification{}, false},
		{&autoscaling.LaunchTemplateSpecification{Version: aws.String("25")}, &autoscaling.LaunchTemplateSpecification{Version: aws.String("26")}, false},
		{&autoscaling.LaunchTemplateSpecification{Version: aws.String("25")}, &autoscaling.LaunchTemplateSpecification{Version: aws.String("25")}, true},
		{&autoscaling.LaunchTemplateSpecification{Version: aws.String("25")}, &autoscaling.LaunchTemplateSpecification{Version: aws.String("25")}, true},
		{&autoscaling.LaunchTemplateSpecification{Version: aws.String("64")}, &autoscaling.LaunchTemplateSpecification{Version: aws.String("$Latest")}, true},
		{&autoscaling.LaunchTemplateSpecification{Version: aws.String("25")}, &autoscaling.LaunchTemplateSpecification{Version: aws.String("$Default")}, true},
		{&autoscaling.LaunchTemplateSpecification{Version: aws.String("$Default")}, &autoscaling.LaunchTemplateSpecification{Version: aws.String("$Default")}, true},
		{&autoscaling.LaunchTemplateSpecification{Version: aws.String("$Latest")}, &autoscaling.LaunchTemplateSpecification{Version: aws.String("$Latest")}, true},
		{&autoscaling.LaunchTemplateSpecification{Version: aws.String("$Default")}, &autoscaling.LaunchTemplateSpecification{Version: aws.String("$Latest")}, false},
	}
	for i, tt := range tests {
		result := compareLaunchTemplateVersions(template, tt.lt1, tt.lt2)
		if result != tt.expected {
			t.Errorf("%d: mismatched results, received %v expected %v", i, result, tt.expected)
		}
	}
}
