import { render, screen, waitFor } from '@testing-library/react';
import { DefaultTheme, UIProvider } from 'hew/Theme';
import { DndProvider } from 'react-dnd';
import { HTML5Backend } from 'react-dnd-html5-backend';
import { BrowserRouter } from 'react-router-dom';

import { ThemeProvider } from 'components/ThemeProvider';
import { SettingsProvider } from 'hooks/useSettingsProvider';
import { getGenericTasks } from 'services/api';
import userStore from 'stores/users';
import { GenericTask, GenericTaskState } from 'types';

import GenericTaskList from './GenericTaskList';

const CURRENT_USER_ID = 3;

const TASK: GenericTask = {
  description: '',
  jobId: 'job-1',
  name: 'eval-sweep',
  noPause: false,
  projectId: 1,
  resourcePool: 'default',
  slots: 1,
  startTime: '2026-01-01T00:00:00Z',
  state: GenericTaskState.Active,
  taskId: 'task-1',
  userId: 4,
  username: 'bob',
  workspaceId: 7,
};

vi.mock('services/api', () => ({
  getGenericTasks: vi.fn(() =>
    Promise.resolve({ pagination: { limit: 10, offset: 0, total: 1 }, tasks: [TASK] }),
  ),
}));

const setup = (workspaceId?: number) =>
  render(
    <UIProvider theme={DefaultTheme.Light}>
      <ThemeProvider>
        <DndProvider backend={HTML5Backend}>
          <SettingsProvider>
            <BrowserRouter>
              <GenericTaskList workspaceId={workspaceId} />
            </BrowserRouter>
          </SettingsProvider>
        </DndProvider>
      </ThemeProvider>
    </UIProvider>,
  );

describe('GenericTaskList', () => {
  beforeAll(() => {
    userStore.updateCurrentUser({
      id: CURRENT_USER_ID,
      isActive: true,
      isAdmin: false,
      username: 'alice',
    });
  });

  afterEach(() => vi.clearAllMocks());

  it("lists all users' tasks of a workspace", async () => {
    setup(7);
    expect(await screen.findByText('eval-sweep')).toBeInTheDocument();
    await waitFor(() => expect(getGenericTasks).toHaveBeenCalled());
    const [params] = vi.mocked(getGenericTasks).mock.calls[0];
    expect(params).toMatchObject({ workspaceId: 7 });
    expect(params.userIds).toBeUndefined();
  });

  it("lists the current user's tasks of all workspaces", async () => {
    setup();
    expect(await screen.findByText('eval-sweep')).toBeInTheDocument();
    const [params] = vi.mocked(getGenericTasks).mock.calls[0];
    expect(params.workspaceId).toBeUndefined();
    expect(params.userIds).toStrictEqual([CURRENT_USER_ID]);
  });
});
