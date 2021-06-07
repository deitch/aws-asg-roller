package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/autoscaling/autoscalingiface"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
)

func testASGEq(a, b []*autoscaling.Group) bool {

	// If one is nil, the other must also be nil.
	if (a == nil) != (b == nil) {
		return false
	}

	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if *a[i].AutoScalingGroupName != *b[i].AutoScalingGroupName {
			return false
		}
	}
	return true
}

var validLaunchTemplates = map[string]*ec2.LaunchTemplate{
	"12345": {
		LaunchTemplateId:     aws.String("12345"),
		LatestVersionNumber:  aws.Int64(65),
		DefaultVersionNumber: aws.Int64(59),
	},
	"67890": {
		LaunchTemplateId:     aws.String("67890"),
		LatestVersionNumber:  aws.Int64(10),
		DefaultVersionNumber: aws.Int64(10),
	},
	"lt1": {
		LaunchTemplateName:   aws.String("lt1"),
		LatestVersionNumber:  aws.Int64(4),
		DefaultVersionNumber: aws.Int64(1),
	},
	"lt2": {
		LaunchTemplateName:   aws.String("lt2"),
		LatestVersionNumber:  aws.Int64(40),
		DefaultVersionNumber: aws.Int64(30),
	},
}

type mockEc2Svc struct {
	ec2iface.EC2API
	autodescribe bool
	counter      funcCounter
}

func (m *mockEc2Svc) DescribeInstances(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
	m.counter.add("DescribeInstances", in)
	hostMap := map[string]string{
		"12345": "host12345",
		"67890": "host67890",
	}
	instances := make([]*ec2.Instance, 0)
	for _, i := range in.InstanceIds {
		if name, ok := hostMap[*i]; ok {
			instances = append(instances, &ec2.Instance{
				InstanceId:     i,
				PrivateDnsName: &name,
			})
			continue
		}
		if m.autodescribe {
			name := fmt.Sprintf("host%s", *i)
			instances = append(instances, &ec2.Instance{
				InstanceId:     i,
				PrivateDnsName: &name,
			})
			continue
		}
		return nil, fmt.Errorf("Unknown ID %s", *i)
	}
	ret := &ec2.DescribeInstancesOutput{
		Reservations: []*ec2.Reservation{
			{
				Instances: instances,
			},
		},
	}
	return ret, nil
}

func (m *mockEc2Svc) DescribeLaunchTemplates(in *ec2.DescribeLaunchTemplatesInput) (*ec2.DescribeLaunchTemplatesOutput, error) {
	m.counter.add("DescribeLaunchTemplates:", in)
	templates := make([]*ec2.LaunchTemplate, 0)
	for _, i := range in.LaunchTemplateIds {
		for _, t := range validLaunchTemplates {
			if t.LaunchTemplateId != nil && *t.LaunchTemplateId == *i {
				templates = append(templates, t)
			}
		}
	}
	for _, i := range in.LaunchTemplateNames {
		for _, t := range validLaunchTemplates {
			if t.LaunchTemplateName != nil && *t.LaunchTemplateName == *i {
				templates = append(templates, t)
			}
		}
	}
	ret := &ec2.DescribeLaunchTemplatesOutput{
		LaunchTemplates: templates,
	}
	return ret, nil
}

type mockAsgSvc struct {
	autoscalingiface.AutoScalingAPI
	err     error
	counter funcCounter
	groups  map[string]*autoscaling.Group
}

