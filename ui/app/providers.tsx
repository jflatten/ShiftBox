'use client';

import { createContext, useContext, useState, type ReactNode } from 'react';
import { MantineProvider, Modal, Text, createTheme, type CSSVariablesResolver } from '@mantine/core';
import { QueryClientProvider } from '@tanstack/react-query';
import { createQueryClient } from '../lib/query-client';

const theme = createTheme({ primaryColor: 'violet', defaultRadius: 'sm', fontFamily: 'system-ui, sans-serif' });
const textColors: CSSVariablesResolver = () => ({
  variables: {},
  light: {
    '--mantine-color-dimmed': 'var(--mantine-color-gray-7)',
    '--shiftbox-color-success-text': '#0e6027', // Carbon Green 70
  },
  dark: {
    '--mantine-color-dimmed': 'var(--mantine-color-dark-1)',
    '--shiftbox-color-success-text': 'var(--mantine-color-green-4)',
  },
});
const ControllerUnavailable = createContext(false);

export function useControllerUnavailable() {
  return useContext(ControllerUnavailable);
}

export function Providers({ children }: { children: ReactNode }) {
  const [unavailable, setUnavailable] = useState(false);
  const [client] = useState(() => createQueryClient(setUnavailable));
  return (
    <MantineProvider theme={theme} cssVariablesResolver={textColors} defaultColorScheme="auto">
      <QueryClientProvider client={client}>
        <ControllerUnavailable value={unavailable}>
          {children}
          <Modal opened={unavailable} onClose={() => {}} title="Controller unavailable" centered
            withCloseButton={false} closeOnClickOutside={false} closeOnEscape={false} transitionProps={{ duration: 0 }}>
            <Text tabIndex={-1} data-autofocus>Trying to reconnect to ShiftBox…</Text>
          </Modal>
        </ControllerUnavailable>
      </QueryClientProvider>
    </MantineProvider>
  );
}
