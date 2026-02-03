BINARY_NAME=httpproxy
VERSION=$(shell git describe --tags --always --dirty)
GO_FILES=$(shell find . -name "*.go" -not -path "./vendor/*")
export GOENV=greenteagc
GOFLAGS=-mod=vendor
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

.PHONY: all build clean test docker-build docker-run

all: build

build:
	go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME) main.go

clean:
	rm -f $(BINARY_NAME)

test:
	go test $(GOFLAGS) -v ./...

docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY_NAME) .

docker-run:
	docker run -p 8443:8443 $(BINARY_NAME)
