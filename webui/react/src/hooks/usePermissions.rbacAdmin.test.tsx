import { renderHook, screen } from '@testing-library/react';

import { V1PermissionType } from 'services/api-ts-sdk/api';

import { setup, usePermissionsHook } from './usePermissions.common';

vi.mock('stores/determinedInfo', async (importOriginal) => {
  const observable = await import('utils/observable');
  const store = {
    info: observable.observable({
      rbacEnabled: true,
    }),
  };
  return {
    ...(await importOriginal<typeof import('stores/determinedInfo')>()),
    default: store,
  };
});

vi.mock('stores/permissions', async (importOriginal) => {
  const loadable = await import('hew/utils/loadable');
  const observable = await import('utils/observable');
  const assigned = observable.observable(
    loadable.Loaded([
      {
        roleId: 1,
        scopeCluster: true,
      },
    ]),
  );
  const roles = observable.observable(
    loadable.Loaded([
      {
        id: 1,
        name: 'TestClusterAdmin',
        permissions: [
          {
            id: V1PermissionType.CREATEWORKSPACE,
          },
          {
            id: V1PermissionType.CREATEPROJECT,
          },
          {
            id: V1PermissionType.DELETEWORKSPACE,
          },
          {
            id: V1PermissionType.UPDATEWORKSPACE,
          },
          {
            id: V1PermissionType.VIEWWORKSPACE,
          },
          { id: V1PermissionType.UPDATENSC },
          { id: V1PermissionType.UPDATEEXPERIMENT },
          { id: V1PermissionType.UPDATEEXPERIMENTMETADATA },
        ],
      },
    ]),
  );
  return {
    ...(await importOriginal<typeof import('stores/permissions')>()),
    default: {
      myAssignments: assigned,
      myRoles: roles,
      permissions: observable.observable([assigned, roles]),
    },
  };
});

describe('usePermissions for RBAC admin user', () => {
  it('should have create/read/update/delete permissions', async () => {
    await setup();

    // sample create / read / update / delete permissions all available
    expect(screen.queryByText('canCreateWorkspace')).toBeInTheDocument();
    expect(screen.queryByText('canCreateProject')).toBeInTheDocument();
    expect(screen.queryByText('canModifyWorkspace')).toBeInTheDocument();
    expect(screen.queryByText('canDeleteWorkspace')).toBeInTheDocument();
    expect(screen.queryByText('canViewWorkspace')).toBeInTheDocument();
  });

  it('uses workspace permissions for controls with RBAC enabled', () => {
    const { result } = renderHook(() => usePermissionsHook());
    for (const check of [
      result.current.canModifyWorkspaceNSC,
      result.current.canModifyExperiment,
      result.current.canModifyExperimentMetadata,
      result.current.canModifyFlatRun,
    ]) {
      expect(check({ userId: 102, workspace: { id: 10 } })).toBe(true);
    }
  });
});
