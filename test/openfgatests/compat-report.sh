#!/bin/bash
# OpenFGA Compatibility Report Generator
# Runs the three official OpenFGA test suites (Check, ListObjects, ListUsers)
# and generates a compatibility report.
#
# Usage:
#   ./compat-report.sh              # Run all three suites
#   ./compat-report.sh --check      # Run only Check suite
#   ./compat-report.sh --list       # Run only ListObjects and ListUsers

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR/.."

# Colors for terminal output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color
BOLD='\033[1m'

RUN_CHECK=true
RUN_LISTOBJECTS=true
RUN_LISTUSERS=true

if [[ "$1" == "--check" ]]; then
    RUN_LISTOBJECTS=false
    RUN_LISTUSERS=false
elif [[ "$1" == "--list" ]]; then
    RUN_CHECK=false
fi

echo -e "${BOLD}OpenFGA Compatibility Report${NC}"
echo "=============================="
echo ""

# Create temp directory for results
TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

echo "Running OpenFGA test suites..."
echo ""

# Run test suites with -p 1 to avoid parallel database conflicts
if $RUN_CHECK; then
    echo -e "${BLUE}Running Check suite...${NC}"
    go test -json -p 1 -count=1 ./openfgatests/... -run "TestOpenFGACheckSuite" 2>&1 > "$TMPDIR/check.json" || true
fi

if $RUN_LISTOBJECTS; then
    echo -e "${BLUE}Running ListObjects suite...${NC}"
    go test -json -p 1 -count=1 ./openfgatests/... -run "TestOpenFGAListObjectsSuite" 2>&1 > "$TMPDIR/listobjects.json" || true
fi

if $RUN_LISTUSERS; then
    echo -e "${BLUE}Running ListUsers suite...${NC}"
    go test -json -p 1 -count=1 ./openfgatests/... -run "TestOpenFGAListUsersSuite" 2>&1 > "$TMPDIR/listusers.json" || true
fi

echo ""
echo -e "${BLUE}Generating report...${NC}"
echo ""

# Parse results and generate report
python3 << 'PYTHON_SCRIPT'
import json
import sys
import os
from collections import defaultdict
from datetime import datetime

tmpdir = os.environ.get('TMPDIR', '/tmp')

def parse_suite(filename, suite_name):
    """Parse a test suite JSON file and extract results."""
    results = defaultdict(lambda: {'pass': 0, 'fail': 0, 'assertions': []})

    try:
        with open(filename) as f:
            for line in f:
                try:
                    obj = json.loads(line)
                    action = obj.get('Action')
                    test = obj.get('Test', '')

                    if action not in ['pass', 'fail']:
                        continue
                    if not test:
                        continue

                    # Skip contextual tuple tests and condition tests
                    if '_ctxTuples' in test or 'condition' in test.lower():
                        continue

                    # Extract test name and assertion
                    # Format: TestOpenFGA*Suite/RunAllTests/Check/test_name/stage_N/assertion_N
                    parts = test.split('/')
                    if len(parts) < 4:
                        continue

                    # Find the test name (after Check/ListObjects/ListUsers)
                    test_name = None
                    for i, part in enumerate(parts):
                        if part in ['Check', 'ListObjects', 'ListUsers']:
                            if i + 1 < len(parts):
                                test_name = parts[i + 1]
                            break

                    if not test_name:
                        continue

                    # Only count leaf assertions
                    if 'assertion' not in test and 'stage' in parts[-1]:
                        continue

                    results[test_name][action] += 1
                    if action == 'fail':
                        results[test_name]['assertions'].append(test)

                except json.JSONDecodeError:
                    continue
    except FileNotFoundError:
        pass

    return dict(results)

def categorize_test(test_name):
    """Categorize a test by its feature."""
    test_lower = test_name.lower()

    categories = [
        ('Direct Assignment', ['this']),
        ('Computed Userset', ['computed_userset', 'computeduserset']),
        ('Tuple-to-Userset', ['tuple_to_userset', 'ttu_']),
        ('Wildcards', ['wildcard', 'public']),
        ('Exclusion', ['exclusion', 'butnot', 'but_not']),
        ('Union', ['union']),
        ('Intersection', ['intersection']),
        ('Userset References', ['userset']),
        ('Cycle Handling', ['cycle', 'recursive']),
        ('Validation', ['validation', 'invalid', 'error', 'err_']),
    ]

    for cat_name, patterns in categories:
        for pattern in patterns:
            if pattern in test_lower:
                return cat_name
    return 'Other'

# Parse all suites
check_results = parse_suite(f'{tmpdir}/check.json', 'Check')
listobjects_results = parse_suite(f'{tmpdir}/listobjects.json', 'ListObjects')
listusers_results = parse_suite(f'{tmpdir}/listusers.json', 'ListUsers')

