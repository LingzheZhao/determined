import React from 'react';

import NtscLaunchModalComponent from 'components/NtscLaunchModal';
import { CommandTask, CommandType, Workspace } from 'types';

interface Props {
  /** "Launch Again": start from this JupyterLab's config. */
  initialTask?: CommandTask;
  workspace?: Workspace;
}

const JupyterLabModalComponent: React.FC<Props> = ({ initialTask, workspace }: Props) => (
  <NtscLaunchModalComponent
    initialTask={initialTask}
    type={CommandType.JupyterLab}
    workspace={workspace}
  />
);

export default JupyterLabModalComponent;
