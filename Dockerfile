ARG GOLANG_IMAGE="golang:1.25"
ARG BASE_IMAGE="scratch"

# step 1: build container binary
FROM ${GOLANG_IMAGE} AS build

ARG IMAGE="osixia/kube-vip-detector:develop"

ARG GOARCH="amd64"

ENV GOOS="linux" \
    GOARCH="${GOARCH}" \
    CGO_ENABLED=0

RUN mkdir /build
WORKDIR /build

COPY . .

RUN go build \
    -ldflags="-w -s -X 'github.com/osixia/kube-vip-detector/config.ImageName=${IMAGE%:*}' -X 'github.com/osixia/kube-vip-detector/config.ImageTag=${IMAGE##*:}'" \
    -o kube-vip-detector \
    main.go

# step 2: create image
FROM ${BASE_IMAGE}
COPY --from=build /build/kube-vip-detector /kube-vip-detector

ENTRYPOINT ["/kube-vip-detector"]

ARG NONROOT_GROUP_ID="65532"
ARG NONROOT_USER_ID="65532"

USER ${NONROOT_USER_ID}:${NONROOT_GROUP_ID}
