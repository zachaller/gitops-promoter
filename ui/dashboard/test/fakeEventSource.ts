/**
 * A stand-in for the browser's EventSource that tests drive by hand: they play the server by
 * emitting events on the instances the code under test creates.
 */
export class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;

  /** Every instance created since the last reset, oldest first. */
  static instances: FakeEventSource[] = [];

  static reset() {
    FakeEventSource.instances = [];
  }

  /** The most recently created instance. */
  static get latest(): FakeEventSource {
    const latest = FakeEventSource.instances.at(-1);
    if (!latest) throw new Error('no EventSource was created');
    return latest;
  }

  readyState = FakeEventSource.CONNECTING;
  private readonly listeners = new Map<string, ((_evt: { data: string }) => void)[]>();

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(name: string, listener: (_evt: { data: string }) => void) {
    this.listeners.set(name, [...(this.listeners.get(name) ?? []), listener]);
  }

  close() {
    this.readyState = FakeEventSource.CLOSED;
  }

  /** Delivers an event as the server would send it. Objects are sent as JSON. */
  emit(name: string, payload: unknown = {}) {
    const data = typeof payload === 'string' ? payload : JSON.stringify(payload);
    for (const listener of this.listeners.get(name) ?? []) listener({ data });
  }

  /** The connection dropped and the browser is retrying on its own. */
  drop() {
    this.readyState = FakeEventSource.CONNECTING;
    this.emit('error');
  }

  /** The browser gave up, as it does when the server answers with an error status. */
  giveUp() {
    this.readyState = FakeEventSource.CLOSED;
    this.emit('error');
  }
}

/** A minimal bundle. The repository name doubles as a revision marker. */
export function testBundle(name: string, revision: string, namespace = 'ns') {
  return {
    apiVersion: 'view.promoter.argoproj.io/v1alpha1',
    kind: 'PromotionStrategyDetails',
    metadata: { name, namespace },
    promotionStrategy: {
      metadata: { name, namespace },
      spec: { gitRepositoryRef: { name: revision }, environments: [] },
    },
  };
}
