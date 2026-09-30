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
   ``plan_changed``, and creates nothing. ``admission`` defaults to ``QUEUE``; ``IMMEDIATE`` is
   not supported yet, and experiments reject it. Requests without ``submit`` options behave as
   before. Unmanaged experiments do not accept ``submit`` options.

-  API: ``GetMaster`` reports ``submission_protocol``, the version of the submission options that
   the master implements. It is ``0`` in this release.

-  Tasks: Task containers have ``DET_JOB_ID``, and with the agent resource manager also
   ``DET_CLUSTER_ID``.

**Improvements**

-  Tasks: A new command, shell, notebook, TensorBoard, or generic task is written with its first
   allocation and the state that restore reads in one transaction before it starts. A task whose
   start fails in the master now ends, with its allocation classed ``INFRASTRUCTURE_FAILED``,
   instead of staying open, and an experiment whose start fails is marked ``ERROR``.

-  Experiments: An experiment created with ``activate`` is stored ``ACTIVE`` from the start, so a
   master restart right after the create restores it active instead of paused. A dry run or
   ``validate_only`` create no longer opens a user session, and it now also checks the warm start
   checkpoint and the agent user group that a create checks.
