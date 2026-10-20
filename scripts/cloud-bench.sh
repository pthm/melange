#!/usr/bin/env bash
#
# Run the benchmark suites from a Compute Engine VM against a Cloud SQL
# PostgreSQL instance in the same zone, over private IP. The numbers include
# a real network round-trip to a managed database, unlike the local runs,
# which talk to a container on the same machine.
#
# Usage:
#   PROJECT=my-gcp-project scripts/cloud-bench.sh all
#
#   scripts/cloud-bench.sh setup     # create the Cloud SQL instance and VM
#   scripts/cloud-bench.sh run       # upload the source, run, wait, fetch results
#   scripts/cloud-bench.sh report    # breakdown tables and benchstat into report.md
#   scripts/cloud-bench.sh teardown  # delete the VM and Cloud SQL instance
#   scripts/cloud-bench.sh all       # all of the above; tears down on exit
#
# `run` is safe to re-run after a dropped connection: if a run is already in
# progress on the VM it waits for that one instead of starting another.
#
# Settings (environment variables):
#   PROJECT        GCP project (default: current gcloud project)
#   ZONE           zone for both machines (default: europe-west2-a)
#   PG_VERSION     Cloud SQL version (default: POSTGRES_17)
#   DB_TIER        Cloud SQL tier (default: db-custom-4-16384, 4 vCPU 16 GB)
#   VM_TYPE        VM machine type (default: n2-standard-4)
#   COUNT          runs per benchmark, for benchstat (default: 6)
#   BENCH_TEST     -bench regex for ./test (default: every suite, scales up to 1M;
#                  the 10M-100M scale benchmarks need hundreds of GB and days)
#   BENCH_OPENFGA  -bench regex for ./test/openfgatests (default: BenchmarkOpenFGA_All$)
#   BENCHTIME_OPENFGA  -benchtime for the OpenFGA suite (default: 200ms; it has
#                  ~2,000 small benchmarks, so Go's 1s default takes ~5 hours)
#   KEEP=1         with `all`, leave the machines running afterwards
#   OUT            local results directory (default: .cloud-bench)

set -euo pipefail

PROJECT="${PROJECT:-$(gcloud config get-value project 2>/dev/null || true)}"
ZONE="${ZONE:-europe-west2-a}"
PG_VERSION="${PG_VERSION:-POSTGRES_17}"
DB_TIER="${DB_TIER:-db-custom-4-16384}"
VM_TYPE="${VM_TYPE:-n2-standard-4}"
COUNT="${COUNT:-6}"
# Scale sub-benchmarks are named 1K..100M; every other sub-benchmark starts
# with a letter. Keep those and the scales up to 1M. Go reports a benchmark
# without sub-benchmarks only on a full match, so those are listed by name.
BENCH_TEST="${BENCH_TEST:-^Benchmark(RoundTrip|ContextualTuples_Lowerbound)\$|/^([A-Za-z]|(1K|10K|100K|1M)\$)}"
BENCH_OPENFGA="${BENCH_OPENFGA:-BenchmarkOpenFGA_All\$}"
BENCHTIME_OPENFGA="${BENCHTIME_OPENFGA:-200ms}"
OUT="${OUT:-.cloud-bench}"

DB_INSTANCE="melange-bench"
VM="melange-bench-vm"
PSA_RANGE="google-managed-services-default"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log() { printf '==> %s\n' "$*" >&2; }

gc() { gcloud --project "$PROJECT" "$@"; }
vm_ssh() { gc compute ssh "$VM" --zone "$ZONE" --command "$1"; }

# True while a benchmark process is alive on the VM. The [c] stops pgrep
# matching the shell that runs this check.
REMOTE_RUNNING='pgrep -f "[c]loud-bench.sh _remote" >/dev/null'

require_project() {
  [[ -n "$PROJECT" ]] || { echo "Set PROJECT or run: gcloud config set project <id>" >&2; exit 1; }
}

