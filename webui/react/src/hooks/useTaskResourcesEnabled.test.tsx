import { act, renderHook, waitFor } from '@testing-library/react';

import { globalStorage } from 'globalStorage';

import useTaskResourcesEnabled from './useTaskResourcesEnabled';

vi.mock('routes/utils', () => ({ serverAddress: (path: string) => `http://master${path}` }));

afterEach(() => {
  vi.unstubAllGlobals();
  globalStorage.removeAuthToken();
});

it('shares concurrent row requests while allowing one row to unmount independently', async () => {
  let resolve!: (value: unknown) => void;
  const fetchMock = vi.fn(
    () =>
      new Promise((done) => {
        resolve = done;
      }),
  );
  vi.stubGlobal('fetch', fetchMock);
  const first = renderHook(useTaskResourcesEnabled);
  const second = renderHook(useTaskResourcesEnabled);
  expect(fetchMock).toHaveBeenCalledTimes(1);
  first.unmount();
  await act(async () => {
    resolve({ json: () => Promise.resolve({ enabled: true }), ok: true });
    await Promise.resolve();
  });
  await waitFor(() => expect(second.result.current).toBe(true));
});

it('retries capability detection on later mounts after a failed request', async () => {
  const fetchMock = vi
    .fn()
    .mockRejectedValueOnce(new Error('offline'))
    .mockResolvedValueOnce({ json: () => Promise.resolve({ enabled: true }), ok: true });
  vi.stubGlobal('fetch', fetchMock);
  const first = renderHook(useTaskResourcesEnabled);
  await waitFor(() => expect(first.result.current).toBe(false));
  first.unmount();
  const second = renderHook(useTaskResourcesEnabled);
  await waitFor(() => expect(second.result.current).toBe(true));
  expect(fetchMock).toHaveBeenCalledTimes(2);
});

it('does not share requests across different authentication sessions', async () => {
  const resolvers: ((value: unknown) => void)[] = [];
  const fetchMock = vi.fn(
    () =>
      new Promise((resolve) => {
        resolvers.push(resolve);
      }),
  );
  vi.stubGlobal('fetch', fetchMock);
  globalStorage.authToken = 'test-session-one';
  const first = renderHook(useTaskResourcesEnabled);
  globalStorage.authToken = 'test-session-two';
  const second = renderHook(useTaskResourcesEnabled);
  expect(fetchMock).toHaveBeenCalledTimes(2);
  await act(async () => {
    resolvers[0]({ json: () => Promise.resolve({ enabled: false }), ok: true });
    resolvers[1]({ json: () => Promise.resolve({ enabled: true }), ok: true });
    await Promise.resolve();
  });
  await waitFor(() => expect(first.result.current).toBe(false));
  expect(second.result.current).toBe(true);
});
