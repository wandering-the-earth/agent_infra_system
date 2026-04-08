# Agent Infra (Phase 1 Decision Core)

This repository now includes a production-style Go project skeleton for Decision-related Phase 1 capabilities from the simplified design document:

- Runtime Decision Kernel (`pure function style`, frozen input required)
- Decision Complexity Budget (DCU) profiles and runtime guard
- Policy phase evaluation (`PRE_CONTEXT` / `PRE_TOOL` / `PRE_RESUME` / `PRE_RELEASE`) basics
- Soft-failure retry guard (`decision_retry_limit_per_step`)
- Decision/Run double-confirmation with pending repair worker
- Scheduler admission decision + dispatch ticket + receipt validation
- Release decision with evidence fusion
- Approval case API + hard-timeout sweep
- Decision outbox events + metric enforcement evaluation

## Project structure

```text
agent/
  cmd/agent-infra/main.go            # service entrypoint
  internal/decision/                 # decision core service
  internal/httpapi/                  # transport adapter (HTTP)
  testdata/decision_cases.json       # executable test cases
  doc/                               # architecture and governance docs
```

## Run

```bash
/usr/local/go/bin/go run ./cmd/agent-infra
```

Server address defaults to `:8080`. Override with `AGENT_INFRA_ADDR`.

## Test

```bash
/usr/local/go/bin/go test ./...
```

## API (implemented)

- `POST /v1/context/resolve`
- `POST /v1/decision/evaluate-runtime`
- `POST /v1/decision/evaluate-schedule-admission`
- `POST /v1/decision/evaluate-release`
- `POST /v1/decision/confirm-run-advance`
- `POST /v1/decision/repair-pending`
- `POST /v1/approval/cases`
- `POST /v1/approval/cases/{case_id}/decision`
- `GET /v1/decision/{decision_id}`
- `GET /v1/decision/outbox`
- `GET /v1/metrics/decision`

## Example request (runtime decision)

```bash
curl -sS -X POST http://127.0.0.1:8080/v1/decision/evaluate-runtime \
  -H 'Content-Type: application/json' \
  -d '{
    "request_id":"req-demo-1",
    "tenant_id":"tenant-a",
    "workflow_id":"wf-a",
    "run_id":"run-1",
    "step_id":"step-1",
    "risk_tier":"high",
    "effect_type":"external_write",
    "approval_system_available":true,
    "policy_engine_available":true,
    "phase":"PRE_TOOL",
    "dcu_input":{
      "feature_reads":2,
      "rule_evals":3,
      "dependency_calls":1,
      "conflict_resolutions":1
    },
    "freeze":{
      "frozen":{
        "context_candidates_snapshot_ref":"ctx-001",
        "policy_bundle_snapshot_ref":"pb-001",
        "feature_snapshot_id":"fs-001",
        "approval_routing_snapshot_ref":"ar-001",
        "quota_snapshot_ref":"qs-001",
        "scheduler_admission_input_snapshot_ref":"sa-001"
      },
      "dynamic_used":["trace_tags"]
    }
  }'
```
