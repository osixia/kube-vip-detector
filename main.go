package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/osixia/container-baseimage/helpers"
	"github.com/osixia/container-baseimage/log"

	"github.com/osixia/kube-network-detector/cmd"
	"github.com/osixia/kube-network-detector/config"
)

func main() {

	// set logger environment variables configuration
	helpers.Mustf(log.SetEnvironmentConfig(config.LogEnvironmentConfig), "Error initializing logger environment")

	// execute cmd
	mainCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := cmd.Run(mainCtx); err != nil {
		os.Exit(1)
	}

}
