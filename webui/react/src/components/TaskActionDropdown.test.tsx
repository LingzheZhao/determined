import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import UIProvider, { DefaultTheme } from 'hew/Theme';
import { ConfirmationProvider } from 'hew/useConfirm';
import { MemoryRouter } from 'react-router-dom';

import { CommandState, CommandTask, CommandType } from 'types';

import TaskActionDropdown from './TaskActionDropdown';

vi.mock('hooks/useTaskResourcesEnabled', () => ({ default: () => false }));
vi.mock('hooks/usePermissions', () => ({
  default: () => ({
    canModifyWorkspaceNSC: ({ userId }: { userId?: number }) => userId === 101,
  }),
}));
vi.mock('routes/utils', () => ({
  paths: { taskLogs: () => '/logs' },
  serverAddress: () => 'http://localhost',
}));
vi.mock('services/api', () => ({ killTask: vi.fn() }));

const task: CommandTask = {
  id: 'task-1',
  name: 'Task',
  resourcePool: 'default',
  startTime: '2024-01-01T00:00:00Z',
  state: CommandState.Running,
  type: CommandType.Command,
  userId: 101,
  workspaceId: 10,
};

const openMenu = async (ownerId: number) => {
  render(
    <MemoryRouter>
      <UIProvider theme={DefaultTheme.Light}>
        <ConfirmationProvider>
          <TaskActionDropdown task={{ ...task, userId: ownerId }} />
        </ConfirmationProvider>
      </UIProvider>
    </MemoryRouter>,
  );
  await userEvent.click(screen.getByRole('button'));
};

describe('TaskActionDropdown', () => {
  it('hides Kill for another user’s task', async () => {
    await openMenu(102);
    expect(screen.queryByText('Kill')).not.toBeInTheDocument();
  });

  it('shows Kill for the user’s own task', async () => {
    await openMenu(101);
    expect(screen.getByText('Kill')).toBeInTheDocument();
  });
});