func (m *mockAsgSvc) TerminateInstanceInAutoScalingGroup(in *autoscaling.TerminateInstanceInAutoScalingGroupInput) (*autoscaling.TerminateInstanceInAutoScalingGroupOutput, error) {
	m.counter.add("TerminateInstanceInAutoScalingGroup", in)
	ret := &autoscaling.TerminateInstanceInAutoScalingGroupOutput{}
	return ret, m.err
}
func (m *mockAsgSvc) DescribeAutoScalingGroups(in *autoscaling.DescribeAutoScalingGroupsInput) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
	m.counter.add("DescribeAutoScalingGroups", in)
	groups := make([]*autoscaling.Group, 0)
	for _, n := range in.AutoScalingGroupNames {
		if group, ok := m.groups[*n]; ok {
			groups = append(groups, group)
		}
	}
	return &autoscaling.DescribeAutoScalingGroupsOutput{
		AutoScalingGroups: groups,
	}, m.err
}
func (m *mockAsgSvc) SetDesiredCapacity(in *autoscaling.SetDesiredCapacityInput) (*autoscaling.SetDesiredCapacityOutput, error) {
	m.counter.add("SetDesiredCapacity", in)
	ret := &autoscaling.SetDesiredCapacityOutput{}
	return ret, m.err
}
func (m *mockAsgSvc) UpdateAutoScalingGroup(in *autoscaling.UpdateAutoScalingGroupInput) (*autoscaling.UpdateAutoScalingGroupOutput, error) {
	m.counter.add("UpdateAutoScalingGroup", in)
	ret := &autoscaling.UpdateAutoScalingGroupOutput{}
	return ret, m.err
}
func (m *mockAsgSvc) DescribeTags(in *autoscaling.DescribeTagsInput) (*autoscaling.DescribeTagsOutput, error) {
	m.counter.add("DescribeTags", in)
	ret := &autoscaling.DescribeTagsOutput{
		// value of "auto-scaling-group" tag is the ASG name
		Tags: m.groups[*in.Filters[0].Values[0]].Tags,
	}
	return ret, m.err
}
func (m *mockAsgSvc) CreateOrUpdateTags(in *autoscaling.CreateOrUpdateTagsInput) (*autoscaling.CreateOrUpdateTagsOutput, error) {
	m.counter.add("CreateOrUpdateTags", in)
	ret := &autoscaling.CreateOrUpdateTagsOutput{}
	return ret, m.err
}

func TestAwsGetHostnames(t *testing.T) {
	tests := []struct {
		ids       []string
		hostnames []string
		err       error
	}{
		{[]string{"12345", "67890"}, []string{"host12345", "host67890"}, nil},
		{[]string{"67890"}, []string{"host67890"}, nil},
		{[]string{"notexist"}, nil, fmt.Errorf("Unable to get description")},
	}
	for _, tt := range tests {
		hostnames, err := awsGetHostnames(&mockEc2Svc{}, tt.ids)
		switch {
		case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
			t.Errorf("Mismatched error, actual then expected")
			t.Logf("%v", err)
			t.Logf("%v", tt.err)
		case !testStringEq(hostnames, tt.hostnames):
			t.Errorf("Mismatched results, actual then expected")
			t.Logf("%v", hostnames)
			t.Logf("%v", tt.hostnames)
		}
	}
}
func TestAwsGetHostname(t *testing.T) {
	tests := []struct {
		id       string
		hostname string
		err      error
	}{
		{"12345", "host12345", nil},
		{"notexist", "", fmt.Errorf("Unable to get description")},
	}
	for _, tt := range tests {
		hostname, err := awsGetHostname(&mockEc2Svc{}, tt.id)
		switch {
		case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
			t.Errorf("Mismatched error, actual then expected")
			t.Logf("%v", err)
			t.Logf("%v", tt.err)
		case hostname != tt.hostname:
			t.Errorf("Mismatched results, actual then expected")
			t.Logf("%v", hostname)
			t.Logf("%v", tt.hostname)
		}
	}
}

func TestAwsGetServices(t *testing.T) {
	ec2, asg, err := awsGetServices()
	if err != nil {
		t.Fatalf("Unexpected err %v", err)
	}
	if ec2 == nil {
		t.Fatalf("ec2 unexpectedly nil")
	}
	if asg == nil {
		t.Fatalf("asg unexpectedly nil")
	}
}

