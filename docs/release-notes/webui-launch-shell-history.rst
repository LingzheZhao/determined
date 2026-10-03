:orphan:

**New Features**

-  WebUI: Launch shells from the browser. The task list (the Tasks page and a workspace's Tasks
   tab) has a **Launch Shell** button next to **Launch JupyterLab**. Both open the same form
   (workspace, start from, name, resource pool, slots) with a full-config YAML mode. After a shell
   starts, the WebUI shows the ``det shell open <id>`` command to connect with and links to the
   shell's logs. A terminal in the browser is planned for a later release. The shell's SSH private
   key, which the launch API returns for the CLI, is discarded by the WebUI as soon as the response
   arrives and is never stored.

-  WebUI: Start a shell or JupyterLab from an earlier config. The launch form's **Start from**
   picker (formerly **Template**) offers:

   -  **Recent on cluster**: your own shells or JupyterLabs that the master still knows about, that
      is, running ones and ones that ended in about the last 24 hours. The master forgets ended
      tasks after that and when it restarts.

   -  **Recently launched in this browser**: up to 20 configs per task type that you launched from
      this browser. They are kept in the browser's local storage under your user ID. Only the launch
      config is stored, plus its workspace and time, without the entrypoint, registry credentials or
      environment variables whose names look like credentials (``TOKEN``, ``SECRET``, ``PASSW``,
      ``KEY``, ``AUTH``, ``CRED``). The list is a convenience only and can be cleared from the
      picker.

   -  **Templates**: picking a template now also fills in its resource pool and slots, which the
      master does not apply from a template for shells and JupyterLabs.

-  WebUI: **Launch Again** on your own shell and JupyterLab rows in the task list opens the launch
   form filled from that task's config. Administrators see it on every shell and JupyterLab.

-  WebUI: **Save as Template** in the full-config mode of the launch form starts a new template from
   the config, keeping only the settings that differ from the cluster defaults. It appears only when
   templates are enabled and you may create templates in the selected workspace. Other users can
   read templates, so review the config before saving it.

-  WebUI: The full config of a new shell is previewed through the JupyterLab preview, because the
   shell launch API has no preview. The JupyterLab-only settings ``idle_timeout`` and
   ``notebook_idle_type`` are removed from it.
