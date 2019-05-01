package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go/service/autoscaling"
)

type testReadyHandler struct {
	unreadyCount   int
	unreadyError   error
	terminateError error
}

func (t *testReadyHandler) getUnreadyCount(hostnames []string, ids []string) (int, error) {
	return t.unreadyCount, t.unreadyError
}
func (t *testReadyHandler) prepareTermination(hostnames []string, ids []string) error {
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
			 - new original desired number
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
		targetOriginalDesired int64
		targetTerminate       string
		err                   error
	}{
		// 1 old, 2 new healthy, 0 new unhealthy, should terminate old
		{[]string{"1"}, []string{"2", "3"}, []string{}, 3, 2, nil, 3, 2, "1", nil},
		// 0 old, 2 new healthy, 0 new unhealthy, should indicate end of process
		{[]string{}, []string{"2", "3"}, []string{}, 3, 2, nil, 2, 0, "", nil},
		// 2 old, 0 new healthy, 0 new unhealthy, should indicate start of process
		{[]string{"1", "2"}, []string{}, []string{}, 2, 0, nil, 3, 2, "", nil},
		// 2 old, 0 new healthy, 0 new unhealthy, started, should not do anything until new healthy one
		{[]string{"1", "2"}, []string{}, []string{}, 3, 2, nil, 3, 2, "", nil},
		// 2 old, 1 new healthy, 0 new unhealthy, remove an old one
		{[]string{"1", "2"}, []string{"3"}, []string{}, 3, 2, nil, 3, 2, "1", nil},
		// 2 old, 0 new healthy, 1 new unhealthy, started, should not do anything until new one is healthy
		{[]string{"1", "2"}, []string{}, []string{"3"}, 3, 2, nil, 3, 2, "", nil},

		// 2 old, 1 new healthy, 0 new unhealthy, 1 new unready, should not change anything
		{[]string{"1", "2"}, []string{"3"}, []string{}, 3, 2, unreadyCountHandler, 3, 2, "", nil},
		// 2 old, 1 new healthy, 0 new unhealthy, 0 new unready, 1 error: should not change anything
		{[]string{"1", "2"}, []string{"3"}, []string{}, 3, 2, unreadyErrorHandler, 3, 2, "", fmt.Errorf("Error")},
		// 2 old, 1 new healthy, 0 new unhealthy, 0 unready, remove an old one
		{[]string{"1", "2"}, []string{"3"}, []string{}, 3, 2, readyHandler, 3, 2, "1", nil},
		// 2 old, 1 new healthy, 0 new unhealthy, 0 new unready, 1 error: should not change anything
		{[]string{"1", "2"}, []string{"3"}, []string{}, 3, 2, terminateErrorHandler, 3, 2, "", fmt.Errorf("Unexpected error")},
		// 2 old, 1 new healthy, 0 new unhealthy, 0 unready, successful terminate: remove an old one
		{[]string{"1", "2"}, []string{"3"}, []string{}, 3, 2, terminateHandler, 3, 2, "1", nil},
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
			id := fmt.Sprintf("%s", instance)
			instances = append(instances, &autoscaling.Instance{
				InstanceId:              &id,
				LaunchConfigurationName: &lcNameOld,
				HealthStatus:            &statusHealthy,
			})
		}
		lcNameNew := lcName
		for _, instance := range tt.newInstancesHealthy {
			id := fmt.Sprintf("%s", instance)
			instances = append(instances, &autoscaling.Instance{
				InstanceId:              &id,
				LaunchConfigurationName: &lcNameNew,
				HealthStatus:            &statusHealthy,
			})
		}
		for _, instance := range tt.newInstancesUnhealthy {
			id := fmt.Sprintf("%s", instance)
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
		}
		desired, originalDesired, terminate, err := calculateAdjustment(asg, hostnameMap, tt.readiness, tt.originalDesired)
		switch {
		case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
			t.Errorf("%d: mismatched errors, actual then expected", i)
			t.Logf("%v", err)
			t.Logf("%v", tt.err)
		case desired != tt.targetDesired:
			t.Errorf("%d: Mismatched desired, actual %d expected %d", i, desired, tt.targetDesired)
		case originalDesired != tt.targetOriginalDesired:
			t.Errorf("%d: Mismatched original desired, actual %d expected %d", i, originalDesired, tt.targetOriginalDesired)
		case terminate != tt.targetTerminate:
			t.Errorf("%d: Mismatched terminate ID, actual %s expected %s", i, terminate, tt.targetTerminate)
		}
	}
}

