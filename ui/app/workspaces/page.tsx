'use client';

import { Suspense } from 'react';
import { useSearchParams } from 'next/navigation';
import { Text } from '@mantine/core';
import { Workspaces } from '../../features/workspaces/workspaces';

function SelectedWorkspace() {
  const search = useSearchParams();
  return <Workspaces workspaceId={search.get('id')} ownerId={search.get('owner')} launch={search.get('launch') === '1'} />;
}

export default function WorkspacesPage() {
  return <Suspense fallback={<Text role="status">Loading workspaces…</Text>}><SelectedWorkspace /></Suspense>;
}
