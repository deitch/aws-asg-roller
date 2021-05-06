package main

import "time"

// Configs struct deals with env configuration
type Configs struct {
	Interval             time.Duration `env:"ROLLER_INTERVAL" envDefault:"30s"`
	CheckDelay           int           `env:"ROLLER_CHECK_DELAY" envDefault:"30"`
	IncreaseMax          bool          `env:"ROLLER_CAN_INCREASE_MAX" envDefault:"false"`
	IgnoreDaemonSets     bool          `env:"ROLLER_IGNORE_DAEMONSETS" envDefault:"false"`
	DeleteLocalData      bool          `env:"ROLLER_DELETE_LOCAL_DATA" envDefault:"false"`
	OriginalDesiredOnTag bool          `env:"ROLLER_ORIGINAL_DESIRED_ON_TAG" envDefault:"false"`
	ASGS                 []string      `env:"ROLLER_ASG,required" envSeparator:","`
	KubernetesEnabled    bool          `env:"ROLLER_KUBERNETES" envDefault:"true"`
	Verbose              bool          `env:"ROLLER_VERBOSE" envDefault:"false"`
}
