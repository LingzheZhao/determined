import Button from 'hew/Button';
import { useModal } from 'hew/Modal';
import Tooltip from 'hew/Tooltip';
import React, { useCallback, useState } from 'react';

import ShellLaunchedModalComponent from 'components/ShellLaunchedModal';
import ShellModalComponent from 'components/ShellModal';
import { CommandResponse, Workspace } from 'types';

interface Props {
  enabled?: boolean;
  workspace?: Workspace;
}

const ShellButton: React.FC<Props> = ({ enabled, workspace }: Props) => {
  const ShellModal = useModal(ShellModalComponent);
  const ShellLaunchedModal = useModal(ShellLaunchedModalComponent);
  const [launched, setLaunched] = useState<CommandResponse>();

  const openShellLaunched = ShellLaunchedModal.open;

  const handleLaunched = useCallback(
    (response: CommandResponse) => {
      setLaunched(response);
      openShellLaunched();
    },
    [openShellLaunched],
  );

  return (
    <div data-testid="shell-button">
      {enabled ? (
        <>
          <Button onClick={ShellModal.open}>Launch Shell</Button>
          <ShellModal.Component workspace={workspace} onLaunched={handleLaunched} />
          {launched && <ShellLaunchedModal.Component response={launched} />}
        </>
      ) : (
        <Tooltip content="You do not have permission to launch a shell" placement="leftBottom">
          <div>
            <Button disabled>Launch Shell</Button>
          </div>
        </Tooltip>
      )}
    </div>
  );
};

export default ShellButton;
