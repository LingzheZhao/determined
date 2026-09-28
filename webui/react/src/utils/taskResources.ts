import { AlignedData } from 'uplot';

export const RESOURCE_METRICS = [
  { key: 'cpu_cores', title: 'CPU', unit: 'cores' },
  { key: 'memory_working_set_bytes', title: 'Memory working set', unit: 'bytes' },
  { key: 'memory_rss_bytes', title: 'Memory RSS', unit: 'bytes' },
  { key: 'gpu_utilization_percent', title: 'Assigned GPU utilization', unit: '%' },
  { key: 'gpu_memory_used_bytes', title: 'Assigned GPU memory', unit: 'bytes' },
  { key: 'gpu_power_watts', title: 'Assigned GPU power', unit: 'W' },
  { key: 'gpu_temperature_celsius', title: 'Assigned GPU temperature', unit: '°C' },
  { key: 'allocation_active', title: 'Allocation lifecycle', unit: 'active' },
] as const;

export type ResourceMetric = (typeof RESOURCE_METRICS)[number];
export interface ResourceSeries {
  metric: ResourceMetric['key'];
  labels: { allocation_id?: string; node?: string; gpu_uuid?: string };
  samples: [number, number | null][];
}
export interface TaskResourcesResponse {
  enabled: boolean;
  series: ResourceSeries[];
  warnings: { code: string; message: string }[];
}
export interface ResourceRange {
  start: number;
  end: number;
  step: number;
}

export const resourceRange = (start: number, end: number): ResourceRange => ({
  end: Math.floor(end),
  start: Math.floor(start),
  // Include both endpoints within the backend's 1,440-point limit.
  step: Math.max(15, Math.ceil((end - start) / 1439)),
});

// A full sampling grid preserves missing scrapes as nulls. Never interpolate
// absent samples or convert them to zero, including between allocation runs.
export const alignResourceSeries = (
  series: ResourceSeries[],
  range: ResourceRange,
): AlignedData => {
  const timestamps: number[] = [];
  for (let t = range.start; t <= range.end; t += range.step) timestamps.push(t);
  return [
    timestamps,
    ...series.map((item) => {
      const samples = new Map(item.samples);
      return timestamps.map((t) => {
        const value = samples.get(t);
        return value != null && Number.isFinite(value) ? value : null;
      });
    }),
  ];
};

export const resourceSeriesName = ({ labels }: ResourceSeries): string =>
  [labels.allocation_id, labels.node, labels.gpu_uuid].filter(Boolean).join(' · ') || 'Task';
