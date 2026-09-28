# CPU continuity workload

This fixture runs a real NumPy optimization loop in one CPU slot. It uses the
Determined Core API to report training and validation metrics and to store and
restore model checkpoints. TensorBoard uses manual mode, so PyTorch and TensorFlow are not required.
It downloads no data and normally finishes in about
three minutes.

Run it from the repository root with a wheel-compatible task image:

```sh
export DET_MASTER=http://127.0.0.1:8080
export DET_USER=admin
export DET_PASS=fork-smoke-password
det experiment create \
  tools/fork/continuity-workload/experiment.yaml \
  tools/fork/continuity-workload
```

The template uses `determined-fork-cpu-task:local`, one CPU slot, and the `default`
resource pool. Override values without editing the fixture when preparing a
fault window:

```sh
det experiment create \
  --config environment.image=determined-fork-cpu-task:local \
  --config hyperparameters.steps=240 \
  --config hyperparameters.step_seconds=1.0 \
  --config hyperparameters.checkpoint_step=12 \
  --config hyperparameters.report_progress=false \
  tools/fork/continuity-workload/experiment.yaml \
  tools/fork/continuity-workload
```

`checkpoint_step` selects the first checkpoint. After that confirmed checkpoint,
the workload checkpoints again only at normal completion or preemption. A
restarted allocation restores the model, optimizer velocity, completed step,
and checkpoint schedule from the latest checkpoint supplied by Determined.

Every step prints one compact JSON record and atomically replaces
`/run/determined/workdir/continuity-progress.json`. Each record includes the
step, loss, PID, unique process `run_id`, allocation/container/task identity,
phase, and latest confirmed checkpoint UUID. Metric reporting uses
`metrics_before`/`metrics_after`, while optional progress reporting uses
`progress_before`/`progress_after`. Checkpoint calls similarly use
`checkpoint_before`/`checkpoint_after`. Set the `report_progress` hyperparameter
to `false` to isolate asynchronous metric behavior. Read the file from the task
container while injecting a fault:

```sh
docker exec TASK_CONTAINER \
  cat /run/determined/workdir/continuity-progress.json
```

Core API and checkpoint errors are intentionally not caught. They produce a
nonzero trial process with its last phase left in the progress file and the
Python traceback in Determined task logs. The template sets `max_restarts: 0`,
so the baseline records the first allocation failure without an automatic retry.
