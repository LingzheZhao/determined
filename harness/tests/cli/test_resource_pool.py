import json
import pathlib

import pytest
from responses import matchers

from determined.cli import cli, errors, resource_pool
from tests.cli import util

MASTER = "http://localhost:8080"
DYNAMIC_POOLS_URL = f"{MASTER}/api/v1/resource-pools/dynamic"


def dynamic_pool_response(state: str = "Ready") -> dict:
    response = {
        "pool_name": "online-gpu",
        "cluster_name": "agent-cluster",
        "config_version": 1,
        "state": state,
        "config": {"pool_name": "online-gpu", "max_aux_containers_per_agent": 100},
        "created_at": "2026-09-20T00:00:00Z",
        "updated_at": "2026-09-20T00:00:00Z",
    }
    if state == "Failed":
        response["error"] = "scheduler initialization failed"
    return response


def test_create_dynamic_pool_posts_yaml_config_and_reports_failed_state(
    tmp_path: pathlib.Path, capsys: pytest.CaptureFixture[str]
) -> None:
    config_path = tmp_path / "pool.yaml"
    config_path.write_text(
        "pool_name: online-gpu\nmax_aux_containers_per_agent: 100\n", encoding="utf-8"
    )
    response = dynamic_pool_response("Failed")

    with util.standard_cli_rsps() as rsps:
        rsps.post(
            DYNAMIC_POOLS_URL,
            status=201,
            match=[
                matchers.json_params_matcher(
                    {
                        "cluster_name": "agent-cluster",
                        "idempotency_key": "request-123",
                        "config": {
                            "pool_name": "online-gpu",
                            "max_aux_containers_per_agent": 100,
                        },
                    }
                )
            ],
            json=response,
        )
        with pytest.raises(SystemExit) as failed_exit:
            cli.main(
                [
                    "resource-pool",
                    "create",
                    str(config_path),
                    "--idempotency-key",
                    "request-123",
                    "--cluster-name",
                    "agent-cluster",
                ]
            )
        assert failed_exit.value.code == 1

    captured = capsys.readouterr()
    output = captured.out
    assert "online-gpu" in output
    assert "Failed" in output
    assert "scheduler initialization failed" in output
    assert "success" not in output.lower()
    assert "failed" in captured.err.lower()


def test_create_dynamic_pool_json_output(
    tmp_path: pathlib.Path, capsys: pytest.CaptureFixture[str]
) -> None:
    config_path = tmp_path / "pool.json"
    config_path.write_text(json.dumps({"pool_name": "online-gpu"}), encoding="utf-8")
    response = dynamic_pool_response()

    with util.standard_cli_rsps() as rsps:
        rsps.post(DYNAMIC_POOLS_URL, status=201, json=response)
        cli.main(
            [
                "resource-pool",
                "create",
                str(config_path),
                "--idempotency-key",
                "request-json",
                "--json",
            ]
        )

    assert json.loads(capsys.readouterr().out) == response


def test_list_dynamic_pools_filters_cluster_and_prints_json(
    capsys: pytest.CaptureFixture[str],
) -> None:
    response = {"resource_pools": [dynamic_pool_response()]}
    with util.standard_cli_rsps() as rsps:
        rsps.get(
            DYNAMIC_POOLS_URL,
            status=200,
            match=[matchers.query_param_matcher({"cluster_name": "agent-cluster"})],
            json=response,
        )
        cli.main(
            [
                "resource-pool",
                "list-dynamic",
                "--cluster-name",
                "agent-cluster",
                "--json",
            ]
        )

    assert json.loads(capsys.readouterr().out) == response


def test_retry_dynamic_pool_posts_empty_body_and_renders_state(
    capsys: pytest.CaptureFixture[str],
) -> None:
    response = dynamic_pool_response()
    with util.standard_cli_rsps() as rsps:
        rsps.post(
            f"{DYNAMIC_POOLS_URL}/online-gpu/retry",
            status=200,
            match=[matchers.query_param_matcher({"cluster_name": "agent-cluster"})],
            json=response,
        )
        cli.main(
            [
                "resource-pool",
                "retry",
                "online-gpu",
                "--cluster-name",
                "agent-cluster",
            ]
        )

    output = capsys.readouterr().out
    assert "online-gpu" in output
    assert "Ready" in output


def test_dynamic_pool_config_must_be_mapping(tmp_path: pathlib.Path) -> None:
    config_path = tmp_path / "pool.yaml"
    config_path.write_text("- not\n- a\n- mapping\n", encoding="utf-8")

    with config_path.open() as config_file, pytest.raises(
        errors.CliError, match="resource pool config must be a YAML or JSON mapping"
    ):
        resource_pool._load_dynamic_pool_config(config_file)


def test_dynamic_pool_create_help_and_required_idempotency_key(
    tmp_path: pathlib.Path, capsys: pytest.CaptureFixture[str]
) -> None:
    with pytest.raises(SystemExit) as help_exit:
        cli.main(["resource-pool", "create", "--help"])
    assert help_exit.value.code == 0
    assert "--idempotency-key" in capsys.readouterr().out

    config_path = tmp_path / "pool.yaml"
    config_path.write_text("pool_name: online-gpu\n", encoding="utf-8")
    with pytest.raises(SystemExit) as parse_exit:
        cli.main(["resource-pool", "create", str(config_path)])
    assert parse_exit.value.code == 2
    assert "--idempotency-key" in capsys.readouterr().err
