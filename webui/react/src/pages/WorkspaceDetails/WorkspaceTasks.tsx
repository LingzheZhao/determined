import Pivot, { PivotProps } from 'hew/Pivot';
import React, { useMemo } from 'react';

import GenericTaskList from 'components/GenericTaskList';
import TaskList from 'components/TaskList';
import { Workspace } from 'types';

interface Props {
  workspace: Workspace;
}

/*
 * The tasks tab of a workspace: its notebooks, shells, commands and TensorBoards, and its generic
 * tasks, as sub-tabs like those of the workspace's config policies.
 */
const WorkspaceTasks: React.FC<Props> = ({ workspace }: Props) => {
  const tabItems: PivotProps['items'] = useMemo(
    () => [
      {
        children: <TaskList workspace={workspace} />,
        key: 'interactive',
        label: 'Notebooks & Commands',
      },
      {
        children: <GenericTaskList workspaceId={workspace.id} />,
        key: 'generic',
        label: 'Generic Tasks',
      },
    ],
    [workspace],
  );

  return <Pivot destroyInactiveTabPane items={tabItems} type="secondary" />;
};

export default WorkspaceTasks;
