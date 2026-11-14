'use client';

import { useState, type ReactNode } from 'react';
import { MantineProvider, createTheme } from '@mantine/core';
import { QueryClientProvider } from '@tanstack/react-query';
import { createQueryClient } from '../lib/query-client';

const theme = createTheme({ primaryColor: 'violet', defaultRadius: 'sm', fontFamily: 'system-ui, sans-serif' });

export function Providers({ children }: { children: ReactNode }) {
  const [client] = useState(createQueryClient);
  return <MantineProvider theme={theme} defaultColorScheme="auto"><QueryClientProvider client={client}>{children}</QueryClientProvider></MantineProvider>;
}
