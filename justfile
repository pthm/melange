# Melange - PostgreSQL Authorization Library
# Run `just` to see available commands

ROOT := "."
TEST := "test"

GO_TEST := "go test"
GO_TEST_JSON := "go test -json -count=1"
GO_TEST_BENCH_MEM := "go test -bench=. -run=^$ -benchmem"

OPENFGA_PKGS := "./openfgatests/..."
OPENFGA_TEST_TIMEOUT := "5m"
OPENFGA_TEST_TIMEOUT_SHORT := "2m"
OPENFGA_TEST_TIMEOUT_LONG := "10m"
OPENFGA_BENCH_TIMEOUT := "30m"
OPENFGA_BENCH_TIMEOUT_SHORT := "10m"
OPENFGA_BENCH_TIMEOUT_TINY := "5m"

# Default recipe: show help
[group('General')]
[doc('Show available commands')]
default:
    @just --list

# Sync internal module versions (usage: just release-prepare VERSION=1.2.3 [ALLOW_DIRTY=1])
[group('Release')]
[doc('Sync internal module versions and tidy go.mod files')]
release-prepare VERSION ALLOW_DIRTY="":
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -z "{{VERSION}}" ]; then
        echo "VERSION is required (e.g. just release-prepare VERSION=1.2.3)"
        exit 1
    fi
    version="{{VERSION}}"
    if [ "${version#v}" = "$version" ]; then
        version="v$version"
    fi
    just _assert-clean ALLOW_DIRTY={{ALLOW_DIRTY}}
    printf "%s\n" "$version" > VERSION
    go mod edit -require=github.com/pthm/melange/melange@"$version"
    go mod tidy
    npm_version="${version#v}"
    NPM_VERSION="$npm_version" python3 -c 'import json, os, pathlib, sys; path = pathlib.Path("clients/typescript/package.json"); if not path.exists(): print("clients/typescript/package.json not found"); sys.exit(1); data = json.loads(path.read_text()); data["version"] = os.environ["NPM_VERSION"]; path.write_text(json.dumps(data, indent=2) + "\n")'

# Tag and push module releases (usage: just release VERSION=1.2.3 [ALLOW_DIRTY=1])
[group('Release')]
[doc('Run full release: prepare, test, commit, tag, push, and goreleaser publish')]
release VERSION="" ALLOW_DIRTY="":
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -z "{{VERSION}}" ]; then
        echo "VERSION is required (e.g. just release VERSION=1.2.3)"
        exit 1
    fi
    just release-prepare VERSION={{VERSION}} ALLOW_DIRTY={{ALLOW_DIRTY}}
    just test-openfga
    version_from_file="$(tr -d '[:space:]' < VERSION)"
    if [ -z "$version_from_file" ]; then
        echo "VERSION file is empty; run release-prepare first."
        exit 1
    fi
    if [ "${version_from_file#v}" = "$version_from_file" ]; then
        version_from_file="v$version_from_file"
    fi
    melange_version="$(awk '$1 == "github.com/pthm/melange/melange" { print $2; exit }' go.mod)"
    if [ -z "$melange_version" ]; then
        echo "Could not read melange module version from go.mod; run release-prepare first."
        exit 1
    fi
    if [ "$melange_version" != "$version_from_file" ]; then
        echo "VERSION file $version_from_file does not match go.mod $melange_version; run release-prepare first."
        exit 1
    fi
    git add VERSION go.mod go.sum clients/typescript/package.json
    git commit -m "chore(release): $version_from_file"
    root_tag="$version_from_file"
    melange_tag="melange/$version_from_file"
    for tag in "$root_tag" "$melange_tag"; do
        if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
            echo "Tag already exists: $tag"
            exit 1
        fi
        git tag -a "$tag" -m "$tag"
    done
    git push origin "$root_tag" "$melange_tag"
    if ! command -v goreleaser >/dev/null 2>&1; then
        echo "goreleaser is required (https://goreleaser.com/install/)"
        exit 1
    fi
    goreleaser release --clean

