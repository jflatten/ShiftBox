'use client';

import { useState } from 'react';
import { Alert, Stack, Stepper, Text } from '@mantine/core';
import type { components } from '../lib/api-types';

/** The durable operation fields needed to display progress; no client-owned progress state. */
export interface WorkflowOperation {
  id: string;
  status: 'pending' | 'running' | 'waiting' | 'succeeded' | 'failed' | 'cancelled';
  currentStep?: string | null;
  steps: {
    key: string;
    index: number;
    label: string;
    status: 'pending' | 'active' | 'succeeded' | 'failed' | 'skipped';
    detail?: string | null;
    problem?: { title: string; detail?: string } | null;
    timeout?: components['schemas']['OperationStep']['timeout'];
  }[];
}

const stepLabels = { pending: 'Pending', active: 'In progress', succeeded: 'Completed', failed: 'Failed', skipped: 'Skipped' };
const operationLabels = { pending: 'Pending', running: 'In progress', waiting: 'Waiting', succeeded: 'Completed', failed: 'Failed', cancelled: 'Cancelled' };

function stepDescription(step: WorkflowOperation['steps'][number]) {
  const status = stepLabels[step.status];
  if (!step.timeout) return status;
  if (step.status === 'active') return 'Timed out · Still checking';
  return step.status === 'succeeded' ? 'Completed after timeout' : `${status} · Timed out`;
}

/** WorkflowStepper displays server progress and lets users inspect reached steps. */
export function WorkflowStepper({ operation, failureTitle }: { operation: WorkflowOperation; failureTitle?: string }) {
  const [inspected, setInspected] = useState<{ operationId: string; key: string } | null>(null);
  const steps = [...operation.steps].sort((a, b) => a.index - b.index);
  const current = steps.findIndex(step => step.key === operation.currentStep);
  const unfinished = steps.findIndex(step => step.status !== 'succeeded' && step.status !== 'skipped');
  const active = operation.status === 'succeeded' ? steps.length : current >= 0 ? current : unfinished >= 0 ? unfinished : steps.length;
  const selected = inspected?.operationId === operation.id ? steps.find(step => step.key === inspected.key && step.status !== 'pending') : undefined;
  const currentDetail = steps[active]?.detail;
  // A worker briefly claims an active step as running on each retry. The
  // persisted wait reason remains until the step advances or finishes.
  const status = operation.status === 'running' && steps[active]?.status === 'active' && currentDetail
    ? 'waiting'
    : operation.status;
  const showWarning = status === 'failed' && !!failureTitle;
  const failedStep = showWarning ? steps.find(step => step.status === 'failed') ?? steps[active] : undefined;
  const failureDetails = new Set([failedStep?.label, failedStep?.problem?.title, failedStep?.detail, failedStep?.problem?.detail]);

  return (
    <Stack gap="md">
      {showWarning ? <Alert color="yellow" title={failureTitle}>
        <Stack gap={4}>
          {[...failureDetails].map(detail => detail ? <Text key={detail} size="sm">{detail}</Text> : null)}
        </Stack>
      </Alert> : <Text role="status" aria-live="polite" aria-atomic="true">
        {steps[active]?.status === 'active' && steps[active]?.timeout ? 'Timed out · Still checking' : operationLabels[status]}
        {steps[active] ? ` — ${steps[active].label}` : ''}
      </Text>}
      <Stepper active={active} allowNextStepsSelect={false} orientation="vertical" onStepClick={index => {
        const step = steps[index];
        if (step && step.status !== 'pending') setInspected({ operationId: operation.id, key: step.key });
      }}>
        {steps.map((step, index) => (
          <Stepper.Step
            key={step.key}
            label={step.label}
            description={stepDescription(step)}
            aria-label={`${step.label}: ${stepDescription(step)}`}
            aria-current={index === active ? 'step' : undefined}
            disabled={step.status === 'pending'}
            allowStepSelect={step.status !== 'pending'}
            loading={step.status === 'active' && status === 'running' && !step.timeout}
            color={step.status === 'failed' ? 'red' : undefined}
            completedIcon={step.status === 'skipped' ? '–' : step.status === 'failed' ? '!' : undefined}
          />
        ))}
      </Stepper>
      {currentDetail && steps[active] !== failedStep && currentDetail !== steps[active]?.timeout?.reason ? <Text size="sm">{currentDetail}</Text> : null}
      {selected?.detail && selected !== failedStep && selected.key !== steps[active]?.key && selected.detail !== selected.timeout?.reason
        ? <Text size="sm">{selected.detail}</Text> : null}
      {steps.map(step => step.timeout ? (
        <Alert key={step.key} color="yellow" title={`${step.label}: Timed out`}>{step.timeout.reason}</Alert>
      ) : null)}
      {steps.filter(step => step.problem && step !== failedStep).map(step => (
        <Alert key={step.key} color="red" title={`${step.label}: ${step.problem!.title}`}>
          {step.problem!.detail}
        </Alert>
      ))}
    </Stack>
  );
}
