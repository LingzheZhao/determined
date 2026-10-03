import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Button from 'hew/Button';
import { useModal } from 'hew/Modal';
import { DefaultTheme, UIProvider } from 'hew/Theme';
import { Loadable } from 'hew/utils/loadable';
import React, { useEffect } from 'react';
import { BrowserRouter } from 'react-router-dom';

import NtscLaunchModalComponent, { Props } from 'components/NtscLaunchModal';
import { ThemeProvider } from 'components/ThemeProvider';
import { SettingsProvider } from 'hooks/useSettingsProvider';
import authStore from 'stores/auth';
import userStore from 'stores/users';
import { CommandState, CommandTask, CommandType, RawJson, WorkspaceState } from 'types';
import { listLaunchHistory, recordLaunch } from 'utils/launchHistory';

const mocks = vi.hoisted(() => ({
  canCreateTemplateWorkspace: true,
  createTaskTemplate: vi.fn(),
  getJupyterLabConfig: vi.fn(),
  getJupyterLabs: vi.fn(),
  getShellConfig: vi.fn(),
  getShells: vi.fn(),
  getTaskTemplates: vi.fn(),
  launchJupyterLab: vi.fn(),
  launchShell: vi.fn(),
  makeToast: vi.fn(),
  openCommandResponse: vi.fn(),
  previewJupyterLab: vi.fn(),
  taskTemplatesOn: true,
  updateTaskTemplate: vi.fn(),
}));

vi.mock('services/api', () => ({
  createTaskTemplate: mocks.createTaskTemplate,
  getAvailableResourcePools: () => Promise.resolve([]),
  getCurrentUser: () => Promise.resolve({ id: 1, isActive: true, isAdmin: false, username: 'me' }),
  getJupyterLabConfig: mocks.getJupyterLabConfig,
  getJupyterLabs: mocks.getJupyterLabs,
  getResourcePools: () => Promise.resolve([]),
  getShellConfig: mocks.getShellConfig,
  getShells: mocks.getShells,
  getTaskTemplates: mocks.getTaskTemplates,
  getUsers: () => Promise.resolve({ users: [] }),
  getUserSetting: () => Promise.resolve({ settings: [] }),
  getWorkspaces: () => Promise.resolve({ workspaces: [] }),
  launchJupyterLab: mocks.launchJupyterLab,
  launchShell: mocks.launchShell,
  previewJupyterLab: mocks.previewJupyterLab,
  updateTaskTemplate: mocks.updateTaskTemplate,
  updateTaskTemplateName: vi.fn(),
  updateUserSetting: () => Promise.resolve(),
}));

// Error toasts render outside the test's UIProvider; capture them instead.
vi.mock('hew/Toast', async (importOriginal) => ({
  ...(await importOriginal<typeof import('hew/Toast')>()),
  makeToast: mocks.makeToast,
}));

vi.mock('hooks/usePermissions', () => ({
  default: () => ({
    canCreateTemplateWorkspace: () => mocks.canCreateTemplateWorkspace,
    canCreateWorkspaceNSC: () => true,
  }),
}));

vi.mock('hooks/useFeature', () => ({
  default: () => ({
    isOn: (feature: string) => (feature === 'task_templates' ? mocks.taskTemplatesOn : false),
  }),
}));

vi.mock('stores/cluster', async (importOriginal) => {
  const loadable = await import('hew/utils/loadable');
  const observable = await import('utils/observable');

  const store = { resourcePools: observable.observable(loadable.Loaded([])) };
  return {
    __esModule: true,
    ...(await importOriginal<typeof import('stores/cluster')>()),
    clusterStore: store,
  };
});

vi.mock('utils/wait', () => ({
  openCommand: () => null,
  openCommandResponse: mocks.openCommandResponse,
  waitPageUrl: () => '',
}));

vi.mock('hew/CodeEditor', () => ({
  __esModule: true,
  default: ({ file }: { file: string | Loadable<string> }) => (
    <pre data-testid="code-editor">
      {typeof file === 'string' ? file : Loadable.getOrElse('', file)}
    </pre>
  ),
}));

const USER_ID = 1;

const WORKSPACE = {
  archived: false,
  id: 1,
  immutable: false,
  name: 'Uncategorized',
  numExperiments: 0,
  numProjects: 0,
  pinned: false,
  state: WorkspaceState.Unspecified,
  userId: USER_ID,
};

const task = (type: CommandType, overrides: Partial<CommandTask> = {}): CommandTask => ({
  id: `${type}-old`,
  name: 'cluster task',
  resourcePool: 'gpu',
  startTime: '2026-01-01T10:00:00Z',
  state: CommandState.Terminated,
  type,
  userId: USER_ID,
  workspaceId: WORKSPACE.id,
  ...overrides,
});

