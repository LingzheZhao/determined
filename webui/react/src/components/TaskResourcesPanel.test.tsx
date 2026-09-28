import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { DefaultTheme, UIProvider } from 'hew/Theme';

import TaskResourcesPanel from './TaskResourcesPanel';

vi.mock('hooks/useTaskResourcesEnabled', () => ({
  default: () => true,
  taskResourcesHeaders: () => ({}),
}));
vi.mock('components/TaskResourceChart', () => ({
  default: ({ series }: { series: { labels: { allocation_id: string } }[] }) => (
    <div>{series.map((item) => item.labels.allocation_id).join(',')}</div>
  ),
}));

const response = (allocation: string) => ({
  json: () =>
    Promise.resolve({
      enabled: true,
      series: [{ labels: { allocation_id: allocation }, metric: 'cpu_cores', samples: [[100, 1]] }],
      warnings: [],
    }),
  ok: true,
});
const page = (taskId: string) => (
  <UIProvider theme={DefaultTheme.Light}>
    <TaskResourcesPanel startTime="2026-01-01T00:00:00Z" taskId={taskId} />
  </UIProvider>
);

afterEach(() => vi.unstubAllGlobals());

it('ignores a late response after changing tasks and aborts the previous request', async () => {
  let resolveOld!: (value: unknown) => void;
  const fetchMock = vi
    .fn()
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveOld = resolve;
        }),
    )
    .mockResolvedValueOnce(response('current-allocation'));
  vi.stubGlobal('fetch', fetchMock);
  const view = render(page('old-task'));
  view.rerender(page('new-task'));
  expect(fetchMock.mock.calls[0][1].signal.aborted).toBe(true);
  expect(await screen.findByText('current-allocation')).toBeInTheDocument();
  await act(async () => {
    resolveOld(response('old-allocation'));
    await Promise.resolve();
  });
  expect(screen.queryByText('old-allocation')).not.toBeInTheDocument();
});

it('labels retained charts when a refresh fails instead of presenting them as current data', async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(response('retained-allocation'))
    .mockResolvedValueOnce({ ok: false, status: 502 });
  vi.stubGlobal('fetch', fetchMock);
  render(page('task'));
  expect(await screen.findByText('retained-allocation')).toBeInTheDocument();
  await userEvent.click(screen.getByRole('button', { name: 'Refresh' }));
  expect(
    await screen.findByText('The charts below retain the last successful response.'),
  ).toBeInTheDocument();
  expect(screen.getByText('retained-allocation')).toBeInTheDocument();
});
