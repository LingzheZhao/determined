import { Loadable } from 'hew/utils/loadable';

import {
  launchJupyterLab as apiLaunchJupyterLab,
  previewJupyterLab as apiPreviewJupyterLab,
} from 'services/api';
import userStore from 'stores/users';
import { CommandType, RawJson } from 'types';
import handleError, { ErrorLevel, ErrorType } from 'utils/error';
import { recordLaunch } from 'utils/launchHistory';
import { NtscLaunchOptions, simpleLaunchConfig } from 'utils/ntscConfig';
import { openCommandResponse } from 'utils/wait';

export type JupyterLabOptions = NtscLaunchOptions;

export interface JupyterLabLaunchOptions extends JupyterLabOptions {
  /** A full config. When given, name, pool, slots and template are ignored. */
  config?: RawJson;
}

export const launchJupyterLab = async (options: JupyterLabLaunchOptions = {}): Promise<void> => {
  try {
    const commandResponse = await apiLaunchJupyterLab({
      config: options.config || simpleLaunchConfig(options),
      templateName: options.config || options.template === '' ? undefined : options.template,
      workspaceId: options.workspaceId,
    });
    const currentUser = Loadable.getOrElse(undefined, userStore.currentUser.get());
    recordLaunch(currentUser?.id, CommandType.JupyterLab, {
      config: commandResponse.config,
      workspaceId: commandResponse.command.workspaceId,
    });
    openCommandResponse(commandResponse);
  } catch (e) {
    handleError(e, {
      level: ErrorLevel.Error,
      silent: false,
      type: ErrorType.Server,
    });
  }
};

export const previewJupyterLab = async (
  options: JupyterLabLaunchOptions = {},
): Promise<RawJson> => {
  try {
    const config = await apiPreviewJupyterLab({
      config: options.config || simpleLaunchConfig(options),
      preview: true,
      templateName: options.config || options.template === '' ? undefined : options.template,
      workspaceId: options.workspaceId,
    });
    return config;
  } catch (e) {
    throw new Error('Unable to load JupyterLab config.');
  }
};
