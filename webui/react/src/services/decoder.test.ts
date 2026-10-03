import hparams from 'fixtures/hyperparameter-configs.json';
import experimentResps from 'fixtures/responses/experiment-details/set-a.json';
import * as ioTypes from 'ioTypes';
import { DateString } from 'ioTypes';
import {
  V1ExperimentActionResult,
  V1GenericTask,
  V1GenericTaskState,
  V1RunActionResult,
  V1Task,
  V1TaskType,
} from 'services/api-ts-sdk';
import * as decoder from 'services/decoder';
import { CommandState, GenericTaskState } from 'types';

type FailReport<T = unknown> = { error: Error; sample: T };

const tryOnSamples = <T = unknown>(samples: T[], fn: (sample: T) => void): FailReport[] => {
  const fails: FailReport[] = [];
  samples.forEach((sample) => {
    try {
      fn(sample);
    } catch (e) {
      fails.push({ error: e as Error, sample });
    }
  });
  if (fails.length > 0) {
    const { sample, error } = fails.last();
    /* eslint-disable no-console */
    console.error(error);
    console.log('Sample:', sample);
    /* eslint-enable no-console */
  }
  return fails;
};

describe('Decoder', () => {
  it('Should decode seeded hyperparameters', () => {
    const fails = tryOnSamples(hparams, (hparam) => {
      ioTypes.decode<ioTypes.ioTypeHyperparameters>(ioTypes.ioHyperparameters, hparam);
    });
    expect(fails).toHaveLength(0);
  });

  it('Should decode experiment configs', () => {
    const fails = tryOnSamples(
      experimentResps.map((r) => r.config),
      (config) => {
        ioTypes.decode<ioTypes.ioTypeExperimentConfig>(ioTypes.ioExperimentConfig, config);
      },
    );
    expect(fails).toHaveLength(0);
  });

  describe('mapV1ActionResults', () => {
    it('should work with Sdk.V1ExperimentActionResult[] input', () => {
      const result: V1ExperimentActionResult[] = [
        { error: '', id: 1 },
        { error: '', id: 2 },
        { error: 'error', id: 3 },
      ];

      const expected = decoder.mapV1ActionResults(result);
      expect(expected).toStrictEqual({
        failed: [{ error: 'error', id: 3 }],
        successful: [1, 2],
      });
    });

    it('should work with Sdk.V1RunActionResult[] input', () => {
      const result: V1RunActionResult[] = [
        { error: '', id: 1 },
        { error: '', id: 2 },
        { error: 'error', id: 3 },
      ];

      const expected = decoder.mapV1ActionResults(result);
      expect(expected).toStrictEqual({
        failed: [{ error: 'error', id: 3 }],
        successful: [1, 2],
      });
    });

    it('should work with empty input', () => {
      const expected = decoder.mapV1ActionResults([]);
      expect(expected).toStrictEqual({
        failed: [],
        successful: [],
      });
    });

    it('should work with all successful input', () => {
      const result: V1RunActionResult[] = [
        { error: '', id: 1 },
        { error: '', id: 2 },
        { error: '', id: 3 },
        { error: '', id: 4 },
        { error: '', id: 5 },
      ];

      const expected = decoder.mapV1ActionResults(result);
      expect(expected).toStrictEqual({
        failed: [],
        successful: [1, 2, 3, 4, 5],
      });
    });

    it('should work with all failed input', () => {
      const result: V1RunActionResult[] = [
        { error: 'oh no', id: 1 },
        { error: 'yare yare', id: 2 },
        { error: 'error', id: 3 },
        { error: 'a', id: 4 },
        { error: 'エラー', id: 5 },
      ];

      const expected = decoder.mapV1ActionResults(result);
      expect(expected).toStrictEqual({
        failed: [
          { error: 'oh no', id: 1 },
          { error: 'yare yare', id: 2 },
          { error: 'error', id: 3 },
          { error: 'a', id: 4 },
          { error: 'エラー', id: 5 },
        ],
        successful: [],
      });
    });
  });

  describe('generic tasks', () => {
    const sdkTask: V1GenericTask = {
      allocationId: 'alloc-1',
      description: 'evaluates the checkpoints of run 12',
      forkedFrom: '',
      jobId: 'job-1',
      name: 'eval-sweep',
      noPause: false,
      parentId: 'parent-1',
      projectId: 1,
      resourcePool: 'default',
      slots: 2,
      startTime: '2026-01-01T00:00:00Z' as DateString,
      state: V1GenericTaskState.STOPPINGPAUSED,
      taskId: 'task-1',
      userId: 3,
      username: 'alice',
      workspaceId: 4,
    };

    it('should strip the state prefix', () => {
      expect(decoder.mapV1GenericTaskState(V1GenericTaskState.ACTIVE)).toBe(
        GenericTaskState.Active,
      );
      expect(decoder.mapV1GenericTaskState(V1GenericTaskState.STOPPINGPAUSED)).toBe(
        GenericTaskState.StoppingPaused,
      );
      expect(decoder.mapV1GenericTaskState('BOGUS' as V1GenericTaskState)).toBe(
        GenericTaskState.Unspecified,
      );
    });

    it('should encode states for requests', () => {
      Object.values(GenericTaskState).forEach((state) => {
        const encoded = decoder.encodeGenericTaskState(state);
        expect(Object.values(V1GenericTaskState)).toContain(encoded);
        expect(decoder.mapV1GenericTaskState(encoded)).toBe(state);
      });
    });

    it('should map a listed generic task', () => {
      expect(decoder.mapV1GenericTask(sdkTask)).toStrictEqual({
        allocationId: 'alloc-1',
        description: 'evaluates the checkpoints of run 12',
        endTime: undefined,
        forkedFrom: undefined,
        jobId: 'job-1',
        name: 'eval-sweep',
        noPause: false,
        parentId: 'parent-1',
        projectId: 1,
        resourcePool: 'default',
        slots: 2,
        startTime: '2026-01-01T00:00:00Z',
        state: GenericTaskState.StoppingPaused,
        taskId: 'task-1',
        userId: 3,
        username: 'alice',
        workspaceId: 4,
      });
    });

    it('should name an unnamed task and paginate', () => {
      const response = decoder.mapV1GenericTasksResponse({
        pagination: { endIndex: 1, limit: 10, offset: 0, startIndex: 0, total: 11 },
        tasks: [{ ...sdkTask, name: '' }],
      });
      expect(response.pagination).toStrictEqual({ limit: 10, offset: 0, total: 11 });
      expect(response.tasks[0].name).toBe('Generic Task task-1');
    });

    it('should carry the generic fields and allocations of GetTask', () => {
      const task: V1Task = {
        allocations: [
          {
            allocationId: 'task-1.1',
            endTime: '2026-01-01T01:00:00Z',
            exitReason: 'allocation stopped after resources exited successfully',
            slots: 2,
            startTime: '2026-01-01T00:00:00Z',
            state: 'STATE_TERMINATED',
            statusCode: 0,
            taskId: 'task-1',
          },
        ],
        forkedFrom: 'origin-1',
        noPause: true,
        parentId: '',
        startTime: '2026-01-01T00:00:00Z' as DateString,
        taskId: 'task-1',
        taskState: V1GenericTaskState.COMPLETED,
        taskType: V1TaskType.GENERIC,
      };
      const item = decoder.mapV1Task(task);
      expect(item.taskState).toBe(GenericTaskState.Completed);
      expect(item.parentId).toBeUndefined();
      expect(item.forkedFrom).toBe('origin-1');
      expect(item.noPause).toBe(true);
      expect(item.taskType).toBe(V1TaskType.GENERIC);
      expect(item.allocations[0]).toMatchObject({
        allocationId: 'task-1.1',
        exitReason: 'allocation stopped after resources exited successfully',
        slots: 2,
        state: CommandState.Terminated,
        statusCode: 0,
      });
    });

    it('should parse the config JSON string', () => {
      expect(decoder.mapGenericTaskConfig('{"entrypoint":["python","eval.py"]}')).toStrictEqual({
        entrypoint: ['python', 'eval.py'],
      });
      expect(() => decoder.mapGenericTaskConfig('[1]')).toThrow();
    });
  });
});
