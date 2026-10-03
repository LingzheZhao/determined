import CodeEditor from 'hew/CodeEditor';
import Message from 'hew/Message';
import Spinner from 'hew/Spinner';
import yaml from 'js-yaml';
import React, { useMemo } from 'react';

import { RawJson } from 'types';
import handleError from 'utils/error';

interface Props {
  config?: RawJson;
  error?: string;
}

/* The config of a generic task, read-only, in YAML like the experiment config view. */
const GenericTaskConfig: React.FC<Props> = ({ config, error }: Props) => {
  const file = useMemo(() => (config ? yaml.dump(config) : ''), [config]);

  if (error) return <Message description={error} icon="warning" title="Unable to load config" />;
  if (!config) return <Spinner center spinning tip="Fetching config..." />;

  return (
    <CodeEditor
      file={file}
      files={[{ key: 'config.yaml' }]}
      height="60vh"
      readonly
      onError={handleError}
    />
  );
};

export default GenericTaskConfig;