[group('Release')]
[private]
[doc('Fail if the working copy has uncommitted changes (git or jj)')]
_assert-clean ALLOW_DIRTY="":
    @set -euo pipefail; \
    if [ "{{ALLOW_DIRTY}}" = "1" ]; then \
        exit 0; \
    fi; \
    if command -v jj >/dev/null 2>&1 && [ -d .jj ]; then \
        if [ -n "$(jj diff --name-only --no-pager)" ]; then \
            echo "Working copy is dirty (jj); commit or stash before continuing."; \
            exit 1; \
        fi; \
        exit 0; \
    fi; \
    if command -v git >/dev/null 2>&1; then \
        if ! git diff --quiet; then \
            echo "Working tree has unstaged changes; commit or stash before continuing."; \
            exit 1; \
        fi; \
        if ! git diff --cached --quiet; then \
            echo "Index has staged changes; commit or stash before continuing."; \
            exit 1; \
        fi; \
        if git ls-files --others --exclude-standard --error-unmatch . >/dev/null 2>&1; then \
            echo "Working tree has untracked files; commit or stash before continuing."; \
            exit 1; \
        fi; \
        exit 0; \
    fi; \
    echo "No supported VCS (jj or git) found; set ALLOW_DIRTY=1 to bypass."; \
    exit 1


# Run all tests (unit + integration)
[group('Test')]
test: test-unit test-integration

# Run unit tests only (no database required)
[group('Test')]
test-unit:
    {{GO_TEST}} -short ./...

# Run integration tests (requires Docker)
[group('Test')]
test-integration:
    cd {{TEST}} && {{GO_TEST}} -timeout 5m ./...

# Run benchmarks (requires Docker)
# Use SCALE to limit to a specific scale: just bench SCALE=1K
[group('Test')]
bench SCALE="":
    cd {{TEST}} && {{GO_TEST_BENCH_MEM}} -timeout 30m {{ if SCALE != "" { "-bench='/" + SCALE + "'" } else { "" } }}

# Run benchmarks with short output (no sub-benchmarks)
[group('Test')]
bench-quick:
    cd {{TEST}} && {{GO_TEST}} -bench='BenchmarkCheck/1K' -run=^$ -timeout 10m -benchmem

# Run benchmarks and save to file
[group('Test')]
bench-save FILE="benchmark_results.txt":
    cd {{TEST}} && {{GO_TEST_BENCH_MEM}} -timeout 30m | tee {{FILE}}

# Run tests with race detection
[group('Test')]
test-race:
    for dir in {{ROOT}}; do (cd "$dir" && {{GO_TEST}} -race -short ./...); done
    cd {{TEST}} && {{GO_TEST}} -race -timeout 5m ./...

# Build the CLI
[group('Build')]
build:
    #!/usr/bin/env bash
    set -euo pipefail
    version=$(cat VERSION 2>/dev/null || echo "dev")
    commit=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
    date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    go build -ldflags "-X main.version=$version -X main.commit=$commit -X main.date=$date" -o bin/melange ./cmd/melange

# Build the CLI without version info (faster for development)
[group('Build')]
build-dev:
    go build -o bin/melange ./cmd/melange

# Generate root THIRD_PARTY_NOTICES from go-licenses output
[group('Release')]
[doc('Generate THIRD_PARTY_NOTICES from go-licenses data')]
licenses:
    go generate ./internal/licenses

# Install the CLI locally
[group('Build')]
install:
    #!/usr/bin/env bash
    set -euo pipefail
    version=$(cat VERSION 2>/dev/null || echo "dev")
    commit=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
    date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    go install -ldflags "-X main.version=$version -X main.commit=$commit -X main.date=$date" ./cmd/melange

# =============================================================================
# Linting and Formatting
# =============================================================================

# Format all code (Go)
[group('Lint')]
fmt: fmt-go

# Format Go code with gofumpt
[group('Lint')]
fmt-go:
    for dir in {{ROOT}} {{TEST}}; do (cd "$dir" && go tool gofumpt -w .); done


# Lint all code (Go)
[group('Lint')]
lint: lint-go

# Lint Go code with golangci-lint
[group('Lint')]
lint-go:
    for dir in {{ROOT}} {{TEST}}; do (cd "$dir" && go tool golangci-lint run ./...); done

# Install linting and formatting tools
[group('Lint')]
install-tools:
    go install tool
    mise install

# Run go vet on all packages (included in lint-go via golangci-lint)
[group('Lint')]
vet:
    for dir in {{ROOT}} {{TEST}}; do (cd "$dir" && go vet ./...); done

# Tidy all go.mod files
[group('Lint')]
tidy:
    for dir in {{ROOT}} {{TEST}}; do (cd "$dir" && go mod tidy); done

# Generate test authz package from schema
[group('Generate')]
generate:
    cd {{TEST}} && {{GO_TEST}} -run TestDB_Integration -timeout 2m -v

# Validate the test schema
[group('Generate')]
validate:
    ./bin/melange validate --schemas-dir {{TEST}}/testutil/testdata

