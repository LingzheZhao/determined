.. _grafana-task-resources:

########################
 Grafana Task Resources
########################

Determined can show an optional **Task Resources** link on a trial's details page and on the
shared task logs page. The latter accepts a task ID for Generic Tasks at
``/det/generic/<task-id>/logs``; Generic Tasks do not currently have a dedicated detail page.
The link opens an externally managed Grafana dashboard in a new tab. It is disabled by default.

Configure the master with the dashboard's full URL and the cluster value used in the dashboard's
``cluster`` variable:

.. code:: yaml

   integrations:
     grafana_task_resources:
       dashboard_url: https://grafana.example/monitoring/d/det-task-resources/task-resources
       det_cluster: lab-a

The URL must use HTTP or HTTPS and the dashboard UID ``det-task-resources``. It may include an
``orgId`` query parameter, but must not contain credentials. The link retains Grafana subpaths
and passes the task ID as ``var-task_id``, the configured cluster as ``var-cluster``, and a selected
allocation as ``var-allocation_id``. When no single allocation is selected, it passes Grafana's
``$__all`` value. The task start and end timestamps become Grafana ``from`` and ``to`` parameters
in Unix milliseconds. Running tasks use Grafana's ``now`` for the end of the range.

Grafana controls access to its dashboards and metrics separately from Determined. A Determined
login does not grant Grafana access, and dashboard variables are selectors rather than an
authorization boundary. Configure Grafana permissions accordingly, especially when the
monitoring instance serves more than one team or cluster. This link does not expose a PromQL
query interface or change metric collection.
