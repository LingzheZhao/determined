:orphan:

**Bug Fixes**

-  Tasks: Access checks for generic tasks, and the task log webhooks of a generic task, now use the
   task's workspace instead of workspace 0.

-  API: ``GetTask`` now returns each allocation's ``slots``, ``exit_reason`` and ``status_code``.
   Previously it always reported 0 slots and no exit reason or status code.

-  Tasks: An allocation that fails with missing resources, with a failure type the master does not
   recognize, or that exits without a reason now reports the failure as an error exit. Previously
   its handler panicked, and the allocation ended through the master's panic recovery as a handler
   crash with status code -1. A failure type from the agent that the master does not know is
   reported as an unknown agent failure, with the agent's type in the message.

-  Tasks: A task whose allocation fails to restore after a master restart now reports the restore
   failure instead of a handler crash. With the agent resource manager, this failure is a restore
   error, which is transient, so it no longer counts against a trial's ``max_restarts``. With the
   Kubernetes resource manager, the failure is reported as missing resources and still counts
   against ``max_restarts``.

-  API: ``CreateExperiment`` now reports an experiment config that cannot be parsed, is
   incomplete, sets ``resources.slots``, names a searcher that was removed, or has no entrypoint
   for a managed experiment as ``InvalidArgument`` (HTTP 400) instead of an internal error (HTTP
   500).

-  Experiments: An experiment that fails to restore after a master restart no longer leaves its
   user session behind.
