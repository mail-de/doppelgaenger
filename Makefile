BINARY_NAME=doppelgaenger
FAKE_BINARY_NAME=fakehttpserver
VERSION=$(shell git describe --tags --always --dirty)
GO_FILES=$(shell find . -name "*.go" -not -path "./vendor/*")
SBOM_FILE=sbom.cdx.json
SBOM_TOOL=github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@v1.9.0
export GOENV=greenteagc
GOFLAGS=-mod=vendor
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

.PHONY: all vet fix build build-fake clean test docker-build docker-build-fake docker-run sbom

all: build build-fake

vet:
	go vet $(GOFLAGS) ./...

fix:
	gofmt -w $(GO_FILES)

build:
	mkdir -p build
	go build $(GOFLAGS) $(LDFLAGS) -o build/$(BINARY_NAME) main.go

build-fake:
	mkdir -p build
	go build $(GOFLAGS) $(LDFLAGS) -o build/$(FAKE_BINARY_NAME) cmd/fakehttpserver/main.go

clean:
	rm -f build/$(BINARY_NAME) build/$(FAKE_BINARY_NAME)

test:
	go test $(GOFLAGS) -v ./...

docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY_NAME) .

docker-build-fake:
	docker build --build-arg VERSION=$(VERSION) -t $(FAKE_BINARY_NAME) -f Dockerfile.faker .

docker-run:
	docker run -p 8443:8443 $(BINARY_NAME)

sbom:
	go run $(SBOM_TOOL) mod -output $(SBOM_FILE) -json
