import { useEffect, useState } from 'react';

import { globalStorage } from 'globalStorage';
import { serverAddress } from 'routes/utils';

export const taskResourcesHeaders = (): HeadersInit =>
  globalStorage.authToken ? { Authorization: `Bearer ${globalStorage.authToken}` } : {};

const useTaskResourcesEnabled = (): boolean | undefined => {
  const [enabled, setEnabled] = useState<boolean>();
  useEffect(() => {
    const controller = new AbortController();
    fetch(serverAddress('/ui/task-resources'), {
      credentials: 'include',
      headers: taskResourcesHeaders(),
      signal: controller.signal,
    })
      .then(async (response) => (response.ok ? (await response.json()).enabled === true : false))
      .then((value) => {
        if (!controller.signal.aborted) setEnabled(value);
      })
      .catch(() => {
        if (!controller.signal.aborted) setEnabled(false);
      });
    return () => controller.abort();
  }, []);
  return enabled;
};

export default useTaskResourcesEnabled;
