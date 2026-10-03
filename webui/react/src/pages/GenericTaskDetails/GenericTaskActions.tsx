import Button from 'hew/Button';
import Row from 'hew/Row';
import useConfirm from 'hew/useConfirm';
import React, { useCallback, useState } from 'react';

import { killGenericTask, pauseGenericTask, unpauseGenericTask } from 'services/api';
import { GenericTaskState } from 'types';
import handleError, { ErrorLevel, ErrorType } from 'utils/error';
import { canKillGenericTask, canPauseGenericTask, canUnpauseGenericTask } from 'utils/task';

export interface GenericTaskActionTarget {
  name: string;
  noPause?: boolean;
  parentId?: string;
  state?: GenericTaskState;
  taskId: string;
}

interface Props {
  canControl: boolean;
  onComplete?: () => void;
  task: GenericTaskActionTarget;
}

const NO_PERMISSION = 'You need to own this task or be an admin to control it.';

/*
 * Shows the master's reason (for example the HTTP 400 or 409 message) instead of the generic
 * message that the confirmation modal would set.
 */
const handleActionError = (subject: string) => (e: unknown) =>
  handleError(e, {
    level: ErrorLevel.Error,
    publicSubject: subject,
    silent: false,
    type: ErrorType.Server,
  });

const GenericTaskActions: React.FC<Props> = ({ canControl, onComplete, task }: Props) => {
  const confirm = useConfirm();
  const [isBusy, setIsBusy] = useState(false);

  const canPause = canPauseGenericTask(task, canControl);
  const canUnpause = canUnpauseGenericTask(task, canControl);
  const canKill = canKillGenericTask(task, canControl);

  const pauseTooltip = !canControl
    ? NO_PERMISSION
    : task.noPause !== false
      ? 'This task was not created pausable (det task create --pausable).'
      : task.state !== GenericTaskState.Active
        ? 'Only an active task can be paused.'
        : 'Stops this task and its pausable descendants.';

  const run = useCallback(
    async (action: () => Promise<void>) => {
      setIsBusy(true);
      try {
        await action();
      } finally {
        setIsBusy(false);
        onComplete?.();
      }
    },
    [onComplete],
  );

  const handlePause = useCallback(() => {
    confirm({
      content:
        'Pause this task and its pausable descendants? Their containers are stopped; ' +
        'unpausing runs their entrypoints again from the start.',
      okText: 'Pause',
      onConfirm: () => run(() => pauseGenericTask({ taskId: task.taskId })),
      onError: handleActionError('Unable to pause task'),
      title: `Pause ${task.name}`,
    });
  }, [confirm, run, task.name, task.taskId]);

  const handleUnpause = useCallback(() => {
    confirm({
      content:
        'Unpause this task and its paused descendants? Their entrypoints run again from the ' +
        'start in new containers.',
      okText: 'Unpause',
      onConfirm: () => run(() => unpauseGenericTask({ taskId: task.taskId })),
      onError: handleActionError('Unable to unpause task'),
      title: `Unpause ${task.name}`,
    });
  }, [confirm, run, task.name, task.taskId]);

  const handleKill = useCallback(() => {
    confirm({
      content: 'Kill this task and all its descendants?',
      danger: true,
      okText: 'Kill',
      onConfirm: () => run(() => killGenericTask({ killFromRoot: false, taskId: task.taskId })),
      onError: handleActionError('Unable to kill task'),
      title: `Kill ${task.name}`,
    });
  }, [confirm, run, task.name, task.taskId]);

  const handleKillTree = useCallback(() => {
    confirm({
      content:
        'Kill the whole tree of this task, from its root task down, including tasks that ' +
        'are not descendants of this one?',
      danger: true,
      okText: 'Kill Tree',
      onConfirm: () => run(() => killGenericTask({ killFromRoot: true, taskId: task.taskId })),
      onError: handleActionError('Unable to kill task tree'),
      title: `Kill the tree of ${task.name}`,
    });
  }, [confirm, run, task.name, task.taskId]);

  return (
    <Row>
      <Button
        data-testid="generic-task-pause"
        disabled={!canPause || isBusy}
        tooltip={pauseTooltip}
        onClick={handlePause}>
        Pause
      </Button>
      <Button
        data-testid="generic-task-unpause"
        disabled={!canUnpause || isBusy}
        tooltip={canControl ? undefined : NO_PERMISSION}
        onClick={handleUnpause}>
        Unpause
      </Button>
      <Button
        danger
        data-testid="generic-task-kill"
        disabled={!canKill || isBusy}
        tooltip={canControl ? undefined : NO_PERMISSION}
        onClick={handleKill}>
        Kill
      </Button>
      {task.parentId && (
        <Button
          danger
          data-testid="generic-task-kill-tree"
          disabled={!canControl || isBusy}
          tooltip={canControl ? 'Kills the whole tree from its root task.' : NO_PERMISSION}
          onClick={handleKillTree}>
          Kill Tree
        </Button>
      )}
    </Row>
  );
};

export default GenericTaskActions;