db_ip() {
  # The instance has no public IP, so its only address is the private one.
  gc sql instances describe "$DB_INSTANCE" --format='value(ipAddresses[0].ipAddress)'
}

cmd_setup() {
  require_project
  mkdir -p "$OUT"

  log "Enabling APIs"
  gc services enable compute.googleapis.com sqladmin.googleapis.com servicenetworking.googleapis.com

  # Private services access lets Cloud SQL take a private IP on the default
  # network. It is free and shared by the project, so teardown leaves it.
  if ! gc compute addresses describe "$PSA_RANGE" --global >/dev/null 2>&1; then
    log "Reserving private services range"
    gc compute addresses create "$PSA_RANGE" --global --purpose=VPC_PEERING \
      --prefix-length=16 --network=default
  fi
  if ! gc services vpc-peerings list --network=default --format='value(service)' | grep -q servicenetworking; then
    log "Peering default network with service networking"
    gc services vpc-peerings connect --service=servicenetworking.googleapis.com \
      --ranges="$PSA_RANGE" --network=default
  fi

  if [[ ! -s "$OUT/db-password" ]]; then
    (umask 077; openssl rand -hex 24 > "$OUT/db-password")
  fi

  if ! gc sql instances describe "$DB_INSTANCE" >/dev/null 2>&1; then
    log "Creating Cloud SQL instance $DB_INSTANCE ($PG_VERSION, $DB_TIER); this takes several minutes"
    gc sql instances create "$DB_INSTANCE" \
      --database-version="$PG_VERSION" --edition=ENTERPRISE --tier="$DB_TIER" \
      --zone="$ZONE" --availability-type=ZONAL \
      --storage-type=SSD --storage-size=100 \
      --network=default --no-assign-ip
  fi
  gc sql users set-password postgres --instance="$DB_INSTANCE" --password="$(cat "$OUT/db-password")"

  if ! gc compute instances describe "$VM" --zone "$ZONE" >/dev/null 2>&1; then
    local go_version startup
    go_version="$(awk '/^go /{print $2; exit}' "$ROOT/go.mod")"
    startup="$(mktemp)"
    cat > "$startup" <<EOF
#!/bin/bash
set -e
[ -f /var/lib/bench-ready ] && exit 0
apt-get update
apt-get install -y postgresql-client
curl -fsSL https://go.dev/dl/go${go_version}.linux-amd64.tar.gz | tar -C /usr/local -xz
touch /var/lib/bench-ready
EOF
    log "Creating VM $VM ($VM_TYPE) with Go $go_version"
    gc compute instances create "$VM" --zone "$ZONE" --machine-type "$VM_TYPE" \
      --image-family=debian-12 --image-project=debian-cloud \
      --boot-disk-size=50GB --metadata-from-file=startup-script="$startup"
    rm -f "$startup"
  fi

  log "Waiting for the VM startup script"
  until vm_ssh 'test -f /var/lib/bench-ready' 2>/dev/null; do sleep 15; done
  log "Setup complete. Database private IP: $(db_ip)"
}

