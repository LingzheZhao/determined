:orphan:

**New Features**

-  API: ``LaunchCommand``, ``LaunchShell``, ``CreateGenericTask``, and ``CreateExperiment`` accept
   optional ``submit`` options and then return a ``submission`` result with the ``job_id`` and a
   ``request_digest`` that identifies the request's content. With an ``idempotency_key`` of at
   most 128 characters of ``[A-Za-z0-9._:-]``, a request whose key its owner already used returns
   that job with ``replayed`` set instead of creating another; if the content differs, it fails
   with ``ALREADY_EXISTS`` naming the job. A ``dry_run`` checks the request and returns its digest
   without creating anything, and ``validate_only`` remains an alias for it. A request whose
   digest differs from its ``expected_digest`` fails with ``FAILED_PRECONDITION`` and reason
   ``plan_changed``, and creates nothing. The digest covers the config of a named template as it
   is at submit, so a template that changed since the dry run also fails with ``plan_changed``,
   and the create applies the template it digested; master and pool defaults are not part of the
   digest and apply as they are at the create. A dry run's result also carries the
   ``effective_config``, with checkpoint storage secrets hidden. ``admission`` defaults to
   ``QUEUE``; ``IMMEDIATE`` is not supported yet, and experiments reject it. Requests without
   ``submit`` options keep their responses, and a ``validate_only`` create runs the added checks
   described under Improvements. Unmanaged experiments do not accept ``submit`` options.

-  API: Add ``GetSubmission`` (``GET /api/v1/submissions/{job_id}``), ``ListSubmissions``
   (``GET /api/v1/submissions``), and ``CancelSubmission``
   (``POST /api/v1/submissions/{job_id}/cancel``) for commands, shells, generic tasks, and
   experiments. They read the database only, so a job
   reads the same after a master restart. A submission reports the job's kind, owner, workspace,
   project, name, admission, submit and end times, a state of ``QUEUED``, ``RUNNING``, ``PAUSED``,
   ``COMPLETED``, ``FAILED``, ``CANCELED``, or ``DELETED``, the exit class and reason of the
   allocation that ended it, and its tasks with their allocations. ``ListSubmissions`` lists one
   owner's jobs, the caller's by default, newest first, and filters them by kind, state, and submit
   time; a page may hold fewer jobs than its ``limit`` and still have a ``next_page_token``, which
   is opaque. Each job needs the read permission of its kind, a deleted experiment is visible only
   to its owner and admins, and only the owner and admins see a job's ``idempotency_key`` and
   ``request_digest``. With RBAC, the admins are the users who may administer users across the
   cluster, as ``ClusterAdmin`` allows, and a user's admin flag has no effect.
   ``CancelSubmission`` records the cancel before it signals the job, so a job that has not
   started yet stops as it starts, and a job that has ended is returned unchanged.

-  API: Task allocations report their ``resource_pool`` and ``placements``, the node and
   accelerator UUIDs of each container that reported them.

-  API: ``GetMaster`` reports ``submission_protocol``, the version of the submission options that
   the master implements. It is ``1`` in this release.

-  Tasks: Task containers have ``DET_JOB_ID``.

**Improvements**

-  Tasks: A new command, shell, notebook, TensorBoard, or generic task is written with its first
   allocation and the state that restore reads in one transaction before it starts. A task whose
   start fails in the master now ends, with its allocation classed ``INFRASTRUCTURE_FAILED``,
   instead of staying open, and an experiment whose start fails is marked ``ERROR``.

-  API: A committed command, shell, generic task, or experiment now always starts without a
   master restart. The master starts it after the commit even if the client disconnects, and if
   the master cannot tell whether a create committed, it looks the job up and starts it, or fails
   with the retryable ``UNAVAILABLE`` when the job does not exist. A replay of an idempotency key
   starts its job if the job never started, and every 30 seconds the master starts the committed
   jobs that nothing started. The master never starts a job's allocation or experiment a second
   time, however many of these start it.

-  Experiments: An experiment created with ``activate`` is stored ``ACTIVE`` from the start, so a
   master restart right after the create restores it active instead of paused. A dry run or
   ``validate_only`` create no longer opens a user session, and it now runs every check a create
   runs before it writes anything: with ``activate``, the permission to activate the experiment,
   plus the warm start checkpoint, the agent user group, and the resources of the config after
   config policies are applied.

-  Tasks: ``KillCommand``, ``KillShell``, and ``KillGenericTask`` record the cancel on the job
   before they signal it, as ``CancelSubmission`` does. A task that ends after the cancel was
   recorded ends ``CANCELED``, a cancel that comes after the task ended changes nothing, and a
   restarted master never continues or unpauses a canceled task. ``KillCommand`` and ``KillShell``
   no longer fail with ``NotFound`` for a command or shell that has not registered yet. Canceling
   a paused generic task ends it ``CANCELED`` at once, and a busy generic task lock now fails with
   the retryable ``UNAVAILABLE``.

-  Experiments: ``ContinueExperiment`` clears the cancel recorded for the experiment's earlier run.

**Bug Fixes**

-  Tasks: A master restart now continues each command, shell, notebook, TensorBoard, and generic
   task that has not ended from the persisted state of its current allocation. An allocation that
   was still queued is queued again under the same ID, dropping any resources the resource manager
   had recorded for it, instead of failing to restore with "0 container snapshots"; if its job was
   asked to stop, or its generic task to pause, the task ends or pauses instead. An allocation that
   was already placed is restored, even before its container pulled its image, and is never placed
   a second time. A task whose allocation had already ended is ended, and a task that cannot be
   restored now ends with its allocation classed ``INFRASTRUCTURE_FAILED`` instead of staying
   open. A generic task that was being unpaused resumes its new allocation the same way.

-  Experiments: An experiment that fails to restore after a master restart no longer leaves its
   user session behind.

-  Tasks: With the agent resource manager, the master records a container's launch before it
   asks the agent to start it, and does not start a container whose launch it cannot record, so
   a restarted master knows every container that may be running. A container that the master
   placed but never launched fails to restore instead of leaving its allocation waiting. A
   container that an agent reports in a different state than the master recorded is killed, and
   its allocation now ends ``INFRASTRUCTURE_FAILED`` instead of as a stop that someone asked for.