# Clean build artifacts
[group('Build')]
clean:
    rm -rf bin/
    go clean ./...

# Run Hugo docs dev server
[group('Docs')]
docs-dev:
    cd docs && hugo mod tidy && hugo server

# Run all checks (fmt, lint, test)
[group('Test')]
check: fmt lint test

# =============================================================================
# OpenFGA Test Suite
# =============================================================================

# Run all OpenFGA feature tests
[group('OpenFGA Test')]
test-openfga:
    cd {{TEST}} && {{GO_TEST}} -count=1 -timeout {{OPENFGA_TEST_TIMEOUT}} \
        -run "TestOpenFGA_" {{OPENFGA_PKGS}}

# Run OpenFGA tests for a specific feature (e.g., just test-openfga-feature Wildcards)
[group('OpenFGA Test')]
test-openfga-feature FEATURE:
    cd {{TEST}} && {{GO_TEST}} -count=1 -timeout {{OPENFGA_TEST_TIMEOUT_SHORT}} \
        -run "TestOpenFGA_{{FEATURE}}" {{OPENFGA_PKGS}}

# Run a single OpenFGA test by name (e.g., just test-openfga-name wildcard_direct)
[group('OpenFGA Test')]
test-openfga-name NAME:
    cd {{TEST}} && OPENFGA_TEST_NAME="{{NAME}}" {{GO_TEST}} -count=1 -timeout {{OPENFGA_TEST_TIMEOUT_SHORT}} \
        -run "TestOpenFGAByName" {{OPENFGA_PKGS}}

# Run OpenFGA tests matching a regex pattern (e.g., just test-openfga-pattern "^wildcard")
[group('OpenFGA Test')]
test-openfga-pattern PATTERN:
    cd {{TEST}} && OPENFGA_TEST_PATTERN="{{PATTERN}}" {{GO_TEST}} -count=1 -timeout {{OPENFGA_TEST_TIMEOUT}} \
        -run "TestOpenFGAByPattern" {{OPENFGA_PKGS}}

# List all available OpenFGA test names
[group('OpenFGA Test')]
test-openfga-list:
    cd {{TEST}} && {{GO_TEST}} -v -count=1 -run "TestOpenFGAListAvailableTests" {{OPENFGA_PKGS}}

# Run the full OpenFGA check suite (WARNING: includes unsupported features, many will fail)
[group('OpenFGA Test')]
test-openfga-full-check:
    @echo "⚠️  Running FULL OpenFGA check suite - this includes unsupported features!"
    @echo "   Many tests will fail. Use 'just test-openfga' for supported features only."
    @echo ""
    cd {{TEST}} && {{GO_TEST}} -count=1 -timeout {{OPENFGA_TEST_TIMEOUT_LONG}} \
        -run "TestOpenFGACheckSuite" {{OPENFGA_PKGS}} || true

# Install gotestfmt if not already installed
[group('OpenFGA Test')]
install-gotestfmt:
    go install github.com/gotesttools/gotestfmt/v2/cmd/gotestfmt@latest

# =============================================================================
# OpenFGA Benchmarks
# =============================================================================

# Run all OpenFGA benchmarks
[group('OpenFGA Bench')]
bench-openfga:
    cd {{TEST}} && {{GO_TEST}} -bench="BenchmarkOpenFGA_All" -run='^$' -timeout {{OPENFGA_BENCH_TIMEOUT}} -benchmem {{OPENFGA_PKGS}}

# Run OpenFGA benchmarks for a specific category (e.g., just bench-openfga-category DirectAssignment)
[group('OpenFGA Bench')]
bench-openfga-category CATEGORY:
    cd {{TEST}} && {{GO_TEST}} -bench="BenchmarkOpenFGA_{{CATEGORY}}" -run='^$' -timeout {{OPENFGA_BENCH_TIMEOUT_SHORT}} -benchmem {{OPENFGA_PKGS}}

# Run OpenFGA benchmarks by pattern (e.g., just bench-openfga-pattern "^wildcard")
[group('OpenFGA Bench')]
bench-openfga-pattern PATTERN:
    cd {{TEST}} && OPENFGA_BENCH_PATTERN="{{PATTERN}}" {{GO_TEST}} -bench="BenchmarkOpenFGAByPattern" -run='^$' -timeout {{OPENFGA_BENCH_TIMEOUT_SHORT}} -benchmem {{OPENFGA_PKGS}}

