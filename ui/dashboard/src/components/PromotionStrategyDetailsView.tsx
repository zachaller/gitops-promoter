import React from 'react';
import Card from '@lib/components/Card';
import { type PromotionStrategy } from '@shared/utils/PSData';

interface PromotionStrategyDetailsViewProps {
  strategy: PromotionStrategy;
  onCopySha?: (sha: string) => void;
}

export const PromotionStrategyDetailsView: React.FC<PromotionStrategyDetailsViewProps> = ({
  strategy,
  onCopySha,
}) => {
  if (!strategy) return <div>No strategy found</div>;


  //Pass raw data
  const environments = strategy.status?.environments || [];
  return <Card environments={environments} onCopySha={onCopySha} />;
};

export default PromotionStrategyDetailsView; 