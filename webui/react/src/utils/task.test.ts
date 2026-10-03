import {
  CommandState,
  CommandTask,
  CommandType,
  ExperimentTask,
  GenericTaskState,
  RunState,
  Task,
} from 'types';

import {
  canBeOpened,
  canKillGenericTask,
  canPauseGenericTask,
  canUnpauseGenericTask,
  isExperimentTask,
  shortTaskId,
} from './task';

const SampleTask: Task = { id: '', name: '', resourcePool: '', startTime: '' };
const SampleExperimentTask: ExperimentTask = {
  ...SampleTask,
  archived: false,
  parentArchived: false,
  projectId: 0,
  resourcePool: '',
  state: 'ACTIVE' as RunState,
  userId: 345,
  username: '',
  workspaceId: 0,
};
const SampleCommandTask: CommandTask = {
  ...SampleTask,
  resourcePool: '',
  state: 'PENDING' as CommandState,
  type: 'COMMAND' as CommandType,
  userId: 345,
  workspaceId: 0,
};

describe('isExperimentTask', () => {
  it('Experiment Task', () => {
    expect(isExperimentTask(SampleExperimentTask)).toStrictEqual(true);
  });
  it('Command Task', () => {
    expect(isExperimentTask(SampleCommandTask)).toStrictEqual(false);
  });
});

describe('canBeOpened', () => {
  it('Experiment Task', () => {
    expect(canBeOpened(SampleExperimentTask)).toStrictEqual(true);
  });
  it('Terminated Command Task', () => {
    expect(
      canBeOpened({ ...SampleCommandTask, state: 'TERMINATED' as CommandState }),
    ).toStrictEqual(false);
  });
  it('Command Task without service address', () => {
    expect(canBeOpened(SampleCommandTask)).toStrictEqual(false);
  });
  it('Command Task with service address', () => {
    expect(canBeOpened({ ...SampleCommandTask, serviceAddress: 'test' })).toStrictEqual(true);
  });
});

describe('generic task actions', () => {
  const active = { noPause: false, state: GenericTaskState.Active };

  it('pauses only active tasks that were created pausable', () => {
    expect(canPauseGenericTask(active, true)).toBe(true);
    expect(canPauseGenericTask({ ...active, noPause: true }, true)).toBe(false);
    expect(canPauseGenericTask({ ...active, noPause: undefined }, true)).toBe(false);
    expect(canPauseGenericTask({ ...active, state: GenericTaskState.Paused }, true)).toBe(false);
    expect(canPauseGenericTask(active, false)).toBe(false);
  });

  it('unpauses only paused tasks', () => {
    expect(canUnpauseGenericTask({ state: GenericTaskState.Paused }, true)).toBe(true);
    expect(canUnpauseGenericTask({ state: GenericTaskState.StoppingPaused }, true)).toBe(false);
    expect(canUnpauseGenericTask({ state: GenericTaskState.Active }, true)).toBe(false);
    expect(canUnpauseGenericTask({ state: GenericTaskState.Paused }, false)).toBe(false);
  });

  it('kills tasks that have not ended', () => {
    expect(canKillGenericTask({ state: GenericTaskState.Active }, true)).toBe(true);
    expect(canKillGenericTask({ state: GenericTaskState.Paused }, true)).toBe(true);
    expect(canKillGenericTask({ state: GenericTaskState.StoppingPaused }, true)).toBe(true);
    expect(canKillGenericTask({ state: GenericTaskState.Completed }, true)).toBe(false);
    expect(canKillGenericTask({ state: GenericTaskState.Canceled }, true)).toBe(false);
    expect(canKillGenericTask({ state: GenericTaskState.StoppingError }, true)).toBe(false);
    expect(canKillGenericTask({ state: undefined }, true)).toBe(false);
    expect(canKillGenericTask({ state: GenericTaskState.Active }, false)).toBe(false);
  });

  it('shortens task IDs', () => {
    expect(shortTaskId('0b7c5e2a-1f2e-4c3d-9a8b-7c6d5e4f3a2b')).toBe('0b7c5e2a');
  });
});
