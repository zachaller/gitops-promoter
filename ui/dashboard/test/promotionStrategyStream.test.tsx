import { describe, it, beforeEach, afterEach, expect, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router';
import type { PromotionExplorerProps } from '@lib/components/PromotionExplorer/PromotionExplorer';
import { FakeEventSource, testBundle } from './fakeEventSource';

vi.mock('@lib/components/PromotionExplorer/PromotionExplorer', () => ({
  default: (props: PromotionExplorerProps) => (
    <div data-testid="explorer">{props.strategy?.spec.gitRepositoryRef.name}</div>
  ),
}));

vi.mock('@lib/components/LiveManifestView', () => ({
  LiveManifestView: () => <div data-testid="manifest" />,
}));

vi.stubGlobal('EventSource', FakeEventSource);
const fetchMock = vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve([]) }));
vi.stubGlobal('fetch', fetchMock);

const { AppRoutes } = await import('../src/App');

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const KIND = 'PromotionStrategyDetails';

describe('PromotionStrategyPage live updates', () => {
  let container: HTMLDivElement;
  let root: Root;

  const renderAt = async (url: string) => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={[url]}>
          <AppRoutes />
        </MemoryRouter>,
      );
    });
  };

  const emit = async (name: string, payload: unknown) => {
    await act(async () => FakeEventSource.latest.emit(name, payload));
  };

  beforeEach(() => {
    FakeEventSource.reset();
    fetchMock.mockClear();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  it('follows updates over a single connection', async () => {
    await renderAt('/promotion-strategies/ns/app-a');
    expect(container.textContent).toContain('Loading promotion strategies');

    await emit('snapshot', [testBundle('app-a', 'rev-1'), testBundle('app-b', 'rev-1')]);
    expect(container.querySelector('[data-testid="explorer"]')?.textContent).toBe('rev-1');

    // Every update re-renders the page. None of them may reopen the stream: events that
    // arrive while it is reconnecting are lost.
    for (let rev = 2; rev <= 6; rev++) {
      await emit(KIND, testBundle('app-a', `rev-${rev}`));
      await emit(KIND, testBundle('app-b', `rev-${rev}`));
    }

    expect(container.querySelector('[data-testid="explorer"]')?.textContent).toBe('rev-6');
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('reports a strategy that does not exist without reloading in a loop', async () => {
    await renderAt('/promotion-strategies/ns/missing');
    await emit('snapshot', [testBundle('app-a', 'rev-1')]);
    await emit(KIND, testBundle('app-a', 'rev-2'));

    expect(container.textContent).toContain("couldn't find a promotion strategy named missing");
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('closes the stream when the page goes away', async () => {
    await renderAt('/promotion-strategies/ns/app-a');
    const stream = FakeEventSource.latest;

    await act(async () => root.render(<div />));

    expect(stream.readyState).toBe(FakeEventSource.CLOSED);
  });
});
