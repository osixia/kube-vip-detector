# Changelog

## 1.0.0

- Detect VIP routing and maintain Kubernetes node labels.
- Configure the detector through Cobra flags and Viper environment variables.
- Provide dry-run mode.
- Build and test amd64 and arm64 container images with container-baseimage/build.
- Add GitHub Actions for Go checks, container builds, releases, image signing, and SBOM attestations.
