import Tooltip from 'hew/Tooltip';
import React from 'react';

import Badge, { BadgeType } from 'components/Badge';
import Link from 'components/Link';
import { paths } from 'routes/utils';
import { shortTaskId } from 'utils/task';

interface Props {
  taskId: string;
}

/* The short ID of a generic task, linking to its detail page. */
const GenericTaskIdLink: React.FC<Props> = ({ taskId }: Props) => (
  <Tooltip content={taskId} placement="topLeft">
    <Link path={paths.genericTaskDetails(taskId)}>
      <Badge type={BadgeType.Id}>{shortTaskId(taskId)}</Badge>
    </Link>
  </Tooltip>
);

export default GenericTaskIdLink;
