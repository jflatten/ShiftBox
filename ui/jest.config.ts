import type { Config } from 'jest';
import nextJest from 'next/jest.js';

const config: Config = {
  testEnvironment: 'jsdom',
  setupFilesAfterEnv: ['<rootDir>/tests/setup.ts'],
  testMatch: ['<rootDir>/**/*.test.ts', '<rootDir>/**/*.test.tsx'],
  testPathIgnorePatterns: ['/node_modules/', '/out/', '/.next/'],
  moduleNameMapper: { '^@/(.*)$': '<rootDir>/$1' },
};

export default nextJest({ dir: './' })(config);
