import Pivot, { PivotProps } from 'hew/Pivot';
import React, { useCallback, useMemo, useRef } from 'react';
import { useNavigate, useParams } from 'react-router-dom';

import GenericTaskList from 'components/GenericTaskList';
import Page from 'components/Page';
import TaskList from 'components/TaskList';
import { paths } from 'routes/utils';
import { ValueOf } from 'types';

const TabType = {
  Generic: 'generic',
  Interactive: 'interactive',
} as const;

type TabType = ValueOf<typeof TabType>;

type Params = {
  tab?: string;
};

const DEFAULT_TAB_KEY = TabType.Interactive;

const TaskListPage: React.FC = () => {
  const pageRef = useRef<HTMLElement>(null);
  const { tab } = useParams<Params>();
  const navigate = useNavigate();
  // The route is the tab's only state, so that the sidebar and links switch tabs too.
  const tabKey: TabType = tab === TabType.Generic ? TabType.Generic : DEFAULT_TAB_KEY;

  const handleTabChange = useCallback(
    (key: string) => {
      navigate(key === TabType.Generic ? paths.genericTaskList() : paths.taskList(), {
        replace: true,
      });
    },
    [navigate],
  );

  const tabItems: PivotProps['items'] = useMemo(
    () => [
      {
        children: <TaskList />,
        key: TabType.Interactive,
        label: 'Notebooks & Commands',
      },
      {
        children: <GenericTaskList />,
        key: TabType.Generic,
        label: 'Generic Tasks',
      },
    ],
    [],
  );

  return (
    <Page
      breadcrumb={[
        {
          breadcrumbName: 'Tasks',
          path: paths.taskList(),
        },
      ]}
      containerRef={pageRef}
      id="tasks"
      title="Tasks">
      <Pivot
        activeKey={tabKey}
        destroyInactiveTabPane
        items={tabItems}
        onChange={handleTabChange}
      />
    </Page>
  );
};

export default TaskListPage;
