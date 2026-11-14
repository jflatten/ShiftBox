import '@mantine/core/styles.css';
import { ColorSchemeScript, mantineHtmlProps } from '@mantine/core';
import type { ReactNode } from 'react';
import { Providers } from './providers';

export const metadata = {
  title: 'ShiftBox',
  icons: { icon: '/brand/favicon.svg', apple: '/brand/apple-touch-icon.png' },
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return <html lang="en" {...mantineHtmlProps}><head><ColorSchemeScript defaultColorScheme="auto" /></head><body><Providers>{children}</Providers></body></html>;
}
