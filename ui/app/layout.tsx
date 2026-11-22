import '@mantine/core/styles.css';
import { ColorSchemeScript, mantineHtmlProps } from '@mantine/core';
import type { Viewport } from 'next';
import type { ReactNode } from 'react';
import brandColors from '../lib/brand-colors.json';
import { Providers } from './providers';

export const metadata = {
  title: 'ShiftBox',
  icons: { icon: '/brand/favicon.svg', apple: '/brand/apple-touch-icon.png' },
};

export const viewport: Viewport = { themeColor: brandColors.violet };

export default function RootLayout({ children }: { children: ReactNode }) {
  return <html lang="en" {...mantineHtmlProps}><head><ColorSchemeScript defaultColorScheme="auto" /></head><body><Providers>{children}</Providers></body></html>;
}
