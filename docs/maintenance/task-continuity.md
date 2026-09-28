# Task continuity and recovery

Master restarts can interrupt task control and reporting. A running container may
continue computing while the master is unavailable, but preserving a container,
keeping computation moving, and delivering metrics and checkpoints afterward are
separate outcomes. Plan upgrades around checkpoints; a master restart is not a
zero-downtime guarantee.

## Pause, resume, and kill

When a generic task tree is unpaused, the master saves the operation and each
member's intended allocation ID before starting allocations. If part of the
tree fails to start, **retry unpause on the same root task ID** after resolving
the cause. The root may already appear `ACTIVE`; the retry continues unfinished
members without assigning new IDs to members that started. Authorization is
checked for every affected task, including on retry.

Pause rejects a conflicting unfinished resume. Kill records cancellation for
affected members and signals allocations that started. On startup, the master
reconciles unfinished resume and cancellation operations before cleaning up
orphan allocations. If reconciliation fails, startup stops rather than silently
closing an intended allocation. Check the master error, repair the underlying
database or runtime issue, and restart; do not delete recovery records to make
startup proceed. A downgrade migration refuses to drop the resume table while
an operation remains unfinished.

## Reconnect window

Version 0.40.0 agents default to 30 reconnect attempts, five seconds apart;
the master-side `agent_reconnect_wait` default is 150 seconds. The last attempt
normally starts about 145 seconds after reconnecting begins, plus connection
time. These values are not an exact outage guarantee. Disconnected agents
cannot take new work while their reservations are retained.

**Upgrading only the master does not change a running agent's settings.** An
unmodified 0.38.1 agent defaults to five attempts at five-second intervals;
the 0.38.1 master's default wait is 25 seconds. Existing explicit agent
settings and saved dynamic resource-pool configurations also keep their
effective reconnect values.

Before a rolling or hot upgrade, inspect both sides of the actual deployment;
configure a compatible window and test the same agent, task SDK, checkpoint
storage, and expected master startup time. `agent_reattach_enabled` is
deprecated and ignored.

Optional `TrainContext.report_progress()` in the 0.40.0 SDK sends the latest
pending UI progress update from a background worker. Intermediate updates can
be coalesced or dropped during an outage without failing training. This does
not change the delivery or error behavior of training metrics, checkpoints,
or searcher decisions. An older SDK in an already-running task does not gain
the new progress reporter when the master is upgraded.

## Diagnose a restart

After the master returns, verify the agent rejoined and is enabled, then check
the task's allocation ID, container/process identity, progress, metrics, and
confirmed checkpoints. A completed experiment alone does not establish that
computation advanced throughout the outage. If an agent exhausted its retries,
or an allocation was replaced, treat that as a continuity failure and recover
from the last confirmed checkpoint. If the master cannot start because a
resume operation remains open, preserve its database state and logs while
fixing the reported cause.

For a disposable CPU diagnostic, use
[`tools/fork/continuity.py`](https://github.com/WU-CVGL/determined/blob/main/tools/fork/continuity.py) with the
[`continuity-workload` instructions](https://github.com/WU-CVGL/determined/blob/main/tools/fork/continuity-workload/README.md).
It runs a real Core API training loop and compares local progress and process
identity with metrics and checkpoints after recovery. Use images and settings
that match the deployment being assessed. The diagnostic does not validate GPU
reservations, every network fault, or all checkpoint failure windows.

For a planned upgrade, follow the [upgrade procedure](../manage/upgrade.rst):
disable agents, allow running work to checkpoint and stop, back up PostgreSQL,
then update the master and agents. Keep the previous images and a compatible
database backup for rollback; selecting an older image does not reverse schema
migrations.
