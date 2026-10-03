.. _generic-tasks:

###############
 Generic Tasks
###############

A generic task runs one container from an entrypoint on the cluster, like a command, but adds what
batch work launched by scripts or agents needs: a name, pause and unpause, forking with changed
settings, and trees of tasks that are paused or killed together. Unlike an experiment, it has no
trials, searcher, metrics or checkpoints.

+-------------------------------+-------------+-------------------------------+-----------------+
|                               | Command     | Generic task                  | Experiment      |
+===============================+=============+===============================+=================+
| Pause and unpause             | no          | yes; unpause starts the       | yes, via trials |
|                               |             | entrypoint again              |                 |
+-------------------------------+-------------+-------------------------------+-----------------+
| Fork with changed settings    | no          | yes                           | yes             |
+-------------------------------+-------------+-------------------------------+-----------------+
| Parent and child tasks        | no          | yes                           | no              |
+-------------------------------+-------------+-------------------------------+-----------------+
| Automatic restarts            | no          | no                            | ``max_restarts``|
+-------------------------------+-------------+-------------------------------+-----------------+
| Metrics and checkpoints       | no          | no                            | yes             |
+-------------------------------+-------------+-------------------------------+-----------------+
| Proxy ports                   | yes         | yes                           | yes             |
+-------------------------------+-------------+-------------------------------+-----------------+

Use a command or shell for interactive work, an experiment for long training that should survive
node failures, and a generic task for batch jobs that you want to name, organize, pause and resume.

**********
 Creating
**********

Write a config file and create the task with ``det task create``:

.. code:: yaml

   name: eval-sweep-seed-894
   description: evaluates the checkpoints of run 12
   entrypoint: ["python", "eval.py", "--seed", "894"]
   resources:
     slots: 1
     resource_pool: default
     priority: 42
   environment:
     image: determinedai/pytorch-ngc:0.38.1
   bind_mounts:
     - host_path: /datasets
       container_path: /datasets
       read_only: true

.. code:: bash

   det task create config.yaml --context . --follow

The config accepts ``name`` and ``description`` (both optional; the job queue and ``det task
list`` show the name, or ``Generic Task <task ID>`` without one), ``entrypoint`` (required), ``resources``,
``environment``, ``bind_mounts``, ``work_dir``, ``debug`` and ``preemption_timeout`` (seconds a task
gets to exit after a pause before its container is killed; default 0). Unknown keys are refused. ``--context``
uploads a directory as the working directory, as for commands. ``--project_id`` places the task in a
project; the default is the default project.

``det task config <task ID>`` prints a task's config, ``det task logs -f <task ID>`` follows its
logs, and ``det task list`` lists tasks.

*************************
 Forking and task trees
*************************

``det task fork <task ID>`` creates a new task from the config and context directory of an existing
one. ``det task create --fork <task ID> overrides.yaml`` merges the keys of ``overrides.yaml`` into
the forked config, e.g. to run the same job with another seed.

``det task create --parent <task ID>`` makes the new task a child of an existing task;
``--inherit_context`` reuses the parent's context directory. ``det task kill`` and ``det task pause``
act on a task and all its descendants; ``det task kill --root`` kills the whole tree from its root.

********************
 Pause and unpause
********************

``det task pause <task ID>`` stops the task and its descendants and marks them ``PAUSED``. Each task
is asked to stop through the Core API's preemption signal and its container is killed after
``preemption_timeout`` seconds, at once by default. A child created with ``--no_pause`` keeps running. ``det task unpause <task ID>``
starts the entrypoint again in a new container, with the same task ID.

Nothing of the stopped process is kept except files the task wrote to shared storage. A task that is
paused and unpaused therefore starts over; write it so that it can: skip work whose outputs are
complete, and resume or discard work that was interrupted. :doc:`Task continuity
</maintenance/task-continuity>` describes how an unpause of a task tree is made safe across master restarts.

A task is ``COMPLETED`` when its entrypoint exits with code 0 and ``ERROR`` otherwise, also when its
agent is lost; generic tasks are not restarted automatically.

***********
 Job queue
***********

A running generic task appears in the job queue under its name. Its priority and weight can be
changed from the web UI's job queue or the CLI (``det job update``), like those of other jobs, and
the change is kept when the task is paused and unpaused or the master restarts. Moving a generic
task to another resource pool is not supported, as for commands: fork it with a different
``resources.resource_pool`` instead.

*************
 Proxy ports
*************

Ports listed in ``environment.proxy_ports`` are exposed by the task's container and proxied through
the master, as for commands and experiments (see :ref:`proxy-ports`):

.. code:: yaml

   environment:
     proxy_ports:
       - proxy_port: 8888
         proxy_tcp: false

.. code:: bash

   python -m determined.cli.tunnel --listener 8888 --auth $DET_MASTER $TASK_ID:8888

*************
 Limitations
*************

-  The web UI has no detail page for generic tasks; they appear in the job queue and their logs can
   be viewed by task ID.
-  A paused or unpaused task starts its entrypoint from the beginning (see above).
