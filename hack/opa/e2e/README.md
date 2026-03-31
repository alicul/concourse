# OPA SetPipeline E2E Test

End-to-end test for [issue #9507](https://github.com/concourse/concourse/issues/9507): verifying that the `SetPipeline` OPA policy input includes both `pipeline` (the target being set) and `origin_pipeline` (the source pipeline running the step).

## What it tests

1. **Parent sets a child pipeline** (`set_pipeline: child-pipeline`):
   - `pipeline` should be `child-pipeline`
   - `origin_pipeline` should be `parent-pipeline`

2. **Parent sets itself** (`set_pipeline: self`):
   - `pipeline` should be `parent-pipeline`
   - `origin_pipeline` should be `parent-pipeline`

## Prerequisites

- Docker and docker compose
- `fly` CLI (`go install ./fly` from the repo root, or download from Concourse)
- `curl` and `jq`

## Usage

From the repository root:

```bash
# Build Concourse image first (if not already built)
docker compose build

# Run the e2e test
./hack/opa/e2e/run.sh

# Tear down after testing
./hack/opa/e2e/run.sh --clean
```

## How it works

1. Starts Concourse (web + worker + db) with OPA sidecar using the root `docker-compose.yml` and the existing `hack/overrides/opa.yml` (which defines the OPA service and mounts `hack/opa/`).
2. The e2e OPA policy (`policy.rego`) adds deny rules to the `concourse` package that reject any `SetPipeline` request missing the `origin_pipeline` or `pipeline` field.
4. A parent pipeline is deployed via `fly set-pipeline`.
5. Two jobs are triggered:
   - `set-child`: the parent pipeline sets `child-pipeline` (a different pipeline).
   - `set-self`: the parent pipeline sets itself using `set_pipeline: self`.
6. The script inspects OPA's container logs for the `SetPipeline` decision inputs and verifies the `pipeline` and `origin_pipeline` fields are present and correct.

## Files

| File | Description |
|------|-------------|
| `run.sh` | E2E test orchestration script |
| `policy.rego` | OPA deny rules that validate `origin_pipeline` presence |
| `pipelines/parent.yml` | Parent pipeline that sets a child and itself |
| `pipelines/child.yml` | Simple child pipeline (used as reference) |
