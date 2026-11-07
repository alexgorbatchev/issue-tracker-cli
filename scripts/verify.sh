#!/usr/bin/env bash
# Runs every check a change must pass. CI, the release workflow, and `just check`
# all run this script, so the three cannot disagree about what verified means.
# The coverage profile goes to a scratch directory of its own under .tmp/.
set -euo pipefail

cd "$(dirname "$0")/.."

go mod tidy -diff
scripts/check-format.sh
go build ./...
go vet ./...

mkdir -p .tmp
scratch="$(mktemp -d .tmp/verify.XXXXXX)"
trap 'rm -rf "$scratch"' EXIT
go test -race -coverprofile="$scratch/coverage.out" ./...
scripts/check-coverage.sh "$scratch/coverage.out"
