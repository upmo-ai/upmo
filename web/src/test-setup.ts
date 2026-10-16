// Mantine reads matchMedia, which jsdom does not provide.
Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }),
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
