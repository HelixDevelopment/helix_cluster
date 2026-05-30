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
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/helix-gateway ./cmd/helix-gateway

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates wget

COPY --from=builder /bin/helix-gateway /usr/local/bin/helix-gateway

EXPOSE 80 443 9090 6061

ENTRYPOINT ["helix-gateway"]
