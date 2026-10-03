import { FilterDropdownProps, TablePaginationConfig } from 'antd/es/table/interface';
import Icon from 'hew/Icon';
import Row from 'hew/Row';
import Select, { Option, SelectValue } from 'hew/Select';
import { Loadable } from 'hew/utils/loadable';
import _ from 'lodash';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import FilterCounter from 'components/FilterCounter';
import GenericTaskIdLink from 'components/GenericTaskIdLink';
import settingsConfig, {
  DEFAULT_COLUMN_WIDTHS,
  Settings,
  WhoseGenericTasks,
} from 'components/GenericTaskList.settings';
import GenericTaskStateBadge from 'components/GenericTaskStateBadge';
import Link from 'components/Link';
import InteractiveTable, { ColumnDef } from 'components/Table/InteractiveTable';
import {
  checkmarkRenderer,
  defaultRowClassName,
  getFullPaginationConfig,
  relativeTimeRenderer,
  userRenderer,
} from 'components/Table/Table';
import TableFilterDropdown from 'components/Table/TableFilterDropdown';
import usePolling from 'hooks/usePolling';
import { useSettings } from 'hooks/useSettings';
import { paths } from 'routes/utils';
import { getGenericTasks } from 'services/api';
import userStore from 'stores/users';
import { GenericTask, GenericTaskPagination, GenericTaskState } from 'types';
import handleError, { ErrorType } from 'utils/error';
import { useObservable } from 'utils/observable';

import css from './TaskList.module.scss';

const filterKeys: Array<keyof Settings> = ['state', 'whose'];

/* The filterable generic task states, in the order of a task's life. */
const filterStates: GenericTaskState[] = [
  GenericTaskState.Active,
  GenericTaskState.StoppingPaused,
  GenericTaskState.Paused,
  GenericTaskState.StoppingCompleted,
  GenericTaskState.Completed,
  GenericTaskState.StoppingCanceled,
  GenericTaskState.Canceled,
  GenericTaskState.StoppingError,
  GenericTaskState.Error,
];