cmd_run() {
  require_project
  mkdir -p "$OUT"

  if vm_ssh "$REMOTE_RUNNING" 2>/dev/null; then
    log "A run is already in progress on $VM; waiting for it"
  else
    log "Uploading source"
    local tarball env
    tarball="$(mktemp -d)/melange-src.tgz"
    # Tracked and untracked (non-ignored) files, so uncommitted changes are
    # benchmarked too, plus the gitignored generated authz client the test
    # package compiles against. The docs site is not needed. COPYFILE_DISABLE
    # and --no-xattrs stop macOS tar adding ._* files, which the OpenFGA test
    # loader would try to parse.
    (cd "$ROOT" && { git ls-files -co --exclude-standard -z -- . ':!docs'; printf 'test/authz/schema_gen.go\0'; } |
      COPYFILE_DISABLE=1 tar --no-xattrs --null -czf "$tarball" -T -)
    gc compute scp "$tarball" "$VM:~/melange-src.tgz" --zone "$ZONE"
    rm -f "$tarball"

    env="$(mktemp)"
    (umask 077; cat > "$env" <<EOF
DATABASE_URL='postgres://postgres:$(cat "$OUT/db-password")@$(db_ip):5432/postgres?sslmode=require'
COUNT='$COUNT'
BENCH_TEST='$BENCH_TEST'
BENCH_OPENFGA='$BENCH_OPENFGA'
BENCHTIME_OPENFGA='$BENCHTIME_OPENFGA'
EOF
)
    gc compute scp "$env" "$VM:~/bench.env" --zone "$ZONE"
    rm -f "$env"

    log "Starting benchmarks (COUNT=$COUNT); this takes hours at the 1M scale"
    # Reset synchronously, so the wait loop below cannot see a previous run's
    # DONE marker, then detach only the benchmark process.
    vm_ssh 'rm -rf ~/melange ~/results && mkdir -p ~/melange &&
      tar -xzf ~/melange-src.tgz -C ~/melange && chmod 600 ~/bench.env'
    # Return only once the process has written STARTED, so the wait loop below
    # cannot mistake a run that has not started yet for one that has finished.
    vm_ssh 'nohup setsid bash ~/melange/scripts/cloud-bench.sh _remote > ~/bench.log 2>&1 < /dev/null &
      for i in $(seq 30); do test -f ~/results/STARTED && exit 0; sleep 1; done
      echo "benchmark process did not start; see ~/bench.log" >&2; exit 1'
  fi

  # Stop waiting when the run finishes, or when its process is gone (killed,
  # VM restarted) without finishing.
  until vm_ssh "test -f ~/results/DONE || ! $REMOTE_RUNNING" 2>/dev/null; do
    vm_ssh 'grep -E "^(Benchmark|ok|FAIL|---)" ~/results/*.txt 2>/dev/null | tail -1' 2>/dev/null || true
    sleep 120
  done

  local dest
  dest="$OUT/results-$(date +%Y%m%d-%H%M%S)"
  mkdir -p "$dest"
  gc compute scp --recurse "$VM:~/results/*" "$dest/" --zone "$ZONE"
  gc compute scp "$VM:~/bench.log" "$dest/" --zone "$ZONE"
  [[ -f "$dest/DONE" ]] || echo "benchmark process stopped without finishing" >> "$dest/errors.txt"
  # Describe the machines that ran it, not this shell's settings. COUNT is
  # already in settings.env, written by the run itself.
  printf "PG_VERSION='%s'\nDB_TIER='%s'\nVM_TYPE='%s'\nZONE='%s'\n" \
    "$(gc sql instances describe "$DB_INSTANCE" --format='value(databaseVersion)')" \
    "$(gc sql instances describe "$DB_INSTANCE" --format='value(settings.tier)')" \
    "$(gc compute instances describe "$VM" --zone "$ZONE" --format='value(machineType.basename())')" \
    "$ZONE" >> "$dest/settings.env"
  ln -sfn "$(basename "$dest")" "$OUT/latest"
  log "Results in $dest"
}

# Fails if any suite in the latest results failed.
check_errors() {
  if [[ -s "$OUT/latest/errors.txt" ]]; then
    log "Some suites failed (see $OUT/latest/bench.log):"; cat "$OUT/latest/errors.txt" >&2
    return 1
  fi
}

