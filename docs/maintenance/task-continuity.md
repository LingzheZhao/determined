# Task continuity baseline

The online-pool smoke proves pool publication and work after master recovery. It
does not prove that an already-running training loop continues during an outage.
This opt-in CPU probe measures that separate boundary without changing runtime
behavior or dispatching GitHub Actions. It uses real NumPy optimization, Core API
metrics, and checkpoints; it is not a substitute for GPU research-workload acceptance.

## Run one scenario

Use a local Docker host with Compose support for inline `configs.content`, an
installed fork wheel/CLI, Requests, and PyYAML. Supply previously built compatible
images; this tool does not build images or install dependencies.

```sh
export FORK_MASTER_IMAGE=your-master-image
export FORK_AGENT_IMAGE=your-agent-image
export DET_PASS='ContinuityTest2026!'
export CONTINUITY_CHECKPOINT_DIR=/absolute/disposable/checkpoint-directory
mkdir -p "$CONTINUITY_CHECKPOINT_DIR"
python tools/fork/continuity.py \
  --compose-file tools/fork/continuity-compose.yaml \
  --master-url http://127.0.0.1:28081 \
  --task-image your-cpu-task-image \
  --scenario baseline --steps 30 \
  --output /absolute/new-output-directory
```

The task image needs the matching Determined wheel and NumPy. The supplied
Compose project uses one artificial CPU slot, bounded service resources, and a
localhost-only API. The driver refuses existing project/task containers and
requires its master URL to match the project's published port. It removes its
containers/network/database after collecting evidence, retaining checkpoint files
and the output directory. Use a new output directory for each invocation.

Select `master-outage --outage-seconds 10 --steps 100` to kill only the disposable
master, hold it offline, and bring it back. `agent-restart` performs the same
operation on the test agent. `--report-progress` additionally exercises synchronous
progress reporting; by default only asynchronous training metrics are reported
inside the computation loop. A checkpoint is confirmed before the fault, then
another is committed at normal completion. Automatic trial restarts are disabled.

An exit status of 1 means the scenario did not meet its invariants; preserve and
inspect `report.json` and logs. A completed experiment alone is not a continuity
pass. The report compares process/container/allocation identity and local training
progress during the fault, then checks checkpoint and metric records after recovery.

## What this measures

The workload writes an atomic progress file inside the task container. Reading it
through Docker avoids depending on the unavailable master. Phase markers distinguish
computation, asynchronous metric enqueueing, synchronous progress calls, and
checkpoint submission. Process UUID/PID and container identity expose restarts.

The first cases should separate three questions: does the container survive, does
computation keep advancing, and does buffered metadata reach the master afterward?
Use the measured failure to choose the next runtime change; do not hide failures
by increasing retries without recording the configuration.

The supplied pool uses a 90-second master-side reconnect wait. Agent reconnection
retains the existing default of five attempts with a five-second backoff, which is
a separate, shorter boundary. Set `CONTINUITY_RECONNECT_ATTEMPTS` explicitly when
comparing a longer retry window. `agent_reattach_enabled` is deprecated and ignored;
setting it does not establish an outage guarantee.

GPU reservation safety, network blackholes, response loss during checkpoint
registration, disk-buffer limits, task completion during an outage, and mixed-version
upgrades require additional targeted scenarios. This baseline makes no claim that
those scenarios pass.

## Measured baseline: 2026-09-20

The isolated workstation used Linux amd64, Docker 29.7.2/Compose 5.4.0, Python
3.12.13 and NumPy 2.5.3. Master, agent and wheel came from the previously accepted
`bf7bb52a4` build, whose production source tree matches merged `4fb5e0305`. This
probe changes no master, agent or harness runtime code. Both GPUs were occupied
and were not used.

All scenarios used a real Core API trial, one artificial CPU slot, one metric per
step, a 0.5-second step interval, no automatic trial restart, and a 90-second
master-side agent reconnect wait. TensorBoard was in manual mode. Checkpoints
were committed at step 3 and the final step, outside the outage window.

| Scenario | Result | Evidence |
| --- | --- | --- |
| No fault, 30 steps | Pass | All 30 metrics, two confirmed checkpoints, zero restarts |
| Master killed for 10 seconds, async metrics | Pass | Same allocation/process/container; sampled progress age at most 0.47s; all 100 metrics and both checkpoints |
| Agent killed for 10 seconds | Pass | Same allocation/process/container; sampled progress age at most 0.44s; all 100 metrics and both checkpoints |
| Master killed for 10 seconds, synchronous progress enabled | Continuity failure | Same process, but stopped at step 10 in `progress_before`; observed progress age reached 8.15s; eventually finished with all 100 metrics |
| Master killed for 30 seconds, default five agent reconnect attempts | Recovery failure | Computation continued in the original process; agent exhausted retries and exited with code 1 |
| Master killed for 45 seconds, twelve agent reconnect attempts | Pass | Same allocation/process/container; sampled progress age at most 0.47s; all 180 metrics, both checkpoints, zero restarts and an enabled recovered agent |

The driver samples through Docker approximately every two seconds. Passing the
computation check requires unchanged identity and no sampled progress age over
three seconds at this step interval. These observations do not promise zero
latency or cover higher metric rates, larger queues, or network blackholes.

The negative cases are deliberate measurements, not expected-pass regressions.
Their JSON reports have `passed: false`; do not suppress that outcome in a release
gate. The synchronous progress case demonstrates why eventual experiment success
and container survival are insufficient evidence of continuous computation.

The next runtime change should separate optional, latest-value progress reporting
from the training thread, with bounded pending state, request timeout, and shutdown.
Checkpoint acknowledgement and control-decision traffic need separate reliable
semantics; this result does not justify dropping their errors.

Raw reports, trial/service logs and checkpoint files are retained on the workstation
under `~/.cache/determined-validation/20260920-continuity/`. The test services are
disposable and are removed after each scenario. No GitHub Actions were run for this acceptance.
