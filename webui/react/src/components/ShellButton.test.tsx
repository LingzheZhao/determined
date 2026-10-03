import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import UIProvider, { DefaultTheme } from 'hew/Theme';
import { BrowserRouter } from 'react-router-dom';

import { ThemeProvider } from 'components/ThemeProvider';
import { CommandState, CommandType, WorkspaceState } from 'types';

import ShellButton from './ShellButton';

const mocks = vi.hoisted(() => ({ launchShell: vi.fn(), openCommandResponse: vi.fn() }));

vi.mock('services/api', () => ({
  getAvailableResourcePools: () => Promise.resolve([]),
  getShells: () => Promise.resolve([]),
  getTaskTemplates: () => Promise.resolve([]),
  getWorkspaces: () => Promise.resolve({ workspaces: [] }),
  launchShell: mocks.launchShell,
  updateUserSetting: () => Promise.resolve(),
}));

vi.mock('utils/wait', () => ({
  openCommand: () => null,
  openCommandResponse: mocks.openCommandResponse,
  waitPageUrl: () => '',
}));

const WORKSPACE = {
  archived: false,
  id: 3,
  immutable: false,
  name: 'Team',
  numExperiments: 0,
  numProjects: 0,
  pinned: false,
  state: WorkspaceState.Unspecified,
  userId: 1,
};

const setup = (enabled: boolean) => {
  render(
    <BrowserRouter>
      <UIProvider theme={DefaultTheme.Light}>
        <ThemeProvider>
          <ShellButton enabled={enabled} workspace={WORKSPACE} />
        </ThemeProvider>
      </UIProvider>
    </BrowserRouter>,
  );
};

describe('ShellButton', () => {
  it('shows a disabled button without permission', () => {
    setup(false);
    expect(screen.getByRole('button', { name: 'Launch Shell' })).toBeDisabled();
  });

  it('launches a shell and shows how to connect to it', async () => {
    mocks.launchShell.mockResolvedValue({
      command: {
        id: 'shell-123',
        name: 'Shell (lively-calm-fox)',
        resourcePool: 'default',
        startTime: '2026-01-01T00:00:00Z',
        state: CommandState.Queued,
        type: CommandType.Shell,
        userId: 1,
        workspaceId: WORKSPACE.id,
      },
      config: {},
      warnings: [],
    });
    const user = userEvent.setup();
    setup(true);

    await user.click(screen.getByRole('button', { name: 'Launch Shell' }));
    await screen.findByText('Start from');
    await user.click(screen.getByRole('button', { name: 'Launch' }));

    expect(await screen.findByText('Shell Launched')).toBeInTheDocument();
    expect(screen.getByText('det shell open shell-123')).toBeInTheDocument();
    expect(
      screen.getByText(/terminal in the browser is planned for a follow-up/),
    ).toBeInTheDocument();
    expect(screen.getByText('View Logs').closest('a')).toHaveAttribute(
      'href',
      expect.stringContaining('/shell/shell-123/logs'),
    );
    expect(mocks.launchShell).toHaveBeenCalledWith(
      expect.objectContaining({ workspaceId: WORKSPACE.id }),
    );
    await waitFor(() => expect(mocks.openCommandResponse).not.toHaveBeenCalled());
  });
});
