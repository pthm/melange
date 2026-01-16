#!/usr/bin/env bash
#
# Integration test runner for all language clients.
#
# This script:
# 1. Runs Go integration tests (uses testcontainers automatically)
# 2. Starts a database for TypeScript tests (if DATABASE_URL not set)
# 3. Runs TypeScript integration tests
# 4. Reports results and cleans up

set -euo pipefail

# Colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo -e "${BLUE}════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}  Melange Multi-Language Integration Tests${NC}"
echo -e "${BLUE}════════════════════════════════════════════════════════${NC}"
echo ""

# Track if we started a database for TypeScript (for cleanup)
STARTED_TS_DB=false
TS_DB_CONTAINER=""

# Cleanup function
cleanup() {
    if [ "$STARTED_TS_DB" = true ] && [ -n "$TS_DB_CONTAINER" ]; then
        echo ""
        echo -e "${YELLOW}Cleaning up TypeScript test database...${NC}"
        docker stop "$TS_DB_CONTAINER" >/dev/null 2>&1 || true
        docker rm "$TS_DB_CONTAINER" >/dev/null 2>&1 || true
        echo -e "${GREEN}✓ Cleanup complete${NC}"
    fi
}

# Register cleanup on exit
trap cleanup EXIT

# Function to run tests with proper error handling
run_test_suite() {
    local name=$1
    local command=$2
    local dir=$3

    echo -e "${YELLOW}▶ Running ${name} tests...${NC}"
    echo ""

    cd "${PROJECT_ROOT}/${dir}"
    if eval "${command}"; then
        echo -e "${GREEN}✓ ${name} tests passed${NC}"
        echo ""
        return 0
    else
        echo -e "${RED}✗ ${name} tests failed${NC}"
        echo ""
        return 1
    fi
}

# Track results (bash 3.2 compatible)
GO_RESULT=0
TS_RESULT=0

# Step 1: Run Go integration tests (uses testcontainers automatically)
echo -e "${YELLOW}Step 1/2: Running Go integration tests${NC}"
echo -e "${YELLOW}  (Go tests will start their own testcontainer)${NC}"
echo ""

if run_test_suite "Go" "go test -timeout 5m ./..." "test"; then
    GO_RESULT=0
else
    GO_RESULT=1
fi

# Step 2: Run TypeScript integration tests
echo -e "${YELLOW}Step 2/2: Running TypeScript integration tests${NC}"
echo ""

# Check if TypeScript needs a database
TS_DATABASE_URL=""
if [ -n "${DATABASE_URL:-}" ]; then
    echo -e "${GREEN}✓ Using existing DATABASE_URL for TypeScript tests${NC}"
    echo "  Database: ${DATABASE_URL}"
    echo ""
    TS_DATABASE_URL="${DATABASE_URL}"
else
    # Start a database for TypeScript only
    echo "Starting database for TypeScript tests..."

    if ! command -v docker >/dev/null 2>&1; then
        echo -e "${RED}✗ Docker not found${NC}"
        echo "Please install Docker or set DATABASE_URL"
        TS_RESULT=1
    else
        TS_DATABASE_URL=$("${PROJECT_ROOT}/scripts/start-test-db.sh")
        STARTED_TS_DB=true
        TS_DB_CONTAINER="melange-test-db"

        echo -e "${GREEN}✓ Database started for TypeScript${NC}"
        echo "  URL: ${TS_DATABASE_URL}"
        echo ""

        # Apply melange schema to the TypeScript test database
        echo "Applying melange schema to database..."
        cd "${PROJECT_ROOT}"

        # Create domain tables
        if ! psql "${TS_DATABASE_URL}" < test/testutil/testdata/domain_tables.sql >/dev/null 2>&1; then
            echo -e "${RED}✗ Failed to create domain tables${NC}"
            TS_RESULT=1
        else
            echo -e "${GREEN}✓ Domain tables created${NC}"

            # Create tuples view
            if ! psql "${TS_DATABASE_URL}" < test/testutil/testdata/tuples_view.sql >/dev/null 2>&1; then
                echo -e "${RED}✗ Failed to create tuples view${NC}"
                TS_RESULT=1
            else
                echo -e "${GREEN}✓ Tuples view created${NC}"

                # Run melange migrate with explicit database URL
                if ! go run ./cmd/melange migrate --db "${TS_DATABASE_URL}" --schema test/testutil/testdata/schema.fga >/dev/null 2>&1; then
                    echo -e "${RED}✗ Failed to run melange migrate${NC}"
                    TS_RESULT=1
                else
                    echo -e "${GREEN}✓ Melange schema migrated${NC}"
                    echo ""

                    # Install TypeScript dependencies if needed
                    cd "${PROJECT_ROOT}/clients/typescript"
                    if [ ! -d "node_modules" ]; then
                        echo "Installing TypeScript dependencies..."
                        pnpm install --silent
                        echo ""
                    fi

                    # Run TypeScript tests with DATABASE_URL
                    if DATABASE_URL="${TS_DATABASE_URL}" run_test_suite "TypeScript" "pnpm test" "clients/typescript"; then
                        TS_RESULT=0
                    else
                        TS_RESULT=1
                    fi
                fi
            fi
        fi
    fi
fi

# Summary
echo -e "${BLUE}════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}  Test Results Summary${NC}"
echo -e "${BLUE}════════════════════════════════════════════════════════${NC}"
echo ""

TOTAL_FAILURES=0

# Report Go results
if [ $GO_RESULT -eq 0 ]; then
    echo -e "  ${GREEN}✓${NC} Go"
else
    echo -e "  ${RED}✗${NC} Go"
    TOTAL_FAILURES=$((TOTAL_FAILURES + 1))
fi

# Report TypeScript results
if [ $TS_RESULT -eq 0 ]; then
    echo -e "  ${GREEN}✓${NC} TypeScript"
else
    echo -e "  ${RED}✗${NC} TypeScript"
    TOTAL_FAILURES=$((TOTAL_FAILURES + 1))
fi

echo ""

if [ $TOTAL_FAILURES -eq 0 ]; then
    echo -e "${GREEN}✓ All integration tests passed!${NC}"
    exit 0
else
    echo -e "${RED}✗ ${TOTAL_FAILURES} test suite(s) failed${NC}"
    exit 1
fi
