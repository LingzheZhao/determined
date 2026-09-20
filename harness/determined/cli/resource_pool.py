import argparse
from typing import Any, Dict, List, Mapping, Optional, Sequence
from urllib import parse

from determined import cli
from determined.cli import errors, render
from determined.common import util
from determined.common.api import bindings

DYNAMIC_RESOURCE_POOLS_PATH = "/api/v1/resource-pools/dynamic"


def _cluster_params(cluster_name: Optional[str]) -> Dict[str, str]:
    return {"cluster_name": cluster_name} if cluster_name else {}


def _render_dynamic_pools(resource_pools: Sequence[Mapping[str, Any]]) -> None:
    render.tabulate_or_csv(
        headers=["Name", "Cluster", "State", "Error"],
        values=[
            [
                pool.get("pool_name", ""),
                pool.get("cluster_name", ""),
                pool.get("state", ""),
                pool.get("error") or "",
            ]
            for pool in resource_pools
        ],
        as_csv=False,
    )


def _fail_for_failed_pool(resource_pool: Mapping[str, Any]) -> None:
    if str(resource_pool.get("state", "")).lower() != "failed":
        return
    name = resource_pool.get("pool_name", "")
    detail = resource_pool.get("error") or "initialization failed"
    raise errors.CliError(f'dynamic resource pool "{name}" failed: {detail}')


def _load_dynamic_pool_config(config_file: Any) -> Dict[str, Any]:
    with config_file:
        config = util.safe_load_yaml_with_exceptions(config_file)
    if not isinstance(config, Mapping):
        raise errors.CliError("resource pool config must be a YAML or JSON mapping")
    return dict(config)


def create_dynamic(args: argparse.Namespace) -> None:
    config = _load_dynamic_pool_config(args.config)
    body: Dict[str, Any] = {
        "idempotency_key": args.idempotency_key,
        "config": config,
    }
    if args.cluster_name:
        body["cluster_name"] = args.cluster_name

    sess = cli.setup_session(args)
    resource_pool = sess.post(DYNAMIC_RESOURCE_POOLS_PATH, json=body).json()
    if args.json:
        render.print_json(resource_pool)
    else:
        _render_dynamic_pools([resource_pool])
    _fail_for_failed_pool(resource_pool)


def list_dynamic(args: argparse.Namespace) -> None:
    sess = cli.setup_session(args)
    response = sess.get(
        DYNAMIC_RESOURCE_POOLS_PATH,
        params=_cluster_params(args.cluster_name),
    ).json()
    resource_pools = response.get("resource_pools", [])
    if args.json:
        render.print_json(response)
    else:
        _render_dynamic_pools(resource_pools)


def retry_dynamic(args: argparse.Namespace) -> None:
    sess = cli.setup_session(args)
    pool_name = parse.quote(args.pool_name, safe="")
    resource_pool = sess.post(
        f"{DYNAMIC_RESOURCE_POOLS_PATH}/{pool_name}/retry",
        params=_cluster_params(args.cluster_name),
    ).json()
    if args.json:
        render.print_json(resource_pool)
    else:
        _render_dynamic_pools([resource_pool])
    _fail_for_failed_pool(resource_pool)


def add_binding(args: argparse.Namespace) -> None:
    sess = cli.setup_session(args)
    body = bindings.v1BindRPToWorkspaceRequest(
        resourcePoolName=args.pool_name, workspaceNames=args.workspace_names
    )
    bindings.post_BindRPToWorkspace(sess, body=body, resourcePoolName=args.pool_name)

    print(
        f'added bindings between the resource pool "{args.pool_name}" '
        f"and the following workspaces: {args.workspace_names}"
    )
    return


def remove_binding(args: argparse.Namespace) -> None:
    sess = cli.setup_session(args)
    body = bindings.v1UnbindRPFromWorkspaceRequest(
        resourcePoolName=args.pool_name,
        workspaceNames=args.workspace_names,
    )
    bindings.delete_UnbindRPFromWorkspace(sess, body=body, resourcePoolName=args.pool_name)

    print(
        f'removed bindings between the resource pool "{args.pool_name}" '
        f"and the following workspaces: {args.workspace_names}"
    )
    return


