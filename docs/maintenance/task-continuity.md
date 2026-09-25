# Task continuity baseline

## Generic task resume operations

Unpausing a generic task tree records an operation ID, its target members, and
each member's old and intended allocation ID before starting any member. If a
later member fails to start, retry unpause on the same root task ID. The root
may already show ACTIVE; the retry continues the recorded members and keeps
allocation IDs that were already started.

Pause rejects a conflicting unfinished resume. Kill records cancellation
for affected members and signals any intended allocations that started. Master
startup reconciles unfinished resume and cancellation records before orphan
allocation cleanup. If reconciliation cannot finish, startup fails so an open
intended allocation is not silently closed; correct the underlying failure and
restart. The downgrade migration refuses to drop the resume table while any
operation is unfinished.

## CPU training continuity

The online-pool smoke proves pool publication and work after master recovery. It
does not prove that an already-running training loop continues during an outage.
This opt-in CPU probe measures that separate boundary without dispatching
GitHub Actions. It uses real NumPy optimization, Core API
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
operation on the test agent. `--report-progress` additionally exercises optional UI
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
computation, asynchronous metric enqueueing, progress API calls, and
checkpoint submission. Process UUID/PID and container identity expose restarts.

The first cases should separate three questions: does the container survive, does
computation keep advancing, and does buffered metadata reach the master afterward?
Use the measured failure to choose the next runtime change; do not hide failures
by increasing retries without recording the configuration.

The supplied pool uses a 150-second master-side reconnect wait and 30 agent
reconnect attempts with a five-second backoff, matching the updated runtime
defaults. These are separate limits: configure `CONTINUITY_RECONNECT_ATTEMPTS`
and `CONTINUITY_RECONNECT_WAIT` together when comparing other windows. Explicit
settings in existing deployments are preserved; upgrading does not replace them. `agent_reattach_enabled` is deprecated and ignored;
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

This baseline motivated separating optional, latest-value progress reporting from
the training thread. Checkpoint acknowledgement and control-decision traffic need
separate reliable semantics; this result does not justify dropping their errors.

Raw reports, trial/service logs and checkpoint files are retained on the workstation
under `~/.cache/determined-validation/20260920-continuity/`. The test services are
disposable and are removed after each scenario. No GitHub Actions were run for this acceptance.


## Recovery changes after the baseline

Optional `TrainContext.report_progress()` now publishes to a dedicated background
worker. Only the latest pending value is retained; intermediate UI updates may be
coalesced. Each HTTP request uses a five-second connect/read timeout and disables
the session's nested HTTP retries. A retry round allows 30 attempts separated by
five seconds, taking the newest pending value before each attempt. Exhausted
rounds drop the optional value with a warning; future reports can still recover.
Permanent API errors disable this reporter with an error log. These failures do
not fail training. Invalid progress values still fail synchronously.

Closing the reporter allows at most five seconds for a final update, then returns
with a warning if an HTTP request remains in flight. It starts no further requests
after the shutdown deadline. A Requests timeout bounds socket connect/read waits,
not every possible DNS or slow-stream duration; the worker is a daemon so it
cannot hold up process exit. Metrics, checkpoints and searcher decisions retain
their existing delivery/error semantics. The five-second bound applies to the
progress reporter, not the entire Core context shutdown.

Agent defaults are now 30 reconnect attempts with five-second spacing; the shared
master default is 150 seconds. The final agent attempt normally starts around
145 seconds after retrying begins, plus connection-attempt time. This is not an
exact wall-clock outage guarantee. Explicit deployment overrides remain in force.
Existing durable online pools also retain their stored reconnect wait; the new
default does not rewrite persisted pool configurations. Online-pool creation
idempotency compares effective configurations; replaying an old request with
omitted defaults after an upgrade can therefore conflict. Use the stored effective
configuration, including its reconnect wait, when replaying an old creation.
Disconnected agents cannot accept new work while their reservations are retained,
so a longer recovery window also delays failed-agent cleanup.


## Recovery acceptance: 2026-09-20

The updated master and agent binaries (Go 1.22.12) and harness wheel were rebuilt
on the same workstation. Disposable `determined-ws-{master,agent,task}:progress-recovery`
images reused the accepted base images and their unchanged UI/dependencies, with
new binaries and wheel installed. This was a local runtime acceptance build, not a
new distribution release. Runtime source hashes, binary hashes and image IDs are
retained with the reports.

All three scenarios enabled `--report-progress`, used a 0.5-second step interval
and disabled automatic trial restarts. To verify actual runtime defaults, the
acceptance Compose configuration removed the agent reconnect environment settings
and the pool's `agent_reconnect_wait` field entirely. The resulting settings were
30 attempts, five-second spacing and a 150-second master wait.

| Scenario | Result | Evidence |
| --- | --- | --- |
| No fault, 30 steps, progress enabled | Pass | All 30 metrics, checkpoints at steps 3 and 30, zero restarts |
| Master killed for 10 seconds, progress enabled | Pass | Same allocation/process/container; sampled progress age at most 0.49s; all 100 metrics, both checkpoints, enabled recovered agent |
| Master killed for 120 seconds, progress enabled | Pass | Same allocation/process/container; step 8 before fault and 253 after recovery; sampled progress age at most 0.51s; all 380 metrics, checkpoints at steps 3 and 380, zero restarts and enabled recovered agent |

The focused local Python progress/metrics checks passed (eight tests). Workstation
Go tests passed for `./agent/internal/options`, `./agent/cmd/determined-agent` and
`./master/internal/config`. The added progress regressions are opt-in and do not
expand the default quick check. No GitHub Actions were dispatched.

Reports are under `~/.cache/determined-validation/20260920-progress-recovery/logs/`
in `baseline-01`, `master-short-01` and `master-long-01`. The resolved configuration
without reconnect overrides is retained as `runtime-defaults.yaml` in the parent
directory. Test containers and network were removed after acceptance. GPU tasks,
checkpoint registration during an outage, network blackholes and task completion
while the master is offline remain outside this acceptance.
