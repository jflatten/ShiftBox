'use client';

import { createContext, useContext, useState, type ReactNode } from 'react';
import { MantineProvider, Modal, Text, createTheme } from '@mantine/core';
import { QueryClientProvider } from '@tanstack/react-query';
import { createQueryClient } from '../lib/query-client';

const theme = createTheme({ primaryColor: 'violet', defaultRadius: 'sm', fontFamily: 'system-ui, sans-serif' });
const ControllerUnavailable = createContext(false);

export function useControllerUnavailable() {
  return useContext(ControllerUnavailable);
}

export function Providers({ children }: { children: ReactNode }) {
  const [unavailable, setUnavailable] = useState(false);
  const [client] = useState(() => createQueryClient(setUnavailable));
  return (
    <MantineProvider theme={theme} defaultColorScheme="auto">
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
