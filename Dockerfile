# Build stage
FROM golang:1.21-alpine AS builder

# Install git and ca-certificates
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /workspace

# Copy cls-controller
COPY cls-controller/ cls-controller/

# Set working directory to cls-controller
WORKDIR /workspace/cls-controller

# Download dependencies and fix missing go.sum entries
RUN go mod download
RUN go mod tidy

# Build the controller
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' \
    -a -installsuffix cgo \
    -o controller \
    cmd/controller/main.go

# Final stage - using distroless for better CA certificate support
FROM gcr.io/distroless/static:nonroot

# Copy timezone info for any time-based operations
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy the binary
COPY --from=builder /workspace/cls-controller/controller /controller

# Set entrypoint (distroless/static:nonroot already runs as non-root)
ENTRYPOINT ["/controller"]