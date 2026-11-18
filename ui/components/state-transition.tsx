import { Box, Progress, Text, VisuallyHidden } from '@mantine/core';
import type { ReactNode } from 'react';
import classes from './state-transition.module.css';

type StateTransitionProps = {
  current: ReactNode;
  next: ReactNode | null;
  transitioning: boolean;
  compact?: boolean;
  paused?: boolean;
};

/** Renders a settled milestone or the current milestone and its transition. */
export function StateTransition({ current, next, transitioning, compact = false, paused = false }: StateTransitionProps) {
  const showNext = next !== null;

  return (
    <Box className={showNext ? classes.root : undefined} mod={{ compact }} role="status" aria-atomic="true">
      <StateLabel compact={compact} className={classes.currentLabel}>Current state</StateLabel>
      <Box className={classes.current}>{current}</Box>
      {transitioning ? (
        <Progress.Root className={classes.connector} size="xs" radius={0}
          role="progressbar" aria-label="State transition in progress">
          <Progress.Section value={100 / 3} withAria={false} color="var(--state-transition-color)" className={classes.segment} mod={{ paused }} />
        </Progress.Root>
      ) : null}
      {showNext ? (
        <>
          <StateLabel compact={compact} className={classes.nextLabel}>{transitioning ? 'Next state' : 'Transition result'}</StateLabel>
          <Box className={classes.next}>{next}</Box>
        </>
      ) : null}
    </Box>
  );
}

function StateLabel({ compact, className, children }: { compact: boolean; className: string; children: string }) {
  return compact ? <VisuallyHidden>{children}</VisuallyHidden> :
    <Text size="sm" c="dimmed" className={className}>{children}</Text>;
}
