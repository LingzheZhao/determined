import Alert from 'hew/Alert';
import Button from 'hew/Button';
import Spinner from 'hew/Spinner';
import React, { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';

import Link from 'components/Link';
import Page from 'components/Page';
import useFeature from 'hooks/useFeature';
import useTaskResourcesEnabled from 'hooks/useTaskResourcesEnabled';
import { paths } from 'routes/utils';
import { getExperimentDetails, getExpTrials } from 'services/api';
import { TrialItem } from 'types';

const pageSize = 25;

const ExperimentResources: React.FC = () => {
  const { experimentId = '' } = useParams<{ experimentId: string }>();
  const id = Number(experimentId);
  const validId = /^\d+$/.test(experimentId) && Number.isSafeInteger(id) && id > 0;
  const navigate = useNavigate();
  const enabled = useTaskResourcesEnabled();
  const flatRuns = useFeature().isOn('flat_runs');
  const [offset, setOffset] = useState(0);
  const [trials, setTrials] = useState<TrialItem[]>();
  const [total, setTotal] = useState<number>();
  const [error, setError] = useState(false);

  useEffect(() => {
    setOffset(0);
    setTrials(undefined);
    setTotal(undefined);
    setError(false);
  }, [experimentId]);

  useEffect(() => {
    if (!validId || enabled !== true) return;
    const controller = new AbortController();
    setTrials(undefined);
    setError(false);
    const load = async () => {
      try {
        // Check access to the experiment before listing its trials. Resource queries also
        // enforce the selected trial task's permissions on the server.
        await getExperimentDetails({ id }, { signal: controller.signal });
        const result = await getExpTrials(
          { id, limit: pageSize, offset, orderBy: 'ORDER_BY_DESC', sortBy: 'SORT_BY_ID' },
          { signal: controller.signal },
        );
        if (controller.signal.aborted) return;
        setTrials(result.trials);
        setTotal(result.pagination?.total);
        if (result.pagination?.total === 1 && result.trials[0]?.taskId) {
          navigate(paths.taskResources(result.trials[0].taskId), { replace: true });
        }
      } catch {
        if (!controller.signal.aborted) setError(true);
      }
    };
    void load();
    return () => controller.abort();
  }, [enabled, id, navigate, offset, validId]);

  const entity = flatRuns ? 'Search' : 'Experiment';
  const detailsPath = flatRuns ? paths.searchDetails(id) : paths.experimentDetails(id);
  const hasMore =
    total === undefined ? (trials?.length ?? 0) === pageSize : offset + pageSize < total;

  return (
    <Page
      breadcrumb={[
        { breadcrumbName: flatRuns ? 'Searches' : 'Experiments', path: paths.experimentList() },
        { breadcrumbName: `${entity} ${experimentId}`, path: detailsPath },
        { breadcrumbName: 'Resources', path: paths.experimentResources(experimentId) },
      ]}
      title={`${entity} Resources`}>
      {!validId || enabled === false || error ? (
        <Alert
          message={
            enabled === false
              ? 'Native task resources are unavailable on this cluster.'
              : `This ${entity.toLowerCase()} is unavailable or you do not have permission to view it.`
          }
          type="error"
        />
      ) : enabled === undefined || !trials ? (
        <Spinner center spinning />
      ) : trials.length === 0 && offset === 0 ? (
        <Alert message={`This ${entity.toLowerCase()} has no trials yet.`} type="info" />
      ) : (
        <>
          <p>Select a trial to view its task resources.</p>
          <ul>
            {trials.map((trial) => (
              <li key={trial.id}>
                Trial {trial.id} · {trial.state} ·{' '}
                {trial.taskId ? (
                  <Link path={paths.taskResources(trial.taskId)}>View Resources</Link>
                ) : (
                  <>task resources unavailable</>
                )}
              </li>
            ))}
          </ul>
          <Button disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - pageSize))}>
            Previous
          </Button>{' '}
          <Button disabled={!hasMore} onClick={() => setOffset(offset + pageSize)}>
            Next
          </Button>
        </>
      )}
    </Page>
  );
};

export default ExperimentResources;
