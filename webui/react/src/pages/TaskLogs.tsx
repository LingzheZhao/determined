import LogViewer, { FetchConfig, FetchDirection, FetchType } from 'hew/LogViewer/LogViewer';
import LogViewerSelect, { Filters } from 'hew/LogViewer/LogViewerSelect';
import { Settings, settingsConfigForTask } from 'hew/LogViewer/LogViewerSelect.settings';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useParams, useSearchParams } from 'react-router-dom';

import Page from 'components/Page';
import TaskResourcesLink from 'components/TaskResourcesLink';
import { commandTypeToLabel } from 'constants/states';
import { ResetSettings, UpdateSettings, useSettings } from 'hooks/useSettings';
import { DateString, decode, optional } from 'ioTypes';
import { paths, serverAddress } from 'routes/utils';
import { getTask } from 'services/api';
import { detApi } from 'services/apiConfig';
import { mapV1LogsResponse } from 'services/decoder';
import { readStream } from 'services/utils';
import { CommandTask, CommandType, TaskItem } from 'types';
import handleError from 'utils/error';

import css from './TaskLogs.module.scss';

type Params = {
  taskId: string;
  taskType: string;
};

interface Props {
  headerComponent?: React.ReactNode;
  onCloseLogs?: () => void;
  taskId: string;
  taskType: string;
}
type OrderBy = 'ORDER_BY_UNSPECIFIED' | 'ORDER_BY_ASC' | 'ORDER_BY_DESC';

export const TaskLogsWrapper: React.FC = () => {
  const { taskId, taskType } = useParams<Params>();
  return <TaskLogs taskId={taskId ?? ''} taskType={taskType ?? ''} />;
};
interface ViewerProps {
  onCloseLogs?: () => void;
  resetSettings: ResetSettings;
  settings: Settings;
  taskId: string;
  updateSettings: UpdateSettings<Settings>;
}

/*
 * The log viewer of a task with its filters, without a page around it. The caller owns the log
 * settings so that it can also read them, e.g. for the selected allocation.
 */
export const TaskLogsViewer: React.FC<ViewerProps> = ({
  onCloseLogs,
  resetSettings,
  settings,
  taskId,
  updateSettings,
}: ViewerProps) => {
  const [filterOptions, setFilterOptions] = useState<Filters>({});

  const filterValues: Filters = useMemo(
    () => ({
      agentIds: settings.agentId,
      containerIds: settings.containerId,
      levels: settings.level,
      rankIds: settings.rankId,
      searchText: settings.searchText,
    }),
    [settings],
  );

  const handleFilterChange = useCallback(
    (filters: Filters) => {
      updateSettings({
        agentId: filters.agentIds,
        allocationId: filters.allocationIds,
        containerId: filters.containerIds,
        level: filters.levels,
        rankId: filters.rankIds,
        searchText: filters.searchText,
      });
    },
    [updateSettings],
  );

  const handleFilterReset = useCallback(() => resetSettings(), [resetSettings]);

  const handleFetch = useCallback(
    (config: FetchConfig, type: FetchType) => {
      const options = {
        follow: false,
        limit: config.limit,
        orderBy: 'ORDER_BY_UNSPECIFIED',
        timestampAfter: undefined as Date | string | undefined,
        timestampBefore: undefined as Date | string | undefined,
      };

      if (type === FetchType.Initial) {
        options.orderBy =
          config.fetchDirection === FetchDirection.Older ? 'ORDER_BY_DESC' : 'ORDER_BY_ASC';
      } else if (type === FetchType.Newer) {
        options.orderBy = 'ORDER_BY_ASC';
        if (config.offsetLog?.time) options.timestampAfter = config.offsetLog.time;
      } else if (type === FetchType.Older) {
        options.orderBy = 'ORDER_BY_DESC';
        if (config.offsetLog?.time) options.timestampBefore = config.offsetLog.time;
      } else if (type === FetchType.Stream) {
        options.follow = true;
        options.limit = 0;
        options.orderBy = 'ORDER_BY_ASC';
        options.timestampAfter = new Date().toISOString();
      }

      return detApi.StreamingJobs.taskLogs(
        taskId,
        options.limit,
        options.follow,
        settings.allocationId,
        settings.agentId,
        settings.containerId,
        settings.rankId,
        settings.level,
        undefined,
        undefined,
        decode(optional(DateString), options.timestampBefore),
        decode(optional(DateString), options.timestampAfter),
        options.orderBy as OrderBy,
        settings.searchText,
        false,
        { signal: config.canceler.signal },
      );
    },
    [settings, taskId],
  );

  useEffect(() => {
    const canceler = new AbortController();

    readStream(
      detApi.StreamingJobs.taskLogsFields(taskId, true, { signal: canceler.signal }),
      (event) => setFilterOptions(event as Filters),
    );

    return () => canceler.abort();
  }, [taskId]);

  const logFilters = (
    <div className={css.filters}>
      <LogViewerSelect
        options={filterOptions}
        showSearch={true}
        values={filterValues}
        onChange={handleFilterChange}
        onReset={handleFilterReset}
      />
    </div>
  );

  return (
    <LogViewer
      decoder={mapV1LogsResponse}
      handleCloseLogs={onCloseLogs}
      serverAddress={serverAddress}
      title={logFilters}
      onError={handleError}
      onFetch={handleFetch}
    />
  );
};

const TaskLogs: React.FC<Props> = ({ taskId, taskType, onCloseLogs, headerComponent }: Props) => {
  const [task, setTask] = useState<TaskItem>();
  const [searchParams] = useSearchParams();

  const taskTypeLabel =
    taskType === 'generic' ? 'Generic Task' : commandTypeToLabel[taskType as CommandType];
  const title = `${searchParams.has('id') ? `${searchParams.get('id')} ` : ''}Logs`;

  const taskSettingsConfig = useMemo(() => settingsConfigForTask(taskId), [taskId]);
  const { resetSettings, settings, updateSettings } = useSettings<Settings>(taskSettingsConfig);
  const selectedAllocation =
    settings.allocationId?.length === 1 ? settings.allocationId[0] : undefined;

  useEffect(() => {
    let active = true;
    setTask(undefined);
    getTask({ taskId })
      .then((value) => {
        if (active) setTask(value);
      })
      .catch(() => {
        // Logs remain available even if the task metadata request fails.
      });
    return () => {
      active = false;
    };
  }, [taskId]);

  return (
    <Page
      bodyNoPadding
      breadcrumb={[
        { breadcrumbName: 'Tasks', path: paths.taskList() },
        {
          breadcrumbName: `${taskTypeLabel} ${taskId.substring(0, 8)}`,
          path:
            taskType === 'generic'
              ? paths.genericTaskDetails(taskId)
              : paths.taskLogs({
                  id: taskId,
                  type: taskType,
                } as CommandTask),
        },
      ]}
      headerComponent={headerComponent}
      id="task-logs"
      options={
        task && (
          <TaskResourcesLink
            nativeUrl={paths.taskResources(task.taskId, selectedAllocation)}
            target={{
              allocationId: selectedAllocation,
              endTime: task.endTime,
              startTime: task.startTime,
              taskId: task.taskId,
            }}
          />
        )
      }
      title={title}>
      <TaskLogsViewer
        resetSettings={resetSettings}
        settings={settings}
        taskId={taskId}
        updateSettings={updateSettings}
        onCloseLogs={onCloseLogs}
      />
    </Page>
  );
};

export default TaskLogs;
