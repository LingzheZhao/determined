import { act, renderHook, screen } from '@testing-library/react';
import { Loadable, Loaded } from 'hew/utils/loadable';

import { DetailedUser } from 'types';
import { WritableObservable } from 'utils/observable';

import { setup, testUserStore, usePermissionsHook } from './usePermissions.common';

vi.mock('stores/determinedInfo', async (importOriginal) => {
  const observable = await import('utils/observable');
  const store = {
    info: observable.observable({
      rbacEnabled: false,
    }),
  };
  return {
    ...(await importOriginal<typeof import('stores/determinedInfo')>()),
    default: store,
  };
});

describe('usePermissions for OSS', () => {
  it('should have OSS permissions', async () => {
    await setup();

    // any user permission in OSS
    expect(screen.queryByText('canCreateWorkspace')).toBeInTheDocument();
    expect(screen.queryByText('canCreateProject')).toBeInTheDocument();
    expect(screen.queryByText('canViewWorkspace')).toBeInTheDocument();

    expect(screen.queryByText('canModifyWorkspace')).not.toBeInTheDocument();
    expect(screen.queryByText('canDeleteWorkspace')).not.toBeInTheDocument();
  });

  it('allows only the owner to control tasks and experiments', () => {
    const { result } = renderHook(() => usePermissionsHook());
    const workspace = { id: 10 };
    const checks = [
      result.current.canModifyWorkspaceNSC,
      result.current.canModifyExperiment,
      result.current.canModifyExperimentMetadata,
      result.current.canModifyFlatRun,
    ];

    for (const check of checks) {
      expect(check({ userId: 101, workspace })).toBe(true);
      expect(check({ userId: 102, workspace })).toBe(false);
      expect(check({ workspace })).toBe(false);
    }
  });

  it('allows admins to control items without a known owner', () => {
    const currentUser = testUserStore.currentUser as WritableObservable<Loadable<DetailedUser>>;
    const original = currentUser.get();
    const user = Loadable.getOrElse(undefined, original);
    expect(user).toBeDefined();
    act(() => currentUser.set(Loaded({ ...user!, isAdmin: true })));

    try {
      const { result } = renderHook(() => usePermissionsHook());
      for (const check of [
        result.current.canModifyWorkspaceNSC,
        result.current.canModifyExperiment,
        result.current.canModifyExperimentMetadata,
        result.current.canModifyFlatRun,
      ]) {
        expect(check({ workspace: { id: 10 } })).toBe(true);
      }
    } finally {
      act(() => currentUser.set(original));
    }
  });
});