# Runs on the VM, started by cmd_run.
cmd_remote() {
  set -a; . ~/bench.env; set +a
  # MELANGE_TEST_SERVER_URL makes the per-test databases from EmptyDB and DSN
  # live on Cloud SQL too, not only the shared one from DATABASE_URL.
  export PATH="/usr/local/go/bin:$PATH" MELANGE_BENCH_LARGE_SCALE=1 MELANGE_TEST_SERVER_URL="$DATABASE_URL"
  local out=~/results
  mkdir -p "$out"; touch "$out/STARTED"
  grep -v '^DATABASE_URL=' ~/bench.env > "$out/settings.env"
  trap "touch '$out/DONE'" EXIT
  cd ~/melange/test

  psql "$DATABASE_URL" -Atc 'SHOW server_version' > "$out/server-version.txt" || true
  # The in-database/network split needs pg_stat_statements; find out now, not
  # after hours of benchmarks.
  if ! psql "$DATABASE_URL" -q -c 'CREATE EXTENSION IF NOT EXISTS pg_stat_statements' \
       -c 'SELECT 1 FROM pg_stat_statements LIMIT 1' > /dev/null; then
    echo "pg_stat_statements is unavailable; no in-database/network breakdown possible" >> "$out/errors.txt"
    return
  fi

  go test -run='^$' -bench="$BENCH_TEST" -benchmem -count="$COUNT" -timeout 0 . \
    > "$out/test.txt" 2>&1 || echo "./test exited $?" >> "$out/errors.txt"
  go test -run='^$' -bench="$BENCH_OPENFGA" -benchtime="$BENCHTIME_OPENFGA" -benchmem -count="$COUNT" -timeout 0 ./openfgatests/ \
    > "$out/openfga.txt" 2>&1 || echo "./test/openfgatests exited $?" >> "$out/errors.txt"
}

cmd_report() {
  local dir="${1:-$OUT/latest}"
  [[ -d "$dir" ]] || { echo "No results in $dir; run first" >&2; exit 1; }
  dir="$(cd "$dir" && pwd)"
  # shellcheck disable=SC1091
  . "$dir/settings.env"
  local report="$dir/report.md" rev
  rev="$(cd "$ROOT" && git rev-parse --short HEAD)"

  {
    echo "# Cloud benchmark results"
    echo
    echo "- Date: $(date -u +%Y-%m-%d)"
    echo "- Source: $rev plus any uncommitted changes at upload time"
    echo "- Database: Cloud SQL $PG_VERSION (server $(cat "$dir/server-version.txt" 2>/dev/null || echo unknown)), $DB_TIER, $ZONE, private IP, SSL"
    echo "- Client: Compute Engine $VM_TYPE, same zone"
    echo "- Runs per benchmark: $COUNT"
    echo
    local files=() f
    for f in "$dir"/test.txt "$dir"/openfga.txt; do [[ -s "$f" ]] && files+=("$f"); done
    if (( ${#files[@]} )); then
      (cd "$ROOT/test" && go run ./cmd/benchreport "${files[@]}")
    fi
    echo
    echo "## All results"
    echo
    echo "Every suite, from benchstat. Times are what the client measured, including the"
    echo "network. db-ns/op, net-ns/op, and stmts/op are the breakdown metrics above."
    for suite in test openfga; do
      [[ -s "$dir/$suite.txt" ]] || continue
      echo
      echo "### $suite"
      echo
      echo '```'
      (cd "$ROOT" && go run golang.org/x/perf/cmd/benchstat@latest "$dir/$suite.txt")
      echo '```'
    done
    if [[ -s "$dir/errors.txt" ]]; then
      echo
      echo "## Failures"
      echo
      echo '```'
      cat "$dir/errors.txt"
      echo '```'
    fi
  } > "$report"
  log "Wrote $report"
}

cmd_teardown() {
  require_project
  log "Deleting $VM and $DB_INSTANCE"
  gc compute instances delete "$VM" --zone "$ZONE" --quiet 2>/dev/null || true
  gc sql instances delete "$DB_INSTANCE" --quiet 2>/dev/null || true
}

cmd_all() {
  require_project
  [[ "${KEEP:-}" == 1 ]] || trap cmd_teardown EXIT
  cmd_setup
  cmd_run
  cmd_report
  check_errors
}

case "${1:-}" in
  setup) cmd_setup ;;
  run) cmd_run; check_errors ;;
  report) shift; cmd_report "$@" ;;
  teardown) cmd_teardown ;;
  all) cmd_all ;;
  _remote) cmd_remote ;;
  *) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 1 ;;
esac