func TestAwsTerminateNode(t *testing.T) {
	id := "12345"
	tests := []struct {
		awserr error
		err    error
	}{
		{awserr.New(autoscaling.ErrCodeScalingActivityInProgressFault, "", nil), fmt.Errorf("Could not terminate instance, autoscaling already in progress")},
		{awserr.New(autoscaling.ErrCodeResourceContentionFault, "", nil), fmt.Errorf("Could not terminate instance, instance in contention")},
		{awserr.New("test it new", "", nil), fmt.Errorf("Unknown aws error when terminating old instance")},
		{fmt.Errorf("test it new"), fmt.Errorf("Unknown non-aws error when terminating old instance")},
	}
	for i, tt := range tests {
		err := awsTerminateNode(&mockAsgSvc{
			err: tt.awserr,
		}, id)
		if (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())) {
			t.Errorf("%d: mismatched errors, actual then expected", i)
			t.Logf("%v", err)
			t.Logf("%v", tt.err)
		}
	}
}
func TestAwsDescribeGroups(t *testing.T) {
	nogroup := "notexist"
	tests := []struct {
		names  []string
		setErr error
		err    error
	}{
		{[]string{"abc", "def"}, nil, nil},
		{[]string{"67890"}, nil, nil},
		{[]string{nogroup}, awserr.New(autoscaling.ErrCodeResourceContentionFault, "", nil), fmt.Errorf("Unexpected AWS Resource")},
		{[]string{nogroup}, awserr.New("testabc", "", nil), fmt.Errorf("Unexpected and unknown AWS error")},
		{[]string{nogroup}, fmt.Errorf("testabc"), fmt.Errorf("Unexpected and unknown non-AWS error")},
	}
	for i, tt := range tests {
		validGroups := map[string]*autoscaling.Group{}
		for _, n := range tt.names {
			if n == nogroup {
				continue
			}
			name := n
			validGroups[n] = &autoscaling.Group{
				AutoScalingGroupName: &name,
			}
		}
		groups, err := awsDescribeGroups(&mockAsgSvc{
			err:    tt.setErr,
			groups: validGroups,
		}, tt.names)
		var expectedGroups []*autoscaling.Group
		if tt.err == nil {
			expectedGroups = make([]*autoscaling.Group, 0)
			for _, n := range tt.names {
				name := n
				expectedGroups = append(expectedGroups, &autoscaling.Group{
					AutoScalingGroupName: &name,
				})
			}
		}
		switch {
		case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
			t.Errorf("%d: Mismatched error, actual then expected", i)
			t.Logf("%v", err)
			t.Logf("%v", tt.err)
		case !testASGEq(groups, expectedGroups):
			t.Errorf("%d: Mismatched results, actual then expected", i)
			t.Logf("%v", groups)
			t.Logf("%v", expectedGroups)
		}
	}
}

func TestAwsSetAsgDesired(t *testing.T) {
	groupName := "mygroup"
	tests := []struct {
		desired        int64
		max            int64
		canIncreaseMax bool
		setErr         error
		err            error
		verbose        bool
	}{
		{3, 3, true, nil, nil, false},
		{2, 2, true, nil, nil, false},
		{15, 15, true, awserr.New(autoscaling.ErrCodeResourceContentionFault, "", nil), fmt.Errorf("unable to increase ASG mygroup desired count to 15 - ResourceContention"), false},
		{1, 1, true, awserr.New("testabc", "", nil), fmt.Errorf("unable to increase ASG mygroup desired count to 1 - unexpected and unknown AWS error"), false},
		{25, 25, true, fmt.Errorf("testabc"), fmt.Errorf("unable to increase ASG mygroup desired count to 25 - unexpected and unknown non-AWS error"), false},
		{31, 30, false, nil, fmt.Errorf("unable to increase ASG mygroup desired size to 31 as greater than max size 30"), false},
		{31, 30, true, nil, nil, false},
	}
	for i, tt := range tests {
		asg := &autoscaling.Group{
			AutoScalingGroupName: &groupName,
			MaxSize:              &tt.max,
		}
		err := setAsgDesired(&mockAsgSvc{
			err: tt.setErr,
		}, asg, tt.desired, tt.canIncreaseMax, tt.verbose)
		switch {
		case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
			t.Errorf("%d: Mismatched error, actual then expected", i)
			t.Logf("%v", err)
			t.Logf("%v", tt.err)
		}
	}
}

