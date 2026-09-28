#!/usr/bin/env python3
"""Measure one CPU continuity scenario in a disposable, explicitly named Compose project."""

import argparse
import json
import os
import pathlib
import re
import signal
import subprocess
import time
from urllib.parse import urlparse

import requests
import yaml


class ExperimentFailed(Exception):
    pass


def run(*args, timeout=30):
    result = subprocess.run(args, text=True, capture_output=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f"{args[0]} failed: {result.stderr[-2000:]} {result.stdout[-2000:]}")
    return result.stdout


def wait_for(label, check, seconds=120):
    deadline = time.monotonic() + seconds
    last_error = None
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (RuntimeError, requests.RequestException, ValueError, KeyError) as error:
            last_error = str(error)
        time.sleep(1)
    raise RuntimeError(f"timed out: {label}; last error: {last_error}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--compose-file", required=True)
    parser.add_argument("--master-url", required=True)
    parser.add_argument("--task-image", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument(
        "--scenario", choices=["baseline", "master-outage", "agent-restart"], required=True
    )
    parser.add_argument("--outage-seconds", type=int, default=10)
    parser.add_argument("--steps", type=int, default=100)
    parser.add_argument("--step-seconds", type=float, default=0.5)
    parser.add_argument("--report-progress", action="store_true")
    args = parser.parse_args()
    if urlparse(args.master_url).hostname not in ("localhost", "127.0.0.1"):
        parser.error("master URL must be localhost")
    if args.outage_seconds < 1 or args.steps < 20 or args.step_seconds <= 0:
        parser.error("require outage >= 1 second, steps >= 20, step-seconds > 0")
    if (
        args.scenario != "baseline"
        and (args.steps - 10) * args.step_seconds < args.outage_seconds + 15
    ):
        parser.error("training must extend at least 15 seconds beyond the fault window")
    compose = ["docker", "compose", "-f", str(pathlib.Path(args.compose_file).resolve())]
    config = json.loads(run(*compose, "config", "--format", "json"))
    project = config["name"]
    if not project.startswith("determined-continuity-"):
        parser.error("use a disposable project named determined-continuity-*")
    agent = config["services"]["determined-agent"]["environment"]
    agent_id = agent["DET_AGENT_ID"]
    if not agent_id.startswith("continuity-") or agent.get("DET_SLOT_TYPE") != "cpu":
        parser.error("use a dedicated continuity-* CPU agent")
    master_url = urlparse(args.master_url)
    if master_url.port is None:
        parser.error("master URL must include the Compose-published port")
    master_ports = config["services"]["determined-master"].get("ports", [])
    matching_ports = [port for port in master_ports if int(port["published"]) == master_url.port]
    if len(matching_ports) != 1:
        parser.error("master URL port must match exactly one determined-master published port")
    if matching_ports[0].get("host_ip") not in ("127.0.0.1", "::1", "localhost"):
        parser.error("determined-master published port must bind only to localhost")
    for service_name in ("determined-master", "determined-agent"):
        service = config["services"][service_name]
        restart = str(service.get("restart", "no")).lower()
        deploy_restart = service.get("deploy", {}).get("restart_policy", {}).get("condition")
        if restart not in ("", "no", "none") or deploy_restart not in (None, "none"):
            parser.error(f"{service_name} must not have an automatic restart policy")
    agent_filter = f"label=ai.determined.container.agent={agent_id}"
    project_filter = f"label=com.docker.compose.project={project}"
    if run("docker", "ps", "-aq", "--filter", project_filter).strip():
        parser.error("project already has containers; refuse to interrupt an existing run")
    if run("docker", "ps", "-aq", "--filter", agent_filter).strip():
        parser.error("agent already owns containers; use a fresh agent ID")
    output = pathlib.Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=False)
    report = {"scenario": args.scenario, "parameters": vars(args), "samples": [], "passed": False}
    session = requests.Session()
    session.trust_env = False
    password = os.environ["DET_PASS"]
    os.environ.update(DET_MASTER=args.master_url, DET_USER="admin")

    def login():
        response = session.post(
            args.master_url + "/api/v1/auth/login",
            json={"username": "admin", "password": password, "isHashed": False},
            timeout=3,
        )
        response.raise_for_status()
        session.headers["Authorization"] = "Bearer " + response.json()["token"]
        return True

    def api(path):
        response = session.get(args.master_url + "/api/v1/" + path, timeout=5)
        response.raise_for_status()
        return response.json()

    def training_metrics(trial_id):
        metrics = []
        with session.get(
            args.master_url + "/api/v1/trials/metrics/training_metrics",
            params={"trialIds": trial_id},
            stream=True,
            timeout=(3, 10),
        ) as response:
            response.raise_for_status()
            for line in response.iter_lines(chunk_size=1024 * 1024):
                if not line:
                    continue
                envelope = json.loads(line)
                if "error" in envelope:
                    raise RuntimeError(f"training metrics stream failed: {envelope['error']}")
                metrics.extend(envelope["result"]["metrics"])
        return metrics

    def service_container(service):
        ids = run(*compose, "ps", "-q", service).split()
        if len(ids) != 1:
            raise RuntimeError(f"expected one running {service} container, found {len(ids)}")
        return ids[0]

    def service_stopped(container_id):
        item = json.loads(run("docker", "inspect", container_id))[0]
        if item["State"]["Running"]:
            raise RuntimeError(f"faulted service container {container_id} restarted during outage")
        return True

    def snapshot():
        ids = run("docker", "ps", "-q", "--no-trunc", "--filter", agent_filter).split()
        if len(ids) != 1:
            raise RuntimeError(f"expected one task container, found {len(ids)}")
        item = json.loads(run("docker", "inspect", ids[0]))[0]
        progress = json.loads(
            run("docker", "exec", ids[0], "cat", "/run/determined/workdir/continuity-progress.json")
        )
        sample = {
            "time": time.time(),
            "docker_id": ids[0],
            "host_pid": item["State"]["Pid"],
            "started_at": item["State"]["StartedAt"],
            "progress": progress,
        }
        report["samples"].append(sample)
        return sample

    def ready_task():
        state = api(f"experiments/{report['experiment_id']}")["experiment"]["state"]
        if state in ("STATE_ERROR", "STATE_CANCELED", "STATE_COMPLETED"):
            raise ExperimentFailed(f"experiment ended before observable training: {state}")
        sample = snapshot()
        return (
            sample
            if sample["progress"]["step"] >= 8 and sample["progress"]["checkpoint_uuid"]
            else None
        )

    def interrupted(signum, frame):
        raise InterruptedError(f"interrupted by signal {signum}")

    signal.signal(signal.SIGTERM, interrupted)
    try:
        run(*compose, "up", "-d", "postgres", "determined-master", timeout=120)
        wait_for("master login", login)
        run(*compose, "up", "-d", "determined-agent", timeout=120)
        workload = pathlib.Path(__file__).resolve().parent / "continuity-workload"
        experiment = yaml.safe_load((workload / "experiment.yaml").read_text())
        experiment["environment"]["image"] = args.task_image
        experiment["max_restarts"] = 0
        experiment["hyperparameters"].update(
            steps=args.steps,
            step_seconds=args.step_seconds,
            checkpoint_step=3,
            metrics_every=1,
            report_progress=args.report_progress,
        )
        experiment_file = output / "experiment.yaml"
        experiment_file.write_text(yaml.safe_dump(experiment))
        submitted = run("det", "experiment", "create", str(experiment_file), str(workload))
        (output / "submit.log").write_text(submitted)
        experiment_id = int(re.search(r"Created experiment (\d+)", submitted).group(1))
        report["experiment_id"] = experiment_id
        trials = wait_for(
            "trial creation", lambda: api(f"experiments/{experiment_id}/trials")["trials"]
        )
        trial_id = int(trials[0]["id"])
        report["trial_id"] = trial_id
        before = wait_for("training with confirmed checkpoint", ready_task)
        report["before"] = before
        report["trial_before"] = api(f"trials/{trial_id}")
        if args.scenario != "baseline":
            service = (
                "determined-master" if args.scenario == "master-outage" else "determined-agent"
            )
            service_id = service_container(service)
            run(*compose, "kill", "--signal", "SIGKILL", service)
            wait_for("faulted service stopped", lambda: service_stopped(service_id), seconds=10)
            deadline = time.monotonic() + args.outage_seconds
            report["fault_started"] = time.time()
            while time.monotonic() < deadline:
                service_stopped(service_id)
                try:
                    snapshot()
                except RuntimeError as error:
                    report["samples"].append({"time": time.time(), "error": str(error)})
                time.sleep(min(2, max(0, deadline - time.monotonic())))
            report["fault_ended"] = time.time()
            run(*compose, "up", "-d", service, timeout=120)
            wait_for("master recovery", login)
            after = wait_for("task after recovery", snapshot)
            report["after"] = after
            identity = ["allocation_id", "container_id", "task_id", "pid", "run_id"]
            report["same_identity"] = all(
                before[key] == after[key] for key in ("docker_id", "host_pid", "started_at")
            ) and all(before["progress"][key] == after["progress"][key] for key in identity)
            offline = [
                sample
                for sample in report["samples"]
                if report["fault_started"] <= sample["time"] <= report["fault_ended"]
                and "progress" in sample
            ]
            report["max_offline_progress_age"] = max(
                (sample["time"] - sample["progress"]["time_unix"] for sample in offline),
                default=float(args.outage_seconds),
            )
            report["offline_progress"] = len(offline) > 1 and (
                offline[-1]["progress"]["step"] > offline[0]["progress"]["step"]
                and report["max_offline_progress_age"] <= max(3, args.step_seconds * 3)
                and not any("error" in sample for sample in report["samples"])
            )

        agent_container = run(*compose, "ps", "-a", "-q", "determined-agent").strip()
        agent_state = json.loads(run("docker", "inspect", agent_container))[0]["State"]
        report["agent_after"] = {key: agent_state[key] for key in ("Running", "ExitCode")}
        if not agent_state["Running"]:
            raise ExperimentFailed("agent exited and did not recover after the fault")
        report["agent_ready"] = wait_for(
            "enabled recovered agent",
            lambda: next(
                (
                    a
                    for a in api("agents").get("agents", [])
                    if a["id"].split("/")[-1] == agent_id
                    and a.get("enabled")
                    and not a.get("draining")
                ),
                None,
            ),
            seconds=30,
        )

        def completed():
            experiment_state = api(f"experiments/{experiment_id}")["experiment"]["state"]
            return (
                experiment_state
                if experiment_state in ("STATE_COMPLETED", "STATE_ERROR", "STATE_CANCELED")
                else None
            )

        final_state = wait_for(
            "experiment completion", completed, seconds=int(args.steps * args.step_seconds + 120)
        )
        report["experiment_state"] = final_state
        if final_state != "STATE_COMPLETED":
            raise RuntimeError(f"experiment ended with {final_state}")
        report["trial_after"] = api(f"trials/{trial_id}")
        if report["trial_after"]["trial"]["restarts"] != 0:
            raise RuntimeError("trial restarted despite max_restarts=0")
        report["checkpoints"] = api(f"trials/{trial_id}/checkpoints")["checkpoints"]
        confirmed = [c for c in report["checkpoints"] if c["state"] == "STATE_COMPLETED"]
        pre_uuid = before["progress"]["checkpoint_uuid"]
        pre = [c for c in confirmed if c["uuid"] == pre_uuid]
        post = [c for c in confirmed if c["uuid"] != pre_uuid]
        if len(confirmed) != 2 or len(pre) != 1 or len(post) != 1:
            raise RuntimeError("expected exactly one initial and one distinct final checkpoint")
        pre_step = int(pre[0]["metadata"]["steps_completed"])
        post_step = int(post[0]["metadata"]["steps_completed"])
        if pre_step != 3 or post_step != args.steps:
            raise RuntimeError(
                f"checkpoint steps do not bracket the run: initial={pre_step}, final={post_step}"
            )
        report["checkpoint_assertions"] = {
            "initial_uuid": pre_uuid,
            "initial_step": pre_step,
            "final_uuid": post[0]["uuid"],
            "final_step": post_step,
        }
        metrics = training_metrics(trial_id)
        metric_steps = [int(metric["totalBatches"]) for metric in metrics]
        expected_steps = list(range(1, args.steps + 1))
        metric_run_ids = sorted({int(metric["trialRunId"]) for metric in metrics})
        report["training_metrics"] = {
            "count": len(metrics),
            "steps": metric_steps,
            "trial_run_ids": metric_run_ids,
        }
        if metric_steps != expected_steps or len(metric_run_ids) != 1:
            missing = sorted(set(expected_steps) - set(metric_steps))
            extra = sorted(set(metric_steps) - set(expected_steps))
            raise RuntimeError(
                "training metrics do not cover each step exactly once: "
                f"missing={missing}, extra={extra}, run_ids={metric_run_ids}"
            )
        report["passed"] = args.scenario == "baseline" or (
            report["same_identity"] and report["offline_progress"]
        )
        if not report["passed"]:
            report["error"] = "training process identity or progress did not survive the fault"
    except Exception as error:
        report["error"] = str(error)
    finally:
        for name, command in (
            ("compose.log", [*compose, "logs", "--no-color"]),
            ("trial.log", ["det", "trial", "logs", str(report.get("trial_id", 0))]),
            ("task.log", ["docker", "logs", report.get("before", {}).get("docker_id", "")]),
        ):
            try:
                result = subprocess.run(command, text=True, capture_output=True, timeout=20)
                (output / name).write_text(result.stdout + result.stderr)
            except subprocess.TimeoutExpired:
                pass
        try:
            run(*compose, "stop", "determined-master", "determined-agent", timeout=60)
            remaining = run("docker", "ps", "-aq", "--filter", agent_filter).split()
            if remaining:
                run("docker", "rm", "-f", *remaining)
            run(*compose, "down", "--volumes", "--remove-orphans", timeout=90)
        except Exception as error:
            report["cleanup_error"] = str(error)
            report["passed"] = False
        (output / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(
        json.dumps(
            {
                "passed": report["passed"],
                "error": report.get("error"),
                "report": str(output / "report.json"),
            }
        )
    )
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
