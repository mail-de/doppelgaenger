BINARY_NAME=httpproxy
VERSION=$(shell git describe --tags --always --dirty)
GO_FILES=$(shell find . -name "*.go" -not -path "./vendor/*")
SBOM_FILE=sbom.cdx.json
SBOM_TOOL=github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@latest
export GOENV=greenteagc
GOFLAGS=-mod=vendor
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

.PHONY: all build clean test docker-build docker-run sbom

all: build

build:
	mkdir -p build
	go build $(GOFLAGS) $(LDFLAGS) -o build/$(BINARY_NAME) main.go

clean:
	rm -f build/$(BINARY_NAME)

test:
	go test $(GOFLAGS) -v ./...

docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY_NAME) .

docker-run:
	docker run -p 8443:8443 $(BINARY_NAME)

sbom:
	go run $(SBOM_TOOL) mod -output $(SBOM_FILE) -json
