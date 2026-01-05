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

# Run benchmarks (requires Docker)
# Use SCALE to limit to a specific scale: just bench SCALE=1K
bench SCALE="":
    cd test && go test -bench=. -run=^$ -timeout 30m -benchmem {{ if SCALE != "" { "-bench='/" + SCALE + "'" } else { "" } }}

# Run benchmarks with short output (no sub-benchmarks)
bench-quick:
    cd test && go test -bench='BenchmarkCheck/1K' -run=^$ -timeout 10m -benchmem

# Run benchmarks and save to file
bench-save FILE="benchmark_results.txt":
    cd test && go test -bench=. -run=^$ -timeout 30m -benchmem | tee {{FILE}}

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

# =============================================================================
# Linting and Formatting
# =============================================================================

# Format all code (Go + SQL)
fmt: fmt-go fmt-sql

# Format Go code with gofumpt
fmt-go:
    gofumpt -w .
    cd tooling && gofumpt -w .
    cd test && gofumpt -w .

# Format SQL files with sqruff
fmt-sql:
    mise exec -- sqruff fix sql/

# Lint all code (Go + SQL)
lint: lint-go lint-sql

# Lint Go code with golangci-lint
lint-go:
    golangci-lint run ./...
    cd tooling && golangci-lint run ./...
    cd test && golangci-lint run ./...

# Lint SQL files with sqruff
lint-sql:
    mise exec -- sqruff lint sql/

# Install linting and formatting tools
install-tools:
    go install tool
    mise install

# Run go vet on all packages (included in lint-go via golangci-lint)
vet:
    go vet ./...
    cd tooling && go vet ./...
    cd test && go vet ./...

# Tidy all go.mod files
tidy:
    go mod tidy
    cd tooling && go mod tidy
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

# Run Hugo docs dev server
docs-dev:
    cd docs && git submodule update --init --recursive && hugo server

# Run all checks (fmt, lint, test)
check: fmt lint test

# =============================================================================
# OpenFGA Test Suite
# =============================================================================

# Run all OpenFGA feature tests (uses gotestfmt for pretty output)
test-openfga:
    cd test && go test -json -count=1 -timeout 5m \
        -run "TestOpenFGA_" ./openfgatests/... 2>&1 | gotestfmt

# Run OpenFGA tests for a specific feature (e.g., just test-openfga-feature Wildcards)
test-openfga-feature FEATURE:
    cd test && go test -json -count=1 -timeout 2m \
        -run "TestOpenFGA_{{FEATURE}}" ./openfgatests/... 2>&1 | gotestfmt

# Run a single OpenFGA test by name (e.g., just test-openfga-name wildcard_direct)
test-openfga-name NAME:
    cd test && OPENFGA_TEST_NAME="{{NAME}}" go test -v -count=1 -timeout 2m \
        -run "TestOpenFGAByName" ./openfgatests/...

# Run OpenFGA tests matching a regex pattern (e.g., just test-openfga-pattern "^wildcard")
test-openfga-pattern PATTERN:
    cd test && OPENFGA_TEST_PATTERN="{{PATTERN}}" go test -v -count=1 -timeout 5m \
        -run "TestOpenFGAByPattern" ./openfgatests/...

# List all available OpenFGA test names
test-openfga-list:
    cd test && go test -v -count=1 -run "TestOpenFGAListAvailableTests" ./openfgatests/...

# Run OpenFGA tests in verbose mode (without gotestfmt)
test-openfga-verbose:
    cd test && go test -v -count=1 -timeout 5m \
        -run "TestOpenFGA_" ./openfgatests/...

# Run the full OpenFGA check suite (WARNING: includes unsupported features, many will fail)
test-openfga-full-check:
    @echo "⚠️  Running FULL OpenFGA check suite - this includes unsupported features!"
    @echo "   Many tests will fail. Use 'just test-openfga' for supported features only."
    @echo ""
    cd test && go test -json -count=1 -timeout 10m \
        -run "TestOpenFGACheckSuite" ./openfgatests/... 2>&1 | gotestfmt || true

# Install gotestfmt if not already installed
install-gotestfmt:
    go install github.com/gotesttools/gotestfmt/v2/cmd/gotestfmt@latest

# =============================================================================
# OpenFGA Benchmarks
# =============================================================================

# Run all OpenFGA benchmarks
bench-openfga:
    cd test && go test -bench="BenchmarkOpenFGA_All" -run='^$$' -timeout 30m -benchmem ./openfgatests/...

# Run OpenFGA benchmarks for a specific category (e.g., just bench-openfga-category DirectAssignment)
bench-openfga-category CATEGORY:
    cd test && go test -bench="BenchmarkOpenFGA_{{CATEGORY}}" -run='^$$' -timeout 10m -benchmem ./openfgatests/...

# Run OpenFGA benchmarks by pattern (e.g., just bench-openfga-pattern "^wildcard")
bench-openfga-pattern PATTERN:
    cd test && OPENFGA_BENCH_PATTERN="{{PATTERN}}" go test -bench="BenchmarkOpenFGAByPattern" -run='^$$' -timeout 10m -benchmem ./openfgatests/...

# Run OpenFGA benchmark for a specific test by name (e.g., just bench-openfga-name wildcard_direct)
bench-openfga-name NAME:
    cd test && OPENFGA_BENCH_NAME="{{NAME}}" go test -bench="BenchmarkOpenFGAByName" -run='^$$' -timeout 5m -benchmem ./openfgatests/...

# Run OpenFGA checks-only benchmarks (isolates Check performance from List operations)
bench-openfga-checks:
    cd test && go test -bench="BenchmarkOpenFGA_ChecksOnly_All" -run='^$$' -timeout 30m -benchmem ./openfgatests/...

# Run OpenFGA benchmarks organized by category
bench-openfga-by-category:
    cd test && go test -bench="BenchmarkOpenFGA_ByCategory" -run='^$$' -timeout 30m -benchmem ./openfgatests/...

# Run OpenFGA benchmarks and save results to file
bench-openfga-save FILE="openfga_benchmark_results.txt":
    cd test && go test -bench="BenchmarkOpenFGA_All" -run='^$$' -timeout 30m -benchmem ./openfgatests/... | tee {{FILE}}

# =============================================================================
# OpenFGA Test Inspection
# =============================================================================

# Build the dumptest utility
build-dumptest:
    cd test && go build -o ../bin/dumptest ./cmd/dumptest

# List all available OpenFGA tests (fast, no database required)
dump-openfga-list: build-dumptest
    ./bin/dumptest

# Dump a specific OpenFGA test by name (e.g., just dump-openfga wildcard_direct)
dump-openfga NAME: build-dumptest
    ./bin/dumptest "{{NAME}}"

# Dump OpenFGA tests matching a regex pattern (e.g., just dump-openfga-pattern "^userset")
dump-openfga-pattern PATTERN: build-dumptest
    ./bin/dumptest -pattern "{{PATTERN}}"

# Dump all OpenFGA tests (warning: very long output)
dump-openfga-all: build-dumptest
    ./bin/dumptest -all