/** A merged config as GET /api/v1/shells/{id} returns it. */
const clusterShellConfig = {
  bind_mounts: [{ container_path: '/data', host_path: '/mnt/data' }],
  description: 'old shell',
  entrypoint: ['/run/determined/ssh/shell-entrypoint.sh', '-p', '3201'],
  environment: { image: { cuda: 'custom:1' } },
  idle_timeout: null,
  resources: { priority: 42, resource_pool: 'gpu', slots: 2 },
};

const GPU_TEMPLATE = {
  config: { resources: { resource_pool: 'gpu', slots: 4 } },
  name: 'gpu-template',
  workspaceId: WORKSPACE.id,
};

/** What the master's notebook preview returns for a request config. */
const previewFor = (params: { config?: RawJson; templateName?: string }) => {
  const config = params.config ?? {};
  const resources = config.resources ?? {};
  const custom = config.description === 'tpl source';
  return Promise.resolve({
    description: config.description ?? 'JupyterLab (kindly-quick-heron)',
    entrypoint: null,
    environment: { image: { cuda: custom ? 'custom:2' : 'default:1' } },
    idle_timeout: '30m',
    notebook_idle_type: 'kernels_or_terminals',
    resources: {
      priority: 42,
      resource_pool: resources.resource_pool ?? 'default',
      slots: resources.slots ?? 1,
    },
  });
};

const launchedShell = {
  command: task(CommandType.Shell, {
    id: 'shell-new',
    name: 'Shell (lively-calm-fox)',
    state: CommandState.Queued,
  }),
  config: { description: 'Shell (lively-calm-fox)', resources: { resource_pool: 'gpu', slots: 1 } },
  warnings: [],
};

const ModalTrigger: React.FC<Props> = (props) => {
  const LaunchModal = useModal(NtscLaunchModalComponent);

  useEffect(() => {
    authStore.setAuth({ isAuthenticated: true });
    authStore.setAuthChecked();
  }, []);

  return (
    <SettingsProvider>
      <>
        <Button onClick={LaunchModal.open}>Open</Button>
        <LaunchModal.Component {...props} />
      </>
    </SettingsProvider>
  );
};

const setup = async (props: Partial<Props> = {}) => {
  const user = userEvent.setup();
  const onLaunched = vi.fn();
  render(
    <BrowserRouter>
      <UIProvider theme={DefaultTheme.Light}>
        <ThemeProvider>
          <ModalTrigger
            type={CommandType.Shell}
            workspace={WORKSPACE}
            onLaunched={onLaunched}
            {...props}
          />
        </ThemeProvider>
      </UIProvider>
    </BrowserRouter>,
  );
  await user.click(await screen.findByRole('button', { name: 'Open' }));
  await screen.findByText('Start from');
  return { onLaunched, user };
};

const openStartFrom = async (user: ReturnType<typeof userEvent.setup>) => {
  await user.click(await screen.findByLabelText('Start from'));
};

const launch = async (user: ReturnType<typeof userEvent.setup>) => {
  await user.click(screen.getByRole('button', { name: 'Launch' }));
};

