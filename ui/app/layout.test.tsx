import { viewport } from './layout';

// The application declares the approved browser chrome color on every route.
test('uses the brand violet as the browser theme color', () => {
  expect(viewport?.themeColor).toBe('#4834F5');
});