const GenericTaskList: React.FC = () => {
  const loadableCurrentUser = useObservable(userStore.currentUser);
  const currentUser = Loadable.getOrElse(undefined, loadableCurrentUser);
  const users = Loadable.getOrElse([], useObservable(userStore.getUsers()));
  const [response, setResponse] = useState<GenericTaskPagination>();
  const pageRef = useRef<HTMLElement>(null);
  const canceler = useRef(new AbortController());
  const { activeSettings, resetSettings, settings, updateSettings } =
    useSettings<Settings>(settingsConfig);

  const filterCount = useMemo(() => activeSettings(filterKeys).length, [activeSettings]);

  const fetchTasks = useCallback(async () => {
    // The default view lists the current user's tasks; wait for the user instead of listing all.
    if (settings.whose === WhoseGenericTasks.Mine && !currentUser) return;
    try {
      const newResponse = await getGenericTasks(
        {
          limit: settings.tableLimit,
          offset: settings.tableOffset,
          states: settings.state,
          userIds:
            settings.whose === WhoseGenericTasks.Mine && currentUser ? [currentUser.id] : undefined,
        },
        { signal: canceler.current.signal },
      );
      setResponse((prev) => (_.isEqual(prev, newResponse) ? prev : newResponse));
    } catch (e) {
      handleError(e, {
        publicSubject: 'Unable to fetch generic tasks.',
        silent: true,
        type: ErrorType.Api,
      });
    }
  }, [currentUser, settings.state, settings.tableLimit, settings.tableOffset, settings.whose]);

  usePolling(fetchTasks, { rerunOnNewFn: true });

  useEffect(() => {
    const currentCanceler = canceler.current;
    return () => currentCanceler.abort();
  }, []);

  const resetFilters = useCallback(() => {
    resetSettings([...filterKeys, 'tableOffset']);
  }, [resetSettings]);

  const handleWhoseSelect = useCallback(
    (value: SelectValue) => {
      updateSettings({ tableOffset: 0, whose: value as WhoseGenericTasks });
    },
    [updateSettings],
  );

  const handleStateFilterApply = useCallback(
    (states: string[]) => {
      updateSettings({
        state: states.length !== 0 ? (states as GenericTaskState[]) : undefined,
        tableOffset: 0,
      });
    },
    [updateSettings],
  );

  const handleStateFilterReset = useCallback(() => {
    updateSettings({ state: undefined, tableOffset: 0 });
  }, [updateSettings]);

  const stateFilterDropdown = useCallback(
    (filterProps: FilterDropdownProps) => (
      <TableFilterDropdown
        {...filterProps}
        multiple
        values={settings.state}
        width={180}
        onFilter={handleStateFilterApply}
        onReset={handleStateFilterReset}
      />
    ),
    [handleStateFilterApply, handleStateFilterReset, settings.state],
  );

  const columns = useMemo(() => {
    const timeRenderer = (time?: string): React.ReactNode =>
      time ? relativeTimeRenderer(new Date(time)) : null;

    const cols: ColumnDef<GenericTask>[] = [
      {
        dataIndex: 'id',
        defaultWidth: DEFAULT_COLUMN_WIDTHS['id'],
        key: 'id',
        render: (_: unknown, record: GenericTask) => <GenericTaskIdLink taskId={record.taskId} />,
        title: 'ID',
      },
      {
        dataIndex: 'name',
        defaultWidth: DEFAULT_COLUMN_WIDTHS['name'],
        key: 'name',
        onCell: () => ({ 'data-testid': 'name' }),
        render: (_: unknown, record: GenericTask) => (
          <Link path={paths.genericTaskDetails(record.taskId)}>{record.name}</Link>
        ),
        title: 'Name',
      },
      {
        dataIndex: 'user',
        defaultWidth: DEFAULT_COLUMN_WIDTHS['user'],
        key: 'user',
        render: (_: unknown, record: GenericTask) => {
          const user = users.find((u) => u.id === record.userId);
          return user ? userRenderer(user) : record.username;
        },
        title: 'Owner',
      },
      {
        dataIndex: 'state',
        defaultWidth: DEFAULT_COLUMN_WIDTHS['state'],
        filterDropdown: stateFilterDropdown,
        filterIcon: <Icon name="filter" title="filter" />,
        filters: filterStates.map((value) => ({
          text: <GenericTaskStateBadge state={value} />,
          value,
        })),
        isFiltered: (settings: unknown) => !!(settings as Settings).state,
        key: 'state',
        onCell: () => ({ 'data-testid': 'state' }),
        render: (_: unknown, record: GenericTask) => <GenericTaskStateBadge state={record.state} />,
        title: 'State',
      },
      {
        align: 'right',
        dataIndex: 'slots',
        defaultWidth: DEFAULT_COLUMN_WIDTHS['slots'],
        key: 'slots',
        title: 'Slots',
      },
      {
        dataIndex: 'resourcePool',
        defaultWidth: DEFAULT_COLUMN_WIDTHS['resourcePool'],
        key: 'resourcePool',
        title: 'Resource Pool',
      },
      {
        align: 'center',
        dataIndex: 'pausable',
        defaultWidth: DEFAULT_COLUMN_WIDTHS['pausable'],
        key: 'pausable',
        render: (_: unknown, record: GenericTask) => checkmarkRenderer(!record.noPause),
        title: 'Pausable',
      },
      {
        dataIndex: 'parent',
        defaultWidth: DEFAULT_COLUMN_WIDTHS['parent'],
        key: 'parent',
        render: (_: unknown, record: GenericTask) =>
          record.parentId ? <GenericTaskIdLink taskId={record.parentId} /> : null,
        title: 'Parent',
      },
      {
        dataIndex: 'startTime',
        defaultWidth: DEFAULT_COLUMN_WIDTHS['startTime'],
        key: 'startTime',
        render: (_: unknown, record: GenericTask) => timeRenderer(record.startTime),
        title: 'Started',
      },
      {
        dataIndex: 'endTime',
        defaultWidth: DEFAULT_COLUMN_WIDTHS['endTime'],
        key: 'endTime',
        render: (_: unknown, record: GenericTask) => timeRenderer(record.endTime),
        title: 'Ended',
      },
    ];
    return cols;
  }, [stateFilterDropdown, users]);

  const handleTableChange = useCallback(
    (tablePagination: TablePaginationConfig) => {
      updateSettings({
        tableLimit: tablePagination.pageSize,
        tableOffset: ((tablePagination.current ?? 1) - 1) * (tablePagination.pageSize ?? 0),
      });
    },
    [updateSettings],
  );

  return (
    <>
      <div className={css.options}>
        <Row>
          {filterCount > 0 && (
            <FilterCounter activeFilterCount={filterCount} onReset={resetFilters} />
          )}
          <Select
            data-testid="whose"
            value={settings.whose}
            width={160}
            onSelect={handleWhoseSelect}>
            <Option value={WhoseGenericTasks.Mine}>My Tasks</Option>
            <Option value={WhoseGenericTasks.All}>All Users</Option>
          </Select>
        </Row>
      </div>
      <div className={css.base}>
        <InteractiveTable<GenericTask, Settings>
          columns={columns}
          containerRef={pageRef}
          dataSource={response?.tasks}
          defaultColumns={settingsConfig.settings.columns.defaultValue}
          loading={response === undefined}
          pagination={getFullPaginationConfig(
            {
              limit: settings.tableLimit,
              offset: settings.tableOffset,
            },
            response?.pagination.total ?? 0,
          )}
          rowClassName={defaultRowClassName({ clickable: false })}
          rowKey="taskId"
          settings={settings}
          showSorterTooltip={false}
          size="small"
          updateSettings={updateSettings}
          onChange={handleTableChange}
        />
      </div>
    </>
  );
};

export default GenericTaskList;
