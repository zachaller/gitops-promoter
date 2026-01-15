import { namespaceStore } from '../../stores/NamespaceStore';
import React, { useEffect, useState, useMemo } from 'react';
import { PromotionStrategyStore } from '../../stores/PromotionStrategyStore';
import PromotionStrategiesTiles from '../../components/PromotionStrategySummary/PromotionStrategyTiles';
import { SkeletonLoader } from '../../components/SkeletonLoader/SkeletonLoader';
import { SearchFilter } from '../../components/SearchFilter/SearchFilter';
import { EmptyState } from '../../components/EmptyState/EmptyState';
import { getOverallPromotionStatus } from '@shared/utils/util';
import { enrichFromCRD } from '@shared/utils/PSData';
import type { StatusType } from '@lib/components/StatusIcon';

interface NamespaceStore {
  namespace: string;
  namespaces: string[];
  setNamespace: (namespace: string) => void;
  setNamespaces: (namespaces: string[]) => void;
}

export function PromotionStrategies() {

    const namespace = namespaceStore((s: NamespaceStore) => s.namespace);

    const { items, loading, error, fetchItems, subscribe, unsubscribe } = PromotionStrategyStore();

    const [searchQuery, setSearchQuery] = useState('');
    const [statusFilter, setStatusFilter] = useState<StatusType | 'all'>('all');

    useEffect(() => {
        if (!namespace) return;
        fetchItems(namespace);
        subscribe(namespace);
        return () => unsubscribe();
    }, [namespace]);

    // Filter promotion strategies based on search and filter criteria
    const filteredItems = useMemo(() => {
        if (!items) return [];

        let filtered = items;

        // Filter by search query
        if (searchQuery) {
            const query = searchQuery.toLowerCase();
            filtered = filtered.filter((ps) => {
                const name = ps.metadata?.name?.toLowerCase() || '';
                const repo = ps.spec?.gitRepositoryRef?.name?.toLowerCase() || '';
                return name.includes(query) || repo.includes(query);
            });
        }

        // Filter by status
        if (statusFilter !== 'all') {
            filtered = filtered.filter((ps) => {
                const enrichedEnvs = enrichFromCRD(ps);
                const environmentStatuses = enrichedEnvs.map(env => env.promotionStatus || 'unknown');
                const overallStatus = getOverallPromotionStatus(environmentStatuses);
                return overallStatus === statusFilter;
            });
        }

        return filtered;
    }, [items, searchQuery, statusFilter]);

    if (!namespace) return <EmptyState type="no-namespace" />;
    if (loading) return <SkeletonLoader count={6} />;
    if (error) return <div className="error-message">Error: {error}</div>;

    const hasSearchOrFilter = searchQuery !== '' || statusFilter !== 'all';
    const hasNoResults = filteredItems.length === 0;
    const hasNoStrategies = items.length === 0;

    const handleClearFilters = () => {
        setSearchQuery('');
        setStatusFilter('all');
    };

    return (
        <div>
            {!hasNoStrategies && (
                <SearchFilter
                    searchQuery={searchQuery}
                    onSearchChange={setSearchQuery}
                    statusFilter={statusFilter}
                    onStatusFilterChange={setStatusFilter}
                    resultCount={filteredItems.length}
                    totalCount={items?.length || 0}
                />
            )}

            {hasNoStrategies ? (
                <EmptyState type="no-strategies" />
            ) : hasNoResults && hasSearchOrFilter ? (
                <EmptyState type="no-results" onClearFilters={handleClearFilters} />
            ) : (
                <PromotionStrategiesTiles promotionStrategies={filteredItems} namespace={namespace} />
            )}
        </div>
    );
}