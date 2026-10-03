import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { DefaultTheme, UIProvider } from 'hew/Theme';

import { Workspace, WorkspaceState } from 'types';

import WorkspaceTasks from './WorkspaceTasks';

vi.mock('components/TaskList', () => ({
  default: ({ workspace }: { workspace?: Workspace }) => (
    <div data-testid="task-list">{workspace?.id}</div>
  ),
}));

vi.mock('components/GenericTaskList', () => ({
  default: ({ workspaceId }: { workspaceId?: number }) => (
    <div data-testid="generic-task-list">{workspaceId}</div>
  ),
}));

const WORKSPACE: Workspace = {
  archived: false,
  id: 7,
  immutable: false,
  name: 'vision',
  numExperiments: 0,
  numProjects: 0,
  pinned: false,
  state: WorkspaceState.Unspecified,
  userId: 1,
};

const user = userEvent.setup();

describe('WorkspaceTasks', () => {
  it('shows the existing task list first and the generic tasks of the workspace', async () => {
    render(
      <UIProvider theme={DefaultTheme.Light}>
        <WorkspaceTasks workspace={WORKSPACE} />
      </UIProvider>,
    );
    expect(screen.getByTestId('task-list')).toHaveTextContent('7');
    expect(screen.queryByTestId('generic-task-list')).not.toBeInTheDocument();

    await user.click(screen.getByText('Generic Tasks'));
    expect(await screen.findByTestId('generic-task-list')).toHaveTextContent('7');
  });
});
