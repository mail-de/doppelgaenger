#!/usr/bin/env bash
# Copyright (c) 2026 mail.de GmbH
# SPDX-License-Identifier: MIT

set -euo pipefail

image=""
expected_certs_digest=""
expected_golang_digest=""

while [[ $# -gt 0 ]]; do
	case "$1" in
		--image)
			image="$2"
			shift 2
			;;
		--expected-certs-digest)
			expected_certs_digest="$2"
			shift 2
			;;
		--expected-golang-digest)
			expected_golang_digest="$2"
			shift 2
			;;
		*)
			printf 'Unknown argument: %s\n' "$1" >&2
			exit 1
			;;
	esac
done

if [[ -z "$image" || -z "$expected_certs_digest" || -z "$expected_golang_digest" ]]; then
	printf 'Image and both expected digests are required.\n' >&2
	exit 1
fi

if ! docker pull "$image" >/dev/null 2>&1; then
	printf 'reason=image-missing\nshould_rebuild=true\n'
	exit 0
fi

existing_certs_digest="$(docker inspect --format '{{ index .Config.Labels "de.mail.doppelgaenger.base.certs.digest" }}' "$image" 2>/dev/null || true)"
existing_golang_digest="$(docker inspect --format '{{ index .Config.Labels "de.mail.doppelgaenger.base.golang.digest" }}' "$image" 2>/dev/null || true)"

printf 'existing_certs_digest=%s\n' "$existing_certs_digest"
printf 'existing_golang_digest=%s\n' "$existing_golang_digest"

if [[ -z "$existing_certs_digest" || -z "$existing_golang_digest" ]]; then
	printf 'reason=missing-base-digest-labels\nshould_rebuild=true\n'
elif [[ "$existing_certs_digest" != "$expected_certs_digest" ]]; then
	printf 'reason=certs-digest-changed\nshould_rebuild=true\n'
elif [[ "$existing_golang_digest" != "$expected_golang_digest" ]]; then
	printf 'reason=golang-digest-changed\nshould_rebuild=true\n'
else
	printf 'reason=up-to-date\nshould_rebuild=false\n'
fi
