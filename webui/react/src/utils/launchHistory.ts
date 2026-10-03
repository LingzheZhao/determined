/**
 * A short, per-browser list of the configs a user recently launched shells and
 * JupyterLabs with. It is a convenience for the launch modal's "Start from"
 * picker and is never authoritative: the master merges and validates every
 * config again at launch, and the list can be empty or missing at any time
 * (private windows, blocked or cleared storage, another browser).
 *
 * What is stored, in window.localStorage under `u:<user id>/launch-history/<type>`:
 * the launch config the master returned (after sanitizeConfig: no entrypoint,
 * no registry_auth, no generated description, no priority), with environment
 * variables whose names look like credentials removed, plus the workspace id,
 * the save time and the names of the removed variables. Nothing else, and
 * never a shell's SSH key. At most LAUNCH_HISTORY_LIMIT entries per user and
 * type are kept.
 *
 * Every storage access is wrapped in try/catch; failures read as an empty list
 * and writes are dropped.
 */
import { isRight } from 'fp-ts/lib/Either';
import * as io from 'io-ts';

import { RawJson } from 'types';
import md5 from 'utils/md5';
import {
  NtscLaunchType,
  redactSensitiveEnv,
  sanitizeConfig,
  stableStringify,
} from 'utils/ntscConfig';
import { StorageManager } from 'utils/storage';

export const LAUNCH_HISTORY_LIMIT = 20;
const ENTRY_VERSION = 1;

const ioLaunchHistoryEntry = io.intersection([
  io.type({
    config: io.UnknownRecord,
    id: io.string,
    savedAt: io.number,
    v: io.literal(ENTRY_VERSION),
    workspaceId: io.number,
  }),
  io.partial({ redactedEnv: io.array(io.string) }),
]);

export interface LaunchHistoryEntry {
  config: RawJson;
  id: string;
  /** Names of environment variables left out because they looked like credentials. */
  redactedEnv?: string[];
  savedAt: number;
  v: typeof ENTRY_VERSION;
  workspaceId: number;
}

const getLocalStorage = (): Storage | undefined => {
  try {
    // Reading window.localStorage itself throws when storage is blocked.
    return window.localStorage ?? undefined;
  } catch {
    return undefined;
  }
};

const historyStorage = (userId: number): StorageManager | undefined => {
  const store = getLocalStorage();
  if (!store) return undefined;
  return new StorageManager({ basePath: `u:${userId}/launch-history`, store });
};

export const listLaunchHistory = (
  userId: number | undefined,
  type: NtscLaunchType,
): LaunchHistoryEntry[] => {
  if (userId === undefined) return [];
  try {
    const stored = historyStorage(userId)?.get<unknown>(type);
    if (!Array.isArray(stored)) return [];
    return stored
      .filter((entry): entry is LaunchHistoryEntry => isRight(ioLaunchHistoryEntry.decode(entry)))
      .slice(0, LAUNCH_HISTORY_LIMIT);
  } catch {
    return [];
  }
};

const writeHistory = (userId: number, type: NtscLaunchType, entries: LaunchHistoryEntry[]) => {
  const storage = historyStorage(userId);
  if (!storage) return;
  try {
    if (entries.length === 0) storage.remove(type);
    else storage.set(type, entries);
  } catch {
    // Most likely the storage quota. Keep the newer half, or give up quietly.
    try {
      storage.set(type, entries.slice(0, Math.floor(entries.length / 2)));
    } catch {
      // The history is only a convenience.
    }
  }
};

export const recordLaunch = (
  userId: number | undefined,
  type: NtscLaunchType,
  launch: { config?: RawJson; workspaceId?: number },
): void => {
  if (userId === undefined || !launch.config || launch.workspaceId === undefined) return;
  try {
    const { config, redacted } = redactSensitiveEnv(sanitizeConfig(launch.config));
    const id = md5(`${type}:${launch.workspaceId}:${stableStringify(config)}`);
    const entry: LaunchHistoryEntry = {
      config,
      id,
      savedAt: Date.now(),
      v: ENTRY_VERSION,
      workspaceId: launch.workspaceId,
    };
    if (redacted.length !== 0) entry.redactedEnv = redacted;
    const others = listLaunchHistory(userId, type).filter((item) => item.id !== id);
    writeHistory(userId, type, [entry, ...others].slice(0, LAUNCH_HISTORY_LIMIT));
  } catch {
    // The history is only a convenience.
  }
};

export const removeLaunchHistoryEntry = (
  userId: number | undefined,
  type: NtscLaunchType,
  id: string,
): void => {
  if (userId === undefined) return;
  try {
    writeHistory(
      userId,
      type,
      listLaunchHistory(userId, type).filter((entry) => entry.id !== id),
    );
  } catch {
    // The history is only a convenience.
  }
};

export const clearLaunchHistory = (userId: number | undefined, type: NtscLaunchType): void => {
  if (userId === undefined) return;
  writeHistory(userId, type, []);
};
