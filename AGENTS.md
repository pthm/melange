# Codex Instructions (Melange)

## Core Commands (Codex Does Not Support Skills)

Use these commands directly instead of skills.

### Search

- Semantic search by concept:
  - `ck --sem --threshold 0.7 --limit 20 "query" .`
- Exact/lexical search:
  - `ck --lex "identifier" .`
- Hybrid search:
  - `ck --hybrid "query" .`

### Dump OpenFGA Test Data

- List tests:
  - `just dump-openfga-list`
- Dump a test:
  - `just dump-openfga <test_name>`
- Dump by pattern:
  - `just dump-openfga-pattern "<pattern>"`

### Dump Generated SQL (OpenFGA)

- Full SQL dump:
  - `just dump-sql <test_name>`
- Model data only:
  - `just dump-sql-models <test_name>`
- Relation analysis only:
  - `just dump-sql-analysis <test_name>`

### Run Tests (OpenFGA)

- Full suite:
  - `just test-openfga`
- By test name:
  - `just test-openfga-name <test_name>`
- By category:
  - `just test-openfga-feature <Category>`
- By pattern:
  - `just test-openfga-pattern "<pattern>"`

### Benchmarks (OpenFGA)

- Full benchmark suite:
  - `just bench-openfga`
- By category:
  - `just bench-openfga-category <Category>`
- By test name:
  - `just bench-openfga-name <test_name>`
- By pattern:
  - `just bench-openfga-pattern "<pattern>"`

### Codex Consult (Second Opinion)

Use when stuck or after one failed fix attempt:

```bash
codex exec "I'm debugging a test failure in Melange (OpenFGA-compatible auth library for PostgreSQL).

**Test output:**
<paste test output>

**Relevant code:**
<paste relevant code or file paths>

**What I've observed:**
<your analysis>

Help me understand: What is causing this failure and what's the correct fix?" 2>/dev/null
```

### Codex Review (Mandatory After Changes)

```bash
codex exec review --uncommitted 2>/dev/null
```

## Required Workflow

1. Investigate (use `ck-search` as needed).
2. Baseline tests **before** changes using `just test-openfga` or targeted `just test-openfga-*` commands.
3. Implement minimal changes.
4. Test after every change using `just test-openfga` (or suite for broad changes).
5. Iterate until tests pass.
6. Run `codex exec review --uncommitted` and address any issues.

## Test Debugging Rule

Before fixing a failing OpenFGA test, always run **both**:
- `just dump-openfga <test_name>`
- `just dump-sql <test_name>`