describe('NtscLaunchModal', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
    mocks.canCreateTemplateWorkspace = true;
    mocks.taskTemplatesOn = true;
    mocks.createTaskTemplate.mockReset().mockResolvedValue({});
    mocks.updateTaskTemplate.mockReset();
    mocks.getJupyterLabConfig.mockReset();
    mocks.getJupyterLabs.mockReset().mockResolvedValue([]);
    mocks.getShellConfig.mockReset().mockResolvedValue(clusterShellConfig);
    mocks.getShells.mockReset().mockResolvedValue([]);
    mocks.getTaskTemplates.mockReset().mockResolvedValue([GPU_TEMPLATE]);
    mocks.launchJupyterLab.mockReset();
    mocks.launchShell.mockReset().mockResolvedValue(launchedShell);
    mocks.makeToast.mockReset();
    mocks.openCommandResponse.mockReset();
    mocks.previewJupyterLab.mockReset().mockImplementation(previewFor);
    userStore.updateCurrentUser({ id: USER_ID, isActive: true, isAdmin: false, username: 'me' });
  });

  it.each([
    [CommandType.JupyterLab, 'Launch JupyterLab'],
    [CommandType.Shell, 'Launch Shell'],
  ])('renders the shared form for %s', async (type, title) => {
    await setup({ type });
    expect(screen.getByText(title)).toBeInTheDocument();
    expect(screen.getByText('Start from')).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Name (optional)')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Show Full Config' })).toBeInTheDocument();
  });

  describe('shell', () => {
    it('launches from the simple form, reports the shell and records it in this browser', async () => {
      const { onLaunched, user } = await setup();
      await user.type(screen.getByPlaceholderText('Name (optional)'), 'debug box');
      await launch(user);

      await waitFor(() => expect(onLaunched).toHaveBeenCalledWith(launchedShell));
      expect(mocks.launchShell).toHaveBeenCalledWith({
        config: { description: 'debug box', resources: { resource_pool: undefined, slots: 1 } },
        templateName: undefined,
        workspaceId: WORKSPACE.id,
      });
      expect(mocks.launchJupyterLab).not.toHaveBeenCalled();
      expect(mocks.openCommandResponse).not.toHaveBeenCalled();
      expect(listLaunchHistory(USER_ID, CommandType.Shell)).toHaveLength(1);
    });

    it('keeps the modal open and does not report a failed launch', async () => {
      mocks.launchShell.mockRejectedValue(new Error('invalid config'));
      const { onLaunched, user } = await setup();
      await launch(user);
      await waitFor(() =>
        expect(mocks.makeToast).toHaveBeenCalledWith(
          expect.objectContaining({ title: 'Could not submit form' }),
        ),
      );
      expect(onLaunched).not.toHaveBeenCalled();
      expect(screen.getByText('Launch Shell')).toBeInTheDocument();
    });

    it('previews the full config through the JupyterLab preview without notebook-only keys', async () => {
      const { onLaunched, user } = await setup();
      await user.click(screen.getByRole('button', { name: 'Show Full Config' }));

      await waitFor(() =>
        expect(screen.getByTestId('code-editor')).toHaveTextContent('resource_pool: default'),
      );
      expect(mocks.previewJupyterLab).toHaveBeenCalledWith(
        expect.objectContaining({ preview: true, workspaceId: WORKSPACE.id }),
      );
      const yamlText = screen.getByTestId('code-editor').textContent ?? '';
      expect(yamlText).not.toContain('idle_timeout');
      expect(yamlText).not.toContain('notebook_idle_type');
      expect(yamlText).not.toContain('JupyterLab (');
      expect(yamlText).not.toContain('entrypoint');

      await launch(user);
      await waitFor(() => expect(onLaunched).toHaveBeenCalled());
      const { config } = mocks.launchShell.mock.calls[0][0];
      expect(config).not.toHaveProperty('idle_timeout');
      expect(config.resources).toEqual({ priority: 42, resource_pool: 'default', slots: 1 });
    });

    it('lists recent tasks on the cluster, this browser’s history and templates', async () => {
      recordLaunch(USER_ID, CommandType.Shell, {
        config: { description: 'from browser', resources: { resource_pool: 'cpu', slots: 0 } },
        workspaceId: WORKSPACE.id,
      });
      recordLaunch(USER_ID, CommandType.JupyterLab, {
        config: { description: 'a notebook' },
        workspaceId: WORKSPACE.id,
      });
      mocks.getShells.mockResolvedValue([task(CommandType.Shell)]);
      const { user } = await setup();

      await waitFor(() =>
        expect(mocks.getShells).toHaveBeenCalledWith({
          limit: 20,
          orderBy: 'ORDER_BY_DESC',
          sortBy: 'SORT_BY_START_TIME',
          users: [String(USER_ID)],
          workspaceId: WORKSPACE.id,
        }),
      );
      expect(mocks.getJupyterLabs).not.toHaveBeenCalled();

      await openStartFrom(user);
      expect(await screen.findByText('Recent on cluster')).toBeInTheDocument();
      expect(screen.getByText('Recently launched in this browser')).toBeInTheDocument();
      expect(screen.getByText('Templates')).toBeInTheDocument();
      expect(screen.getByText(/^cluster task · Terminated ·/)).toBeInTheDocument();
      expect(screen.getByText(/^from browser · cpu, 0 slots ·/)).toBeInTheDocument();
      expect(screen.getByText('gpu-template')).toBeInTheDocument();
      expect(screen.queryByText(/^a notebook/)).not.toBeInTheDocument();
      // A config is only fetched once an item is picked.
      expect(mocks.getShellConfig).not.toHaveBeenCalled();
    });

    it('starts from a recent task: fetches its config, fills the form and launches the merged config', async () => {
      mocks.getShells.mockResolvedValue([task(CommandType.Shell)]);
      const { onLaunched, user } = await setup();
      await openStartFrom(user);
      await user.click(await screen.findByText(/^cluster task ·/));

      await waitFor(() =>
        expect(screen.getByPlaceholderText('Name (optional)')).toHaveValue('old shell'),
      );
      expect(mocks.getShellConfig).toHaveBeenCalledWith({ commandId: 'shell-old' });

      await launch(user);
      await waitFor(() => expect(onLaunched).toHaveBeenCalled());
      expect(mocks.launchShell).toHaveBeenCalledWith({
        config: {
          bind_mounts: clusterShellConfig.bind_mounts,
          description: 'old shell',
          environment: { image: { cuda: 'custom:1' } },
          idle_timeout: null,
          resources: { resource_pool: 'gpu', slots: 2 },
        },
        workspaceId: WORKSPACE.id,
      });
    });

    it('drops a recent task the master no longer knows', async () => {
      mocks.getShells.mockResolvedValue([task(CommandType.Shell)]);
      mocks.getShellConfig.mockRejectedValue(new Error('shell not found'));
      const { user } = await setup();
      await openStartFrom(user);
      await user.click(await screen.findByText(/^cluster task ·/));

      await waitFor(() =>
        expect(mocks.makeToast).toHaveBeenCalledWith(
          expect.objectContaining({
            severity: 'Warning',
            title: 'Unable to load the config of cluster task.',
          }),
        ),
      );
      await openStartFrom(user);
      await waitFor(() => expect(screen.queryByText(/^cluster task ·/)).not.toBeInTheDocument());
    });

    it('starts from this browser’s history', async () => {
      recordLaunch(USER_ID, CommandType.Shell, {
        config: {
          description: 'from browser',
          environment: { environment_variables: ['HF_TOKEN=abc', 'LANG=C.UTF-8'] },
          resources: { resource_pool: 'cpu', slots: 0 },
        },
        workspaceId: WORKSPACE.id,
      });
      const { onLaunched, user } = await setup();
      await openStartFrom(user);
      await user.click(await screen.findByText(/^from browser ·/));

      await waitFor(() =>
        expect(screen.getByPlaceholderText('Name (optional)')).toHaveValue('from browser'),
      );
      expect(
        screen.getByText(/Environment variables that looked like credentials were not saved/),
      ).toBeInTheDocument();
      expect(screen.getByText(/HF_TOKEN/)).toBeInTheDocument();

      await launch(user);
      await waitFor(() => expect(onLaunched).toHaveBeenCalled());
      expect(mocks.launchShell).toHaveBeenCalledWith({
        config: {
          description: 'from browser',
          environment: { environment_variables: ['LANG=C.UTF-8'] },
          resources: { resource_pool: 'cpu', slots: 0 },
        },
        workspaceId: WORKSPACE.id,
      });
    });

    it('starts from a template and copies its pool and slots into the form', async () => {
      const { onLaunched, user } = await setup();
      await openStartFrom(user);
      await user.click(await screen.findByText('gpu-template'));
      await launch(user);

      await waitFor(() => expect(onLaunched).toHaveBeenCalled());
      expect(mocks.launchShell).toHaveBeenCalledWith({
        config: { description: undefined, resources: { resource_pool: 'gpu', slots: 4 } },
        templateName: 'gpu-template',
        workspaceId: WORKSPACE.id,
      });
    });

    it('starts from the given task for "Launch Again"', async () => {
      const { onLaunched, user } = await setup({ initialTask: task(CommandType.Shell) });

      await waitFor(() =>
        expect(screen.getByPlaceholderText('Name (optional)')).toHaveValue('old shell'),
      );
      expect(mocks.getShellConfig).toHaveBeenCalledWith({ commandId: 'shell-old' });
      expect(screen.getByText(/^cluster task ·/)).toBeInTheDocument();

      await launch(user);
      await waitFor(() => expect(onLaunched).toHaveBeenCalled());
      expect(mocks.launchShell.mock.calls[0][0].config).toMatchObject({
        description: 'old shell',
        resources: { resource_pool: 'gpu', slots: 2 },
      });
      expect(mocks.launchShell.mock.calls[0][0].templateName).toBeUndefined();
    });

    it('still works when local storage throws', async () => {
      vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
        throw new DOMException('blocked', 'SecurityError');
      });
      vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
        throw new DOMException('full', 'QuotaExceededError');
      });
      const { onLaunched, user } = await setup();
      await openStartFrom(user);
      expect(await screen.findByText('gpu-template')).toBeInTheDocument();
      expect(screen.queryByText('Recently launched in this browser')).not.toBeInTheDocument();

      await launch(user);
      await waitFor(() => expect(onLaunched).toHaveBeenCalledWith(launchedShell));
    });
  });

  describe('JupyterLab', () => {
    it('keeps the existing launch: template name with the simple fields, then opens the notebook', async () => {
      mocks.launchJupyterLab.mockResolvedValue({
        command: task(CommandType.JupyterLab),
        warnings: [],
      });
      const { user } = await setup({ type: CommandType.JupyterLab });
      await openStartFrom(user);
      await user.click(await screen.findByText('gpu-template'));
      await launch(user);

      await waitFor(() => expect(mocks.openCommandResponse).toHaveBeenCalled());
      expect(mocks.launchJupyterLab).toHaveBeenCalledWith({
        config: { description: undefined, resources: { resource_pool: 'gpu', slots: 4 } },
        templateName: 'gpu-template',
        workspaceId: WORKSPACE.id,
      });
      expect(mocks.launchShell).not.toHaveBeenCalled();
    });

    it('starts from a recent notebook and previews its config as the base', async () => {
      mocks.getJupyterLabs.mockResolvedValue([task(CommandType.JupyterLab, { name: 'nb' })]);
      mocks.getJupyterLabConfig.mockResolvedValue({
        description: 'JupyterLab (kindly-quick-heron)',
        entrypoint: ['/run/determined/jupyter/notebook-entrypoint.sh'],
        environment: { image: { cuda: 'custom:1' } },
        idle_timeout: '1h',
        resources: { priority: 42, resource_pool: 'gpu', slots: 1 },
      });
      const { user } = await setup({ type: CommandType.JupyterLab });
      await openStartFrom(user);
      await user.click(await screen.findByText(/^nb ·/));
      await waitFor(() => expect(mocks.getJupyterLabConfig).toHaveBeenCalled());
      expect(mocks.getShells).not.toHaveBeenCalled();
      expect(mocks.getShellConfig).not.toHaveBeenCalled();

      await user.click(screen.getByRole('button', { name: 'Show Full Config' }));
      await waitFor(() =>
        expect(mocks.previewJupyterLab).toHaveBeenCalledWith({
          config: {
            environment: { image: { cuda: 'custom:1' } },
            idle_timeout: '1h',
            resources: { resource_pool: 'gpu', slots: 1 },
          },
          preview: true,
          templateName: undefined,
          workspaceId: WORKSPACE.id,
        }),
      );
    });
  });

  describe('Save as Template', () => {
    it('opens a prefilled new template with only the non-default settings and creates it', async () => {
      const { user } = await setup({ type: CommandType.JupyterLab });
      await user.type(screen.getByPlaceholderText('Name (optional)'), 'tpl source');
      await user.click(screen.getByRole('button', { name: 'Show Full Config' }));
      await waitFor(() => expect(screen.getByTestId('code-editor')).toHaveTextContent('custom:2'));

      await user.click(screen.getByRole('button', { name: 'Save as Template' }));
      expect(await screen.findByText('New Template')).toBeInTheDocument();
      expect(screen.getByText(/Other users can read templates/)).toBeInTheDocument();
      const editors = screen.getAllByTestId('code-editor');
      const draft = editors[editors.length - 1].textContent ?? '';
      expect(draft).toContain('custom:2');
      expect(draft).toContain('resource_pool: default');
      expect(draft).not.toContain('tpl source');
      expect(draft).not.toContain('idle_timeout');
      expect(draft).not.toContain('priority');

      await user.type(screen.getByLabelText('Name'), 'my-template');
      await user.click(screen.getByRole('button', { name: 'Create Template' }));
      await waitFor(() =>
        expect(mocks.createTaskTemplate).toHaveBeenCalledWith({
          config: {
            environment: { image: { cuda: 'custom:2' } },
            resources: { resource_pool: 'default', slots: 1 },
          },
          name: 'my-template',
          workspaceId: WORKSPACE.id,
        }),
      );
      expect(mocks.updateTaskTemplate).not.toHaveBeenCalled();
    });

    it('is hidden without permission to create templates', async () => {
      mocks.canCreateTemplateWorkspace = false;
      const { user } = await setup();
      await user.click(screen.getByRole('button', { name: 'Show Full Config' }));
      await screen.findByRole('button', { name: 'Show Simple Config' });
      expect(screen.queryByRole('button', { name: 'Save as Template' })).not.toBeInTheDocument();
    });

    it('is hidden when templates are turned off', async () => {
      mocks.taskTemplatesOn = false;
      const { user } = await setup();
      await user.click(screen.getByRole('button', { name: 'Show Full Config' }));
      await screen.findByRole('button', { name: 'Show Simple Config' });
      expect(screen.queryByRole('button', { name: 'Save as Template' })).not.toBeInTheDocument();
    });
  });
});
