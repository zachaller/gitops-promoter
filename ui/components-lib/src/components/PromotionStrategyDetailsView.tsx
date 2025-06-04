import React from 'react';
import EnvironmentCardWrapper from '../../../dashboard/src/components/EnvironmentCardWrapper';

interface PromotionStrategyDetailsViewProps {
  strategy: any;
  envOrder: string[];
}

export const PromotionStrategyDetailsView: React.FC<PromotionStrategyDetailsViewProps> = ({ strategy, envOrder }) => {
  if (!strategy) return <div>No strategy found</div>;
  
  return (
    <EnvironmentCardWrapper
      psName={strategy.metadata.name}
      namespace={strategy.metadata.namespace || ''}
      envOrder={envOrder}
    />
  );
};

export default PromotionStrategyDetailsView; 