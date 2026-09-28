import { useEffect, useState } from 'react';

import { globalStorage } from 'globalStorage';
import { serverAddress } from 'routes/utils';
import { GrafanaTaskResourcesConfig } from 'utils/grafanaTaskResources';

const useGrafanaTaskResources = (): GrafanaTaskResourcesConfig | undefined => {
  const [config, setConfig] = useState<GrafanaTaskResourcesConfig>();

  useEffect(() => {
    const canceler = new AbortController();
    fetch(serverAddress('/ui/grafana-task-resources'), {
      credentials: 'include',
      headers: globalStorage.authToken
        ? { Authorization: `Bearer ${globalStorage.authToken}` }
        : undefined,
      signal: canceler.signal,
    })
      .then((response) => (response.ok ? response.json() : undefined))
      .then((data: GrafanaTaskResourcesConfig | undefined) => {
        if (!canceler.signal.aborted && data?.dashboard_url && data.det_cluster) setConfig(data);
      })
      .catch(() => {
        // An unavailable optional integration does not affect task views.
      });
    return () => canceler.abort();
  }, []);

  return config;
};

export default useGrafanaTaskResources;
