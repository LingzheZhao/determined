import Alert from 'hew/Alert';
import CodeSample from 'hew/CodeSample';
import { Modal } from 'hew/Modal';
import Row from 'hew/Row';
import { Body, Label } from 'hew/Typography';
import React from 'react';

import Link from 'components/Link';
import { paths } from 'routes/utils';
import { V1LaunchWarning } from 'services/api-ts-sdk';
import { CommandResponse } from 'types';

interface Props {
  response: CommandResponse;
}

/**
 * Shown after a shell launch: how to connect and where to follow the task.
 * Shells cannot use the JupyterLab "open in a new tab" flow (utils/wait).
 */
const ShellLaunchedModalComponent: React.FC<Props> = ({ response }: Props) => {
  const { command, warnings = [] } = response;
  return (
    <Modal size="medium" title="Shell Launched">
      {warnings.includes(V1LaunchWarning.CURRENTSLOTSEXCEEDED) && (
        <Alert
          message="The shell asks for more slots than are free right now, so it may wait in the queue."
          showIcon
          type="warning"
        />
      )}
      <Body>
        {command.name} ({command.id}) is starting. You can connect once its state is Running.
      </Body>
      <Label>Start an interactive SSH session in the terminal:</Label>
      <CodeSample text={`det shell open ${command.id}`} />
      <Body inactive>A terminal in the browser is planned for a follow-up release.</Body>
      <Row>
        <Link path={paths.taskLogs(command)}>View Logs</Link>
        <Link path={paths.taskList()}>View Tasks</Link>
      </Row>
    </Modal>
  );
};

export default ShellLaunchedModalComponent;
