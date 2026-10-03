import React from 'react';

import NtscLaunchModalComponent from 'components/NtscLaunchModal';
import { CommandResponse, CommandTask, CommandType, Workspace } from 'types';

interface Props {
  /** "Launch Again": start from this shell's config. */
  initialTask?: CommandTask;
  /** Called with the new shell (without its SSH key) after a successful launch. */
  onLaunched?: (response: CommandResponse) => void;
  workspace?: Workspace;
}

const ShellModalComponent: React.FC<Props> = ({ initialTask, onLaunched, workspace }: Props) => (
  <NtscLaunchModalComponent
    initialTask={initialTask}
    type={CommandType.Shell}
    workspace={workspace}
    onLaunched={onLaunched}
  />
);

export default ShellModalComponent;
