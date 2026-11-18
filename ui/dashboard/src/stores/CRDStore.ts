import { create } from 'zustand';
import { enrichFromCRD, mergeCommitStatusManagers } from '@shared/utils/PSData';
import { environmentsFromBundle } from '@shared/utils/environments';
import { scmProviderSpecFromBundle, sortStrategyCommitStatuses } from '@shared/utils/util';
import type { PromotionStrategy, CommitStatusManagerBundle } from '@shared/utils/PSData';
import type { PromotionStrategyDetails, ScmProviderSpec } from '@shared/types/view';

interface CRDItem extends PromotionStrategy {
  enriched?: unknown;
}

export type ConnectionStatus = 'connecting' | 'open' | 'error';

// Events on the /watch stream besides the per-kind update event (see internal/webserver/stream.go).
/** The complete current state, sent first on every connection. */
const SNAPSHOT_EVENT = 'snapshot';
/** An object was deleted. */
const DELETED_EVENT = 'deleted';
/** Heartbeat, sent every 15 seconds. */
const PING_EVENT = 'ping';

/**
 * With a heartbeat every 15 seconds, hearing nothing for this long means the connection is dead
 * even though the browser has not reported an error (a laptop that slept, a NAT or proxy that
 * silently dropped the flow).
 */
export const STALE_AFTER_MS = 45_000;
/** How long to wait for the stream's snapshot before loading the list over plain HTTP instead. */
export const SNAPSHOT_FALLBACK_MS = 5_000;
export const RECONNECT_MIN_MS = 1_000;
export const RECONNECT_MAX_MS = 30_000;

interface DeletedRef {
  kind: string;
  namespace: string;
  name: string;
}

function managersFromBundle(bundle: PromotionStrategyDetails): CommitStatusManagerBundle {
  return {
    timedCommitStatuses: bundle.timedCommitStatuses,
    gitCommitStatuses: bundle.gitCommitStatuses,
    scheduledCommitStatuses: bundle.scheduledCommitStatuses,
    argoCDCommitStatuses: bundle.argoCDCommitStatuses,
    webRequestCommitStatuses: bundle.webRequestCommitStatuses,
    dependentsSuccessfulCommitStatuses: bundle.dependentsSuccessfulCommitStatuses,
  };
}

function bundleToItem<T extends CRDItem>(bundle: PromotionStrategyDetails): T {
  const ps = bundle.promotionStrategy;
  const environments = environmentsFromBundle(
    ps.spec,
    bundle.changeTransferPolicies ?? [],
    bundle.changeTransferPolicyHistories ?? [],
    bundle.restoreActiveCommits ?? [],
  );
  const psWithEnvironments = {
    ...ps,
    metadata: {
      ...ps.metadata,
      name: bundle.metadata.name,
      namespace: bundle.metadata.namespace,
    },
    status: { ...ps.status, environments },
  } as PromotionStrategy;
  sortStrategyCommitStatuses(psWithEnvironments);
  const merged = mergeCommitStatusManagers(psWithEnvironments, managersFromBundle(bundle));
  return {
    ...merged,
    enriched: enrichFromCRD(merged),
  } as T;
}

function itemKey(item: { metadata: { namespace?: string; name?: string } }): string {
  return `${item.metadata.namespace}/${item.metadata.name}`;
}

function stateFromBundles<T extends CRDItem>(bundles: PromotionStrategyDetails[]) {
  return {
    items: bundles.map((b) => bundleToItem<T>(b)),
    scmProviders: Object.fromEntries(
      bundles.map((b) => [itemKey(b), scmProviderSpecFromBundle(b)]),
    ),
  };
}

