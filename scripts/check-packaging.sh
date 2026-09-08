#!/usr/bin/env bash
# Copyright (c) 2026 mail.de GmbH
# SPDX-License-Identifier: MIT

set -euo pipefail

repo_root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$repo_root"

fail() {
	printf 'check-packaging: %s\n' "$*" >&2
	exit 1
}

require_file() {
	[[ -f "$1" ]] || fail "missing required file: $1"
}

require_contains() {
	local path="$1"
	local pattern="$2"
	local description="$3"

	grep -Eq "$pattern" "$path" || fail "$description"
}

require_file ".dockerignore"
require_file "Dockerfile"
require_file "Dockerfile.faker"
require_file "LICENSE"
require_file "Makefile"
require_file "docs/operator/deployment.md"

# GOENV names a configuration file; it must not be used as an experiment flag.
if grep -Eq 'GOENV[[:space:]]*[:?]?=[[:space:]]*greenteagc' Makefile Dockerfile Dockerfile.faker; then
	fail "GOENV must not be used to select garbage collection"
fi

for dockerfile in Dockerfile Dockerfile.faker; do
	require_contains "$dockerfile" '^ARG GO_IMAGE=golang:1\.27\.1-alpine3\.23$' \
		"$dockerfile must use the Go 1.27.1 Alpine builder"
	require_contains "$dockerfile" '^ARG CERTS_IMAGE=alpine:3\.23$' \
		"$dockerfile must use an explicit certificate-stage image"
	require_contains "$dockerfile" '^ARG RUNTIME_IMAGE=scratch$' \
		"$dockerfile must default to a scratch runtime image"
	# The Dockerfile variables in these regular expressions are literal.
	# shellcheck disable=SC2016
	require_contains "$dockerfile" '^FROM --platform=\$BUILDPLATFORM \$\{GO_IMAGE\} AS builder$' \
		"$dockerfile builder must run on BUILDPLATFORM"
	# shellcheck disable=SC2016
	require_contains "$dockerfile" '^FROM --platform=\$BUILDPLATFORM \$\{CERTS_IMAGE\} AS runtime-files$' \
		"$dockerfile runtime-files stage must run on BUILDPLATFORM"
	require_contains "$dockerfile" 'GOFLAGS=-mod=vendor' \
		"$dockerfile must build from vendored dependencies"
	require_contains "$dockerfile" '^USER 10001:10001$' \
		"$dockerfile must run as the dedicated non-root user"
	require_contains "$dockerfile" 'COPY --chmod=0444 LICENSE /app/LICENSE' \
		"$dockerfile must include the project license"

	for label in title description version revision created source authors licenses; do
		grep -F "org.opencontainers.image.${label}" "$dockerfile" >/dev/null || \
			fail "$dockerfile is missing OCI label: ${label}"
	done

	if grep -Eq '^[[:space:]]*COPY[[:space:]]+\.[[:space:]]+\.' "$dockerfile"; then
		fail "$dockerfile must not copy the complete source tree"
	fi

	if grep -Eq '^[[:space:]]*RUN[[:space:]].*go install' "$dockerfile"; then
		fail "$dockerfile must not download build tools"
	fi

	if grep -nE '^[[:space:]]*FROM[[:space:]].*:latest([[:space:]]|$)' "$dockerfile"; then
		fail "$dockerfile must not use mutable latest base tags"
	fi
done

for ignored_path in '.git' '.github' 'build' 'dist' 'temp'; do
	grep -Eq "^${ignored_path}/?$" .dockerignore || \
		fail ".dockerignore must exclude ${ignored_path}"
done

for target in check-packaging check-release-hardening docker-build docker-smoke govulncheck release-guardrails; do
	grep -Eq "^${target}:" Makefile || fail "Makefile is missing target: ${target}"
done

guardrails_line="$(awk '/^guardrails:/{print; exit}' Makefile)"
[[ "$guardrails_line" == *"check-packaging"* ]] || \
	fail "guardrails must include packaging checks"
[[ "$guardrails_line" == *"check-release-hardening"* ]] || \
	fail "guardrails must include release hardening checks"

if [[ "$guardrails_line" == *"docker-build"* || "$guardrails_line" == *"docker-smoke"* ]]; then
	fail "guardrails must stay host-independent and must not require Docker"
fi

grep -R "Go 1.27.1" README.md docs >/dev/null || \
	fail "documentation must state the current Go toolchain"

printf 'check-packaging: hardened scratch image contracts are present\n'
