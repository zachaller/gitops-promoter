import React from 'react';
import { useNavigate } from 'react-router-dom';
import type { PromotionStrategyType } from '@shared/models/PromotionStrategyType';
import { PromotionStrategyTile, } from '../PromotionStrategySummary/PromotionStrategyTile'
import { getLastCommitTime, getPromotionPhase } from '../PromotionStrategySummary/promotionStrategyUtils';
import './PromotionStrategiesTiles.scss';

interface Props {
  promotionStrategies: PromotionStrategyType[];
  namespace: string;
}

export const PromotionStrategiesTiles: React.FC<Props> = ({ promotionStrategies, namespace }) => {
  const navigate = useNavigate();
  return (
    
    <div className="applications-tiles">
      {promotionStrategies.map((ps) => {
        const lastCommitTime = getLastCommitTime(ps);
        const lastUpdated = lastCommitTime ? lastCommitTime.toLocaleString() : '-';
        const { borderStatus, promotedPhase } = getPromotionPhase(ps);

        return (
          <PromotionStrategyTile
            key={ps.metadata.name}
            ps={ps}
            namespace={namespace}
            borderStatus={borderStatus}
            promotedPhase={promotedPhase}
            lastUpdated={lastUpdated}
            onClick={() => navigate(`/promotion-strategies/${namespace}/${ps.metadata.name}`)}
          />
        );
      })}
    </div>
  );
};

export default PromotionStrategiesTiles; 