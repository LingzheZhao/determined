import { CommandType } from 'types';

import {
  clearLaunchHistory,
  LAUNCH_HISTORY_LIMIT,
  listLaunchHistory,
  recordLaunch,
  removeLaunchHistoryEntry,
} from './launchHistory';

const SHELL = CommandType.Shell;
const JUPYTER = CommandType.JupyterLab;

const launchConfig = (name: string) => ({
  description: name,
  entrypoint: ['/run/determined/ssh/shell-entrypoint.sh', '-p', '3333'],
  environment: {
    environment_variables: ['WANDB_API_KEY=secret-value', 'LANG=C.UTF-8'],
    image: { cpu: 'img' },
    registry_auth: { password: 'hunter2', username: 'me' },
  },
  resources: { priority: 42, resource_pool: 'default', slots: 1 },
});

describe('launchHistory', () => {
  beforeEach(() => {
    window.localStorage.clear();
    vi.restoreAllMocks();
  });

  it('records sanitized configs per user and type, newest first', () => {
    recordLaunch(1, SHELL, { config: launchConfig('first'), workspaceId: 5 });
    recordLaunch(1, SHELL, { config: launchConfig('second'), workspaceId: 5 });

    const entries = listLaunchHistory(1, SHELL);
    expect(entries.map((entry) => entry.config.description)).toEqual(['second', 'first']);
    expect(entries[0].workspaceId).toBe(5);
    expect(entries[0].config).not.toHaveProperty('entrypoint');
    expect(entries[0].config.environment).not.toHaveProperty('registry_auth');
    expect(entries[0].config.resources).toEqual({ resource_pool: 'default', slots: 1 });

    expect(listLaunchHistory(2, SHELL)).toEqual([]);
    expect(listLaunchHistory(1, JUPYTER)).toEqual([]);
    expect(listLaunchHistory(undefined, SHELL)).toEqual([]);
  });

  it('keys the stored data by user id', () => {
    recordLaunch(7, SHELL, { config: launchConfig('a'), workspaceId: 1 });
    expect(Object.keys(window.localStorage)).toEqual(['u:7/launch-history/shell']);
  });

  it('leaves out credential-like environment variables and never stores secrets', () => {
    recordLaunch(1, SHELL, { config: launchConfig('a'), workspaceId: 1 });
    const [entry] = listLaunchHistory(1, SHELL);
    expect(entry.redactedEnv).toEqual(['WANDB_API_KEY']);
    expect(entry.config.environment.environment_variables).toEqual(['LANG=C.UTF-8']);

    const raw = window.localStorage.getItem('u:1/launch-history/shell') ?? '';
    expect(raw).not.toContain('secret-value');
    expect(raw).not.toContain('hunter2');
    expect(raw).not.toMatch(/private_?key/i);
  });

  it('does nothing without a user, a config or a workspace', () => {
    recordLaunch(undefined, SHELL, { config: launchConfig('a'), workspaceId: 1 });
    recordLaunch(1, SHELL, { config: undefined, workspaceId: 1 });
    recordLaunch(1, SHELL, { config: launchConfig('a') });
    expect(window.localStorage.length).toBe(0);
  });

  it('moves a repeated config to the front instead of duplicating it', () => {
    recordLaunch(1, SHELL, { config: launchConfig('a'), workspaceId: 1 });
    recordLaunch(1, SHELL, { config: launchConfig('b'), workspaceId: 1 });
    recordLaunch(1, SHELL, { config: launchConfig('a'), workspaceId: 1 });
    expect(listLaunchHistory(1, SHELL).map((entry) => entry.config.description)).toEqual([
      'a',
      'b',
    ]);
  });

  it(`keeps at most ${LAUNCH_HISTORY_LIMIT} entries`, () => {
    for (let i = 0; i < LAUNCH_HISTORY_LIMIT + 5; i++) {
      recordLaunch(1, SHELL, { config: launchConfig(`shell ${i}`), workspaceId: 1 });
    }
    const entries = listLaunchHistory(1, SHELL);
    expect(entries).toHaveLength(LAUNCH_HISTORY_LIMIT);
    expect(entries[0].config.description).toBe(`shell ${LAUNCH_HISTORY_LIMIT + 4}`);
  });

  it('removes one entry or clears the list', () => {
    recordLaunch(1, SHELL, { config: launchConfig('a'), workspaceId: 1 });
    recordLaunch(1, SHELL, { config: launchConfig('b'), workspaceId: 1 });
    const [newest] = listLaunchHistory(1, SHELL);
    removeLaunchHistoryEntry(1, SHELL, newest.id);
    expect(listLaunchHistory(1, SHELL).map((entry) => entry.config.description)).toEqual(['a']);
    clearLaunchHistory(1, SHELL);
    expect(listLaunchHistory(1, SHELL)).toEqual([]);
    expect(window.localStorage.getItem('u:1/launch-history/shell')).toBeNull();
  });

  describe('storage failures', () => {
    it('reads corrupt or foreign data as an empty list or drops bad entries', () => {
      window.localStorage.setItem('u:1/launch-history/shell', '{not json');
      expect(listLaunchHistory(1, SHELL)).toEqual([]);

      window.localStorage.setItem('u:1/launch-history/shell', JSON.stringify({ a: 1 }));
      expect(listLaunchHistory(1, SHELL)).toEqual([]);

      recordLaunch(1, SHELL, { config: launchConfig('good'), workspaceId: 1 });
      const stored = JSON.parse(window.localStorage.getItem('u:1/launch-history/shell') ?? '[]');
      window.localStorage.setItem(
        'u:1/launch-history/shell',
        JSON.stringify([{ v: 99 }, 'junk', ...stored]),
      );
      expect(listLaunchHistory(1, SHELL).map((entry) => entry.config.description)).toEqual([
        'good',
      ]);
    });

    it('tolerates a getItem that throws', () => {
      vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
        throw new DOMException('denied', 'SecurityError');
      });
      expect(listLaunchHistory(1, SHELL)).toEqual([]);
      expect(() =>
        recordLaunch(1, SHELL, { config: launchConfig('a'), workspaceId: 1 }),
      ).not.toThrow();
    });

    it('tolerates a setItem that throws (quota exceeded)', () => {
      vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
        throw new DOMException('full', 'QuotaExceededError');
      });
      expect(() =>
        recordLaunch(1, SHELL, { config: launchConfig('a'), workspaceId: 1 }),
      ).not.toThrow();
      expect(listLaunchHistory(1, SHELL)).toEqual([]);
      expect(() => clearLaunchHistory(1, SHELL)).not.toThrow();
    });

    it('tolerates window.localStorage itself throwing', () => {
      const descriptor = Object.getOwnPropertyDescriptor(window, 'localStorage');
      Object.defineProperty(window, 'localStorage', {
        configurable: true,
        get: () => {
          throw new DOMException('blocked', 'SecurityError');
        },
      });
      try {
        expect(listLaunchHistory(1, SHELL)).toEqual([]);
        expect(() =>
          recordLaunch(1, SHELL, { config: launchConfig('a'), workspaceId: 1 }),
        ).not.toThrow();
        expect(() => clearLaunchHistory(1, SHELL)).not.toThrow();
      } finally {
        if (descriptor) Object.defineProperty(window, 'localStorage', descriptor);
      }
    });
  });
});
