import { describe, expect, it } from 'vitest';

import { buildGrafanaTaskResourcesUrl } from './grafanaTaskResources';

describe('Grafana task resources URL', () => {
  const config = {
    dashboard_url: 'https://grafana.example/monitoring/d/det-task-resources/resources?orgId=2',
    det_cluster: 'lab & west',
  };
  const target = { startTime: '2026-09-27T01:02:03.000Z', taskId: 'task/a & b' };

  it('preserves the Grafana subpath and encodes the dashboard selectors in milliseconds', () => {
    const raw = buildGrafanaTaskResourcesUrl(config, {
      ...target,
      allocationId: 'alloc/a & b',
      endTime: '2026-09-27T02:02:03.000Z',
    });
    expect(raw).toBeDefined();
    const url = new URL(raw ?? '');
    expect(url.pathname).toBe('/monitoring/d/det-task-resources/resources');
    expect(url.searchParams.get('orgId')).toBe('2');
    expect(url.searchParams.get('var-cluster')).toBe('lab & west');
    expect(url.searchParams.get('var-task_id')).toBe('task/a & b');
    expect(url.searchParams.get('var-allocation_id')).toBe('alloc/a & b');
    expect(url.searchParams.get('from')).toBe(String(Date.parse(target.startTime)));
    expect(url.searchParams.get('to')).toBe(String(Date.parse('2026-09-27T02:02:03.000Z')));
  });

  it('uses all allocations and live end time when no allocation or end is selected', () => {
    const url = new URL(buildGrafanaTaskResourcesUrl(config, target) ?? '');
    expect(url.searchParams.get('var-allocation_id')).toBe('$__all');
    expect(url.searchParams.get('to')).toBe('now');
  });

  it('does not make a link for disabled, unsafe, or unbounded targets', () => {
    expect(buildGrafanaTaskResourcesUrl(undefined, target)).toBeUndefined();
    expect(
      buildGrafanaTaskResourcesUrl({ ...config, dashboard_url: 'javascript:alert(1)' }, target),
    ).toBeUndefined();
    expect(
      buildGrafanaTaskResourcesUrl(
        { ...config, dashboard_url: 'https://u:p@grafana.example/d/det-task-resources/x' },
        target,
      ),
    ).toBeUndefined();
    expect(buildGrafanaTaskResourcesUrl(config, { ...target, startTime: 'bad' })).toBeUndefined();
  });
});
