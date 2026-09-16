import React, { useEffect, useCallback, useMemo } from 'react';
import { useParams, useSearchParams } from 'react-router';
import HistoryView from '@lib/components/HistoryView/HistoryView';
import type { CellSelection } from '@lib/components/HistoryView/HistoryView';
import { PromotionStrategyStore } from '../stores/PromotionStrategyStore';
import { PromotionStrategyHistoryStore } from '../stores/PromotionStrategyHistoryStore';
import { mergePromotionStrategyHistory } from '@shared/utils/historyMerge';
import { useNavigateWithParams } from '../hooks/useNavigateWithParams';

const HistoryPage: React.FC = () => {
  const { namespace, name } = useParams();
  const navigate = useNavigateWithParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const { items, fetchItems, subscribe, unsubscribe } = PromotionStrategyStore();
  const {
    items: historyItems,
    fetchItems: fetchHistory,
    subscribe: subscribeHistory,
    unsubscribe: unsubscribeHistory,
  } = PromotionStrategyHistoryStore();

  useEffect(() => {
    if (!namespace) return;
    fetchItems(namespace);
    fetchHistory(namespace);
    subscribe(namespace);
    subscribeHistory(namespace);
    return () => {
      unsubscribe();
      unsubscribeHistory();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [namespace]);

  const strategy = useMemo(() => {
    const ps = items.find((p) => p.metadata.name === name);
    if (!ps) return undefined;
    const hist = historyItems.find((h) => h.metadata.name === name);
    return mergePromotionStrategyHistory(ps, hist);
  }, [items, historyItems, name]);

  const commit = searchParams.get('commit');
  const env = searchParams.get('env');
  const initialSelection = commit && env ? { rowId: commit, branch: env } : null;

  const handleSelectionChange = useCallback(
    (selection: CellSelection | null) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (selection) {
            next.set('commit', selection.rowId);
            next.set('env', selection.branch);
          } else {
            next.delete('commit');
            next.delete('env');
          }
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );

  return (
    <HistoryView
      strategy={strategy}
      name={name}
      namespace={namespace}
      onBack={() => navigate(`/promotion-strategies/${namespace}/${name}`)}
      fillViewport
      initialSelection={initialSelection}
      onSelectionChange={handleSelectionChange}
    />
  );
};

export default HistoryPage;
