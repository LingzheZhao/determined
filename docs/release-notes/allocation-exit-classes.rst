:orphan:

**New Features**

-  API: ``GetTask`` and ``GetAllocation`` report how each allocation ended. ``exit_class`` is
   ``NONE`` for an allocation that did not fail, or a failure class such as ``WORKLOAD_FAILED`` or
   ``INFRASTRUCTURE_FAILED``, and ``exit_detail`` holds the failure type, container exit code,
   and message. An allocation left open by a master restart is ``NONE`` if it was still queued
   and ``INFRASTRUCTURE_FAILED`` otherwise. Allocations that ended before the upgrade have no
   class. ``GetTask`` also returns each allocation's ``slots``, ``exit_reason``, and
   ``status_code``, which it previously left empty.

**Bug Fixes**

-  Tasks: An allocation whose resources go missing, or that fails with a failure type the master
   does not recognize, such as one from a newer agent, now fails as an infrastructure failure
   instead of crashing its handler.

-  Tasks: With the agent resource manager, a failure to restore an allocation after a master
   restart is now reported as a restore error instead of a handler crash, and, like agent
   failures, no longer counts against a trial's ``max_restarts``. On Kubernetes, a restore failure
   is now reported as missing resources and still counts against ``max_restarts``.

-  Tasks: An allocation that fails to restore after a master restart keeps the last cluster
   heartbeat as its end time instead of the time the new master released it.

-  Tasks: Access checks and workspace-scoped task log webhooks for generic tasks now use the
   task's workspace instead of workspace 0, so a workspace's ``TASK_LOG`` webhooks now also fire
   on log lines from its generic tasks.
