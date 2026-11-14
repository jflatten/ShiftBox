'use client';

import { Suspense } from 'react';
import { useSearchParams } from 'next/navigation';
import { Text } from '@mantine/core';
import { Workspaces } from '../../features/workspaces/workspaces';

function SelectedWorkspace() {
  return <Workspaces workspaceId={useSearchParams().get('id')} />;
}

export default function WorkspacesPage() {
  return <Suspense fallback={<Text role="status">Loading workspaces…</Text>}><SelectedWorkspace /></Suspense>;
}
