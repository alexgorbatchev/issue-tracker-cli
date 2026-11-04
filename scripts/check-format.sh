#!/usr/bin/env bash
# Fails when gofmt would change a tracked Go file, or cannot read one. It checks
# tracked files only, because .workspaces/ holds other checkouts.
set -euo pipefail

unformatted="$(git ls-files -z '*.go' | xargs -0 gofmt -l)"
if [ -n "$unformatted" ]; then
    printf '%s\n' "$unformatted" >&2
    exit 1
fi
