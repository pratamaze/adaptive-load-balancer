# A/B Test Playbook (CPU Guarded)

## Goal
Compare baseline fuzzy vs offline-tuned fuzzy fairly, while preventing both backend nodes from saturating at 100% CPU.

## Phase Setup
1. Baseline:
- `LB_ALGO=fuzzy`
- `ACTIVE_ALGO=FUZZY_MANUAL`
- `FUZZY_CONFIG_PATH=configs/base_fuzzy_params.json`

2. Offline training:
- Pull dataset (`make pull-logs`)
- Train locally (`make train`)

3. Offline tuned:
- `LB_ALGO=fuzzy`
- `ACTIVE_ALGO=FUZZY_MOPSO_OFFLINE`
- `FUZZY_CONFIG_PATH=configs/optimized_fuzzy_params.json`

## Dataset Logging Strategy
Recommended for richer training data:
- `TRAFFIC_LOG_MODE=per_hit`
- `TRAFFIC_LOG_INTERVAL=1s` (ignored in `per_hit` mode)
- `TRAFFIC_LOG_ONLY_LOADTEST=true` (hanya endpoint load test `/api/stress-test` dan `/fetch`)

Alternative low-overhead mode:
- `TRAFFIC_LOG_MODE=window`
- `TRAFFIC_LOG_INTERVAL=1s`

## CPU Guard Rules
1. Target working zone:
- Node-1 CPU: 60%-85%
- Node-2 CPU: 45%-80%

2. Hard guard:
- If both nodes are `>90%` for more than 30s, reduce load input (RPS or stress_ms).

3. Rebalance load profile before rerun:
- Decrease `stress_ms`
- Reduce throughput timer
- Increase pacing jitter

## Fair Run Protocol
1. Warm-up each run for 60s (not counted).
2. Measure window: 300s.
3. Repeat each phase at least 3 runs.
4. Compare median + p95 latency + imbalance metrics.
5. Use the same JMeter script, host, and timing across phases.

## Verification Before Each Run
1. `make verify-config`
2. `make cpu-snapshot`
3. Confirm LB startup logs show active config path and mode.
4. Confirm `traffic_dataset.csv` is growing in the expected pattern.
