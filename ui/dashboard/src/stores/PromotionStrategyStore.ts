import type { PromotionStrategyView } from '@shared/utils/PSData';
import { createCRDStore } from './CRDStore';

// Use PromotionStrategyView for aggregated data from the API
export const PromotionStrategyStore = createCRDStore<PromotionStrategyView>('PromotionStrategyView', 'PromotionStrategyView') 