import Alert from 'hew/Alert';
import Spinner from 'hew/Spinner';
import React, { useEffect, useState } from 'react';
import { useParams, useSearchParams } from 'react-router-dom';

import Page from 'components/Page';
import TaskResourcesPanel from 'components/TaskResourcesPanel';
import { paths } from 'routes/utils';
import { getTask } from 'services/api';
import { TaskItem } from 'types';

const TaskResources: React.FC = () => {
  const { taskId = '' } = useParams<{ taskId: string }>();
  const [params] = useSearchParams();
  const [task, setTask] = useState<TaskItem>();
  const [error, setError] = useState(false);
  useEffect(() => {
    let active = true;
    setTask(undefined);
    setError(false);
    getTask({ taskId })
      .then((value) => {
        if (active) setTask(value);
      })
      .catch(() => {
        if (active) setError(true);
      });
    return () => {
      active = false;
    };
  }, [taskId]);
  return (
    <Page
      breadcrumb={[
        { breadcrumbName: 'Tasks', path: paths.taskList() },
        { breadcrumbName: taskId, path: paths.taskResources(taskId) },
      ]}
      title="Task Resources">
      {error ? (
        <Alert
          message="This task is unavailable or you do not have permission to view it."
          type="error"
        />
      ) : task ? (
        <TaskResourcesPanel
          endTime={task.endTime}
          initialAllocationId={params.get('allocation_id') || undefined}
          key={task.taskId}
          startTime={task.startTime}
          taskId={task.taskId}
        />
      ) : (
        <Spinner center spinning />
      )}
    </Page>
  );
};

export default TaskResources;
