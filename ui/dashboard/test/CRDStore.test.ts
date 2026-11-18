import { describe, it, beforeEach, afterEach, expect, vi } from 'vitest';
import type { PromotionStrategy } from '@shared/utils/PSData';
import {
  createCRDStore,
  RECONNECT_MIN_MS,
  SNAPSHOT_FALLBACK_MS,
  STALE_AFTER_MS,
} from '../src/stores/CRDStore';
import { FakeEventSource, testBundle } from './fakeEventSource';

const KIND = 'PromotionStrategyDetails';

describe('CRDStore live updates', () => {
  let store: ReturnType<typeof createCRDStore<PromotionStrategy>>;
  let fetchMock: ReturnType<typeof vi.fn>;

  /** What the store holds, as name=revision. */
  const contents = () =>
    store.getState().items.map((ps) => `${ps.metadata.name}=${ps.spec.gitRepositoryRef.name}`);

  const listResponse = (bundles: unknown[]) => ({ ok: true, json: () => Promise.resolve(bundles) });

  beforeEach(() => {
    vi.useFakeTimers();
    FakeEventSource.reset();
    vi.stubGlobal('EventSource', FakeEventSource);
    fetchMock = vi.fn(() => Promise.resolve(listResponse([])));
    vi.stubGlobal('fetch', fetchMock);
    store = createCRDStore<PromotionStrategy>(KIND, KIND);
  });

  afterEach(() => {
    store.getState().unsubscribe();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('loads its contents from the snapshot the stream opens with', () => {
    store.getState().subscribe('ns');

    expect(FakeEventSource.latest.url).toBe(`/watch?kind=${KIND}&namespace=ns`);
    expect(store.getState().loading).toBe(true);
    expect(store.getState().connectionStatus).toBe('connecting');

    FakeEventSource.latest.emit('snapshot', [testBundle('a', 'rev-1'), testBundle('b', 'rev-1')]);

    expect(contents()).toEqual(['a=rev-1', 'b=rev-1']);
    expect(store.getState().loading).toBe(false);
    expect(store.getState().connectionStatus).toBe('open');
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('applies updates, additions and deletions', () => {
    store.getState().subscribe('ns');
    const stream = FakeEventSource.latest;
    stream.emit('snapshot', [testBundle('a', 'rev-1'), testBundle('b', 'rev-1')]);

    stream.emit(KIND, testBundle('a', 'rev-2'));
    stream.emit(KIND, testBundle('c', 'rev-1'));
    expect(contents()).toEqual(['a=rev-2', 'b=rev-1', 'c=rev-1']);

    stream.emit('deleted', { kind: KIND, namespace: 'ns', name: 'b' });
    expect(contents()).toEqual(['a=rev-2', 'c=rev-1']);
    expect(Object.keys(store.getState().scmProviders)).toEqual(['ns/a', 'ns/c']);
    expect(store.getState().error).toBeNull();
  });

  it('ignores the deletion of an object of another kind', () => {
    store.getState().subscribe('ns');
    FakeEventSource.latest.emit('snapshot', [testBundle('a', 'rev-1')]);

    FakeEventSource.latest.emit('deleted', { kind: 'SomethingElse', namespace: 'ns', name: 'a' });

    expect(contents()).toEqual(['a=rev-1']);
  });

  it('catches up from the snapshot after the browser reconnects', () => {
    store.getState().subscribe('ns');
    const stream = FakeEventSource.latest;
    stream.emit('snapshot', [testBundle('a', 'rev-1'), testBundle('b', 'rev-1')]);

    stream.drop();
    expect(store.getState().connectionStatus).toBe('error');

    // While it was away, a changed and b was deleted. The browser reuses the same EventSource.
    stream.emit('snapshot', [testBundle('a', 'rev-2')]);

    expect(contents()).toEqual(['a=rev-2']);
    expect(store.getState().connectionStatus).toBe('open');
    expect(FakeEventSource.instances).toHaveLength(1);
  });

  it('opens a new connection when the browser gives up, backing off until one works', () => {
    store.getState().subscribe('ns');
    FakeEventSource.latest.emit('snapshot', [testBundle('a', 'rev-1')]);

    FakeEventSource.latest.giveUp();
    expect(store.getState().connectionStatus).toBe('error');
    vi.advanceTimersByTime(RECONNECT_MIN_MS - 1);
    expect(FakeEventSource.instances).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeEventSource.instances).toHaveLength(2);

    // Still failing: the next attempt waits twice as long.
    FakeEventSource.latest.giveUp();
    vi.advanceTimersByTime(RECONNECT_MIN_MS * 2 - 1);
    expect(FakeEventSource.instances).toHaveLength(2);
    vi.advanceTimersByTime(1);
    expect(FakeEventSource.instances).toHaveLength(3);

    FakeEventSource.latest.emit('snapshot', [testBundle('a', 'rev-2')]);
    expect(contents()).toEqual(['a=rev-2']);
    expect(store.getState().connectionStatus).toBe('open');

    // A snapshot resets the backoff.
    FakeEventSource.latest.giveUp();
    vi.advanceTimersByTime(RECONNECT_MIN_MS);
    expect(FakeEventSource.instances).toHaveLength(4);
  });

  it('reconnects when the heartbeat stops', () => {
    store.getState().subscribe('ns');
    const stream = FakeEventSource.latest;
    stream.emit('snapshot', [testBundle('a', 'rev-1')]);

    // Heartbeats keep the connection considered alive.
    for (let i = 0; i < 10; i++) {
      vi.advanceTimersByTime(STALE_AFTER_MS - 1);
      stream.emit('ping');
    }
    expect(FakeEventSource.instances).toHaveLength(1);

    // Then the connection goes quiet without the browser reporting anything.
    vi.advanceTimersByTime(STALE_AFTER_MS);
    expect(stream.readyState).toBe(FakeEventSource.CLOSED);
    expect(store.getState().connectionStatus).toBe('error');
    vi.advanceTimersByTime(RECONNECT_MIN_MS);
    expect(FakeEventSource.instances).toHaveLength(2);
  });

  it('restarts the stream when an event cannot be applied', () => {
    store.getState().subscribe('ns');
    const stream = FakeEventSource.latest;
    stream.emit('snapshot', [testBundle('a', 'rev-1')]);

    stream.emit(KIND, 'not json');

    expect(contents()).toEqual(['a=rev-1']);
    expect(store.getState().error).toBeNull();
    expect(stream.readyState).toBe(FakeEventSource.CLOSED);
    vi.advanceTimersByTime(RECONNECT_MIN_MS);
    expect(FakeEventSource.instances).toHaveLength(2);
  });

  it('reports a snapshot it cannot read instead of loading forever', () => {
    store.getState().subscribe('ns');

    FakeEventSource.latest.emit('snapshot', 'not json');

    expect(store.getState().loading).toBe(false);
    expect(store.getState().error).toBe('Failed to read live updates');

    // The next snapshot that can be read clears the error.
    vi.advanceTimersByTime(RECONNECT_MIN_MS);
    FakeEventSource.latest.emit('snapshot', [testBundle('a', 'rev-1')]);
    expect(store.getState().error).toBeNull();
    expect(contents()).toEqual(['a=rev-1']);
  });

  it('falls back to /list when the stream delivers no snapshot', async () => {
    fetchMock.mockResolvedValue(listResponse([testBundle('a', 'rev-1')]));
    store.getState().subscribe('ns');

    await vi.advanceTimersByTimeAsync(SNAPSHOT_FALLBACK_MS);

    expect(fetchMock).toHaveBeenCalledWith(`/list?kind=${KIND}&namespace=ns`);
    expect(contents()).toEqual(['a=rev-1']);
    expect(store.getState().loading).toBe(false);
  });

  it('does not let a late /list response overwrite the snapshot', async () => {
    let respond: (_value: unknown) => void = () => undefined;
    fetchMock.mockReturnValue(new Promise((resolve) => (respond = resolve)));
    store.getState().subscribe('ns');
    await vi.advanceTimersByTimeAsync(SNAPSHOT_FALLBACK_MS);
    expect(fetchMock).toHaveBeenCalledTimes(1);

    FakeEventSource.latest.emit('snapshot', [testBundle('a', 'rev-2')]);
    respond(listResponse([testBundle('a', 'rev-1')]));
    await vi.advanceTimersByTimeAsync(0);

    expect(contents()).toEqual(['a=rev-2']);
  });

  it('ignores a stream it has unsubscribed from', () => {
    store.getState().subscribe('ns');
    const stream = FakeEventSource.latest;
    stream.emit('snapshot', [testBundle('a', 'rev-1')]);

    store.getState().unsubscribe();
    expect(stream.readyState).toBe(FakeEventSource.CLOSED);

    stream.emit(KIND, testBundle('a', 'rev-2'));
    stream.giveUp();
    vi.advanceTimersByTime(STALE_AFTER_MS * 2);

    expect(contents()).toEqual(['a=rev-1']);
    expect(FakeEventSource.instances).toHaveLength(1);
  });

  it('keeps showing a namespace while re-subscribing to it, and clears it for another', () => {
    store.getState().subscribe('ns');
    FakeEventSource.latest.emit('snapshot', [testBundle('a', 'rev-1')]);

    store.getState().unsubscribe();
    store.getState().subscribe('ns');
    expect(contents()).toEqual(['a=rev-1']);
    expect(store.getState().loading).toBe(false);

    store.getState().subscribe('other');
    expect(contents()).toEqual([]);
    expect(store.getState().loading).toBe(true);
    expect(FakeEventSource.latest.url).toBe(`/watch?kind=${KIND}&namespace=other`);
  });
});
