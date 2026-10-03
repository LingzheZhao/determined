import { Taskv1State, V1LaunchWarning } from './api-ts-sdk';
import * as utils from './apiConfig';

const PRIVATE_KEY = '-----BEGIN OPENSSH PRIVATE KEY-----\nsecret-key-material\n-----END';
const PUBLIC_KEY = 'ssh-ed25519 public-key-material';

const shellResponse = () => ({
  config: { description: 'Shell (lively-calm-fox)', resources: { slots: 1 } },
  shell: {
    agentUserGroup: { user: 'root' },
    description: 'Shell (lively-calm-fox)',
    id: 'shell-1',
    jobId: 'job-1',
    privateKey: PRIVATE_KEY,
    publicKey: PUBLIC_KEY,
    resourcePool: 'default',
    startTime: new Date('2026-01-01T00:00:00Z'),
    state: Taskv1State.QUEUED,
    userId: 3,
    username: 'me',
    workspaceId: 9,
  },
  warnings: [V1LaunchWarning.CURRENTSLOTSEXCEEDED],
});

describe('apiConfig', () => {
  describe('getUserIds', () => {
    it('should convert user id strings into user id numbers', () => {
      expect(utils.getUserIds(['123'])).toStrictEqual([123]);
      expect(utils.getUserIds(['456', '789'])).toStrictEqual([456, 789]);
    });

    it('should filter out non-numeric string user ids', () => {
      expect(utils.getUserIds(['abc', '123'])).toStrictEqual([123]);
    });

    it('should return `undefined` when there are no valid user ids', () => {
      expect(utils.getUserIds(['abc', 'def'])).toBeUndefined();
    });
  });

  describe('launchShell', () => {
    it('returns the task, config and warnings without the SSH keys', () => {
      const response = shellResponse();
      const result = utils.launchShell.postProcess(response);

      expect(result.command).toMatchObject({ id: 'shell-1', type: 'shell', workspaceId: 9 });
      expect(result.config).toEqual(response.config);
      expect(result.warnings).toEqual([V1LaunchWarning.CURRENTSLOTSEXCEEDED]);
      const serialized = JSON.stringify(result);
      expect(serialized).not.toContain('secret-key-material');
      expect(serialized).not.toContain('public-key-material');
    });

    it('removes the keys from the raw response object too', () => {
      const response = shellResponse();
      utils.launchShell.postProcess(response);
      expect(response.shell).not.toHaveProperty('privateKey');
      expect(response.shell).not.toHaveProperty('publicKey');
    });
  });

  describe('config getters', () => {
    it('getShellConfig returns only the config of a shell response', () => {
      const response = shellResponse();
      const result = utils.getShellConfig.postProcess(response);
      expect(result).toEqual(response.config);
      expect(JSON.stringify(result)).not.toContain('secret-key-material');
    });

    it('getJupyterLabConfig returns only the config', () => {
      const config = { description: 'JupyterLab (a-b-c)' };
      expect(
        utils.getJupyterLabConfig.postProcess({
          config,
          notebook: {
            description: 'x',
            id: 'nb',
            jobId: 'j',
            resourcePool: 'default',
            startTime: new Date('2026-01-01T00:00:00Z'),
            state: Taskv1State.RUNNING,
            username: 'me',
            workspaceId: 1,
          },
        }),
      ).toEqual(config);
    });

    it('launchJupyterLab now returns the merged config', () => {
      const config = { resources: { slots: 0 } };
      const result = utils.launchJupyterLab.postProcess({
        config,
        notebook: {
          description: 'x',
          id: 'nb',
          jobId: 'j',
          resourcePool: 'default',
          startTime: new Date('2026-01-01T00:00:00Z'),
          state: Taskv1State.QUEUED,
          username: 'me',
          workspaceId: 1,
        },
      });
      expect(result.config).toEqual(config);
      expect(result.command.id).toBe('nb');
    });
  });
});
