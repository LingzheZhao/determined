import { DateString } from 'ioTypes';
import { V1GenericTask, V1GenericTaskState, V1GetGenericTasksResponse } from 'services/api-ts-sdk';
import { GenericTaskState } from 'types';

import {
  getGenericTask,
  getGenericTaskConfig,
  getGenericTasks,
  killGenericTask,
  pauseGenericTask,
  unpauseGenericTask,
} from './api';
import { detApi } from './apiConfig';

// setupTests mocks services/api for every test; these tests exercise the real wrappers.
vi.unmock('services/api');

const sdkTask = (taskId: string, parentId?: string): V1GenericTask => ({
  description: '',
  jobId: `job-${taskId}`,
  name: taskId,
  noPause: false,
  parentId,
  projectId: 1,
  resourcePool: 'default',
  slots: 1,
  startTime: '2026-01-01T00:00:00Z' as DateString,
  state: V1GenericTaskState.ACTIVE,
  taskId,
  userId: 3,
  username: 'alice',
  workspaceId: 1,
});

const listResponse = (tasks: V1GenericTask[]): V1GetGenericTasksResponse => ({
  pagination: { limit: 0, offset: 0, total: tasks.length },
  tasks,
});

describe('generic task services', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('getGenericTasks passes filters in the API order and decodes the response', async () => {
    const spy = vi
      .spyOn(detApi.Tasks, 'getGenericTasks')
      .mockResolvedValue(listResponse([sdkTask('a')]));

    const response = await getGenericTasks({
      limit: 10,
      offset: 20,
      parentId: 'p',
      states: [GenericTaskState.Active, GenericTaskState.StoppingPaused],
      userIds: [3],
      workspaceId: 4,
    });

    expect(spy).toHaveBeenCalledWith(
      20,
      10,
      undefined,
      [3],
      4,
      [V1GenericTaskState.ACTIVE, V1GenericTaskState.STOPPINGPAUSED],
      'p',
      undefined,
      undefined,
    );
    expect(response.tasks[0].state).toBe(GenericTaskState.Active);
    expect(response.pagination.total).toBe(1);
  });

  it('killGenericTask sends killFromRoot', async () => {
    const spy = vi.spyOn(detApi.Tasks, 'killGenericTask').mockResolvedValue({});
    await killGenericTask({ taskId: 't' });
    expect(spy).toHaveBeenLastCalledWith('t', { killFromRoot: false, taskId: 't' }, undefined);
    await killGenericTask({ killFromRoot: true, taskId: 't' });
    expect(spy).toHaveBeenLastCalledWith('t', { killFromRoot: true, taskId: 't' }, undefined);
  });

  it('pause and unpause call their endpoints', async () => {
    const pause = vi.spyOn(detApi.Tasks, 'pauseGenericTask').mockResolvedValue({});
    const unpause = vi.spyOn(detApi.Tasks, 'unpauseGenericTask').mockResolvedValue({});
    await pauseGenericTask({ taskId: 't' });
    await unpauseGenericTask({ taskId: 't' });
    expect(pause).toHaveBeenCalledWith('t', undefined);
    expect(unpause).toHaveBeenCalledWith('t', undefined);
  });

  it('keeps the master message of a refused action', async () => {
    vi.spyOn(detApi.Tasks, 'pauseGenericTask').mockRejectedValue(
      new Response(JSON.stringify({ code: 9, message: 'cannot pause task t' }), { status: 400 }),
    );
    await expect(pauseGenericTask({ taskId: 't' })).rejects.toMatchObject({
      publicMessage: 'cannot pause task t',
    });
  });

  it('getGenericTaskConfig parses the config', async () => {
    vi.spyOn(detApi.Tasks, 'getGenericTaskConfig').mockResolvedValue({
      config: '{"name":"eval"}',
    });
    expect(await getGenericTaskConfig({ taskId: 't' })).toStrictEqual({ name: 'eval' });
  });

  describe('getGenericTask', () => {
    it('filters the list by the task ID', async () => {
      const spy = vi
        .spyOn(detApi.Tasks, 'getGenericTasks')
        .mockResolvedValue(listResponse([sdkTask('b', 'p')]));
      const task = await getGenericTask('b');
      expect(task?.taskId).toBe('b');
      expect(task?.parentId).toBe('p');
      expect(spy).toHaveBeenCalledTimes(1);
      expect(spy.mock.calls[0][7]).toStrictEqual(['b']);
      expect(spy.mock.calls[0][3]).toBeUndefined();
      expect(spy.mock.calls[0][6]).toBeUndefined();
    });

    it('returns undefined for a task it cannot see', async () => {
      vi.spyOn(detApi.Tasks, 'getGenericTasks').mockResolvedValue(listResponse([]));
      expect(await getGenericTask('x')).toBeUndefined();
    });
  });
});
