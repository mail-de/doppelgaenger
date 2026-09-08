BINARY_NAME := doppelgaenger
FAKE_BINARY_NAME := fakehttpserver
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= unknown
IMAGE_SOURCE ?= https://github.com/mail-de/doppelgaenger
IMAGE_TAG ?= $(BINARY_NAME):$(VERSION)
FAKE_IMAGE_TAG ?= $(FAKE_BINARY_NAME):$(VERSION)
GO_IMAGE ?= golang:1.27.1-alpine3.23
CERTS_IMAGE ?= alpine:3.23
RUNTIME_IMAGE ?= scratch
DOCKER ?= docker
GO ?= go
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK ?= govulncheck
GOVULNCHECK_GOFLAGS ?= -mod=vendor
GOVULNCHECK_SCAN ?= package
GO_FILES := $(shell find . -name '*.go' -not -path './vendor/*')
GOFLAGS := -mod=vendor
LDFLAGS := -ldflags "-s -w -buildid= -X main.version=$(VERSION)"
SYFT_VERSION ?= v1.16.0

export GOEXPERIMENT ?= runtimesecret

.PHONY: all vet lint-config lint fix build build-check build-fake clean test race \
	e2e-http e2e-milter e2e-grpc e2e-docker e2e \
	docker-build docker-build-fake docker-build-all docker-smoke docker-run sbom \
	check-packaging check-release-hardening check-release-packages guardrails govulncheck \
	release-guardrails install-hooks

all: build build-fake

vet:
	$(GO) vet $(GOFLAGS) ./...

lint-config:
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { echo "$(GOLANGCI_LINT) not found. Install it and rerun make guardrails"; exit 1; }
	$(GOLANGCI_LINT) config verify

lint: lint-config
	$(GOLANGCI_LINT) run ./...

fix:
	gofmt -w $(GO_FILES)

build:
	mkdir -p build
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -trimpath -buildvcs=false $(LDFLAGS) -o build/$(BINARY_NAME) .

build-check:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -trimpath -buildvcs=false ./...

build-fake:
	mkdir -p build
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -trimpath -buildvcs=false $(LDFLAGS) -o build/$(FAKE_BINARY_NAME) ./cmd/fakehttpserver

clean:
	rm -f build/$(BINARY_NAME) build/$(FAKE_BINARY_NAME)

test:
	$(GO) test $(GOFLAGS) -v ./...

race:
	$(GO) test $(GOFLAGS) -race -short $$(go list $(GOFLAGS) ./... | grep -v /vendor/)

e2e-http:
	contrib/e2e/http/run.sh

e2e-milter:
	contrib/e2e/milter/run.sh

e2e-grpc:
	contrib/e2e/grpc/run.sh

e2e-docker:
	contrib/e2e/docker/run.sh

e2e: e2e-http e2e-milter e2e-grpc e2e-docker

check-packaging:
	bash ./scripts/check-packaging.sh

check-release-hardening:
	bash ./scripts/check-release-hardening.sh

check-release-packages:
	bash ./scripts/check-release-packages.sh

docker-build:
	$(DOCKER) build \
		--file Dockerfile \
		--build-arg VERSION="$(VERSION)" \
		--build-arg REVISION="$(REVISION)" \
		--build-arg BUILD_DATE="$(BUILD_DATE)" \
		--build-arg SOURCE="$(IMAGE_SOURCE)" \
		--build-arg GO_IMAGE="$(GO_IMAGE)" \
		--build-arg CERTS_IMAGE="$(CERTS_IMAGE)" \
		--build-arg RUNTIME_IMAGE="$(RUNTIME_IMAGE)" \
		--tag "$(IMAGE_TAG)" \
		.

docker-build-fake:
	$(DOCKER) build \
		--file Dockerfile.faker \
		--build-arg VERSION="$(VERSION)" \
		--build-arg REVISION="$(REVISION)" \
		--build-arg BUILD_DATE="$(BUILD_DATE)" \
		--build-arg SOURCE="$(IMAGE_SOURCE)" \
		--build-arg GO_IMAGE="$(GO_IMAGE)" \
		--build-arg CERTS_IMAGE="$(CERTS_IMAGE)" \
		--build-arg RUNTIME_IMAGE="$(RUNTIME_IMAGE)" \
		--tag "$(FAKE_IMAGE_TAG)" \
		.

docker-build-all: docker-build docker-build-fake

docker-smoke: docker-build
	@set -e; \
	output="$$( $(DOCKER) run --rm --network=none --read-only --tmpfs /tmp:rw,noexec,nosuid,size=16m "$(IMAGE_TAG)" --version )"; \
	case "$$output" in \
		"doppelgaenger "*) printf '%s\n' "$$output" ;; \
		*) printf 'docker-smoke: unexpected version output: %s\n' "$$output" >&2; exit 1 ;; \
	esac

docker-run:
	$(DOCKER) run --rm -p 8443:8443 \
		--mount type=bind,src="$(CURDIR)/config.docker.yaml",dst=/etc/doppelgaenger/config.yaml,readonly \
		"$(IMAGE_TAG)"

sbom:
	bash ./scripts/sbom.sh \
		--output-dir sbom \
		--output-prefix $(BINARY_NAME) \
		--skip-docker \
		--syft-version $(SYFT_VERSION)

guardrails: check-packaging check-release-hardening check-release-packages fix vet lint test race e2e build-check

govulncheck:
	@command -v $(GOVULNCHECK) >/dev/null 2>&1 || { echo "$(GOVULNCHECK) not found. Install it with: go install golang.org/x/vuln/cmd/govulncheck@latest"; exit 1; }
	CGO_ENABLED=0 GOFLAGS="$(GOVULNCHECK_GOFLAGS)" $(GOVULNCHECK) -scan=$(GOVULNCHECK_SCAN) ./...

release-guardrails: guardrails govulncheck

install-hooks:
	bash ./scripts/install-hooks.sh
