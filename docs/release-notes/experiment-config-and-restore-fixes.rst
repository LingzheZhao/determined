:orphan:

**Bug Fixes**

-  API: ``CreateExperiment`` now reports an experiment config that cannot be parsed, is
   incomplete, sets ``resources.slots``, names a searcher that was removed, or has no entrypoint
   for a managed experiment as ``InvalidArgument`` (HTTP 400) instead of an internal error (HTTP
   500).

-  Experiments: An experiment that fails to restore after a master restart no longer leaves its
   user session behind.
