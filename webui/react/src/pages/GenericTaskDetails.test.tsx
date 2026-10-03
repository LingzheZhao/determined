import { act, render, screen, waitFor } from '@testing-library/react';
import { DefaultTheme, UIProvider } from 'hew/Theme';
import { ConfirmationProvider } from 'hew/useConfirm';
import { HelmetProvider } from 'react-helmet-async';
import { BrowserRouter } from 'react-router-dom';

import { ThemeProvider } from 'components/ThemeProvider';
import { getGenericTask, getGenericTasks, getTask } from 'services/api';
import { V1TaskType } from 'services/api-ts-sdk';
import { CommandState, GenericTask, GenericTaskState, TaskItem } from 'types';

import GenericTaskDetails from './GenericTaskDetails';

const TASK_ID = 'task-1';

vi.mock('react-router-dom', async (importOriginal) => ({
  ...(await importOriginal<typeof import('react-router-dom')>()),
  useParams: () => ({ taskId: TASK_ID }),
}));

vi.mock('hooks/useTaskResourcesEnabled', () => ({ default: () => false }));

vi.mock('services/api', () => ({
  getGenericTask: vi.fn(),
  getGenericTaskConfig: vi.fn(() => Promise.resolve({ name: 'eval-sweep' })),
  getGenericTasks: vi.fn(() =>
    Promise.resolve({ pagination: { limit: 0, offset: 0, total: 0 }, tasks: [] }),
  ),
  getTask: vi.fn(),
}));

const taskItem = (taskState: GenericTaskState): TaskItem => ({
  allocations: [{ isReady: false, state: CommandState.Terminated }],
  noPause: false,
  startTime: '2026-01-01T00:00:00Z',
  taskId: TASK_ID,
  taskState,
  taskType: V1TaskType.GENERIC,
});

const summary: GenericTask = {
  description: '',
  jobId: 'job-1',
  name: 'eval-sweep',
  noPause: false,
  projectId: 1,
  resourcePool: 'default',
  slots: 1,
  startTime: '2026-01-01T00:00:00Z',
  state: GenericTaskState.Active,
  taskId: TASK_ID,
  userId: 1,
  username: 'alice',
  workspaceId: 1,
};

const setup = () =>
  render(
    <BrowserRouter>
      <UIProvider theme={DefaultTheme.Light}>
        <ThemeProvider>
          <HelmetProvider>
            <ConfirmationProvider>
              <GenericTaskDetails />
            </ConfirmationProvider>
          </HelmetProvider>
        </ThemeProvider>
      </UIProvider>
    </BrowserRouter>,
  );

const pollTwice = async () => {
  for (let i = 0; i < 2; i++) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
  }
};

describe('GenericTaskDetails', () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.mocked(getGenericTask).mockResolvedValue(summary);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  it('looks the task up by its ID once', async () => {
    vi.mocked(getTask).mockResolvedValue(taskItem(GenericTaskState.Active));
    setup();
    const name = await screen.findByTestId('generic-task-name');
    await waitFor(() => expect(name).toHaveTextContent('eval-sweep'));
    expect(getGenericTask).toHaveBeenCalledTimes(1);
    expect(vi.mocked(getGenericTask).mock.calls[0][0]).toBe(TASK_ID);
    expect(getGenericTasks).toHaveBeenCalledWith(
      { limit: 0, parentId: TASK_ID },
      expect.anything(),
    );
  });

  it('keeps polling an active task', async () => {
    vi.mocked(getTask).mockResolvedValue(taskItem(GenericTaskState.Active));
    setup();
    await screen.findByTestId('generic-task-name');
    await pollTwice();
    expect(vi.mocked(getTask).mock.calls.length).toBeGreaterThanOrEqual(3);
  });

  it('stops polling once the task is completed', async () => {
    vi.mocked(getTask).mockResolvedValue(taskItem(GenericTaskState.Completed));
    setup();
    await screen.findByTestId('generic-task-name');
    await pollTwice();
    expect(getTask).toHaveBeenCalledTimes(1);
  });
});
