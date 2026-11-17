import '@testing-library/jest-dom';

// Mantine queries browser layout APIs that jsdom does not implement.
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener() {},
    removeListener() {},
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent() { return false; },
  }),
});
global.ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};
// Mantine scrolls the selected combobox option into view when reopening it.
Element.prototype.scrollIntoView = () => {};
