import React from 'react';
import { Link } from 'react-router-dom';

import GrafanaTaskResourcesLink from 'components/GrafanaTaskResourcesLink';
import useGrafanaTaskResources from 'hooks/useGrafanaTaskResources';
import useTaskResourcesEnabled from 'hooks/useTaskResourcesEnabled';
import {
  buildGrafanaTaskResourcesUrl,
  GrafanaTaskResourcesTarget,
} from 'utils/grafanaTaskResources';

interface Props {
  className?: string;
  nativeUrl: string;
  target: GrafanaTaskResourcesTarget;
}

const TaskResourcesLink: React.FC<Props> = ({ className, nativeUrl, target }) => {
  const enabled = useTaskResourcesEnabled();
  const grafana = useGrafanaTaskResources();
  if (enabled === undefined) return null;
  if (enabled)
    return (
      <Link className={className} to={nativeUrl}>
        Resources
      </Link>
    );
  const externalUrl = buildGrafanaTaskResourcesUrl(grafana, target);
  return externalUrl ? <GrafanaTaskResourcesLink className={className} url={externalUrl} /> : null;
};

export default TaskResourcesLink;
