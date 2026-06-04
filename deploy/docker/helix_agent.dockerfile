# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git make

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/helix-agent ./cmd/helix-agent

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates wget

COPY --from=builder /bin/helix-agent /usr/local/bin/helix-agent

# Image default user is unprivileged (DS-0002: image user must not be root).
# The node DaemonSet that needs host /proc + /sys introspection overrides this
# with an explicit privileged securityContext at deploy time; the image itself
# must not default to root.
RUN addgroup -g 65532 -S nonroot && adduser -u 65532 -S nonroot -G nonroot
USER 65532:65532

EXPOSE 8081 51820 9090 6062

ENTRYPOINT ["helix-agent"]
