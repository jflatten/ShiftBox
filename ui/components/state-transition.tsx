import { Box, Progress, Text, VisuallyHidden } from '@mantine/core';
import type { ReactNode } from 'react';
import classes from './state-transition.module.css';

type StateTransitionProps = {
  observed: ReactNode;
  desired: ReactNode;
  transitioning: boolean;
  compact?: boolean;
};

/** Shows the observed and desired state supplied by the caller's live query. */
export function StateTransition({ observed, desired, transitioning, compact = false }: StateTransitionProps) {
  const showDesired = !compact || transitioning;

  return (
    <Box className={showDesired ? classes.root : undefined} mod={{ compact }} role="status" aria-atomic="true">
      <StateLabel compact={compact} className={classes.observedLabel}>Observed state</StateLabel>
      <Box className={classes.observed}>{observed}</Box>
      {transitioning ? (
        <Progress.Root className={classes.connector} size="xs" radius={0}
          role="progressbar" aria-label="State transition in progress">
          <Progress.Section value={100 / 3} withAria={false} color="var(--state-transition-color)" className={classes.segment} />
        </Progress.Root>
      ) : null}
      {showDesired ? (
        <>
          <StateLabel compact={compact} className={classes.desiredLabel}>Desired state</StateLabel>
          <Box className={classes.desired}>{desired}</Box>
        </>
      ) : null}
    </Box>
  );
}

function StateLabel({ compact, className, children }: { compact: boolean; className: string; children: string }) {
  return compact ? <VisuallyHidden>{children}</VisuallyHidden> :
    <Text size="sm" c="dimmed" className={className}>{children}</Text>;
}
