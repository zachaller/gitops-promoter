import React, { useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { namespaceStore } from '@shared/stores/NamespaceStore'
import { PromotionStrategyStore } from '@shared/stores/PromotionStrategyStore';
import BackButton from '../components/BackButton';
import HeaderBar from '@lib/components/HeaderBar';
import PromotionStrategyDetailsView from '@lib/components/PromotionStrategyDetailsView';

interface PromotionStrategyPageProps {
  namespace?: string;
  strategyName?: string;
}

const PromotionStrategyPage: React.FC<PromotionStrategyPageProps> = ({ namespace: propsNamespace, strategyName: propsStrategyName }) => {
  const { namespace: urlNamespace, name: urlStrategyName } = useParams();
  const namespace = propsNamespace || urlNamespace;
  const strategyName = propsStrategyName || urlStrategyName;

  const currentNamespace = namespaceStore((s: any) => s.namespace);
  const setNamespace = namespaceStore((s: any) => s.setNamespace);

  const { items: promotionStrategyList, fetchItems: fetchPromotionStrategies, subscribe, unsubscribe } = PromotionStrategyStore();

  // Find the namespace to use
  const activeNamespace = namespace || currentNamespace;
  useEffect(() => {
    if (activeNamespace && activeNamespace !== currentNamespace) {
      setNamespace(activeNamespace);
      fetchPromotionStrategies(activeNamespace);
      subscribe(activeNamespace);
    }
    return () => {
      unsubscribe();
    };
  }, [activeNamespace, currentNamespace, setNamespace, fetchPromotionStrategies, subscribe, unsubscribe]);

  // Find the selected strategy -> Send to EnvironmentCard
  const selectedStrategy = promotionStrategyList.find(
    (ps: any) => ps.metadata.name === strategyName
  );

  //Navigation:
  const navigate = useNavigate();

  const handleBack = () => {
    setNamespace(currentNamespace);
    navigate('/promotion-strategies');
  };

  
  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', position: 'relative', width: '100%', backgroundColor: 'white'}}>
        <div style={{ flex: '0 0 auto' }}>
          <BackButton onClick={handleBack} />
        </div>
        <div style={{ flex: 1, display: 'flex', justifyContent: 'center', marginRight: '100px'}}>
          <HeaderBar name={strategyName || ""} />
        </div>
      </div>

      {promotionStrategyList.length === 0 ? (
        <div style={{ textAlign: 'center', marginTop: '20px' }}>Loading strategies...</div>
      ) : selectedStrategy ? (
        <PromotionStrategyDetailsView
          namespace={activeNamespace}
          strategyName={selectedStrategy.metadata.name}
          envOrder={selectedStrategy.spec.environments.map((env: any) => env.branch)}
        />
      ) : (
        <div style={{ textAlign: 'center', marginTop: '20px' }}>No strategy found for {strategyName}</div>
      )}
    </>
  );
};

export default PromotionStrategyPage; 