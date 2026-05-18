BINARY_NAME=doppelgaenger
FAKE_BINARY_NAME=fakehttpserver
VERSION=$(shell git describe --tags --always --dirty)
GO_FILES=$(shell find . -name "*.go" -not -path "./vendor/*")
SBOM_FILE=sbom.cdx.json
SBOM_TOOL=github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@v1.9.0
export GOENV=greenteagc
GOFLAGS=-mod=vendor
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

.PHONY: all vet lint fix build build-check build-fake clean test race docker-build docker-build-fake docker-run sbom guardrails

all: build build-fake

vet:
	go vet $(GOFLAGS) ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not found. Install it and rerun make guardrails"; exit 1; }
	golangci-lint run ./...

fix:
	gofmt -w $(GO_FILES)

build:
	mkdir -p build
	go build $(GOFLAGS) $(LDFLAGS) -o build/$(BINARY_NAME) main.go

build-check:
	go build $(GOFLAGS) ./...

build-fake:
	mkdir -p build
	go build $(GOFLAGS) $(LDFLAGS) -o build/$(FAKE_BINARY_NAME) cmd/fakehttpserver/main.go

clean:
	rm -f build/$(BINARY_NAME) build/$(FAKE_BINARY_NAME)

test:
	go test $(GOFLAGS) -v ./...

race:
	go test $(GOFLAGS) -race -short $$(go list $(GOFLAGS) ./... | grep -v /vendor/)

docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY_NAME) .

docker-build-fake:
	docker build --build-arg VERSION=$(VERSION) -t $(FAKE_BINARY_NAME) -f Dockerfile.faker .

docker-run:
	docker run -p 8443:8443 $(BINARY_NAME)

sbom:
	go run $(SBOM_TOOL) mod -output $(SBOM_FILE) -json

guardrails: fix vet lint test race build-check
