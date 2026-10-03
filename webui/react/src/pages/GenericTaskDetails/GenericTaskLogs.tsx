import { Settings, settingsConfigForTask } from 'hew/LogViewer/LogViewerSelect.settings';
import React, { useMemo } from 'react';

import { useSettings } from 'hooks/useSettings';
import { TaskLogsViewer } from 'pages/TaskLogs';

import css from './GenericTaskLogs.module.scss';

interface Props {
  taskId: string;
}

/* The logs tab of a generic task, with the same viewer and filters as the task logs page. */
const GenericTaskLogs: React.FC<Props> = ({ taskId }: Props) => {
  const taskSettingsConfig = useMemo(() => settingsConfigForTask(taskId), [taskId]);
  const { resetSettings, settings, updateSettings } = useSettings<Settings>(taskSettingsConfig);

  return (
    <div className={css.base}>
      <TaskLogsViewer
        resetSettings={resetSettings}
        settings={settings}
        taskId={taskId}
        updateSettings={updateSettings}
      />
    </div>
  );
};

export default GenericTaskLogs;
