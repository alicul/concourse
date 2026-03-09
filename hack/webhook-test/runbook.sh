#!/usr/bin/env bash
# =============================================================================
# Shared Webhook — Local Test Runbook
# Run these commands in order after `docker compose up -d`
# =============================================================================

set -e

CONCOURSE_URL="http://localhost:8080"
FLY="fly -t local"

# ─── Step 0: target ───────────────────────────────────────────────────────────
fly login -t local -c "$CONCOURSE_URL" -u test -p test

# ─── Step 1: Set the shared webhook (no secret — token-in-URL mode) ──────────
# This creates the shared webhook endpoint at
#   http://localhost:8080/api/v1/teams/main/webhooks/github-shared?token=<auto>
$FLY set-webhook \
    --webhook github-shared \
    --type github

# The output will show the full URL including ?token=. Save it:
# WEBHOOK_URL="http://localhost:8080/api/v1/teams/main/webhooks/github-shared?token=<token>"

# ─── Step 2: Set the pipeline (subscribes my-repo to the webhook) ─────────────
$FLY set-pipeline \
    -p webhook-test \
    -c hack/webhook-test/pipeline.yml \
    --non-interactive

$FLY unpause-pipeline -p webhook-test

# ─── Step 3: Verify webhook subscriptions were saved ─────────────────────────
$FLY webhooks
# Should show: github-shared (type: github)

# ─── Step 4: Simulate a GitHub push webhook ──────────────────────────────────
# Replace <token> with the token from Step 1.
#
# This simulates: push to concourse/concourse master branch
# The resource `my-repo` in the pipeline has:
#   uri: https://github.com/concourse/concourse
#   branch: master
# So both the URI rule and the branch rule should match → trigger expected.

TOKEN="<replace-with-token-from-set-webhook-output>"
PAYLOAD='{
  "repository": {
    "full_name": "concourse/concourse"
  },
  "ref": "refs/heads/master",
  "pusher": {
    "name": "test-user"
  }
}'

curl -v -X POST \
    "$CONCOURSE_URL/api/v1/teams/main/webhooks/github-shared?token=$TOKEN" \
    -H "Content-Type: application/json" \
    -d "$PAYLOAD"

# Expected: HTTP 200 {"checks_triggered":1,"build":{...}}

# ─── Step 5: Simulate wrong branch (should NOT trigger) ───────────────────────
PAYLOAD_WRONG_BRANCH='{
  "repository": {"full_name": "concourse/concourse"},
  "ref": "refs/heads/develop"
}'
curl -v -X POST \
    "$CONCOURSE_URL/api/v1/teams/main/webhooks/github-shared?token=$TOKEN" \
    -H "Content-Type: application/json" \
    -d "$PAYLOAD_WRONG_BRANCH"

# Expected: HTTP 200 {"checks_triggered":0}  — branch rule filtered it out

# ─── Step 6: Simulate wrong repo (should NOT trigger) ─────────────────────────
PAYLOAD_WRONG_REPO='{
  "repository": {"full_name": "some-other/project"},
  "ref": "refs/heads/main"
}'
curl -v -X POST \
    "$CONCOURSE_URL/api/v1/teams/main/webhooks/github-shared?token=$TOKEN" \
    -H "Content-Type: application/json" \
    -d "$PAYLOAD_WRONG_REPO"

# Expected: HTTP 200 {"checks_triggered":0}  — URI rule filtered it out

# =============================================================================
# HMAC mode test (with secret)
# =============================================================================

# ─── Step 7: Create a second webhook with HMAC secret ─────────────────────────
$FLY set-webhook \
    --webhook github-hmac \
    --type github \
    --secret "supersecret123" \
    --signature-header X-Hub-Signature-256 \
    --signature-algo hmac-sha256

# URL will be WITHOUT ?token=:
#   http://localhost:8080/api/v1/teams/main/webhooks/github-hmac

# ─── Step 8: Send signed webhook ─────────────────────────────────────────────
SECRET="supersecret123"
PAYLOAD='{"repository":{"full_name":"concourse/concourse"},"ref":"refs/heads/master"}'

# Compute HMAC-SHA256 signature (same as GitHub does)
SIG=$(echo -n "$PAYLOAD" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print "sha256="$2}')

curl -v -X POST \
    "$CONCOURSE_URL/api/v1/teams/main/webhooks/github-hmac" \
    -H "Content-Type: application/json" \
    -H "X-Hub-Signature-256: $SIG" \
    -d "$PAYLOAD"

# Expected: HTTP 200 {"checks_triggered":1}

# ─── Step 9: Send with wrong signature (should be rejected) ───────────────────
curl -v -X POST \
    "$CONCOURSE_URL/api/v1/teams/main/webhooks/github-hmac" \
    -H "Content-Type: application/json" \
    -H "X-Hub-Signature-256: sha256=deadbeefdeadbeef" \
    -d "$PAYLOAD"

# Expected: HTTP 401 Unauthorized

# ─── Step 10: Send with ?token= instead of signature (should also be rejected) -
curl -v -X POST \
    "$CONCOURSE_URL/api/v1/teams/main/webhooks/github-hmac?token=anything" \
    -H "Content-Type: application/json" \
    -d "$PAYLOAD"

# Expected: HTTP 401 Unauthorized (token-in-URL not accepted when secret is set)

echo ""
echo "All tests complete. Check fly builds -p webhook-test to see triggered checks."
