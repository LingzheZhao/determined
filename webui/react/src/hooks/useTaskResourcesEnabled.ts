import { useEffect, useState } from 'react';

import { globalStorage } from 'globalStorage';
import { serverAddress } from 'routes/utils';

export const taskResourcesHeaders = (): HeadersInit =>
  globalStorage.authToken ? { Authorization: `Bearer ${globalStorage.authToken}` } : {};

// Row menus mount together. Share their in-flight capability request without
// retaining an authorization-dependent result across later mounts or sign-ins.
const pendingCapabilities = new Map<string, Promise<boolean>>();
const loadCapability = (): Promise<boolean> => {
  const url = serverAddress('/ui/task-resources');
  const key = JSON.stringify([url, globalStorage.authToken]);
  const pending = pendingCapabilities.get(key);
  if (pending) return pending;
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), 10000);
  const request = fetch(url, {
    credentials: 'include',
    headers: taskResourcesHeaders(),
    signal: controller.signal,
  })
    .then(async (response) => (response.ok ? (await response.json()).enabled === true : false))
    .catch(() => false)
    .finally(() => {
      window.clearTimeout(timeout);
      pendingCapabilities.delete(key);
    });
  pendingCapabilities.set(key, request);
  return request;
};

const useTaskResourcesEnabled = (): boolean | undefined => {
  const [enabled, setEnabled] = useState<boolean>();
  useEffect(() => {
    let active = true;
    loadCapability().then((value) => {
      if (active) setEnabled(value);
    });
    return () => {
      active = false;
    };
  }, []);
  return enabled;
};

export default useTaskResourcesEnabled;
