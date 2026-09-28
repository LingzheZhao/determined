export interface GrafanaTaskResourcesConfig {
  dashboard_url: string;
  det_cluster: string;
}

export interface GrafanaTaskResourcesTarget {
  allocationId?: string;
  endTime?: string;
  startTime: string;
  taskId: string;
}

// Keep this URL contract aligned with the externally provisioned det-task-resources dashboard.
export const buildGrafanaTaskResourcesUrl = (
  config: GrafanaTaskResourcesConfig | undefined,
  target: GrafanaTaskResourcesTarget,
): string | undefined => {
  if (!config?.dashboard_url || !config.det_cluster || !target.taskId) return undefined;
  let url: URL;
  try {
    url = new URL(config.dashboard_url);
  } catch {
    return undefined;
  }
  if (
    !['http:', 'https:'].includes(url.protocol) ||
    !url.hostname ||
    url.username ||
    url.password ||
    !/\/d\/det-task-resources(?:\/|$)/.test(url.pathname)
  ) {
    return undefined;
  }

  const from = Date.parse(target.startTime);
  if (!Number.isFinite(from)) return undefined;
  url.searchParams.set('var-cluster', config.det_cluster);
  url.searchParams.set('var-task_id', target.taskId);
  url.searchParams.set('var-allocation_id', target.allocationId || '$__all');
  url.searchParams.set('from', String(from));
  if (target.endTime) {
    const to = Date.parse(target.endTime);
    if (!Number.isFinite(to)) return undefined;
    url.searchParams.set('to', String(to));
  } else {
    url.searchParams.set('to', 'now');
  }
  return url.toString();
};
