import {useEffect} from 'react'
import { namespaceStore } from '@shared/stores/NamespaceStore';
import PromotionStrategiesTiles from '../../components/PromotionStrategySummary/PromotionStrategiesTiles';
import { PromotionStrategyStore } from '@shared/stores/PromotionStrategyStore';

export function PromotionStrategyDetails() {
    
    const namespace = namespaceStore((s: any) => s.namespace);

    
    const { items: promotionStrategiesStore, loading: promotionStrategiesLoading, error: promotionStrategiesError, fetchItems, subscribe, unsubscribe } = PromotionStrategyStore();

    useEffect(() => {
        
        if (!namespace) return;
        fetchItems(namespace);
        subscribe(namespace);

        //Cleanup
        return () => {
            unsubscribe();
        };


    }, [namespace, fetchItems, subscribe, unsubscribe]);

    if (!namespace) return null;
    if (promotionStrategiesLoading) return <div>Loading...</div>;
    if (promotionStrategiesError) return <div>Error: {promotionStrategiesError}</div>;



    return (
        <div>
            <PromotionStrategiesTiles promotionStrategies={promotionStrategiesStore || []} namespace={namespace} />

        </div>
    );
}