import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { DefaultTheme, UIProvider } from 'hew/Theme';

import { getExperimentDetails, getExpTrials } from 'services/api';

import ExperimentResources from './ExperimentResources';

const { navigate, params } = vi.hoisted(() => ({
  navigate: vi.fn(),
  params: { experimentId: '42' },
}));

vi.mock('react-router-dom', async (importOriginal) => ({
  ...(await importOriginal<typeof import('react-router-dom')>()),
  useNavigate: () => navigate,
  useParams: () => params,
}));
vi.mock('hooks/useTaskResourcesEnabled', () => ({ default: () => true }));
vi.mock('hooks/useFeature', () => ({ default: () => ({ isOn: () => false }) }));
vi.mock('components/Page', () => ({
  default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));
vi.mock('components/Link', () => ({
  default: ({ children, path }: { children: React.ReactNode; path: string }) => (
    <a href={path}>{children}</a>
  ),
}));
vi.mock('services/api', () => ({ getExperimentDetails: vi.fn(), getExpTrials: vi.fn() }));

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(getExperimentDetails).mockResolvedValue({} as never);
});

const renderPage = () =>
  render(
    <UIProvider theme={DefaultTheme.Light}>
      <ExperimentResources />
    </UIProvider>,
  );

it('lists every trial separately and links its task, never the experiment ID', async () => {
  vi.mocked(getExpTrials).mockResolvedValue({
    pagination: { limit: 25, offset: 0, total: 2 },
    trials: [
      { id: 7, state: 'RUNNING', taskId: '101.abc' },
      { id: 8, state: 'COMPLETED', taskId: '102.def' },
    ],
  } as never);
  renderPage();
  const links = await screen.findAllByRole('link', { name: 'View Resources' });
  expect(links[0]).toHaveAttribute('href', '/tasks/101.abc/resources');
  expect(links[1]).toHaveAttribute('href', '/tasks/102.def/resources');
  expect(screen.getByText(/Trial 7 · RUNNING/)).toBeInTheDocument();
  expect(screen.getByText(/Trial 8 · COMPLETED/)).toBeInTheDocument();
  expect(navigate).not.toHaveBeenCalled();
  expect(getExpTrials).toHaveBeenCalledWith(
    expect.objectContaining({ id: 42, limit: 25, offset: 0 }),
    expect.anything(),
  );
});

it('opens the only trial using its task ID', async () => {
  vi.mocked(getExpTrials).mockResolvedValue({
    pagination: { limit: 25, offset: 0, total: 1 },
    trials: [{ id: 7, state: 'RUNNING', taskId: '101.abc' }],
  } as never);
  renderPage();
  await waitFor(() =>
    expect(navigate).toHaveBeenCalledWith('/tasks/101.abc/resources', { replace: true }),
  );
});

it('does not list trials when experiment access fails', async () => {
  vi.mocked(getExperimentDetails).mockRejectedValue(new Error('forbidden'));
  renderPage();
  expect(await screen.findByText(/do not have permission/)).toBeInTheDocument();
  expect(getExpTrials).not.toHaveBeenCalled();
});

it('pages through multiple trials and leaves trials without a task unlinked', async () => {
  vi.mocked(getExpTrials)
    .mockResolvedValueOnce({
      pagination: { limit: 25, offset: 0, total: 26 },
      trials: [{ id: 7, state: 'QUEUED', taskId: undefined }],
    } as never)
    .mockResolvedValueOnce({
      pagination: { limit: 25, offset: 25, total: 26 },
      trials: [{ id: 8, state: 'RUNNING', taskId: 'task-eight' }],
    } as never);
  renderPage();
  expect(await screen.findByText(/Trial 7 · QUEUED/)).toBeInTheDocument();
  expect(screen.queryByRole('link', { name: 'View Resources' })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole('button', { name: 'Next' }));
  expect(await screen.findByRole('link', { name: 'View Resources' })).toHaveAttribute(
    'href',
    '/tasks/task-eight/resources',
  );
  expect(getExpTrials).toHaveBeenLastCalledWith(
    expect.objectContaining({ id: 42, offset: 25 }),
    expect.anything(),
  );
});
