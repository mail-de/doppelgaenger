#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../../.."
export GOCACHE="$PWD/temp/go-build-cache"
export GOTMPDIR="$PWD/temp/go-tmp"
mkdir -p "$GOCACHE" "$GOTMPDIR"

go test -mod=vendor -tags=e2e -count=1 -v ./contrib/e2e/http
