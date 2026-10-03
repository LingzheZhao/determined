:orphan:

**New Features**

-  Generic tasks: A guide for generic tasks, and the ``det task create``, ``config``, ``fork``,
   ``kill``, ``pause`` and ``unpause`` commands now appear in ``det task --help`` with help texts.
   See :ref:`generic-tasks`.

-  Generic tasks: The config accepts optional ``name`` and ``description`` keys. The job queue and
   ``det task list`` show the name, or ``Generic Task <task ID>`` without one. Configs with these
   keys are refused by masters without this change.

-  Generic tasks: Ports listed in ``environment.proxy_ports`` are exposed and proxied through the
   master, as for commands, also after the task is unpaused or the master restarts.

**Improvements**

-  Generic tasks: A running generic task is now a regular entry of the job queue. Changing its
   priority or weight, from the web UI or with ``det job update``, applies the change in the
   resource manager and keeps it across pause, unpause and master restarts. Previously the change
   was accepted and ignored, the queue showed every generic task as ``generic-task`` with priority
   0, and generic tasks unpaused or restored after a master restart were missing from the queue.
   Moving a generic task to another resource pool now fails with "not supported", as for commands,
   instead of being silently ignored.

-  Generic tasks: Pausing a generic task gives it ``preemption_timeout`` seconds to exit through the
   preemption signal before its container is killed, also on its first run and after a master
   restart. Previously only a task that had been unpaused before got this timeout. The default
   timeout is 0, so tasks that do not set it are stopped at once, as before.

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

-  Generic tasks: A finished generic task no longer stays registered in the job service and the
   scheduler's priority callbacks.
