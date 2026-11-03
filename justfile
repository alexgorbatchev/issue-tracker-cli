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

# Check per-package coverage the way CI does
coverage:
    mkdir -p .tmp
    go test -race -coverprofile=.tmp/coverage.out ./...
    scripts/check-coverage.sh .tmp/coverage.out

# Build binary into bin/
build:
    mkdir -p bin
    go build -o bin/issue-tracker ./cmd/issue-tracker

# Install binary to GOPATH bin directory
install:
    go install ./cmd/issue-tracker

# Run static analysis and vet
lint:
    go mod tidy -diff
    go vet ./...

# Alias for lint
vet: lint

# Format Go code
fmt:
    go fmt ./...

# Run static analysis and test suite in sequence
check: lint test

# Clean up build binaries and temporary files
clean:
    rm -rf bin coverage.out .tmp dist