# Run OpenFGA benchmark for a specific test by name (e.g., just bench-openfga-name wildcard_direct)
[group('OpenFGA Bench')]
bench-openfga-name NAME:
    cd {{TEST}} && OPENFGA_BENCH_NAME="{{NAME}}" {{GO_TEST}} -bench="BenchmarkOpenFGAByName" -run='^$' -timeout {{OPENFGA_BENCH_TIMEOUT_TINY}} -benchmem {{OPENFGA_PKGS}}

# Run OpenFGA checks-only benchmarks (isolates Check performance from List operations)
[group('OpenFGA Bench')]
bench-openfga-checks:
    cd {{TEST}} && {{GO_TEST}} -bench="BenchmarkOpenFGA_ChecksOnly_All" -run='^$' -timeout {{OPENFGA_BENCH_TIMEOUT}} -benchmem {{OPENFGA_PKGS}}

# Run OpenFGA benchmarks organized by category
[group('OpenFGA Bench')]
bench-openfga-by-category:
    cd {{TEST}} && {{GO_TEST}} -bench="BenchmarkOpenFGA_ByCategory" -run='^$' -timeout {{OPENFGA_BENCH_TIMEOUT}} -benchmem {{OPENFGA_PKGS}}

# Run OpenFGA benchmarks and save results to file
[group('OpenFGA Bench')]
bench-openfga-save FILE="openfga_benchmark_results.txt":
    cd {{TEST}} && {{GO_TEST}} -bench="BenchmarkOpenFGA_All" -run='^$' -timeout {{OPENFGA_BENCH_TIMEOUT}} -benchmem {{OPENFGA_PKGS}} | tee {{FILE}}

# =============================================================================
# OpenFGA Test Inspection
# =============================================================================

# Build the dumptest utility
[group('OpenFGA Inspect')]
build-dumptest:
    cd {{TEST}} && go build -o ../bin/dumptest ./cmd/dumptest

# List all available OpenFGA tests (fast, no database required)
[group('OpenFGA Inspect')]
dump-openfga-list: build-dumptest
    ./bin/dumptest

# Dump a specific OpenFGA test by name (e.g., just dump-openfga wildcard_direct)
[group('OpenFGA Inspect')]
dump-openfga NAME: build-dumptest
    ./bin/dumptest "{{NAME}}"

# Dump OpenFGA tests matching a regex pattern (e.g., just dump-openfga-pattern "^userset")
[group('OpenFGA Inspect')]
dump-openfga-pattern PATTERN: build-dumptest
    ./bin/dumptest -pattern "{{PATTERN}}"

# Dump all OpenFGA tests (warning: very long output)
[group('OpenFGA Inspect')]
dump-openfga-all: build-dumptest
    ./bin/dumptest -all

# Build the dumpsql utility
[group('OpenFGA Inspect')]
build-dumpsql:
    cd {{TEST}} && go build -o ../bin/dumpsql ./cmd/dumpsql

# Dump generated SQL for a specific OpenFGA test by name (e.g., just dump-sql wildcard_direct)
[group('OpenFGA Inspect')]
dump-sql NAME: build-dumpsql
    ./bin/dumpsql "{{NAME}}"

# Dump only model data for a specific OpenFGA test
[group('OpenFGA Inspect')]
dump-sql-models NAME: build-dumpsql
    ./bin/dumpsql -models "{{NAME}}"

# Dump only analysis data for a specific OpenFGA test
[group('OpenFGA Inspect')]
dump-sql-analysis NAME: build-dumpsql
    ./bin/dumpsql -analysis "{{NAME}}"

# Build the dumpinventory utility
[group('OpenFGA Inspect')]
build-dumpinventory:
    cd {{TEST}} && go build -o ../bin/dumpinventory ./cmd/dumpinventory

# Show codegen coverage inventory report (relations falling back to generic)
[group('OpenFGA Inspect')]
dump-inventory: build-dumpinventory
    ./bin/dumpinventory

# Show codegen inventory summary only (counts by reason)
[group('OpenFGA Inspect')]
dump-inventory-summary: build-dumpinventory
    ./bin/dumpinventory -summary

# Show only list function codegen inventory
[group('OpenFGA Inspect')]
dump-inventory-list: build-dumpinventory
    ./bin/dumpinventory -list

# Show only check function codegen inventory
[group('OpenFGA Inspect')]
dump-inventory-check: build-dumpinventory
    ./bin/dumpinventory -check

# Show codegen inventory for a specific test
[group('OpenFGA Inspect')]
dump-inventory-test NAME: build-dumpinventory
    ./bin/dumpinventory "{{NAME}}"
