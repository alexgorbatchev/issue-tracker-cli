set dotenv-load := false

# Default recipe: list available recipes
default:
    @just --list

# Download project dependencies for local development
setup:
    go mod download

# Run CLI in human mode (default)
run *args:
    go run ./cmd/issue-tracker {{args}}

# Run CLI in agent-facing mode
run-ai *args:
    AGENT=1 go run ./cmd/issue-tracker {{args}}

# Run test suite
test:
    go test -race -cover ./...

# Check per-package coverage the way CI does, in a scratch directory of its own
coverage:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p .tmp
    scratch="$(mktemp -d .tmp/coverage.XXXXXX)"
    trap 'rm -rf "$scratch"' EXIT
    go test -race -coverprofile="$scratch/coverage.out" ./...
    scripts/check-coverage.sh "$scratch/coverage.out"

# Build binary into bin/
build:
    mkdir -p bin
    go build -o bin/issue-tracker ./cmd/issue-tracker

# Install binary to GOPATH bin directory
install:
    go install ./cmd/issue-tracker

# Check module hygiene and formatting, then vet
lint:
    go mod tidy -diff
    scripts/check-format.sh
    go vet ./...

# Alias for lint
vet: lint

# Format Go code
fmt:
    go fmt ./...

# Run static analysis and test suite in sequence
check: lint test

# Remove build outputs; .tmp/ holds other sessions' scratch and stays
clean:
    rm -rf bin coverage.out dist
