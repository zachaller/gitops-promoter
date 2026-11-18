import { namespaceStore } from '../../stores/NamespaceStore';
import React, { useEffect } from 'react';
import { PromotionStrategyStore } from '../../stores/PromotionStrategyStore';
import PromotionStrategiesTiles from '../../components/PromotionStrategySummary/PromotionStrategyTiles';

interface NamespaceStore {
  namespace: string;
  namespaces: string[];
  setNamespace: (_ns: string) => void;
  setNamespaces: (_nsList: string[]) => void;
}

export function PromotionStrategies() {
  const namespace = namespaceStore((s: NamespaceStore) => s.namespace);

  const { items, loading, error, subscribe, unsubscribe } = PromotionStrategyStore();

  // The stream delivers the current state when it opens and follows it from there.
  useEffect(() => {
    if (!namespace) return;
    subscribe(namespace);
    return () => unsubscribe();
  }, [namespace, subscribe, unsubscribe]);

  if (!namespace) return null;
  if (loading) return <div>Loading...</div>;
  if (error) return <div>Error: {error}</div>;

  return (
    <div>
      <PromotionStrategiesTiles promotionStrategies={items} namespace={namespace} />
    </div>
  );
}
