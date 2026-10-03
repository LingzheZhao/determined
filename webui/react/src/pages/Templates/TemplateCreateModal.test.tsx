import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Button from 'hew/Button';
import { useModal } from 'hew/Modal';
import UIProvider, { DefaultTheme } from 'hew/Theme';
import React from 'react';

import TemplateCreateModalComponent from './TemplateCreateModal';

const mocks = vi.hoisted(() => ({
  createTaskTemplate: vi.fn(),
  updateTaskTemplate: vi.fn(),
}));

vi.mock('services/api', () => ({
  createTaskTemplate: mocks.createTaskTemplate,
  getWorkspaces: () => Promise.resolve({ workspaces: [] }),
  updateTaskTemplate: mocks.updateTaskTemplate,
  updateTaskTemplateName: vi.fn(),
}));

vi.mock('hew/CodeEditor', () => ({
  __esModule: true,
  default: ({ file }: { file: string }) => <pre data-testid="code-editor">{file}</pre>,
}));

const CONFIG = `environment:
  environment_variables:
    - HF_TOKEN=abc
    - LANG=C.UTF-8
resources:
  resource_pool: gpu
  slots: 2
`;

const Trigger: React.FC<{ initialConfig?: string }> = ({ initialConfig }) => {
  const Modal = useModal(TemplateCreateModalComponent);
  return (
    <>
      <Button onClick={Modal.open}>Open</Button>
      <Modal.Component initialConfig={initialConfig} initialWorkspaceId={4} />
    </>
  );
};

const setup = async (initialConfig?: string) => {
  const user = userEvent.setup();
  render(
    <UIProvider theme={DefaultTheme.Light}>
      <Trigger initialConfig={initialConfig} />
    </UIProvider>,
  );
  await user.click(screen.getByRole('button', { name: 'Open' }));
  await screen.findByText('New Template');
  return user;
};

describe('TemplateCreateModal with a starting config', () => {
  beforeEach(() => {
    mocks.createTaskTemplate.mockReset().mockResolvedValue({});
    mocks.updateTaskTemplate.mockReset();
  });

  it('shows the config, warns about credential-like variables and creates a new template', async () => {
    const user = await setup(CONFIG);

    expect(screen.getByTestId('code-editor')).toHaveTextContent('resource_pool: gpu');
    expect(screen.getByText(/Other users can read templates/)).toBeInTheDocument();
    expect(screen.getByText(/look like credentials: HF_TOKEN\./)).toBeInTheDocument();

    await user.type(screen.getByLabelText('Name'), 'gpu-shell');
    await user.click(screen.getByRole('button', { name: 'Create Template' }));

    await waitFor(() =>
      expect(mocks.createTaskTemplate).toHaveBeenCalledWith({
        config: {
          environment: { environment_variables: ['HF_TOKEN=abc', 'LANG=C.UTF-8'] },
          resources: { resource_pool: 'gpu', slots: 2 },
        },
        name: 'gpu-shell',
        workspaceId: 4,
      }),
    );
    expect(mocks.updateTaskTemplate).not.toHaveBeenCalled();
  });

  it('keeps the workspace selectable', async () => {
    await setup(CONFIG);
    expect(screen.getByLabelText('Workspace')).not.toBeDisabled();
  });

  it('shows no review notice without a starting config', async () => {
    await setup();
    expect(screen.queryByText(/Other users can read templates/)).not.toBeInTheDocument();
  });
});