# Calculate totals
def calc_totals(results):
    total_pass = sum(r['pass'] for r in results.values())
    total_fail = sum(r['fail'] for r in results.values())
    passing = [t for t, r in results.items() if r['fail'] == 0 and r['pass'] > 0]
    failing = [(t, r['pass'], r['fail']) for t, r in results.items() if r['fail'] > 0]
    return total_pass, total_fail, passing, failing

check_pass, check_fail, check_passing, check_failing = calc_totals(check_results)
lo_pass, lo_fail, lo_passing, lo_failing = calc_totals(listobjects_results)
lu_pass, lu_fail, lu_passing, lu_failing = calc_totals(listusers_results)

# Print report
print("=" * 70)
print("OPENFGA COMPATIBILITY REPORT")
print(f"Generated: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
print("Excludes: Conditional tests, Contextual tuple tests")
print("=" * 70)

print("\n## SUMMARY\n")
print(f"{'Suite':<20} {'Pass':<10} {'Fail':<10} {'Total':<10} {'Rate':<10}")
print("-" * 60)

def print_suite_row(name, passed, failed):
    total = passed + failed
    rate = f"{100 * passed / total:.1f}%" if total > 0 else "N/A"
    status = "✓" if failed == 0 and total > 0 else ("⚠" if passed > failed else "✗")
    print(f"{name:<20} {passed:<10} {failed:<10} {total:<10} {rate:<10} {status}")

print_suite_row("Check", check_pass, check_fail)
print_suite_row("ListObjects", lo_pass, lo_fail)
print_suite_row("ListUsers", lu_pass, lu_fail)
print("-" * 60)

total_pass = check_pass + lo_pass + lu_pass
total_fail = check_fail + lo_fail + lu_fail
print_suite_row("TOTAL", total_pass, total_fail)

# Feature breakdown for Check suite
print("\n\n## CHECK SUITE - FEATURE BREAKDOWN\n")

categories = defaultdict(lambda: {'pass': 0, 'fail': 0})
for test_name, r in check_results.items():
    cat = categorize_test(test_name)
    categories[cat]['pass'] += r['pass']
    categories[cat]['fail'] += r['fail']

print(f"{'Feature':<25} {'Pass':<8} {'Fail':<8} {'Rate':<10} {'Status'}")
print("-" * 60)

for cat in ['Direct Assignment', 'Computed Userset', 'Tuple-to-Userset',
            'Wildcards', 'Exclusion', 'Union', 'Intersection',
            'Userset References', 'Cycle Handling', 'Validation', 'Other']:
    r = categories[cat]
    total = r['pass'] + r['fail']
    if total == 0:
        continue
    rate = 100 * r['pass'] / total
    status = "✓ WORKING" if rate >= 90 else ("⚠ PARTIAL" if rate >= 50 else "✗ GAPS")
    print(f"{cat:<25} {r['pass']:<8} {r['fail']:<8} {rate:.0f}%{'':<6} {status}")

# Passing tests
print("\n\n## FULLY PASSING TESTS\n")

print(f"### Check Suite ({len(check_passing)} tests)")
for t in sorted(check_passing)[:30]:
    print(f"  ✓ {t}")
if len(check_passing) > 30:
    print(f"  ... and {len(check_passing) - 30} more")

print(f"\n### ListObjects Suite ({len(lo_passing)} tests)")
for t in sorted(lo_passing)[:30]:
    print(f"  ✓ {t}")
if len(lo_passing) > 30:
    print(f"  ... and {len(lo_passing) - 30} more")

print(f"\n### ListUsers Suite ({len(lu_passing)} tests)")
for t in sorted(lu_passing)[:30]:
    print(f"  ✓ {t}")
if len(lu_passing) > 30:
    print(f"  ... and {len(lu_passing) - 30} more")

# Failing tests
print("\n\n## FAILING TESTS\n")

print(f"### Check Suite ({len(check_failing)} tests with failures)")
for t, p, f in sorted(check_failing, key=lambda x: -x[2])[:30]:
    print(f"  ✗ {t}: {p} pass, {f} fail")
if len(check_failing) > 30:
    print(f"  ... and {len(check_failing) - 30} more")

print(f"\n### ListObjects Suite ({len(lo_failing)} tests with failures)")
for t, p, f in sorted(lo_failing, key=lambda x: -x[2])[:30]:
    print(f"  ✗ {t}: {p} pass, {f} fail")
if len(lo_failing) > 30:
    print(f"  ... and {len(lo_failing) - 30} more")

print(f"\n### ListUsers Suite ({len(lu_failing)} tests with failures)")
for t, p, f in sorted(lu_failing, key=lambda x: -x[2])[:30]:
    print(f"  ✗ {t}: {p} pass, {f} fail")
if len(lu_failing) > 30:
    print(f"  ... and {len(lu_failing) - 30} more")

print("\n" + "=" * 70)
print("END OF REPORT")
print("=" * 70)

PYTHON_SCRIPT

echo ""
echo -e "${GREEN}Report complete!${NC}"
