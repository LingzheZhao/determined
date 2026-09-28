import { describe, expect, it } from 'vitest';

import { alignResourceSeries, resourceRange, ResourceSeries } from './taskResources';

describe('task resource sampling', () => {
  it('keeps missing samples and non-finite values as gaps, while preserving true zero', () => {
    const series: ResourceSeries[] = [
      {
        labels: { allocation_id: 'retry.1' },
        metric: 'cpu_cores',
        samples: [
          [100, 0],
          [130, 2],
          [145, null],
          [160, NaN],
          [175, Infinity],
        ],
      },
    ];
    expect(alignResourceSeries(series, { end: 175, start: 100, step: 15 })).toEqual([
      [100, 115, 130, 145, 160, 175],
      [0, null, 2, null, null, null],
    ]);
  });

  it('does not connect separate allocation lifetimes', () => {
    const series: ResourceSeries[] = [
      { labels: { allocation_id: 'run.1' }, metric: 'cpu_cores', samples: [[100, 1]] },
      { labels: { allocation_id: 'run.2' }, metric: 'cpu_cores', samples: [[130, 3]] },
    ];
    expect(alignResourceSeries(series, { end: 130, start: 100, step: 15 })).toEqual([
      [100, 115, 130],
      [1, null, null],
      [null, null, 3],
    ]);
  });

  it('keeps seven-day requests within the server point budget including both endpoints', () => {
    const range = resourceRange(100, 100 + 7 * 86400);
    expect(Math.floor((range.end - range.start) / range.step) + 1).toBeLessThanOrEqual(1440);
    expect(resourceRange(100, 101).step).toBe(15);
  });
});
