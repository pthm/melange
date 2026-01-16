#!/usr/bin/env bash
#
# Run all integration tests (Go + TypeScript) with a shared database.
#
# This script runs both Go and TypeScript integration tests against the same
# PostgreSQL testcontainers instance. The Go tests start the container, and
# the TypeScript tests reuse it via the DATABASE_URL environment variable.

set -euo pipefail

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="${PROJECT_ROOT}/test"
TS_CLIENT_DIR="${PROJECT_ROOT}/clients/typescript"

echo -e "${BLUE}════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}  Melange Integration Tests (Go + TypeScript)${NC}"
echo -e "${BLUE}════════════════════════════════════════════════════════${NC}"
echo ""

# Track test results
GO_TEST_RESULT=0
TS_TEST_RESULT=0

# Step 1: Run Go integration tests
echo -e "${YELLOW}▶ Step 1/3: Running Go integration tests...${NC}"
echo ""

cd "${TEST_DIR}"
if go test -timeout 5m ./...; then
    echo -e "${GREEN}✓ Go integration tests passed${NC}"
    echo ""
else
    GO_TEST_RESULT=$?
    echo -e "${RED}✗ Go integration tests failed${NC}"
    echo ""
fi

# Step 2: Check if TypeScript dependencies are installed
echo -e "${YELLOW}▶ Step 2/3: Ensuring TypeScript dependencies are installed...${NC}"
echo ""

cd "${TS_CLIENT_DIR}"
if [ ! -d "node_modules" ]; then
    echo "Installing TypeScript dependencies..."
    pnpm install --silent
    echo -e "${GREEN}✓ Dependencies installed${NC}"
else
    echo -e "${GREEN}✓ Dependencies already installed${NC}"
fi
echo ""

# Step 3: Run TypeScript integration tests
echo -e "${YELLOW}▶ Step 3/3: Running TypeScript integration tests...${NC}"
echo ""

# Check if DATABASE_URL is set (from Go tests or external)
if [ -z "${DATABASE_URL:-}" ]; then
    echo -e "${YELLOW}⚠ DATABASE_URL not set${NC}"
    echo "TypeScript tests will attempt to connect to localhost:5432"
    echo "Make sure a PostgreSQL instance with melange schema is running"
    echo ""
fi

cd "${TS_CLIENT_DIR}"
if pnpm test; then
    echo -e "${GREEN}✓ TypeScript integration tests passed${NC}"
    echo ""
else
    TS_TEST_RESULT=$?
    echo -e "${RED}✗ TypeScript integration tests failed${NC}"
    echo ""
fi

# Summary
echo -e "${BLUE}════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}  Test Summary${NC}"
echo -e "${BLUE}════════════════════════════════════════════════════════${NC}"
echo ""

if [ $GO_TEST_RESULT -eq 0 ] && [ $TS_TEST_RESULT -eq 0 ]; then
    echo -e "${GREEN}✓ All integration tests passed!${NC}"
    echo ""
    exit 0
elif [ $GO_TEST_RESULT -ne 0 ] && [ $TS_TEST_RESULT -ne 0 ]; then
    echo -e "${RED}✗ Both Go and TypeScript tests failed${NC}"
    echo ""
    exit 1
elif [ $GO_TEST_RESULT -ne 0 ]; then
    echo -e "${RED}✗ Go tests failed${NC}"
    echo -e "${GREEN}✓ TypeScript tests passed${NC}"
    echo ""
    exit 1
else
    echo -e "${GREEN}✓ Go tests passed${NC}"
    echo -e "${RED}✗ TypeScript tests failed${NC}"
    echo ""
    exit 1
fi
