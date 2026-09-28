import React from 'react';

interface Props {
  className?: string;
  url: string;
}

const GrafanaTaskResourcesLink: React.FC<Props> = ({ className, url }) => {
  return (
    <a className={className} href={url} rel="noopener noreferrer" target="_blank">
      Task Resources
    </a>
  );
};

export default GrafanaTaskResourcesLink;
