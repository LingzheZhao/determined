.. _native-task-resources:

#######################
 Native Task Resources
#######################

The native **Resources** tab shows task CPU, memory, assigned GPU metrics, and allocation
lifetimes inside the Determined WebUI. Trial details include the tab; task logs link to a
dedicated resource page. The page uses the existing Determined login and task permissions.

Choose **View Resources** from a task row's action menu, an experiment's action menu, or a
supported job's action menu under **Cluster > Resource Pool > Active Tasks**. Task entries open
that task directly. Experiment entries open a Trial selector so multiple Trials are not silently
combined or reduced to an arbitrary Trial. Generic Tasks in the cluster queue link to their own
resource page. External jobs and entries without a visible entity ID do not offer this action.
These actions appear only when native resource monitoring is enabled.

Enable the integration in the master configuration and restart the master:

.. code:: yaml

   integrations:
     task_resources:
       prometheus_url: http://prometheus:9090
       det_cluster: lab-a

The Prometheus URL is a server-side HTTP or HTTPS origin. Credentials, subpaths, query strings,
and fragments are not supported. The master must be able to reach that origin directly;
redirects and environment HTTP proxies are disabled. The URL is never returned to the browser.
Protect Prometheus on the private monitoring network. This integration does not create a new
Prometheus instance or change its storage.

Metric Collection
=================

The integration expects the agent-cluster recording rules and exporter labels provided by
`cluster-setup <https://github.com/WU-CVGL/cluster-setup>`__. In particular, Prometheus must have
``det:allocation_task:info``, ``det:runtime_task:info``, and ``det:gpu_task:info`` ownership rules,
cAdvisor metrics with ``det_cluster``, ``container_runtime_id``, and ``node`` labels, and DCGM
metrics with ``det_cluster`` and ``gpu_uuid`` labels. The configured cluster must match the
``det_cluster`` label. The existing Kubernetes dashboard's pod-label schema alone does not
satisfy this contract.

The master runs a fixed set of queries after checking access to the task. It does not expose a
general PromQL proxy. An optional allocation selector is checked against the task's allocations.
Queries are limited to seven days, 1,440 points per series, a minimum 15-second step, and a shared
10-second timeout. At most four resource requests run concurrently per master.

Reading the Charts
==================

Select a preset or a custom time range and optionally one allocation. Running tasks refresh
every 30 seconds while the page is visible; ended tasks use a window preceding their end time.
Drag across a chart to zoom the shared timeline. Empty periods remain gaps rather than zeros.
If a refresh fails, retained charts are explicitly marked as the last successful response.

GPU values describe the entire assigned device and may include other processes. Shared-device
ownership conflicts are omitted by the recording rules. Child tasks are not aggregated. An
all-zero RSS response produces a warning because some cAdvisor environments do not report RSS;
working set is a separate memory signal. Retention and historical ownership availability depend
on the existing Prometheus deployment; enabling the page does not reconstruct missing history.

REST API
========

MCP servers and agents can query the same metrics after submitting a task. The endpoints use
the existing Determined login token and task read permissions:

-  ``GET /api/v1/task-resources/capability`` reports whether the integration is enabled.
-  ``GET /api/v1/tasks/{task_id}/resources`` returns metrics for one task.

Set ``DET_MASTER`` to the master origin, ``DET_TOKEN`` to a login token, and ``TASK_ID`` to the
submitted task's ID. Query the last hour with:

.. code:: bash

   END=$(date +%s)
   START=$((END - 3600))
   curl --get "$DET_MASTER/api/v1/tasks/$TASK_ID/resources" \
     -H "Authorization: Bearer $DET_TOKEN" \
     --data-urlencode "start=$START" \
     --data-urlencode "end=$END" \
     --data-urlencode "step=30"

``start`` and ``end`` are Unix seconds; ``step`` is seconds between samples. Add
``allocationId`` to select one allocation belonging to the task. The query limits described
above also apply to the API.

The response contains ``enabled``, ``series``, and ``warnings``. Each series identifies its
``metric`` and allocation, node, or GPU labels. Samples contain numeric ``timestampSeconds``
and an optional numeric ``value``. Treat a missing or null value as unavailable, not zero.
Automation should inspect warnings before making optimization decisions, especially for
whole-device GPU measurements and unverified RSS values.

The generated :ref:`REST API reference <rest-api>` documents the request and response types.
Deployed masters serve the same specification at ``/api/v1/api.swagger.json`` and the interactive
reference at ``/docs/rest-api/``. The existing WebUI endpoints remain available for compatibility.

The :ref:`Grafana link <grafana-task-resources>` remains available when native monitoring is
disabled and a Grafana dashboard is configured. When native monitoring is enabled, resource
links stay within Determined. Grafana continues to manage its own access permissions separately.
