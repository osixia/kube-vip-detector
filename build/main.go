package main

import (
	"context"
	"os"

	"dagger.io/dagger"
	"github.com/osixia/container-baseimage/build/cmd"
	"github.com/osixia/container-baseimage/build/config"
	"github.com/osixia/container-baseimage/build/job"
)

func main() {

	// image

	var KubeNetworkDetectorImage = &config.Image{
		BaseImage:    "scratch",
		Distribution: config.None,

		Name:        "osixia/kube-network-detector",
		Description: "Kube Network Detector container image 🐳📡🌴",

		Url:           "https://github.com/osixia/kube-network-detector",
		Documentation: "https://github.com/osixia/kube-network-detector",
		Source:        "https://github.com/osixia/kube-network-detector",

		Authors: "The osixia/kube-network-detector maintainers",
		Vendor:  "Osixia",

		Licences: "MIT",
	}

	config.Images = []*config.Image{
		KubeNetworkDetectorImage,
	}

	config.DefaultImage = KubeNetworkDetectorImage

	// github

	config.ProjectGithubRepo = &config.GithubRepo{
		Organization: "osixia",
		Project:      "kube-network-detector",
	}

	// custom function

	job.BuildArgs = func(options *job.BuildImageOptions, image *config.Image, platform *config.Platform, tag string) []dagger.BuildArg {

		var buildArgs = job.DefaultBuildArgs(options, image, platform, tag)

		// platform build args
		goarchArg := dagger.BuildArg{
			Name:  "GOARCH",
			Value: platform.GoArch,
		}

		// nonroot group args
		nonrootGroupIDArg := dagger.BuildArg{
			Name:  "NONROOT_GROUP_ID",
			Value: "65532",
		}

		// nonroot user args
		nonrootUserIDArg := dagger.BuildArg{
			Name:  "NONROOT_USER_ID",
			Value: "65532",
		}

		return append(buildArgs, goarchArg, nonrootGroupIDArg, nonrootUserIDArg)
	}

	// execute cmd

	mainCtx := context.Background()
	if err := cmd.Run(mainCtx); err != nil {
		os.Exit(1)
	}

}
