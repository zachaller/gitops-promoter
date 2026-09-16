import { create } from 'zustand';
import type { PromotionStrategyHistory } from '@shared/types/view';

const LIST_KIND = 'promotionstrategyhistories';
const WATCH_KIND = 'PromotionStrategyHistory';
const SSE_EVENT = 'PromotionStrategyHistory';

export const PromotionStrategyHistoryStore = create<{
  items: PromotionStrategyHistory[];
  loading: boolean;
  error: string | null;
  connectionStatus?: 'connecting' | 'open' | 'error';
  fetchItems: (_ns: string) => Promise<void>;
  subscribe: (_ns: string) => void;
  unsubscribe: () => void;
  reset: () => void;
}>((set) => {
  let eventSource: EventSource | null = null;

  return {
    items: [],
    loading: false,
    error: null,
    connectionStatus: 'connecting',

    fetchItems: async (namespace: string) => {
      set({ loading: true, error: null });
      try {
        const res = await fetch(`/list?kind=${LIST_KIND}&namespace=${namespace}`);
        if (!res.ok) throw new Error(`Error: ${res.status}`);
        const data = (await res.json()) as PromotionStrategyHistory[] | null;
        set({ items: data ?? [], loading: false });
      } catch (err: unknown) {
        const errorMessage = err instanceof Error ? err.message : 'Unknown error';
        set({ error: errorMessage, loading: false });
      }
    },

    subscribe: (namespace: string) => {
      if (eventSource) eventSource.close();

      eventSource = new EventSource(`/watch?kind=${WATCH_KIND}&namespace=${namespace}`);

      eventSource.onopen = () => set({ connectionStatus: 'open' });
      eventSource.onerror = () => set({ connectionStatus: 'error' });

      eventSource.addEventListener(SSE_EVENT, (evt: MessageEvent) => {
        try {
          const updated = JSON.parse(evt.data) as PromotionStrategyHistory;
          set((state) => {
            const idx = state.items.findIndex(
              (item) =>
                item.metadata.name === updated.metadata.name &&
                item.metadata.namespace === updated.metadata.namespace,
            );
            let newItems: PromotionStrategyHistory[];
            if (idx >= 0) {
              newItems = [...state.items];
              newItems[idx] = updated;
            } else {
              newItems = [...state.items, updated];
            }
            return { items: newItems };
          });
        } catch {
          set({ error: 'Failed to parse promotion history update' });
        }
      });
    },

    unsubscribe: () => {
      if (eventSource) {
        eventSource.close();
        eventSource = null;
      }
    },

    reset: () => set({ items: [] }),
  };
});
