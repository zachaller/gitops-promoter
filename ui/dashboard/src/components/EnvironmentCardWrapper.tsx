import React, { useEffect, useState } from 'react';
import EnvironmentCard from '@lib/components/EnvironmentCard';
import { GitRepositoryStore } from '@shared/stores/GitRepositoryStore';
import type { ChangeTransferPolicyType } from '@shared/models/ChangeTransferPolicyType';
import { ChangeTransferPolicyStore } from '@shared/stores/ChangeTransferPolicyStore';
import { getEnvironmentDetails } from '@shared/models/EnvironmentCardUtils';

interface Props {
  psName: string;
  namespace: string;
  envOrder: string[];
}

const EnvironmentCardWrapper: React.FC<Props> = ({ psName, namespace, envOrder }) => {
  const [enrichedEnvs, setEnrichedEnvs] = useState<any[]>([]);
  const [specEnvs, setSpecEnvs] = useState<any[]>([]);

  const { items: ctps, fetchItems: fetchCtps } = ChangeTransferPolicyStore();
  const { items: gitRepositories, fetchItems: fetchGitRepositories } = GitRepositoryStore();

  useEffect(() => {
    if (namespace) {
      fetchCtps(namespace);
      fetchGitRepositories(namespace);


      // Subscribe to real-time updates
      const store = ChangeTransferPolicyStore.getState();
      store.subscribe(namespace);
      return () => {
        store.unsubscribe();
      };
    }
  }, [namespace, fetchCtps, fetchGitRepositories]);

  useEffect(() => {
    const filtered: ChangeTransferPolicyType[] = ctps.filter(
      (ctp: ChangeTransferPolicyType) =>
        ctp.metadata.labels?.['promoter.argoproj.io/promotion-strategy'] === psName
    );

    const ordered = envOrder
      .map(branch => filtered.find(ctp => ctp.spec.activeBranch === branch))
      .filter(Boolean) as ChangeTransferPolicyType[];

    setSpecEnvs(
      ordered.map((ctp) => ({
        branch: ctp.spec.activeBranch,
        autoMerge: ctp.spec.autoMerge,
      }))
    );

    async function enrichAll() {
      const enriched = await Promise.all(
        ordered.map(ctp => getEnvironmentDetails(ctp, gitRepositories, namespace))
      );
      setEnrichedEnvs(enriched);
    }
    enrichAll();
  }, [ctps, psName, gitRepositories, envOrder, namespace]);

  return (
    <EnvironmentCard
      environments={enrichedEnvs}
      mergeSpecs={specEnvs}
      gitRepositories={gitRepositories}
    />
  );
};

export default EnvironmentCardWrapper; 