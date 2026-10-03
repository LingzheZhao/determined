import { array, boolean, literal, number, undefined as undefinedType, union } from 'io-ts';

import { InteractiveTableSettings } from 'components/Table/InteractiveTable';
import { MINIMUM_PAGE_SIZE } from 'components/Table/Table';
import { SettingsConfig } from 'hooks/useSettings';
import { GenericTaskState, ValueOf } from 'types';

export type GenericTaskColumnName =
  | 'endTime'
  | 'id'
  | 'name'
  | 'parent'
  | 'pausable'
  | 'resourcePool'
  | 'slots'
  | 'startTime'
  | 'state'
  | 'user';

export const DEFAULT_COLUMNS: GenericTaskColumnName[] = [
  'id',
  'name',
  'user',
  'state',
  'slots',
  'resourcePool',
  'pausable',
  'parent',
  'startTime',
  'endTime',
];

export const DEFAULT_COLUMN_WIDTHS: Record<GenericTaskColumnName, number> = {
  endTime: 117,
  id: 100,
  name: 200,
  parent: 100,
  pausable: 90,
  resourcePool: 128,
  slots: 70,
  startTime: 117,
  state: 106,
  user: 85,
};

export const WhoseGenericTasks = {
  All: 'ALL_TASKS',
  Mine: 'MY_TASKS',
} as const;

export type WhoseGenericTasks = ValueOf<typeof WhoseGenericTasks>;

export interface Settings extends InteractiveTableSettings {
  columns: GenericTaskColumnName[];
  state?: GenericTaskState[];
  whose: WhoseGenericTasks;
}

const config: SettingsConfig<Settings> = {
  settings: {
    columns: {
      defaultValue: DEFAULT_COLUMNS,
      skipUrlEncoding: true,
      storageKey: 'columns',
      type: array(
        union([
          literal('endTime'),
          literal('id'),
          literal('name'),
          literal('parent'),
          literal('pausable'),
          literal('resourcePool'),
          literal('slots'),
          literal('startTime'),
          literal('state'),
          literal('user'),
        ]),
      ),
    },
    columnWidths: {
      defaultValue: DEFAULT_COLUMNS.map((col: GenericTaskColumnName) => DEFAULT_COLUMN_WIDTHS[col]),
      skipUrlEncoding: true,
      storageKey: 'columnWidths',
      type: array(number),
    },
    sortDesc: {
      defaultValue: true,
      skipUrlEncoding: true,
      storageKey: 'sortDesc',
      type: boolean,
    },
    state: {
      defaultValue: undefined,
      storageKey: 'state',
      type: union([
        undefinedType,
        array(
          union([
            literal(GenericTaskState.Active),
            literal(GenericTaskState.Canceled),
            literal(GenericTaskState.Completed),
            literal(GenericTaskState.Error),
            literal(GenericTaskState.Paused),
            literal(GenericTaskState.StoppingCanceled),
            literal(GenericTaskState.StoppingCompleted),
            literal(GenericTaskState.StoppingError),
            literal(GenericTaskState.StoppingPaused),
            literal(GenericTaskState.Unspecified),
          ]),
        ),
      ]),
    },
    tableLimit: {
      defaultValue: MINIMUM_PAGE_SIZE,
      storageKey: 'tableLimit',
      type: number,
    },
    tableOffset: {
      defaultValue: 0,
      storageKey: 'tableOffset',
      type: number,
    },
    whose: {
      defaultValue: WhoseGenericTasks.Mine,
      storageKey: 'whose',
      type: union([literal(WhoseGenericTasks.All), literal(WhoseGenericTasks.Mine)]),
    },
  },
  storagePath: 'generic-task-list',
};

export default config;
