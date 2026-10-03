import { CommandType, JobType } from 'types';

import * as utils from './job';

describe('Job Utilities', () => {
  describe('jobTypeIconName', () => {
    it('should support experiment and command types', () => {
      expect(utils.jobTypeIconName(JobType.EXPERIMENT)).toEqual('experiment');
      expect(utils.jobTypeIconName(JobType.NOTEBOOK)).toEqual(CommandType.JupyterLab);
    });
    it('should not show generic tasks as experiments', () => {
      expect(utils.jobTypeIconName(JobType.GENERIC)).toEqual('tasks');
    });
  });

  describe('jobTypeLabel', () => {
    it('should label generic tasks', () => {
      expect(utils.jobTypeLabel(JobType.GENERIC)).toEqual('Generic Task');
      expect(utils.jobTypeLabel(JobType.EXPERIMENT)).toEqual('Experiment');
    });
  });

  describe('genericJobLabel', () => {
    const taskId = '0b7c5e2a-1f2e-4c3d-9a8b-7c6d5e4f3a2b';
    it('should add the short task ID to a named task', () => {
      expect(utils.genericJobLabel('eval-sweep', taskId)).toEqual('eval-sweep (0b7c5e2a)');
    });
    it('should not repeat the ID of the default name', () => {
      expect(utils.genericJobLabel(`Generic Task ${taskId}`, taskId)).toEqual(
        `Generic Task ${taskId}`,
      );
      expect(utils.genericJobLabel('', taskId)).toEqual(`Generic Task ${taskId}`);
    });
  });

  describe('jobTypeToCommandType', () => {
    it('should convert notebook to jupyterlab', () => {
      expect(utils.jobTypeToCommandType(JobType.NOTEBOOK)).toEqual(CommandType.JupyterLab);
    });
    it('should return undefined for non command types', () => {
      expect(utils.jobTypeToCommandType(JobType.EXPERIMENT)).toBeUndefined();
    });
  });
});
