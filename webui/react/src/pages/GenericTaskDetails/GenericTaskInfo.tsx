import Card from 'hew/Card';
import React from 'react';

import GenericTaskIdLink from 'components/GenericTaskIdLink';
import OverviewStats from 'components/OverviewStats';
import Section from 'components/Section';
import TimeAgo from 'components/TimeAgo';
import { GenericTask } from 'types';

import css from './GenericTaskInfo.module.scss';

interface Props {
  childCount?: number;
  endTime?: string;
  forkedFrom?: string;
  noPause?: boolean;
  parentId?: string;
  startTime?: string;
  summary?: GenericTask;
  taskId: string;
}

/* The summary of a generic task shown above its tabs. */
const GenericTaskInfo: React.FC<Props> = ({
  childCount,
  endTime,
  forkedFrom,
  noPause,
  parentId,
  startTime,
  summary,
  taskId,
}: Props) => {
  return (
    <Section>
      <div className={css.base}>
        <div className={css.id}>
          <span>ID</span>
          <code data-testid="generic-task-id">{taskId}</code>
        </div>
        {summary?.description && (
          <p className={css.description} data-testid="generic-task-description">
            {summary.description}
          </p>
        )}
        <Card.Group size="small">
          <OverviewStats title="Owner">{summary?.username ?? '-'}</OverviewStats>
          <OverviewStats title="Slots">{summary?.slots ?? '-'}</OverviewStats>
          <OverviewStats title="Resource Pool">{summary?.resourcePool || '-'}</OverviewStats>
          <OverviewStats title="Pausable">
            {noPause === undefined ? '-' : noPause ? 'No' : 'Yes'}
          </OverviewStats>
          <OverviewStats title="Parent">
            {parentId ? <GenericTaskIdLink taskId={parentId} /> : 'None (root task)'}
          </OverviewStats>
          {forkedFrom && (
            <OverviewStats title="Forked From">
              <GenericTaskIdLink taskId={forkedFrom} />
            </OverviewStats>
          )}
          <OverviewStats title="Children">{childCount ?? '-'}</OverviewStats>
          {startTime && (
            <OverviewStats title="Started">
              <TimeAgo datetime={startTime} />
            </OverviewStats>
          )}
          {endTime && (
            <OverviewStats title="Ended">
              <TimeAgo datetime={endTime} />
            </OverviewStats>
          )}
        </Card.Group>
      </div>
    </Section>
  );
};

export default GenericTaskInfo;
