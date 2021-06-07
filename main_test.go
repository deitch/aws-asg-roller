package main

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setBaseEnvs() {
	os.Clearenv()

	os.Setenv("ROLLER_ASG", "group1")
	os.Setenv("ROLLER_INTERVAL", "30s")
}

func TestGetConfigs(t *testing.T) {
	tests := []struct {
		env         string
		name        string
		field       string
		want        interface{}
		envValue    string
		shouldError bool
	}{
		// check delay gets translated to interval
		{"ROLLER_CHECK_DELAY", "should return default", "Interval", time.Duration(30 * time.Second), "", false},
		{"ROLLER_CHECK_DELAY", "should return override", "Interval", time.Duration(17 * time.Second), "17", false},
		{"ROLLER_CHECK_DELAY", "should fail due to wrong type", "CheckDelay", 0, "17s", true},
		{"ROLLER_CHECK_DELAY", "should error if override invalid", "CheckDelay", 0, "fake", true},
		{"ROLLER_INTERVAL", "should return default", "Interval", time.Duration(30 * time.Second), "", false},
		{"ROLLER_INTERVAL", "should fail due to wrong type", "Interval", 0, "17", true},
		{"ROLLER_INTERVAL", "should return override", "Interval", time.Duration(17 * time.Second), "17s", false},
		{"ROLLER_INTERVAL", "should error if override invalid", "Interval", 0, "fake", true},
		{"ROLLER_ASG", "should error on empty", "ASGS", 0, "", true},
		{"ROLLER_ASG", "should work with single value", "ASGS", []string{"grp1"}, "grp1", false},
		{"ROLLER_ASG", "should work with multiple values", "ASGS", []string{"grp1", "grp2"}, "grp1,grp2", false},
		{"ROLLER_ASG", "should work with multiple values with space after comma", "ASGS", []string{"grp1", " grp2"}, "grp1, grp2", false},
	}
	for _, tt := range tests {
		t.Run(tt.env+":"+tt.name, func(t *testing.T) {
			setBaseEnvs()
			os.Unsetenv(tt.env)

			if tt.envValue != "" {
				os.Setenv(tt.env, tt.envValue)
			}

			if tt.shouldError {
				require.Panics(t, func() {
					getConfigs()
				})
			} else {
				got := getConfigs()
				// use reflect to access struct dynamically
				r := reflect.ValueOf(got)
				f := reflect.Indirect(r).FieldByName(tt.field).Interface()
				assert.EqualValues(t, tt.want, f)
			}
		})
	}
}
