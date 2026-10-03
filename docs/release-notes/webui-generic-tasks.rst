:orphan:

**New Features**

-  WebUI: The Tasks page has a "Generic Tasks" tab that lists generic tasks with their owner, state,
   slots, resource pool, whether they are pausable, their parent and their start and end times. It
   shows your tasks by default, or the tasks of all users, and can be filtered by state.

-  WebUI: A generic task has a detail page with its name, ID, state, owner, description, parent,
   child tasks and the task it was forked from, its allocations with their slots, exit reasons and
   status codes, its config and its logs. The page can pause, unpause and kill the task, or kill its
   whole tree from the root, after a confirmation, and shows the master's reason when the master
   refuses an action.

**Improvements**

-  WebUI: Generic tasks in the job queue are shown under their name with a task icon instead of as
   "Experiment" with part of their ID, link to their detail page and offer "View Logs" and "Kill" in
   their action menu.
