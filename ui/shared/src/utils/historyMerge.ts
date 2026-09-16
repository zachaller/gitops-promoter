import type { PromotionStrategy } from '../types/promotion';
import type { PromotionStrategyHistory } from '../types/view';

/** Merges PromotionStrategyHistory environments into a PromotionStrategy for HistoryView. */
export function mergePromotionStrategyHistory(
  ps: PromotionStrategy,
  history: PromotionStrategyHistory | undefined,
): PromotionStrategy {
  if (!history?.environments?.length || !ps.status?.environments) {
    return ps;
  }
  const byBranch = new Map(history.environments.map((e) => [e.branch ?? '', e.history]));
  return {
    ...ps,
    status: {
      ...ps.status,
      environments: ps.status.environments.map((env) => ({
        ...env,
        history: byBranch.get(env.branch ?? '') ?? env.history,
      })),
    },
  };
}
