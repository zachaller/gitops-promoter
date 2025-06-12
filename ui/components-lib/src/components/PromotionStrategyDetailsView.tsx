import React, { useEffect, useState } from 'react';
import EnvironmentCard from './EnvironmentCard';
import { getEnvironmentDetails } from '../../../shared/utils/CTPData';
import { GitRepositoryStore } from '../../../shared/stores/GitRepositoryStore';
import { ChangeTransferPolicyStore } from '../../../shared/stores/ChangeTransferPolicyStore';

interface PromotionStrategyDetailsViewProps {
  namespace: string;
  strategyName: string;
  envOrder: string[];
}

//Required: PromotionStrategy MetaData, EnvOrder: Array of branches
export const PromotionStrategyDetailsView: React.FC<PromotionStrategyDetailsViewProps> = ({ namespace, strategyName, envOrder }) => {
  const [enrichedEnvs, setEnrichedEnvs] = useState<any[]>([]);
  const { items: gitRepositories, fetchItems: fetchGitRepositories } = GitRepositoryStore();
  const { items: changeTransferPolicies, fetchItems: fetchCTPs } = ChangeTransferPolicyStore();

  useEffect(() => {
    if (namespace) {
      fetchGitRepositories(namespace);
      fetchCTPs(namespace);

      // Subscribes to real-time updates from ChangeTransferPolicyStore
      const store = ChangeTransferPolicyStore.getState();
      store.subscribe(namespace);
      return () => {
        store.unsubscribe();
      };
    }
  }, [namespace, fetchGitRepositories, fetchCTPs]);


  
  useEffect(() => {
    if (!namespace || !strategyName) return;
    const strategyCTP = changeTransferPolicies.filter(
      (ctp: any) => ctp.metadata.labels?.['promoter.argoproj.io/promotion-strategy'] === strategyName
    );


    const ordered = envOrder
      .map(branch => strategyCTP.find((ctp: any) => ctp.spec.activeBranch === branch))
      .filter(Boolean);

    async function enrichAll() {
      const enriched = await Promise.all(
        ordered.map(env => getEnvironmentDetails(env, gitRepositories))
      );

      setEnrichedEnvs(enriched);
    }

    enrichAll();


  }, [namespace, strategyName, envOrder, gitRepositories, changeTransferPolicies]);

  
  if (!strategyName) return <div>No strategy found</div>;

  return <EnvironmentCard environments={enrichedEnvs} gitRepositories={gitRepositories} />;
};

export default PromotionStrategyDetailsView; 