import { Loadable } from 'hew/utils/loadable';

import { launchShell as apiLaunchShell } from 'services/api';
import userStore from 'stores/users';
import { CommandResponse, CommandType, RawJson } from 'types';
import { JupyterLabLaunchOptions, previewJupyterLab } from 'utils/jupyter';
import { recordLaunch } from 'utils/launchHistory';
import { NtscLaunchOptions, simpleLaunchConfig, toShellConfig } from 'utils/ntscConfig';

export interface ShellLaunchOptions extends NtscLaunchOptions {
  /** A full config. When given, name, pool, slots and template are ignored. */
  config?: RawJson;
}

/**
 * Launches a shell and returns the new task. The services wrapper has already
 * dropped the shell's SSH key from the response, so nothing here can keep it.
 * Errors are thrown so the launch modal can stay open and show them.
 *
 * A shell cannot be opened like a JupyterLab (utils/wait only opens notebooks
 * and TensorBoards); callers show how to connect instead.
 */
export const launchShell = async (options: ShellLaunchOptions = {}): Promise<CommandResponse> => {
  const response = await apiLaunchShell({
    config: options.config || simpleLaunchConfig(options),
    templateName: options.config || options.template === '' ? undefined : options.template,
    workspaceId: options.workspaceId,
  });
  const currentUser = Loadable.getOrElse(undefined, userStore.currentUser.get());
  recordLaunch(currentUser?.id, CommandType.Shell, {
    config: response.config,
    workspaceId: response.command.workspaceId,
  });
  return response;
};

/**
 * The master's LaunchShell has no preview mode, so the full shell config is
 * previewed through the JupyterLab preview, which merges the cluster defaults,
 * the template and the form fields exactly as a shell launch does, and then
 * has its notebook-only keys removed (see toShellConfig). No task is created.
 */
export const previewShell = async (options: JupyterLabLaunchOptions = {}): Promise<RawJson> => {
  try {
    return toShellConfig(await previewJupyterLab(options));
  } catch (e) {
    throw new Error('Unable to load shell config.');
  }
};
