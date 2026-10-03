import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import UIProvider, { DefaultTheme } from 'hew/Theme';
import { ConfirmationProvider } from 'hew/useConfirm';

import { killGenericTask, pauseGenericTask, unpauseGenericTask } from 'services/api';
import { GenericTaskState } from 'types';
import handleError from 'utils/error';

import GenericTaskActions, { GenericTaskActionTarget } from './GenericTaskActions';

const user = userEvent.setup();

vi.mock('services/api', () => ({
  killGenericTask: vi.fn(() => Promise.resolve()),
  pauseGenericTask: vi.fn(() => Promise.resolve()),
  unpauseGenericTask: vi.fn(() => Promise.resolve()),
}));

vi.mock('utils/error', async (importOriginal) => ({
  ...(await importOriginal<typeof import('utils/error')>()),
  default: vi.fn(),
}));

const TASK: GenericTaskActionTarget = {
  name: 'eval-sweep',
  noPause: false,
  state: GenericTaskState.Active,
  taskId: 'task-1',
};

const setup = (task: Partial<GenericTaskActionTarget> = {}, canControl = true) => {
  const onComplete = vi.fn();
  render(
    <UIProvider theme={DefaultTheme.Light}>
      <ConfirmationProvider>
        <GenericTaskActions
          canControl={canControl}
          task={{ ...TASK, ...task }}
          onComplete={onComplete}
        />
      </ConfirmationProvider>
    </UIProvider>,
  );
  return { onComplete };
};

describe('GenericTaskActions', () => {
  afterEach(() => vi.clearAllMocks());

  it('enables pause for an active pausable task', () => {
    setup();
    expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'Unpause' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Kill' })).toBeEnabled();
  });

  it('disables pause for a task that is not pausable', () => {
    setup({ noPause: true });
    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled();
  });

  it('enables only unpause and kill for a paused task', () => {
    setup({ state: GenericTaskState.Paused });
    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Unpause' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'Kill' })).toBeEnabled();
  });

  it('disables every action without permission', () => {
    setup({ parentId: 'root-1' }, false);
    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Kill' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Kill Tree' })).toBeDisabled();
  });

  it('offers kill tree only for a child task', () => {
    setup();
    expect(screen.queryByRole('button', { name: 'Kill Tree' })).not.toBeInTheDocument();
  });

  it('pauses after confirmation', async () => {
    const { onComplete } = setup();
    await user.click(screen.getByRole('button', { name: 'Pause' }));
    expect(pauseGenericTask).not.toHaveBeenCalled();
    // The second button is the confirmation modal's.
    const buttons = await screen.findAllByRole('button', { name: 'Pause' });
    await user.click(buttons[buttons.length - 1]);
    await waitFor(() => expect(pauseGenericTask).toHaveBeenCalledWith({ taskId: 'task-1' }));
    await waitFor(() => expect(onComplete).toHaveBeenCalled());
  });

  it('kills the tree from the root after confirmation', async () => {
    setup({ parentId: 'root-1' });
    await user.click(screen.getByRole('button', { name: 'Kill Tree' }));
    const buttons = await screen.findAllByRole('button', { name: 'Kill Tree' });
    await user.click(buttons[buttons.length - 1]);
    await waitFor(() =>
      expect(killGenericTask).toHaveBeenCalledWith({ killFromRoot: true, taskId: 'task-1' }),
    );
  });

  it("reports the master's reason when an action is refused", async () => {
    const refusal = Object.assign(new Error('Request unpauseGenericTask failed.'), {
      publicMessage: 'cannot unpause task task-1 as it is not in paused state',
    });
    vi.mocked(unpauseGenericTask).mockRejectedValueOnce(refusal);
    setup({ state: GenericTaskState.Paused });
    await user.click(screen.getByRole('button', { name: 'Unpause' }));
    const buttons = await screen.findAllByRole('button', { name: 'Unpause' });
    await user.click(buttons[buttons.length - 1]);
    await waitFor(() => expect(handleError).toHaveBeenCalled());
    const [error, options] = vi.mocked(handleError).mock.calls[0];
    expect(error).toBe(refusal);
    expect(options).toMatchObject({ publicSubject: 'Unable to unpause task', silent: false });
    expect(options).not.toHaveProperty('publicMessage');
  });
});
