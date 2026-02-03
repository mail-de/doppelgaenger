# Build stage
FROM golang:1.23-alpine AS builder

ARG VERSION=dev

WORKDIR /app

# Enable greenteagc env
ENV GOENV=greenteagc

# Copy source code and vendor directory
COPY . .

# Build the binary using vendor directory
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -ldflags "-X main.version=${VERSION}" -o httpproxy main.go

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy the binary from the builder stage
COPY --from=builder /app/httpproxy .

# Expose the default port
EXPOSE 8443

# Run the binary
ENTRYPOINT ["./httpproxy"]
