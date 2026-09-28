import React, { useMemo } from 'react';

import Section from 'components/Section';
import UPlotChart, { Options } from 'components/UPlot/UPlotChart';
import { glasbeyColor } from 'utils/color';
import { humanReadableBytes } from 'utils/string';
import {
  alignResourceSeries,
  ResourceMetric,
  ResourceRange,
  ResourceSeries,
  resourceSeriesName,
} from 'utils/taskResources';

import css from './TaskResourceChart.module.scss';

interface Props {
  metric: ResourceMetric;
  range: ResourceRange;
  series: ResourceSeries[];
}

const TaskResourceChart: React.FC<Props> = ({ metric, range, series }) => {
  const aligned = useMemo(() => alignResourceSeries(series, range), [series, range]);
  const hasData = series.some((item) =>
    item.samples.some(([, value]) => value != null && Number.isFinite(value)),
  );
  const options = useMemo<Options>(() => {
    const format = (value: number): string =>
      metric.unit === 'bytes'
        ? humanReadableBytes(value)
        : `${Number(value.toFixed(2))} ${metric.unit}`;
    return {
      axes: [{}, { size: 85, values: (_plot, values) => values.map(format) }],
      cursor: { drag: { x: true, y: false } },
      height: 260,
      legend: { live: true, show: true },
      scales: { x: { max: range.end, min: range.start, time: true } },
      series: [
        { label: 'Time', value: (_plot, value) => new Date(value * 1000).toLocaleString() },
        ...series.map((item, index) => ({
          label: resourceSeriesName(item),
          points: { show: false },
          spanGaps: false,
          stroke: glasbeyColor(index),
          value: (_plot: unknown, value: number | null) => (value == null ? 'N/A' : format(value)),
          width: 2,
        })),
      ],
    };
  }, [metric, range, series]);

  return (
    <Section bodyBorder title={metric.title}>
      {hasData ? (
        <div className={css.chart}>
          <UPlotChart data={aligned} options={options} />
        </div>
      ) : (
        <p>No attributed samples in this time range.</p>
      )}
    </Section>
  );
};

export default TaskResourceChart;
