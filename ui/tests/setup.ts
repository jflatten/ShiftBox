import '@testing-library/jest-dom';

// Component tests mount client screens without a Next.js route provider.
jest.mock('next/navigation', () => ({
  ...jest.requireActual('next/navigation'),
  useRouter: () => ({ push: jest.fn() }),
}));

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
