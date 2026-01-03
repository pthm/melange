# Melange - PostgreSQL Authorization Library
# Run `just` to see available commands

# Default recipe: show help
default:
    @just --list

# Run all tests (unit + integration)
test: test-unit test-integration

# Run unit tests only (no database required)
test-unit:
    go test -short ./...
    cd tooling && go test -short ./...

# Run integration tests (requires Docker)
test-integration:
    cd test && go test -v -timeout 5m ./...

# Run tests with race detection
test-race:
    go test -race -short ./...
    cd tooling && go test -race -short ./...
    cd test && go test -race -timeout 5m ./...

# Build the CLI
build:
    go build -o bin/melange ./cmd/melange

# Install the CLI locally
install:
    go install ./cmd/melange

# Format all Go code
fmt:
    go fmt ./...
    cd tooling && go fmt ./...
    cd cmd/melange && go fmt ./...
    cd test && go fmt ./...

# Run go vet on all packages
vet:
    go vet ./...
    cd tooling && go vet ./...
    cd cmd/melange && go vet ./...
    cd test && go vet ./...

# Tidy all go.mod files
tidy:
    go mod tidy
    cd tooling && go mod tidy
    cd cmd/melange && go mod tidy
    cd test && go mod tidy

# Generate test authz package from schema
generate:
    cd test && go test -run TestDB_Integration -timeout 2m -v

# Validate the test schema
validate:
    ./bin/melange validate --schemas-dir test/testutil/testdata

# Clean build artifacts
clean:
    rm -rf bin/
    go clean ./...

# Run all checks (fmt, vet, test)
check: fmt vet test