export function createCRDStore<T extends CRDItem>(kind: string, eventName: string) {
  let eventSource: EventSource | null = null;
  // Identifies the current subscription. Callbacks belonging to an earlier one compare their
  // own value against it and do nothing, so a closed stream can never touch the store.
  let generation = 0;
  // Whether the current subscription has received a snapshot from the stream.
  let synced = false;
  let reconnectDelay = RECONNECT_MIN_MS;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  let staleTimer: ReturnType<typeof setTimeout> | undefined;
  let fallbackTimer: ReturnType<typeof setTimeout> | undefined;

  const closeStream = () => {
    clearTimeout(reconnectTimer);
    clearTimeout(staleTimer);
    if (eventSource) {
      eventSource.close();
      eventSource = null;
    }
  };

  return create<{
    items: T[];
    // The namespace the items belong to, or null before the first subscription.
    namespace: string | null;
    // The SCM provider spec of each item's bundle, keyed by namespace/name. Kept off the items so
    // they stay the PromotionStrategy the manifest view shows.
    scmProviders: Record<string, ScmProviderSpec | undefined>;
    loading: boolean;
    error: string | null;
    connectionStatus: ConnectionStatus;
    fetchItems: (_ns: string) => Promise<void>;
    subscribe: (_ns: string) => void;
    unsubscribe: () => void;
    reset: () => void;
  }>((set, get) => {
    // Closes the stream and opens a new one after a delay that doubles on every attempt until a
    // snapshot arrives. The new stream starts with a snapshot, so whatever was missed is picked up.
    const reconnect = (namespace: string, gen: number) => {
      closeStream();
      set({ connectionStatus: 'error' });
      reconnectTimer = setTimeout(() => {
        if (gen === generation) connect(namespace, gen);
      }, reconnectDelay);
      reconnectDelay = Math.min(reconnectDelay * 2, RECONNECT_MAX_MS);
    };

    const connect = (namespace: string, gen: number) => {
      const source = new EventSource(
        `/watch?kind=${encodeURIComponent(kind)}&namespace=${encodeURIComponent(namespace)}`,
      );
      eventSource = source;

      // Called for everything the server sends. Only armed once the server has shown that it
      // sends heartbeats, so that silence means a dead connection.
      const heard = () => {
        clearTimeout(staleTimer);
        if (!synced) return;
        staleTimer = setTimeout(() => {
          if (gen === generation) reconnect(namespace, gen);
        }, STALE_AFTER_MS);
      };

      // Runs a handler for an event of the current subscription. A payload that cannot be
      // applied leaves the store in an unknown state, so the stream is restarted to resync.
      const on = (name: string, handle: (_data: string) => void) => {
        source.addEventListener(name, (evt: MessageEvent<string>) => {
          if (gen !== generation) return;
          try {
            handle(evt.data);
          } catch {
            // With nothing to show, say so rather than leave the page waiting.
            if (!get().items.length) set({ error: 'Failed to read live updates', loading: false });
            reconnect(namespace, gen);
            return;
          }
          heard();
        });
      };

      on(SNAPSHOT_EVENT, (data) => {
        const bundles = JSON.parse(data) as PromotionStrategyDetails[];
        synced = true;
        reconnectDelay = RECONNECT_MIN_MS;
        clearTimeout(fallbackTimer);
        set({
          ...stateFromBundles<T>(bundles),
          loading: false,
          error: null,
          connectionStatus: 'open',
        });
      });

      on(eventName, (data) => {
        const bundle = JSON.parse(data) as PromotionStrategyDetails;
        const updated = bundleToItem<T>(bundle);
        const key = itemKey(updated);
        set((state) => {
          const idx = state.items.findIndex((item) => itemKey(item) === key);
          const items = [...state.items];
          if (idx >= 0) {
            items[idx] = updated;
          } else {
            items.push(updated);
          }
          return {
            items,
            scmProviders: { ...state.scmProviders, [key]: scmProviderSpecFromBundle(bundle) },
          };
        });
      });

      on(DELETED_EVENT, (data) => {
        const ref = JSON.parse(data) as DeletedRef;
        if (ref.kind !== kind) return;
        const key = `${ref.namespace}/${ref.name}`;
        set((state) => {
          const scmProviders = { ...state.scmProviders };
          delete scmProviders[key];
          return { items: state.items.filter((item) => itemKey(item) !== key), scmProviders };
        });
      });

      // A heartbeat carries nothing; receiving it is the point.
      on(PING_EVENT, () => undefined);

      source.addEventListener('error', () => {
        if (gen !== generation) return;
        if (source.readyState === EventSource.CLOSED) {
          // The browser retries a dropped connection by itself, but gives up for good when the
          // server answers with an error status or another content type, such as a proxy's 502,
          // 503 or 504 page while the dashboard restarts. Keep trying.
          reconnect(namespace, gen);
        } else {
          set({ connectionStatus: 'error' });
        }
      });
    };

    return {
      items: [],
      namespace: null,
      scmProviders: {},
      loading: false,
      error: null,
      connectionStatus: 'connecting',

      // Loads the list over plain HTTP. The stream's snapshot is the primary source; this is the
      // fallback for when the stream cannot deliver one.
      fetchItems: async (namespace: string) => {
        const gen = generation;
        const superseded = () => gen !== generation || synced;
        if (!get().items.length) set({ loading: true, error: null });

        try {
          const res = await fetch(
            `/list?kind=${encodeURIComponent(kind)}&namespace=${encodeURIComponent(namespace)}`,
          );

          if (!res.ok) throw new Error(`Error: ${res.status}`);
          const data = (await res.json()) as PromotionStrategyDetails[] | null;

          // The stream has delivered a snapshot in the meantime, or the subscription changed.
          // Either way this response is older than what the store holds.
          if (superseded()) return;
          set({ ...stateFromBundles<T>(data ?? []), namespace, loading: false, error: null });
        } catch (err: unknown) {
          if (superseded()) return;
          const errorMessage = err instanceof Error ? err.message : 'Unknown error';
          set({ error: errorMessage, loading: false });
        }
      },

      // Opens the live stream for a namespace. The store then holds that namespace's current
      // state and follows it, across disconnects, until unsubscribe is called.
      subscribe: (namespace: string) => {
        closeStream();
        clearTimeout(fallbackTimer);
        const gen = ++generation;
        synced = false;
        reconnectDelay = RECONNECT_MIN_MS;

        if (get().namespace === namespace) {
          // Keep showing what is already there; the snapshot will bring it up to date.
          set({ connectionStatus: 'connecting' });
        } else {
          set({
            namespace,
            items: [],
            scmProviders: {},
            loading: true,
            error: null,
            connectionStatus: 'connecting',
          });
        }

        connect(namespace, gen);
        fallbackTimer = setTimeout(() => {
          if (gen === generation && !synced) void get().fetchItems(namespace);
        }, SNAPSHOT_FALLBACK_MS);
      },

      unsubscribe: () => {
        generation++;
        synced = false;
        closeStream();
        clearTimeout(fallbackTimer);
      },

      reset: () => set({ items: [], scmProviders: {}, namespace: null }),
    };
  });
}
