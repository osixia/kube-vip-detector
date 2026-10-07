package config

import (
	"github.com/osixia/container-baseimage/log"
)

// global variables
var (
	ImageName = "osixia/kube-vip-detector"
	ImageTag  = "develop"

	EnvironmentPrefix = "KUBE_VIP_DETECTOR"

	DefaultVIPLabelPrefix = "kube-vip-detector/"
)

// logger environment configuration
var LogEnvironmentConfig = &log.EnvironmentConfig{
	LevelKey:  "CONTAINER_LOG_LEVEL",
	FormatKey: "CONTAINER_LOG_FORMAT",
}

// Image returns the complete container image reference.
func Image() string {
	return ImageName + ":" + ImageTag
}
