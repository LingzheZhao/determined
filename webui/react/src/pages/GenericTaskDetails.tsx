import Message from 'hew/Message';
import Pivot, { PivotProps } from 'hew/Pivot';
import Spinner from 'hew/Spinner';
import _ from 'lodash';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';

import GenericTaskStateBadge from 'components/GenericTaskStateBadge';
import Page from 'components/Page';
import TaskResourcesPanel from 'components/TaskResourcesPanel';
import { terminalGenericTaskStates } from 'constants/states';
import usePermissions from 'hooks/usePermissions';
import usePolling from 'hooks/usePolling';
import useTaskResourcesEnabled from 'hooks/useTaskResourcesEnabled';
import GenericTaskActions from 'pages/GenericTaskDetails/GenericTaskActions';
import GenericTaskConfig from 'pages/GenericTaskDetails/GenericTaskConfig';
import GenericTaskInfo from 'pages/GenericTaskDetails/GenericTaskInfo';
import GenericTaskLogs from 'pages/GenericTaskDetails/GenericTaskLogs';
import GenericTaskOverview from 'pages/GenericTaskDetails/GenericTaskOverview';
import { paths } from 'routes/utils';
import { getGenericTask, getGenericTaskConfig, getGenericTasks, getTask } from 'services/api';
import { V1TaskType } from 'services/api-ts-sdk';
import { GenericTask, RawJson, TaskItem, ValueOf } from 'types';
import handleError, { ErrorType } from 'utils/error';
import { isAborted, isNotFound } from 'utils/service';
import { shortTaskId } from 'utils/task';

import css from './GenericTaskDetails.module.scss';

const TabType = {
  Config: 'config',
  Logs: 'logs',
  Overview: 'overview',
  Resources: 'resources',
} as const;

type TabType = ValueOf<typeof TabType>;

type Params = {
  tab?: string;
  taskId: string;
};

const TAB_KEYS: string[] = Object.values(TabType);
const DEFAULT_TAB_KEY = TabType.Overview;