func TestAdjust(t *testing.T) {
	tests := []struct {
		desc                    string
		asgs                    []string
		handler                 readiness
		err                     error
		oldIds                  map[string][]string
		newIds                  map[string][]string
		asgOriginalDesired      map[string]int64
		originalDesired         map[string]int64
		newOriginalDesired      map[string]int64
		newDesired              map[string]int64
		expectedOriginalDesired map[string]int64
		terminate               []string
	}{
		{
			"2 asgs adjust in progress",
			[]string{"myasg", "anotherasg"},
			nil,
			nil,
			map[string][]string{
				"myasg":      []string{"1"},
				"anotherasg": []string{},
			},
			map[string][]string{
				"myasg":      []string{"2", "3"},
				"anotherasg": []string{"8", "9", "10"},
			},
			map[string]int64{"myasg": 2, "anotherasg": 10},
			map[string]int64{"myasg": 2, "anotherasg": 10},
			map[string]int64{"myasg": 2, "anotherasg": 0},
			map[string]int64{"myasg": 2},
			map[string]int64{"myasg": 2, "anotherasg": 0},
			[]string{"1"},
		},
		{
			"2 asgs adjust first run",
			[]string{"myasg", "anotherasg"},
			nil,
			nil,
			map[string][]string{
				"myasg":      []string{"1"},
				"anotherasg": []string{},
			},
			map[string][]string{
				"myasg":      []string{"2", "3"},
				"anotherasg": []string{"8", "9", "10"},
			},
			map[string]int64{"myasg": 2},
			map[string]int64{},
			map[string]int64{"myasg": 2},
			map[string]int64{"myasg": 3},
			map[string]int64{"myasg": 2},
			[]string{},
		},
	}

	for i, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			validGroups := map[string]*autoscaling.Group{}
			for _, n := range tt.asgs {
				name := fmt.Sprintf("%s", n)
				lcName := "lconfig"
				oldLcName := fmt.Sprintf("old%s", lcName)
				myHealthy := fmt.Sprintf("%s", healthy)
				desired := tt.asgOriginalDesired[name]
				instances := make([]*autoscaling.Instance, 0)
				for _, id := range tt.oldIds[name] {
					idd := fmt.Sprintf("%s", id)
					instances = append(instances, &autoscaling.Instance{
						InstanceId:              &idd,
						LaunchConfigurationName: &oldLcName,
						HealthStatus:            &myHealthy,
					})
				}
				for _, id := range tt.newIds[name] {
					idd := fmt.Sprintf("%s", id)
					instances = append(instances, &autoscaling.Instance{
						InstanceId:              &idd,
						LaunchConfigurationName: &lcName,
						HealthStatus:            &myHealthy,
					})
				}
				// construct the Group we will pass
				validGroups[n] = &autoscaling.Group{
					AutoScalingGroupName:    &name,
					DesiredCapacity:         &desired,
					Instances:               instances,
					LaunchConfigurationName: &lcName,
				}
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
				ks := fmt.Sprintf("%s", k)
				originalDesiredPtr[&ks] = v
			}
			newDesiredPtr := map[*string]int64{}
			for k, v := range tt.newDesired {
				ks := fmt.Sprintf("%s", k)
				newDesiredPtr[&ks] = v
			}
			err := adjust(tt.asgs, ec2Svc, asgSvc, tt.handler, tt.originalDesired)
			// what were our last calls to each?
			switch {
			case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
				t.Errorf("%d: mismatched errors, actual then expected", i)
				t.Logf("%v", err)
				t.Logf("%v", tt.err)
			case !testStringInt64MapEq(tt.newOriginalDesired, tt.expectedOriginalDesired):
				t.Errorf("%d: Mismatched desired, actual then expected", i)
				t.Logf("%v", tt.originalDesired)
				t.Logf("%v", tt.newOriginalDesired)
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

		})
	}
}

func TestGroupInstances(t *testing.T) {
	tests := []struct {
		oldIds []string
		newIds []string
	}{
		{[]string{"1", "2"}, []string{"3"}},
		{[]string{"1", "2", "3"}, []string{}},
		{[]string{}, []string{"1", "2", "3"}},
	}
	for i, tt := range tests {
		instances := make([]*autoscaling.Instance, 0)
		lcName := "lcname"
		lcNameNew := fmt.Sprintf("%s", lcName)
		lcNameOld := fmt.Sprintf("old-%s", lcName)
		for _, instance := range tt.oldIds {
			id := fmt.Sprintf("%s", instance)
			instances = append(instances, &autoscaling.Instance{
				InstanceId:              &id,
				LaunchConfigurationName: &lcNameOld,
			})
		}
		for _, instance := range tt.newIds {
			id := fmt.Sprintf("%s", instance)
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
		oldInstances, newInstances := groupInstances(asg)
		oldList := make([]string, 0)
		newList := make([]string, 0)
		for _, i := range oldInstances {
			oldList = append(oldList, *i.InstanceId)
		}
		for _, i := range newInstances {
			newList = append(newList, *i.InstanceId)
		}
		if !testStringEq(oldList, tt.oldIds) {
			t.Errorf("%d: mismatched old Ids. Actual %v, expected %v", i, oldList, tt.oldIds)
		}
		if !testStringEq(newList, tt.newIds) {
			t.Errorf("%d: mismatched new Ids. Actual %v, expected %v", i, newList, tt.newIds)
		}
	}
}

func TestMapInstanceIds(t *testing.T) {
	ids := []string{"1", "2", "10"}
	instances := make([]*autoscaling.Instance, 0)
	for _, i := range ids {
		id := fmt.Sprintf("%s", i)
		instances = append(instances, &autoscaling.Instance{
			InstanceId: &id,
		})
	}
	m := mapInstancesIds(instances)
	if !testStringEq(m, ids) {
		t.Errorf("mismatched ids. Actual %v, expected %v", m, ids)
	}
}
