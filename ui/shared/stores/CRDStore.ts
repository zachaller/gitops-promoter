import { create } from 'zustand';


export function createCRDStore<T>(kind: string, eventName: string) {
    let eventSource: EventSource | null = null;

    return create<
    {
        items: T[];
        loading: boolean;
        error: string | null;
        fetchItems: (namespace: string) => Promise<void>;
        subscribe: (namespace: string) => void;
        unsubscribe: () => void;
        reset: () => void;
        
    }>
    
    
    
    ((set) => ({
        items: [],
        loading: false,
        error: null,

        // Fetch items from via /list endpoint
        fetchItems: async (namespace: string) => {
            set({ loading: true, error: null });
            try {
                const res = await fetch(`/list?kind=${kind}&namespace=${namespace}`);


                if (!res.ok) throw new Error(`Error: ${res.status}`);
                const data = await res.json();
                
                set({ items: data, 
                    loading: false });

                    
            } catch (err: any) {
                set({ error: err.message, loading: false });
            }
        },

        //Subscribing to SSE
        subscribe: (namespace: string) => {
            if (eventSource) eventSource.close();

            //Real-Time fetch via /watch endpoint
            eventSource = new EventSource(`/watch?kind=${kind}&namespace=${namespace}`);
            eventSource.addEventListener(eventName, (evt: MessageEvent) => {

                try {
                    const updated = JSON.parse(evt.data);
                    set((state) => {
                        const idx = state.items.findIndex(
                            (item: any) =>
                                item.metadata?.name === updated.metadata?.name &&
                                item.metadata?.namespace === updated.metadata?.namespace
                        );
                        if (idx >= 0) {
                            state.items[idx] = updated;
                        } else {
                            state.items.push(updated);
                        }
                        return { items: [...state.items] };
                    });
                } catch {}
            });
        },



        // Unsubscribe from SSE
        unsubscribe: () => {
            if (eventSource) {
                eventSource.close();
                eventSource = null;
            }
        },

        // Reset items
        reset: () => set({ items: [] }),
    }));
} 