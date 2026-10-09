# Unreleased

- Discover namespaced VIP and Peer CRDs by default alongside existing flags, with live target updates, VIP deduplication, namespaced peer labels, and restart-safe CRD label cleanup. Add --disable-crds for static-only deployments.

- Update golang.org/x/net to v0.60.0 and build with Go 1.26.9 for HTTP/2 security fixes.

- Support IPv6 VIPs with Kubernetes-safe IPv6 label keys.

- Rename the project to kube-network-detector.
- Add named IPv4/IPv6 TCP peers with per-node labels and automatic VIP/peer mode selection.
- Add Node get permission for peer reconciliation.

# Changelog

## 1.0.0

- Detect VIP routing and maintain Kubernetes node labels.
- Configure the detector through Cobra flags and Viper environment variables.
- Provide dry-run mode.
- Build and test amd64 and arm64 container images with container-baseimage/build.
- Add GitHub Actions for Go checks, container builds, releases, image signing, and SBOM attestations.
