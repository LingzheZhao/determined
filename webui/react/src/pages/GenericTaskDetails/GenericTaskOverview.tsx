import React from 'react';

import Badge, { BadgeType } from 'components/Badge';
import GenericTaskIdLink from 'components/GenericTaskIdLink';
import GenericTaskStateBadge from 'components/GenericTaskStateBadge';
import Link from 'components/Link';
import Section from 'components/Section';
import ResponsiveTable from 'components/Table/ResponsiveTable';
import { relativeTimeRenderer } from 'components/Table/Table';
import { paths } from 'routes/utils';
import { GenericTask, TaskItem } from 'types';

type Allocation = TaskItem['allocations'][number];

interface Props {
  childTasks?: GenericTask[];
  task: TaskItem;
}

const timeRenderer = (time?: string): React.ReactNode =>
  time ? relativeTimeRenderer(new Date(time)) : null;

/* The allocations and child tasks of a generic task. */
const GenericTaskOverview: React.FC<Props> = ({ childTasks, task }: Props) => {
  return (
    <>
      <Section title="Allocations">
        <ResponsiveTable<Allocation>
          columns={[
            {
              dataIndex: 'allocationId',
              key: 'allocationId',
              render: (_, record) => <code>{record.allocationId}</code>,
              title: 'Allocation ID',
            },
            {
              dataIndex: 'state',
              key: 'state',
              render: (_, record) => <Badge state={record.state} type={BadgeType.State} />,
              title: 'State',
            },
            { align: 'right', dataIndex: 'slots', key: 'slots', title: 'Slots' },
            {
              dataIndex: 'startTime',
              key: 'startTime',
              render: (_, record) => timeRenderer(record.startTime),
              title: 'Started',
            },
            {
              dataIndex: 'endTime',
              key: 'endTime',
              render: (_, record) => timeRenderer(record.endTime),
              title: 'Ended',
            },
            {
              align: 'right',
              dataIndex: 'statusCode',
              key: 'statusCode',
              render: (_, record) => record.statusCode ?? null,
              title: 'Status Code',
            },
            { dataIndex: 'exitReason', key: 'exitReason', title: 'Exit Reason' },
          ]}
          dataSource={task.allocations}
          pagination={false}
          rowKey={(record) => record.allocationId ?? ''}
          size="small"
        />
      </Section>
      <Section title="Child Tasks">
        <ResponsiveTable<GenericTask>
          columns={[
            {
              dataIndex: 'taskId',
              key: 'taskId',
              render: (_, record) => <GenericTaskIdLink taskId={record.taskId} />,
              title: 'ID',
            },
            {
              dataIndex: 'name',
              key: 'name',
              render: (_, record) => (
                <Link path={paths.genericTaskDetails(record.taskId)}>{record.name}</Link>
              ),
              title: 'Name',
            },
            {
              dataIndex: 'state',
              key: 'state',
              render: (_, record) => <GenericTaskStateBadge state={record.state} />,
              title: 'State',
            },
            { dataIndex: 'username', key: 'username', title: 'Owner' },
            {
              dataIndex: 'startTime',
              key: 'startTime',
              render: (_, record) => timeRenderer(record.startTime),
              title: 'Started',
            },
          ]}
          dataSource={childTasks}
          loading={childTasks === undefined}
          rowKey="taskId"
          size="small"
        />
      </Section>
    </>
  );
};

export default GenericTaskOverview;
