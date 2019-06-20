package main

import (
	"os"
	"testing"
)

func TestGetDelay(t *testing.T) {
	tests := []struct {
		name        string
		want        int
		envValue    string
		shouldError bool
	}{
		{"should return default", 30, "", false},
		{"should return override", 17, "17", false},
		{"should error if override invalid", 0, "fake", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv("ROLLER_CHECK_DELAY")

			if tt.envValue != "" {
				os.Setenv("ROLLER_CHECK_DELAY", tt.envValue)
			}

			got, err := getDelay()
			if err != nil {
				if !tt.shouldError {
					t.Errorf("getDelay() returned error: %s", err.Error())
				}
			} else {
				if tt.shouldError {
					t.Error("getDelay() should have returned error")
				} else if got != tt.want {
					t.Errorf("getDelay() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}
