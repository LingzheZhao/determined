import { render, RenderResult } from '@testing-library/react';
import UIProvider, { DefaultTheme } from 'hew/Theme';
import React from 'react';

import usePermissions from 'hooks/usePermissions';
import { ActionWorkspaceParams } from 'services/types';
import userStore from 'stores/users';

export const workspace = {
  id: 10,
  name: 'Test Workspace',
};

export const usePermissionsHook = usePermissions;
export const testUserStore = userStore;

vi.mock('services/api', () => ({
  getWorkspace: (params: ActionWorkspaceParams) => {
    return {
      ...workspace,
      id: params.workspaceId,
    };
  },
}));

vi.mock('stores/users', async (importOriginal) => {
  const loadable = await import('hew/utils/loadable');
  const observable = await import('utils/observable');
  const store = {
    currentUser: observable.observable(
      loadable.Loaded({
        id: 101,
        isAdmin: false,
      }),
    ),
  };
  return {
    ...(await importOriginal<typeof import('stores/users')>()),
    default: store,
  };
});

interface Props {
  workspaceId: number;
}

const PermissionRenderer: React.FC<Props> = () => {
  const {
    canCreateProject,
    canCreateWorkspace,
    canDeleteWorkspace,
    canModifyWorkspace,
    canViewWorkspace,
  } = usePermissions();

  return (
    <ul>
      <li>{canCreateProject({ workspace }) && 'canCreateProject'}</li>
      <li>{canCreateWorkspace && 'canCreateWorkspace'}</li>
      <li>{canDeleteWorkspace({ workspace }) && 'canDeleteWorkspace'}</li>
      <li>{canModifyWorkspace({ workspace }) && 'canModifyWorkspace'}</li>
      <li>{canViewWorkspace({ workspace }) && 'canViewWorkspace'}</li>
    </ul>
  );
};

export const setup = async (): Promise<RenderResult> => {
  return await render(
    <UIProvider theme={DefaultTheme.Light}>
      <PermissionRenderer workspaceId={1} />
    </UIProvider>,
  );
};
