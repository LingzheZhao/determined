import dayjs from 'dayjs';
import Button from 'hew/Button';
import Select, { OptGroup, Option, SelectValue } from 'hew/Select';
import { Loadable } from 'hew/utils/loadable';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import {
  getJupyterLabConfig,
  getJupyterLabs,
  getShellConfig,
  getShells,
  getTaskTemplates,
} from 'services/api';
import userStore from 'stores/users';
import { CommandTask, CommandType, RawJson, Template } from 'types';
import handleError, { ErrorLevel, ErrorType } from 'utils/error';
import { clearLaunchHistory, LaunchHistoryEntry, listLaunchHistory } from 'utils/launchHistory';
import { formFieldsFromConfig, NtscLaunchType } from 'utils/ntscConfig';
import { useObservable } from 'utils/observable';
import { isNotFound } from 'utils/service';
import { capitalize } from 'utils/string';

/** How many of the user's recent tasks the master is asked for. */
export const RECENT_TASK_LIMIT = 20;

/** What the launch form starts from once a "Start from" item is picked. */
export type StartFrom =
  | { kind: 'blank' }
  | { kind: 'template'; template: Template }
  | { kind: 'config'; config: RawJson; redactedEnv: string[]; workspaceId: number };

const TASK_PREFIX = 'task:';
const LOCAL_PREFIX = 'local:';
const TEMPLATE_PREFIX = 'template:';

export const startFromTaskValue = (taskId: string): string => `${TASK_PREFIX}${taskId}`;
export const startFromTemplateValue = (name: string): string => `${TEMPLATE_PREFIX}${name}`;
export const startFromLocalValue = (id: string): string => `${LOCAL_PREFIX}${id}`;

/** The template name a "Start from" value points at, if it is a template. */
export const templateNameFromStartFrom = (value?: string): string | undefined =>
  value?.startsWith(TEMPLATE_PREFIX) ? value.slice(TEMPLATE_PREFIX.length) : undefined;

const formatTime = (time: number | string): string => dayjs(time).format('MMM D, HH:mm');

const taskLabel = (task: CommandTask): string =>
  [task.name, capitalize(task.state.toLowerCase()), formatTime(task.startTime)].join(' · ');

const localLabel = (entry: LaunchHistoryEntry): string => {
  const { name, pool, slots = 1 } = formFieldsFromConfig(entry.config);
  return [
    name || 'Unnamed',
    `${pool ?? 'default pool'}, ${slots} slot${slots === 1 ? '' : 's'}`,
    formatTime(entry.savedAt),
  ].join(' · ');
};

interface Props {
  /** Workspaces the user may launch in; items from other workspaces are disabled. */
  allowedWorkspaceIds: number[];
  /**
   * Whether to make the first selection (initialTask, or else defaultTemplate).
   * The modal turns it off through onAutoSelected, so that remounting the
   * picker (after the full-config mode) does not select again.
   */
  autoSelect: boolean;
  /** Template to preselect, from the user's last launch. */
  defaultTemplate?: string;
  /** Set by Form.Item, so that the field's label points at the select. */
  id?: string;
  /** "Launch Again": the task to start from as soon as the picker mounts. */
  initialTask?: CommandTask;
  /** When the modal is locked to a workspace. */
  lockedWorkspaceId?: number;
  onAutoSelected: () => void;
  onChange?: (value?: string) => void;
  onResolve: (start: StartFrom) => void;
  type: NtscLaunchType;
  value?: string;
}

/**
 * The launch form's "Start from" picker. It offers, for the same task type:
 * - Recent on cluster: the user's own shells or JupyterLabs that the master
 *   still knows (running, or ended in about the last 24 hours and not lost in a
 *   master restart). Their config is fetched only when one is picked.
 * - Recently launched in this browser: utils/launchHistory.
 * - Templates.
 */