const GenericTaskDetails: React.FC = () => {
  const { tab, taskId = '' } = useParams<Params>();
  const navigate = useNavigate();
  const canceler = useRef(new AbortController());
  const { canModifyWorkspaceNSC } = usePermissions();
  const resourcesEnabled = useTaskResourcesEnabled();
  const [task, setTask] = useState<TaskItem>();
  const [taskError, setTaskError] = useState<Error>();
  const [summary, setSummary] = useState<GenericTask>();
  const [summaryFailed, setSummaryFailed] = useState(false);
  const [childTasks, setChildTasks] = useState<GenericTask[]>();
  const [config, setConfig] = useState<RawJson>();
  const [configError, setConfigError] = useState<string>();

  const tabKey: TabType = tab && TAB_KEYS.includes(tab) ? (tab as TabType) : DEFAULT_TAB_KEY;
  // The task shown now; responses for a task navigated away from are dropped.
  const currentTaskId = useRef(taskId);
  currentTaskId.current = taskId;

  useEffect(() => {
    // Start over when another task is opened from this page, e.g. its parent.
    setTask(undefined);
    setTaskError(undefined);
    setSummary(undefined);
    setSummaryFailed(false);
    setChildTasks(undefined);
    setConfig(undefined);
    setConfigError(undefined);
  }, [taskId]);

  useEffect(() => {
    const currentCanceler = canceler.current;
    return () => currentCanceler.abort();
  }, []);

  const fetchTask = useCallback(async () => {
    const options = { signal: canceler.current.signal };
    try {
      const newTask = await getTask({ taskId }, options);
      if (currentTaskId.current !== taskId) return;
      setTask((prev) => (_.isEqual(prev, newTask) ? prev : newTask));
      setTaskError(undefined);
    } catch (e) {
      if (!isAborted(e) && currentTaskId.current === taskId) setTaskError(e as Error);
      return;
    }
    try {
      const { tasks } = await getGenericTasks({ limit: 0, parentId: taskId }, options);
      if (currentTaskId.current !== taskId) return;
      setChildTasks((prev) => (_.isEqual(prev, tasks) ? prev : tasks));
    } catch (e) {
      handleError(e, {
        publicSubject: 'Unable to fetch child tasks.',
        silent: true,
        type: ErrorType.Api,
      });
    }
  }, [taskId]);

  const { stopPolling } = usePolling(fetchTask, { rerunOnNewFn: true });

  // A completed, errored or canceled task does not change any more.
  const isTerminal = !!task?.taskState && terminalGenericTaskStates.has(task.taskState);
  useEffect(() => {
    if (isTerminal) stopPolling();
  }, [isTerminal, stopPolling]);

  const isGeneric = task?.taskType === V1TaskType.GENERIC;
  const parentId = task?.parentId;

  // Owner, name and resources are only in the generic task list; look the task up once.
  useEffect(() => {
    if (!isGeneric) return;
    let active = true;
    getGenericTask(taskId, { signal: canceler.current.signal })
      .then((found) => {
        if (!active) return;
        setSummary(found);
        setSummaryFailed(!found);
      })
      .catch((e) => {
        if (!active) return;
        setSummaryFailed(true);
        handleError(e, {
          publicSubject: 'Unable to fetch generic task details.',
          silent: true,
          type: ErrorType.Api,
        });
      });
    return () => {
      active = false;
    };
  }, [isGeneric, taskId]);

  useEffect(() => {
    if (!isGeneric) return;
    let active = true;
    getGenericTaskConfig({ taskId }, { signal: canceler.current.signal })
      .then((value) => {
        if (active) setConfig(value);
      })
      .catch((e) => {
        if (!active || isAborted(e)) return;
        setConfigError((e as Error).message);
        handleError(e, { publicSubject: 'Unable to fetch config.', silent: true });
      });
    return () => {
      active = false;
    };
  }, [isGeneric, taskId]);

  const handleTabChange = useCallback(
    (key: string) => {
      navigate(paths.genericTaskDetails(taskId, key), { replace: true });
    },
    [navigate, taskId],
  );

  const state = task?.taskState ?? summary?.state;
  const noPause = task?.noPause ?? summary?.noPause;
  const name = summary?.name ?? `Generic Task ${shortTaskId(taskId)}`;
  /*
   * Owners and admins can control a task. Without the task's owner, e.g. when the lookup fails,
   * the actions stay enabled and the master decides.
   */
  const canControl = summary
    ? canModifyWorkspaceNSC({ userId: summary.userId, workspace: { id: summary.workspaceId } })
    : summaryFailed;

  const tabItems: PivotProps['items'] = useMemo(() => {
    if (!task) return [];
    const tabs: PivotProps['items'] = [
      {
        children: <GenericTaskOverview childTasks={childTasks} task={task} />,
        key: TabType.Overview,
        label: 'Overview',
      },
      {
        children: <GenericTaskConfig config={config} error={configError} />,
        key: TabType.Config,
        label: 'Configuration',
      },
      {
        children: <GenericTaskLogs taskId={taskId} />,
        key: TabType.Logs,
        label: 'Logs',
      },
    ];
    if (resourcesEnabled || tabKey === TabType.Resources) {
      tabs.push({
        children: (
          <TaskResourcesPanel
            endTime={task.endTime}
            key={task.taskId}
            startTime={task.startTime}
            taskId={task.taskId}
          />
        ),
        key: TabType.Resources,
        label: 'Resources',
      });
    }
    return tabs;
  }, [childTasks, config, configError, resourcesEnabled, tabKey, task, taskId]);

  if (taskError && !isNotFound(taskError)) {
    return (
      <Message
        description={taskError.message}
        icon="warning"
        title={`Unable to fetch task ${taskId}`}
      />
    );
  }

  if (!task && !taskError) return <Spinner center spinning tip="Fetching task..." />;

  if (task && !isGeneric) {
    return (
      <Message
        description="This page shows generic tasks only."
        icon="warning"
        title={`Task ${taskId} is not a generic task`}
      />
    );
  }

  return (
    <Page
      breadcrumb={[
        { breadcrumbName: 'Tasks', path: paths.taskList() },
        { breadcrumbName: 'Generic Tasks', path: paths.genericTaskList() },
        { breadcrumbName: name, path: paths.genericTaskDetails(taskId) },
      ]}
      headerComponent={
        task && (
          <div className={css.header}>
            <div className={css.title}>
              <h1 data-testid="generic-task-name">{name}</h1>
              {state && <GenericTaskStateBadge state={state} />}
            </div>
            <GenericTaskActions
              canControl={canControl}
              task={{ name, noPause, parentId, state, taskId }}
              onComplete={fetchTask}
            />
          </div>
        )
      }
      id="generic-task-details"
      notFound={!!taskError && isNotFound(taskError)}
      title={name}>
      {task && (
        <>
          <GenericTaskInfo
            childCount={childTasks?.length}
            endTime={task.endTime}
            forkedFrom={task.forkedFrom ?? summary?.forkedFrom}
            noPause={noPause}
            parentId={parentId}
            startTime={task.startTime}
            summary={summary}
            taskId={taskId}
          />
          <Pivot
            activeKey={tabKey}
            destroyInactiveTabPane
            items={tabItems}
            onChange={handleTabChange}
          />
        </>
      )}
    </Page>
  );
};

export default GenericTaskDetails;
