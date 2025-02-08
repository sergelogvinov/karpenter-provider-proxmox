# syntax = docker/dockerfile:1.13
########################################

FROM golang:1.23-bookworm AS develop

WORKDIR /src
COPY ["go.mod", "go.sum", "/src"]
RUN go mod download

########################################

FROM --platform=${BUILDPLATFORM} golang:1.23.5-alpine3.21 AS builder
RUN apk update && apk add --no-cache make
ENV GO111MODULE=on
WORKDIR /src

COPY ["go.mod", "go.sum", "/src"]
RUN go mod download && go mod verify

COPY . .
ARG TAG
ARG SHA
RUN make build-all-archs

########################################

FROM --platform=${TARGETARCH} scratch AS karpenter-provider-proxmox
LABEL org.opencontainers.image.source="https://github.com/sergelogvinov/karpenter-provider-proxmox" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.description="karpenter provider for Proxmox VE"

COPY --from=gcr.io/distroless/static-debian12:nonroot . .
ARG TARGETARCH
COPY --from=builder /src/bin/karpenter-provider-proxmox-${TARGETARCH} /bin/karpenter-provider-proxmox

ENTRYPOINT ["/bin/karpenter-provider-proxmox"]
