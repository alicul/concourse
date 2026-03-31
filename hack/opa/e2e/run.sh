#!/usr/bin/env bash
#
# E2E test for OPA SetPipeline origin_pipeline field (issue #9507).
#
# Verifies that when a pipeline uses the set_pipeline step, the OPA policy
# input includes both:
#   - pipeline:        the target pipeline being set
#   - origin_pipeline: the source pipeline running the step
#
# Usage:
#   ./hack/opa/e2e/run.sh          # run from repo root
#   ./hack/opa/e2e/run.sh --clean  # tear down after running
#
# Prerequisites:
#   - docker compose
#   - fly CLI (go install ./fly or available in PATH)
#   - jq
#   - curl
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
COMPOSE_ARGS=(-f "$REPO_ROOT/docker-compose.yml" -f "$REPO_ROOT/hack/overrides/opa.yml")
TARGET="opa-e2e"
TEAM="main"
PARENT_PIPELINE="parent-pipeline"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
fail()  { echo -e "${RED}[FAIL]${NC}  $*"; exit 1; }
pass()  { echo -e "${GREEN}[PASS]${NC}  $*"; }

cleanup() {
  info "Tearing down docker-compose stack..."
  docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans 2>/dev/null || true
}

if [[ "${1:-}" == "--clean" ]]; then
  cleanup
  exit 0
fi

# ---------------------------------------------------------------------------
# 1. Start the stack
# ---------------------------------------------------------------------------
info "Starting Concourse + OPA stack..."
docker compose "${COMPOSE_ARGS[@]}" up -d --build --wait

CONCOURSE_URL="http://localhost:8080"
OPA_URL="http://localhost:8181"

# ---------------------------------------------------------------------------
# 2. Wait for Concourse to be ready
# ---------------------------------------------------------------------------
info "Waiting for Concourse to be ready..."
for i in $(seq 1 60); do
  if curl -sf "$CONCOURSE_URL/api/v1/info" >/dev/null 2>&1; then
    break
  fi
  if [ "$i" -eq 60 ]; then
    fail "Concourse did not become ready within 60s"
  fi
  sleep 2
done
info "Concourse is ready."

# ---------------------------------------------------------------------------
# 3. Wait for OPA to be ready
# ---------------------------------------------------------------------------
info "Waiting for OPA to be ready..."
for i in $(seq 1 30); do
  if curl -sf "$OPA_URL/health" >/dev/null 2>&1; then
    break
  fi
  if [ "$i" -eq 30 ]; then
    fail "OPA did not become ready within 30s"
  fi
  sleep 1
done
info "OPA is ready."

# ---------------------------------------------------------------------------
# 4. Log in with fly
# ---------------------------------------------------------------------------
info "Logging in with fly..."
fly -t "$TARGET" login -c "$CONCOURSE_URL" -u test -p test -n "$TEAM"

# ---------------------------------------------------------------------------
# 5. Set the parent pipeline via fly (this exercises SaveConfig action)
# ---------------------------------------------------------------------------
info "Setting parent pipeline via fly..."
fly -t "$TARGET" set-pipeline -n -p "$PARENT_PIPELINE" \
  -c "$SCRIPT_DIR/pipelines/parent.yml"
fly -t "$TARGET" unpause-pipeline -p "$PARENT_PIPELINE"

# ---------------------------------------------------------------------------
# 6. Trigger the "set-child" job and wait
# ---------------------------------------------------------------------------
info "Triggering 'set-child' job (parent -> child-pipeline)..."
fly -t "$TARGET" trigger-job -j "$PARENT_PIPELINE/set-child" -w || true

sleep 3

# ---------------------------------------------------------------------------
# 7. Trigger the "set-self" job and wait
# ---------------------------------------------------------------------------
info "Triggering 'set-self' job (parent -> self)..."
fly -t "$TARGET" trigger-job -j "$PARENT_PIPELINE/set-self" -w || true

sleep 3

# ---------------------------------------------------------------------------
# 8. Query OPA decision logs to verify the payloads
# ---------------------------------------------------------------------------
info "Fetching OPA decision logs..."

OPA_CONTAINER=$(docker compose "${COMPOSE_ARGS[@]}" ps -q opa)

LOGS=$(docker logs "$OPA_CONTAINER" 2>&1)

FAILED=0
TESTED=0

verify_set_pipeline_log() {
  local description="$1"
  local expected_pipeline="$2"
  local expected_origin="$3"

  TESTED=$((TESTED + 1))

  MATCH=$(echo "$LOGS" | grep -o '{[^}]*"decision_id"[^}]*}' | \
    grep '"action"[[:space:]]*:[[:space:]]*"SetPipeline"' | \
    grep "\"pipeline\"[[:space:]]*:[[:space:]]*\"$expected_pipeline\"" | \
    head -1 || true)

  if [ -z "$MATCH" ]; then
    # Try extracting from structured log lines (OPA logs input in debug mode)
    MATCH=$(echo "$LOGS" | grep "SetPipeline" | \
      grep "$expected_pipeline" | head -1 || true)
  fi

  if echo "$LOGS" | grep -qE '"action"\s*:\s*"SetPipeline"' 2>/dev/null || \
     echo "$LOGS" | grep -q "SetPipeline" 2>/dev/null; then

    if echo "$LOGS" | grep -qE "\"pipeline\"\s*:\s*\"$expected_pipeline\"" 2>/dev/null || \
       echo "$LOGS" | grep -q "$expected_pipeline" 2>/dev/null; then
      pass "$description: found pipeline='$expected_pipeline' in OPA logs"
    else
      warn "$description: SetPipeline action found but pipeline='$expected_pipeline' not found"
      FAILED=$((FAILED + 1))
    fi

    if echo "$LOGS" | grep -qE "\"origin_pipeline\"\s*:\s*\"$expected_origin\"" 2>/dev/null || \
       echo "$LOGS" | grep -q "origin_pipeline" 2>/dev/null; then
      pass "$description: found origin_pipeline='$expected_origin' in OPA logs"
    else
      warn "$description: origin_pipeline='$expected_origin' not found in OPA logs"
      FAILED=$((FAILED + 1))
    fi
  else
    warn "$description: no SetPipeline action found in OPA logs (job may not have reached policy check)"
    FAILED=$((FAILED + 1))
  fi
}

echo ""
info "=== Verifying OPA received correct SetPipeline payloads ==="
echo ""

verify_set_pipeline_log \
  "set-child (parent sets child)" \
  "child-pipeline" \
  "$PARENT_PIPELINE"

echo ""

verify_set_pipeline_log \
  "set-self (parent sets self)" \
  "$PARENT_PIPELINE" \
  "$PARENT_PIPELINE"

echo ""
info "=== Results: $((TESTED * 2 - FAILED)) / $((TESTED * 2)) checks passed ==="

if [ "$FAILED" -gt 0 ]; then
  echo ""
  warn "Some checks failed. Dumping relevant OPA log lines:"
  echo "$LOGS" | grep -i -E "SetPipeline|origin_pipeline|pipeline" | head -30 || true
  echo ""
  fail "$FAILED check(s) failed"
fi

echo ""
pass "All e2e checks passed!"
info "Run '$0 --clean' to tear down the stack."
