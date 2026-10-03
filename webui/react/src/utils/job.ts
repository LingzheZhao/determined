import { IconName } from 'hew/Icon';

import * as Api from 'services/api-ts-sdk';
import { CommandType, Job, JobType, ResourcePool } from 'types';
import { capitalize } from 'utils/string';

export const jobTypeIconName = (jobType: JobType): IconName => {
  if (jobType === JobType.EXTERNAL) {
    return 'external';
  }
  if (jobType === JobType.GENERIC) {
    return 'tasks';
  }
  const type = jobTypeToCommandType(jobType);
  return type ?? 'experiment';
};

export const jobTypeLabel = (jobType: JobType): string => {
  if (jobType === JobType.GENERIC) {
    return 'Generic Task';
  }
  return capitalize(jobTypeIconName(jobType));
};

/*
 * The job name of a generic task with its short task ID. The master names a task without a name
 * "Generic Task <task ID>", which already contains the ID.
 */
export const genericJobLabel = (name: string, taskId: string): string => {
  if (!name || name === 'Generic Task') return `Generic Task ${taskId}`;
  if (name.includes(taskId)) return name;
  return `${name} (${taskId.split('-')[0]})`;
};

// translate JobType to CommandType
export const jobTypeToCommandType = (jobType: JobType): CommandType | undefined => {
  switch (jobType) {
    case JobType.NOTEBOOK:
      return CommandType.JupyterLab;
    case JobType.SHELL:
      return CommandType.Shell;
    case JobType.TENSORBOARD:
      return CommandType.TensorBoard;
    case JobType.COMMAND:
      return CommandType.Command;
    default:
      return undefined;
  }
};

export const orderedSchedulers = new Set<Api.V1SchedulerType>([
  Api.V1SchedulerType.PRIORITY,
  Api.V1SchedulerType.KUBERNETES,
]);

/*
We cannot modify scheduling parameters of non fault tolerant jobs in Kubernetes.
*/
export const canManageJob = (job: Job, rp?: ResourcePool): boolean => {
  if (!rp) return false;
  return !(rp.schedulerType === Api.V1SchedulerType.KUBERNETES && job.type !== JobType.EXPERIMENT);
};
