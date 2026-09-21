# Multi-stage build for the jev-mcp binary. The final image is distroless
# static (no shell, no package manager) and runs as the nonroot user, so the
# only thing in it is the binary and the CA bundle.
#
# Build:  docker build -t jev-mcp .
# Run:    docker run --rm jev-mcp -version
#
# The toolchain image tag tracks the go directive in go.mod; bump both together.
FROM golang:1.27 AS build

WORKDIR /src

# Download modules in their own layer so a source-only change does not refetch.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off so the binary is static and runs in distroless/static. TARGETOS and
# TARGETARCH come from buildx when building multi-platform images; a plain
# docker build leaves them empty and Go falls back to the host platform.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/jev-mcp ./cmd/jev-mcp

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/jev-mcp /jev-mcp

USER nonroot:nonroot
ENTRYPOINT ["/jev-mcp"]
