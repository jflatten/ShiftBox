import { formatKeepAlive, parseKeepAlive, parseKeepAliveOptions } from './keep-alive';

// Administrator duration input must accept the requested units and Unlimited
// without silently accepting fractional, overflowing, or ambiguous durations.
test('parses minute, hour, day and unlimited keep-alive values', () => {
  expect(parseKeepAlive('30m')).toBe(30);
  expect(parseKeepAlive('2h')).toBe(120);
  expect(parseKeepAlive('1d')).toBe(1440);
  expect(parseKeepAlive('15 minutes')).toBe(15);
  expect(parseKeepAlive('Unlimited')).toBe(0);
  for (const value of ['', '0m', '-5m', '1.5h', '1h30m', '60', '999999999999999999d']) {
    expect(parseKeepAlive(value)).toBeNull();
  }
  expect(formatKeepAlive(0)).toBe('Unlimited');
  expect(formatKeepAlive(30)).toBe('30 minutes');
  expect(formatKeepAlive(60)).toBe('1 hour');
  expect(formatKeepAlive(1440)).toBe('1 day');
});

// Equivalent entries must not create duplicate user choices; malformed or empty
// lists must be rejected before an administrator mutation is sent.
test('normalizes and validates administrator choice lists', () => {
  expect(parseKeepAliveOptions('30m, 1h, 2h, 4h, 8h, 12h, Unlimited')).toEqual([30, 60, 120, 240, 480, 720, 0]);
  expect(parseKeepAliveOptions('1h, 60m')).toBeNull();
  expect(parseKeepAliveOptions('30m, ')).toBeNull();
  expect(parseKeepAliveOptions('')).toBeNull();
});
