#!/usr/bin/env bash
# Copyright (c) 2026 mail.de GmbH
# SPDX-License-Identifier: MIT

set -euo pipefail

certs_image="${CERTS_IMAGE:-alpine:3.23}"
golang_image="${GO_IMAGE:-golang:1.26.5-alpine3.23}"

hash_cmd() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum
		return
	fi

	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256
		return
	fi

	printf 'No SHA-256 tool found (sha256sum/shasum).\n' >&2
	exit 1
}

manifest_digest() {
	local image="$1"
	local digest

	digest="$(docker buildx imagetools inspect "$image" --raw | hash_cmd | awk '{print $1}')"
	printf 'sha256:%s\n' "$digest"
}

printf 'certs_image=%s\n' "$certs_image"
printf 'certs_digest=%s\n' "$(manifest_digest "$certs_image")"
printf 'golang_image=%s\n' "$golang_image"
printf 'golang_digest=%s\n' "$(manifest_digest "$golang_image")"
