ARG GOLANG_IMAGE="golang:1.26.9"
ARG BASE_IMAGE="scratch"

# step 1: build container binary
FROM ${GOLANG_IMAGE} AS build

ARG IMAGE="osixia/kube-network-detector:develop"

ARG GOARCH="amd64"

ENV GOOS="linux" \
    GOARCH="${GOARCH}" \
    CGO_ENABLED=0

RUN mkdir /build
WORKDIR /build

COPY . .

RUN go build \
    -ldflags="-w -s -X 'github.com/osixia/kube-network-detector/config.ImageName=${IMAGE%:*}' -X 'github.com/osixia/kube-network-detector/config.ImageTag=${IMAGE##*:}'" \
    -o kube-network-detector \
    main.go

# step 2: create image
FROM ${BASE_IMAGE}
COPY --from=build /build/kube-network-detector /kube-network-detector

ENTRYPOINT ["/kube-network-detector"]

ARG NONROOT_GROUP_ID="65532"
ARG NONROOT_USER_ID="65532"

USER ${NONROOT_USER_ID}:${NONROOT_GROUP_ID}
