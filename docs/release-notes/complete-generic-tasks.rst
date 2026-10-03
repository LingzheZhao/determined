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

-  Generic tasks: ``det task list-generic`` and ``GET /api/v1/generic-tasks`` list generic tasks
   with their owner, name, state and parent, filtered by owner (``--user``, ``--all``), workspace,
   state or parent. Previously generic tasks could only be found through the running allocations of
   ``det task list``, which do not show the owner.

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

-  API: The generic task endpoints (create, get config, kill, pause and unpause) are listed under
   ``Tasks`` instead of ``Internal`` in the REST API reference, with their own descriptions. In the
   TypeScript bindings they moved from ``InternalApi`` to ``TasksApi``.

-  Generic tasks: A generic task can be paused only if it was created with ``--pausable``
   (``no_pause: false`` in the API), because unpausing runs its entrypoint again from the start.
   Previously a root task could be paused unless it was created with ``--no_pause``, which the CLI
   still accepts and ignores. ``det task fork`` also accepts ``--pausable``.

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

-  Generic tasks: Creating a generic task with an unknown or mistyped config key, negative slots or
   a config that cannot be merged with the forked task's config now fails with an invalid-argument
   error (HTTP 400) instead of an internal error (HTTP 500).

-  Generic tasks: A kill, pause or unpause that the master refuses now fails with a client error
   instead of an internal error (HTTP 500): HTTP 404 for a task that does not exist or is not a
   generic task, HTTP 400 for a task in a state that does not allow the change (for example pausing
   a paused task, or a task with ``no_pause``), and HTTP 409 while another kill, pause or unpause, or
   an unpause of the same tree, is in progress.

-  Generic tasks: A scheduler with preemption enabled no longer preempts generic tasks. A preempted
   generic task ended as completed or errored instead of paused, so it was never resumed, even with
   ``no_pause``. Generic tasks still receive the preemption signal and their ``preemption_timeout``
   when they are paused.

-  Generic tasks: Creating a generic task with ``--parent`` now requires permission to control the
   parent task (its owner or an admin). Previously any user could add a child to another user's
   task, after which the owner could no longer pause or kill the tree without an admin, because
   those actions require control of every task in it.

-  Generic tasks: The scheduler now starts a generic task's allocation with the task's saved priority
   and weight, also after an unpause or a master restart, instead of the resource pool's defaults.
   A priority outside 1 to 99, a weight that is not a positive finite number, or a priority beyond
   the task config policy's limit for NTSC workloads is refused, at creation as in later updates.

-  Generic tasks: An unpaused task no longer loses its scheduling group. Retrying an unpause after the
   new allocation started, or the cleanup of a pause finishing after the unpause, dropped the group
   of the running allocation. A paused task now keeps its scheduling registration until it ends.

-  Generic tasks: Killing a paused generic task, or a tree whose root is paused, now cancels it and
   kills the rest of the tree. Previously the kill failed on the paused task's missing allocation,
   left it in ``STOPPING_CANCELED`` for good, and marked its running descendants as stopping without
   stopping them.