const StartFromSelect: React.FC<Props> = ({
  allowedWorkspaceIds,
  autoSelect,
  defaultTemplate,
  id,
  initialTask,
  lockedWorkspaceId,
  onAutoSelected,
  onChange,
  onResolve,
  type,
  value,
}: Props) => {
  const currentUser = Loadable.getOrElse(undefined, useObservable(userStore.currentUser));
  const userId = currentUser?.id;
  const [templates, setTemplates] = useState<Template[]>();
  const [recentTasks, setRecentTasks] = useState<CommandTask[]>([]);
  const [historyVersion, setHistoryVersion] = useState(0);
  const [loadingTaskId, setLoadingTaskId] = useState<string>();
  const pendingTaskId = useRef<string>();

  const localEntries = useMemo(
    () => listLaunchHistory(userId, type),
    // historyVersion re-reads the history after it was cleared.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [userId, type, historyVersion],
  );

  const taskOptions = useMemo(() => {
    if (!initialTask || recentTasks.some((task) => task.id === initialTask.id)) return recentTasks;
    return [initialTask, ...recentTasks];
  }, [initialTask, recentTasks]);

  const isAllowed = useCallback(
    (workspaceId: number) =>
      lockedWorkspaceId !== undefined
        ? workspaceId === lockedWorkspaceId
        : allowedWorkspaceIds.includes(workspaceId),
    [allowedWorkspaceIds, lockedWorkspaceId],
  );

  useEffect(() => {
    let active = true;
    getTaskTemplates({})
      .then((list) => active && setTemplates(list))
      .catch((e) => {
        if (active) setTemplates([]);
        handleError(e);
      });
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    if (userId === undefined) return;
    let active = true;
    const getTasks = type === CommandType.Shell ? getShells : getJupyterLabs;
    getTasks({
      limit: RECENT_TASK_LIMIT,
      orderBy: 'ORDER_BY_DESC',
      sortBy: 'SORT_BY_START_TIME',
      users: [String(userId)],
      workspaceId: lockedWorkspaceId,
    })
      .then((tasks) => active && setRecentTasks(tasks.filter((task) => task.userId === userId)))
      .catch((e) =>
        handleError(e, {
          publicSubject: 'Unable to fetch recent tasks.',
          silent: true,
          type: ErrorType.Api,
        }),
      );
    return () => {
      active = false;
    };
  }, [lockedWorkspaceId, type, userId]);

  const resolveTask = useCallback(
    async (task: CommandTask) => {
      pendingTaskId.current = task.id;
      setLoadingTaskId(task.id);
      try {
        const getConfig = type === CommandType.Shell ? getShellConfig : getJupyterLabConfig;
        const config = await getConfig({ commandId: task.id });
        if (pendingTaskId.current !== task.id) return;
        onResolve({ config, kind: 'config', redactedEnv: [], workspaceId: task.workspaceId });
      } catch (e) {
        if (pendingTaskId.current !== task.id) return;
        if (isNotFound(e as Error)) {
          setRecentTasks((tasks) => tasks.filter((item) => item.id !== task.id));
        }
        onChange?.(undefined);
        onResolve({ kind: 'blank' });
        handleError(e, {
          level: ErrorLevel.Warn,
          publicMessage:
            'The master keeps ended tasks for about 24 hours and forgets them when it restarts.',
          publicSubject: `Unable to load the config of ${task.name}.`,
          silent: false,
          type: ErrorType.Server,
        });
      } finally {
        if (pendingTaskId.current === task.id) {
          pendingTaskId.current = undefined;
          setLoadingTaskId(undefined);
        }
      }
    },
    [onChange, onResolve, type],
  );

  const resolve = useCallback(
    (next?: string) => {
      pendingTaskId.current = undefined;
      setLoadingTaskId(undefined);
      if (!next) {
        onResolve({ kind: 'blank' });
      } else if (next.startsWith(TEMPLATE_PREFIX)) {
        const template = templates?.find((item) => item.name === templateNameFromStartFrom(next));
        if (template) onResolve({ kind: 'template', template });
      } else if (next.startsWith(LOCAL_PREFIX)) {
        const entry = localEntries.find((item) => startFromLocalValue(item.id) === next);
        if (entry) {
          onResolve({
            config: entry.config,
            kind: 'config',
            redactedEnv: entry.redactedEnv ?? [],
            workspaceId: entry.workspaceId,
          });
        }
      } else if (next.startsWith(TASK_PREFIX)) {
        const task = taskOptions.find((item) => startFromTaskValue(item.id) === next);
        if (task) resolveTask(task);
      }
    },
    [localEntries, onResolve, resolveTask, taskOptions, templates],
  );

  const handleChange = useCallback(
    (selected: SelectValue) => {
      const next = typeof selected === 'string' && selected !== '' ? selected : undefined;
      onChange?.(next);
      resolve(next);
    },
    [onChange, resolve],
  );

  const handleClearHistory = useCallback(() => {
    clearLaunchHistory(userId, type);
    setHistoryVersion((version) => version + 1);
    if (value?.startsWith(LOCAL_PREFIX)) handleChange(undefined);
  }, [handleChange, type, userId, value]);

  // "Launch Again": start from the given task right away.
  useEffect(() => {
    if (!autoSelect || !initialTask) return;
    onAutoSelected();
    onChange?.(startFromTaskValue(initialTask.id));
    resolveTask(initialTask);
  }, [autoSelect, initialTask, onAutoSelected, onChange, resolveTask]);

  // Otherwise preselect the template of the user's last launch once the
  // templates load, unless something was picked already. Only the picker is
  // set: the form already holds the user's last pool and slots, so the
  // template's resources are not copied in as they are on an explicit pick.
  useEffect(() => {
    if (!autoSelect || initialTask || !templates) return;
    onAutoSelected();
    if (value || !defaultTemplate) return;
    if (templates.some((item) => item.name === defaultTemplate)) {
      onChange?.(startFromTemplateValue(defaultTemplate));
    }
  }, [autoSelect, defaultTemplate, initialTask, onAutoSelected, onChange, templates, value]);

  return (
    <div>
      <Select
        allowClear
        id={id}
        loading={loadingTaskId !== undefined}
        placeholder="Blank: cluster defaults (optional)"
        value={value}
        onChange={handleChange}>
        {taskOptions.length > 0 && (
          <OptGroup key="recent" label="Recent on cluster">
            {taskOptions.map((task) => (
              <Option
                disabled={!isAllowed(task.workspaceId)}
                key={startFromTaskValue(task.id)}
                value={startFromTaskValue(task.id)}>
                {taskLabel(task)}
              </Option>
            ))}
          </OptGroup>
        )}
        {localEntries.length > 0 && (
          <OptGroup key="local" label="Recently launched in this browser">
            {localEntries.map((entry) => (
              <Option
                disabled={!isAllowed(entry.workspaceId)}
                key={startFromLocalValue(entry.id)}
                value={startFromLocalValue(entry.id)}>
                {localLabel(entry)}
              </Option>
            ))}
          </OptGroup>
        )}
        {(templates ?? []).length > 0 && (
          <OptGroup key="templates" label="Templates">
            {(templates ?? []).map((template) => (
              <Option
                key={startFromTemplateValue(template.name)}
                value={startFromTemplateValue(template.name)}>
                {template.name}
              </Option>
            ))}
          </OptGroup>
        )}
      </Select>
      {localEntries.length > 0 && (
        <Button size="small" type="text" onClick={handleClearHistory}>
          Clear browser history
        </Button>
      )}
    </div>
  );
};

export default StartFromSelect;
