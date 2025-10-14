# Build stage
FROM golang:1.21-alpine AS builder

# Install git and ca-certificates (for pulling dependencies and HTTPS)
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /workspace

# Copy the entire project structure to handle local dependencies
COPY . .

# Copy cls-controller-sdk from parent directory (this needs to be in build context)
COPY ../cls-controller-sdk ./cls-controller-sdk

# Download dependencies
RUN go mod download

# Build the controller
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' \
    -a -installsuffix cgo \
    -o controller \
    cmd/controller/main.go

# Final stage
FROM scratch

# Import ca-certificates from builder
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy the binary
COPY --from=builder /workspace/controller /controller

# Use non-root user
USER 65534:65534

# Set entrypoint
ENTRYPOINT ["/controller"]