def replace_bindings(args: argparse.Namespace) -> None:
    sess = cli.setup_session(args)
    body = bindings.v1OverwriteRPWorkspaceBindingsRequest(
        resourcePoolName=args.pool_name,
        workspaceNames=args.workspace_names,
    )
    bindings.put_OverwriteRPWorkspaceBindings(sess, body=body, resourcePoolName=args.pool_name)

    print(
        f'replaced bindings of the resource pool "{args.pool_name}" '
        f"with those to the following workspaces: {args.workspace_names}"
    )
    return


def list_workspaces(args: argparse.Namespace) -> None:
    sess = cli.setup_session(args)
    resp = bindings.get_ListWorkspacesBoundToRP(sess, resourcePoolName=args.pool_name)
    workspace_names = ""

    if resp.workspaceIds:
        workspace_names = ", ".join(
            [
                workspace.name
                for workspace in bindings.get_GetWorkspaces(sess).workspaces
                if workspace.id in set(resp.workspaceIds)
            ]
        )

    render.tabulate_or_csv(
        headers=["resource pool", "workspaces"],
        values=[[args.pool_name, workspace_names]],
        as_csv=False,
    )
    return


args_description = [
    cli.Cmd(
        "resource-pool rp",
        None,
        "manage resource pools",
        [
            cli.Cmd(
                "create",
                create_dynamic,
                "create a dynamic resource pool",
                [
                    cli.Arg(
                        "config",
                        type=argparse.FileType("r"),
                        help="path to a YAML or JSON resource pool configuration",
                    ),
                    cli.Arg(
                        "--idempotency-key",
                        required=True,
                        help="unique key used to safely retry this request",
                    ),
                    cli.Arg(
                        "--cluster-name",
                        help="target agent resource manager cluster",
                    ),
                    cli.Arg("--json", action="store_true", help="print as JSON"),
                ],
            ),
            cli.Cmd(
                "list-dynamic",
                list_dynamic,
                "list dynamic resource pools",
                [
                    cli.Arg(
                        "--cluster-name",
                        help="filter by resource manager cluster",
                    ),
                    cli.Arg("--json", action="store_true", help="print as JSON"),
                ],
            ),
            cli.Cmd(
                "retry",
                retry_dynamic,
                "retry a failed dynamic resource pool",
                [
                    cli.Arg("pool_name", help="name of the dynamic resource pool"),
                    cli.Arg(
                        "--cluster-name",
                        help="target agent resource manager cluster",
                    ),
                    cli.Arg("--json", action="store_true", help="print as JSON"),
                ],
            ),
            cli.Cmd(
                "bindings",
                None,
                "manage resource pool bindings",
                [
                    cli.Cmd(
                        "add",
                        add_binding,
                        "add a resource-pool-to-workspace binding",
                        [
                            cli.Arg(
                                "pool_name", type=str, help="name of the resource pool to bind"
                            ),
                            cli.Arg(
                                "workspace_names",
                                nargs=argparse.ONE_OR_MORE,
                                type=str,
                                default=None,
                                help="the workspace to bind to",
                            ),
                        ],
                    ),
                    cli.Cmd(
                        "remove",
                        remove_binding,
                        "remove a resource-pool-to-workspace binding",
                        [
                            cli.Arg(
                                "pool_name", type=str, help="name of the resource pool to unbind"
                            ),
                            cli.Arg(
                                "workspace_names",
                                nargs=argparse.ONE_OR_MORE,
                                type=str,
                                default=None,
                                help="the workspace to unbind from",
                            ),
                        ],
                    ),
                    cli.Cmd(
                        "replace",
                        replace_bindings,
                        "replace all existing resource-pool-to-workspace bindings",
                        [
                            cli.Arg(
                                "pool_name", type=str, help="name of the resource pool to bind"
                            ),
                            cli.Arg(
                                "workspace_names",
                                nargs=argparse.ONE_OR_MORE,
                                type=str,
                                default=None,
                                help="the workspaces to bind to",
                            ),
                        ],
                    ),
                    cli.Cmd(
                        "list-workspaces",
                        list_workspaces,
                        "list all workspaces bound to the pool",
                        [
                            cli.Arg("pool_name", type=str, help="name of the resource pool"),
                        ],
                    ),
                ],
            ),
        ],
    )
]  # type: List[Any]