func TestAwsSetAsgMax(t *testing.T) {
	groupName := "mygroup"
	tests := []struct {
		max     int64
		setErr  error
		err     error
		verbose bool
	}{
		{3, nil, nil, false},
		{2, nil, nil, false},
		{15, awserr.New(autoscaling.ErrCodeResourceContentionFault, "", nil), fmt.Errorf("unable to increase ASG mygroup max size to 15 - ResourceContention"), false},
		{1, awserr.New("testabc", "", nil), fmt.Errorf("unable to increase ASG mygroup max size to 1 - unexpected and unknown AWS error: testabc"), false},
		{25, fmt.Errorf("testabc"), fmt.Errorf("unable to increase ASG mygroup max size to 25 - unexpected and unknown non-AWS error: testabc"), false},
	}
	for i, tt := range tests {
		asg := &autoscaling.Group{
			AutoScalingGroupName: &groupName,
		}
		err := setAsgMax(&mockAsgSvc{
			err: tt.setErr,
		}, asg, tt.max, tt.verbose)
		switch {
		case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
			t.Errorf("%d: Mismatched error, actual then expected", i)
			t.Logf("%v", err)
			t.Logf("%v", tt.err)
		}
	}
}

func TestAwsGetLaunchTemplate(t *testing.T) {
	tests := []struct {
		names    []string
		ids      []string
		template *ec2.LaunchTemplate
		err      error
	}{
		{nil, nil, nil, nil}, // nothing passed, should get nothing back but no errors
		{[]string{"lt1", "lt2"}, nil, validLaunchTemplates["lt1"], nil},                          // two names match, so should get first one
		{[]string{"lt2", "lt1"}, nil, validLaunchTemplates["lt2"], nil},                          // two names match, so should get first one
		{nil, []string{"12345", "67890"}, validLaunchTemplates["12345"], nil},                    // two ids match, so should get first one
		{nil, []string{"67890", "12345"}, validLaunchTemplates["67890"], nil},                    // two ids match, so should get first one
		{[]string{"lt2", "lt1"}, []string{"67890", "12345"}, validLaunchTemplates["67890"], nil}, // ids override names
	}
	for i, tt := range tests {
		input := &ec2.DescribeLaunchTemplatesInput{
			LaunchTemplateNames: aws.StringSlice(tt.names),
			LaunchTemplateIds:   aws.StringSlice(tt.ids),
		}
		template, err := awsGetLaunchTemplate(&mockEc2Svc{}, input)
		switch {
		case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
			t.Errorf("%d: Mismatched error, actual then expected", i)
			t.Logf("%v", err)
			t.Logf("%v", tt.err)
		case (template == nil && tt.template != nil) || (template != nil && tt.template == nil):
			t.Errorf("%d: Mismatched nil/not-nil templates, actual then expected", i)
			t.Logf("%v:", template)
			t.Logf("%v:", tt.template)
		case template != nil && tt.template != nil && !testCompareLaunchTemplate(template, tt.template):
			t.Errorf("%d: Mismatched templates, actual then expected", i)
			t.Logf("%v:", template)
			t.Logf("%v:", tt.template)
		}
	}
}

func testCompareLaunchTemplate(t1, t2 *ec2.LaunchTemplate) bool {
	return t1.LaunchTemplateName == t2.LaunchTemplateName && t1.LaunchTemplateId == t2.LaunchTemplateId && t1.DefaultVersionNumber == t2.DefaultVersionNumber && t1.LatestVersionNumber == t2.LatestVersionNumber
}
