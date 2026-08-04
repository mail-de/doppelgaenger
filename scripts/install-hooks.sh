#!/usr/bin/env bash
# Copyright (c) 2026 mail.de GmbH
# SPDX-License-Identifier: MIT

set -euo pipefail

project_root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
hooks_dir="${project_root}/.git/hooks"

if [[ ! -d "${project_root}/.git" ]]; then
	printf 'install-hooks: %s is not a Git working tree\n' "$project_root" >&2
	exit 1
fi

mkdir -p "$hooks_dir"

cat >"${hooks_dir}/pre-push" <<'HOOKEOF'
#!/usr/bin/env bash
set -euo pipefail

git_root="$(git rev-parse --show-toplevel)"
exec "${git_root}/scripts/pre-push-govulncheck.sh" "$@"
HOOKEOF

chmod +x "${hooks_dir}/pre-push" "${project_root}/scripts/pre-push-govulncheck.sh"
printf 'Installed pre-push govulncheck hook for main and version tags.\n'
