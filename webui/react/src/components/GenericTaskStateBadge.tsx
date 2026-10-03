import React from 'react';

import Badge, { BadgeType } from 'components/Badge';
import { genericTaskStateToLabel, genericTaskStateToRunState } from 'constants/states';
import { GenericTaskState } from 'types';

interface Props {
  state: GenericTaskState;
}

/* A state badge for a generic task, colored like the closest experiment run state. */
const GenericTaskStateBadge: React.FC<Props> = ({ state }: Props) => (
  <Badge state={genericTaskStateToRunState[state]} type={BadgeType.State}>
    {genericTaskStateToLabel[state]}
  </Badge>
);

export default GenericTaskStateBadge;
