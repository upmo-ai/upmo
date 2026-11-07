// Mantine reads matchMedia, which jsdom does not provide. Min-width queries
// are answered from window.innerWidth (1024 in jsdom, a desktop width), so a
// test can pick a layout by setting innerWidth before it renders.
const rootFontSize = 16;

function matches(query: string) {
  const min = /\(min-width:\s*([\d.]+)(em|px)\)/.exec(query);
  if (!min) return false;
  const width = Number(min[1]) * (min[2] === "em" ? rootFontSize : 1);
  return window.innerWidth >= width;
}

type Listener = (event: { matches: boolean; media: string }) => void;

const watchers = new Set<{ query: string; matches: boolean; listeners: Set<Listener> }>();

// A resize event re-evaluates every live query, so a test can change
// innerWidth, dispatch "resize" and see the layout follow.
window.addEventListener("resize", () => {
  for (const watcher of watchers) {
    const next = matches(watcher.query);
    if (next === watcher.matches) continue;
    watcher.matches = next;
    for (const listener of watcher.listeners) listener({ matches: next, media: watcher.query });
  }
});

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: (query: string) => {
    const watcher = { query, matches: matches(query), listeners: new Set<Listener>() };
    watchers.add(watcher);
    return {
      get matches() {
        return matches(query);
      },
      media: query,
      onchange: null,
      addListener: (listener: Listener) => watcher.listeners.add(listener),
      removeListener: (listener: Listener) => watcher.listeners.delete(listener),
      addEventListener: (_type: string, listener: Listener) => watcher.listeners.add(listener),
      removeEventListener: (_type: string, listener: Listener) => watcher.listeners.delete(listener),
      dispatchEvent: () => false,
    };
  },
});

// SegmentedControl measures its indicator with ResizeObserver.
globalThis.ResizeObserver = class ResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
} as typeof ResizeObserver;

// Textarea autosize listens for font loads on document.fonts, which jsdom lacks.
if (!("fonts" in document)) {
  Object.defineProperty(document, "fonts", {
    value: { addEventListener() {}, removeEventListener() {} },
  });
}

// The Select dropdown scrolls the active option into view, which jsdom lacks.
Element.prototype.scrollIntoView ??= function scrollIntoView() {};

// ECharts (inside the design-system charts) measures text on a canvas. jsdom has none, logs "not implemented" for every call, and the charts then fall back to their own estimate, so answer quietly with no context.
HTMLCanvasElement.prototype.getContext = (() => null) as typeof HTMLCanvasElement.prototype.getContext;
