'use client';

import { Suspense } from 'react';
import { Text } from '@mantine/core';
import { useSearchParams } from 'next/navigation';
import { LocalHelper } from './helper';

function HelperContent() {
  const search = useSearchParams();
  const query = search.toString();
  return <LocalHelper key={query} query={query} />;
}

export default function HelperPage() {
  return <Suspense fallback={<Text role="status">Loading the local helper setup…</Text>}><HelperContent /></Suspense>;
}
