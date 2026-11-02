# Multi-stage build producing a tiny image with just the static `verity` binary.
# Result is based on scratch + CA certs: a few MB, no shell, minimal surface.

# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download

# Build a fully static binary (CGO disabled) so it runs on scratch.
COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/verity ./cmd/verity

# ---- runtime stage ----
FROM scratch
# CA certificates so HTTPS targets verify correctly.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/verity /usr/local/bin/verity

# Run as non-root.
USER 65534:65534
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/verity"]
CMD ["--help"]
