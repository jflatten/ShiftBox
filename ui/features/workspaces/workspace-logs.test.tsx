import { MantineProvider } from '@mantine/core';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { WorkspaceLogs } from './workspace-logs';

class LogStream extends EventTarget {
  static latest: LogStream;
  static CLOSED = 2;
  readonly readyState = 0;
  readonly close = jest.fn();
  constructor(readonly url: string) {
    super();
    LogStream.latest = this;
  }

  output(id: string, text: string) {
    this.dispatchEvent(new MessageEvent('workspace.log', { lastEventId: id, data: JSON.stringify({ text }) }));
  }
}

// A reconnect can replay a batch. Keep output ordered and render guest markup
// as literal text, then release the connection when the panel closes.
test('follows log updates across reconnects and closes on unmount', () => {
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'EventSource');
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: LogStream });
  try {
    const { unmount } = render(<MantineProvider><WorkspaceLogs workspaceId="workspace-1" /></MantineProvider>);
    const stream = LogStream.latest;
    expect(stream.url).toBe('/api/v1/workspaces/workspace-1/logs');
    act(() => {
      stream.dispatchEvent(new Event('open'));
      stream.output('1', 'Building image\n');
    });
    expect(screen.getByText('Live')).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: 'Pause auto-scroll' }));
    act(() => {
      stream.dispatchEvent(new Event('error'));
      stream.output('1', 'Building image\n');
      stream.output('2', '<img src=x onerror=alert(1)>\n');
    });
    expect(screen.getByText('Reconnecting…')).toBeVisible();
    const output = screen.getByRole('region', { name: 'Dev Container output' });
    expect(output.textContent).toBe('Building image\n<img src=x onerror=alert(1)>\n');
    expect(output.querySelector('img')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Resume auto-scroll' }));
    unmount();
    expect(stream.close).toHaveBeenCalledTimes(1);
  } finally {
    if (previous) Object.defineProperty(globalThis, 'EventSource', previous);
    else Reflect.deleteProperty(globalThis, 'EventSource');
  }
});
