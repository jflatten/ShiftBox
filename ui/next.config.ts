import type { NextConfig } from 'next';

// Go serves the exported files; the product has no Node server.
const config: NextConfig = {
  output: 'export',
  trailingSlash: true,
  images: { unoptimized: true },
};

export default config;
