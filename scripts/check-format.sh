#!/usr/bin/env bash
# Fails when gofmt would change a tracked Go file, or cannot read one. It checks
# tracked files only, because .workspaces/ holds other checkouts. gofmt reports
# unreadable files on stderr and keeps listing the rest, so both reach the reader.
set -euo pipefail

status=0
unformatted="$(git ls-files -z '*.go' | xargs -0 gofmt -l)" || status=$?
if [ -n "$unformatted" ]; then
    printf '%s\n' "$unformatted" >&2
    status=1
fi
exit "$status"
