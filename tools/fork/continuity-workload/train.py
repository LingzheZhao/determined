#!/usr/bin/env python3
"""Small NumPy training workload for observing task continuity failures."""

import hashlib
import json
import math
import os
import pathlib
import socket
import time
import uuid
from typing import Any, Dict, Optional

import numpy as np

import determined as det


PROGRESS_PATH = pathlib.Path(
    os.environ.get("CONTINUITY_PROGRESS_PATH", "/run/determined/workdir/continuity-progress.json")
)


class Progress:
    def __init__(self, checkpoint_uuid: Optional[str]) -> None:
        self.run_id = str(uuid.uuid4())
        self.state: Dict[str, Any] = {
            "allocation_id": os.environ.get("DET_ALLOCATION_ID"),
            "checkpoint_uuid": checkpoint_uuid,
            "container_id": os.environ.get("DET_CONTAINER_ID"),
            "hostname": socket.gethostname(),
            "loss": None,
            "pid": os.getpid(),
            "run_id": self.run_id,
            "step": 0,
            "task_id": os.environ.get("DET_TASK_ID"),
            "trial_id": os.environ.get("DET_TRIAL_ID"),
        }

    def write(self, phase: str, **updates: Any) -> None:
        self.state.update(updates, phase=phase, time_unix=time.time())
        encoded = json.dumps(self.state, sort_keys=True, separators=(",", ":"), allow_nan=False)
        PROGRESS_PATH.parent.mkdir(parents=True, exist_ok=True)
        temporary = PROGRESS_PATH.with_name(f".{PROGRESS_PATH.name}.{self.run_id}.tmp")
        with temporary.open("w", encoding="utf-8") as stream:
            stream.write(encoded + "\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, PROGRESS_PATH)
        print(encoded, flush=True)


def restore(core_context: det.core.Context, checkpoint_uuid: str) -> Dict[str, Any]:
    with core_context.checkpoint.restore_path(checkpoint_uuid) as path:
        with np.load(path / "state.npz", allow_pickle=False) as saved:
            model = {
                "weights": saved["weights"].astype(np.float64, copy=True),
                "velocity": saved["velocity"].astype(np.float64, copy=True),
                "step": int(saved["step"].item()),
                "first_checkpoint_done": bool(saved["first_checkpoint_done"].item()),
            }
    if model["weights"].shape != (32,) or model["velocity"].shape != (32,):
        raise ValueError("checkpoint contains incompatible model state")
    model["last_checkpoint_step"] = model["step"]
    return model


def checkpoint(core_context: det.core.Context, model: Dict[str, Any], progress: Progress) -> str:
    progress.write("checkpoint_before")
    metadata = dict(
        steps_completed=model["step"], loss=progress.state["loss"], run_id=progress.run_id
    )
    with core_context.checkpoint.store_path(metadata) as (path, checkpoint_uuid):
        np.savez(
            path / "state.npz",
            weights=model["weights"],
            velocity=model["velocity"],
            step=np.asarray(model["step"], dtype=np.int64),
            first_checkpoint_done=np.asarray(model["first_checkpoint_done"], dtype=np.bool_),
        )
    model["last_checkpoint_step"] = model["step"]
    progress.write("checkpoint_after", checkpoint_uuid=checkpoint_uuid)
    return checkpoint_uuid


def report(
    core_context: det.core.Context,
    progress: Progress,
    steps: int,
    weight_error: float,
    report_progress: bool,
) -> None:
    step, loss = progress.state["step"], progress.state["loss"]
    progress.write("metrics_before")
    core_context.train.report_training_metrics(
        steps_completed=step, metrics={"loss": loss, "weight_error": weight_error}
    )
    progress.write("metrics_after")
    if report_progress:
        progress.write("progress_before")
        core_context.train.report_progress(step / steps)
        progress.write("progress_after")


def main() -> None:
    info = det.get_cluster_info()
    assert info is not None and info.trial is not None, "run this workload as a Determined trial"
    hp = info.trial.hparams
    steps, checkpoint_step, metrics_every = map(
        int, (hp["steps"], hp["checkpoint_step"], hp["metrics_every"])
    )
    step_seconds, learning_rate = map(float, (hp["step_seconds"], hp["learning_rate"]))
    report_progress = hp["report_progress"]
    if not isinstance(report_progress, bool):
        raise ValueError("report_progress must be a boolean")
    if min(steps, checkpoint_step, metrics_every) <= 0 or step_seconds < 0 or learning_rate <= 0:
        raise ValueError("steps/intervals/learning_rate are outside their supported range")

    rng = np.random.default_rng(20260920)
    features = rng.normal(size=(512, 32)).astype(np.float64)
    target_weights = rng.normal(scale=0.5, size=32).astype(np.float64)
    targets = features @ target_weights + rng.normal(scale=0.05, size=512)
    model: Dict[str, Any] = {
        "weights": np.zeros(32, dtype=np.float64),
        "velocity": np.zeros(32, dtype=np.float64),
        "step": 0,
        "first_checkpoint_done": False,
        "last_checkpoint_step": -1,
    }
    progress = Progress(info.latest_checkpoint)

    with det.core.init(tensorboard_mode=det.core.TensorboardMode.MANUAL) as core_context:
        progress.write("starting")
        if info.latest_checkpoint is not None:
            model = restore(core_context, info.latest_checkpoint)
            progress.write("restored", step=model["step"])

        loss = float(np.mean(np.square(features @ model["weights"] - targets)))
        for step in range(model["step"] + 1, steps + 1):
            offset = ((step - 1) * 64) % len(features)
            batch_x, batch_y = features[offset : offset + 64], targets[offset : offset + 64]
            residual = batch_x @ model["weights"] - batch_y
            loss = float(np.mean(np.square(residual)))
            gradient = (2.0 / len(batch_x)) * (batch_x.T @ residual) + 1e-4 * model["weights"]
            model["velocity"] = 0.9 * model["velocity"] + gradient
            model["weights"] -= learning_rate * model["velocity"]
            model["step"] = step
            weight_error = float(np.linalg.norm(model["weights"] - target_weights))
            if not math.isfinite(loss) or not np.all(np.isfinite(model["weights"])):
                raise FloatingPointError(f"non-finite training state at step {step}")

            progress.write(
                "training",
                step=step,
                loss=loss,
                weight_digest=hashlib.sha256(model["weights"].tobytes()).hexdigest()[:16],
                weight_error=weight_error,
            )
            if step % metrics_every == 0 or step == steps:
                report(core_context, progress, steps, weight_error, report_progress)
            if not model["first_checkpoint_done"] and step >= checkpoint_step:
                model["first_checkpoint_done"] = True
                checkpoint(core_context, model, progress)
            if core_context.preempt.should_preempt():
                if model["last_checkpoint_step"] != step:
                    checkpoint(core_context, model, progress)
                progress.write("preempted")
                return
            if step < steps:
                time.sleep(step_seconds)

        if model["last_checkpoint_step"] != steps:
            checkpoint(core_context, model, progress)
        final_loss = float(np.mean(np.square(features @ model["weights"] - targets)))
        progress.write("validation_before", loss=final_loss)
        core_context.train.report_validation_metrics(
            steps_completed=steps, metrics={"loss": final_loss}
        )
        progress.write("completed")


if __name__ == "__main__":
    main()
