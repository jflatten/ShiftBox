import { useRef, useState } from 'react';
import {
  Button, Card, CloseButton, Group, Pagination, SegmentedControl, SimpleGrid, Stack, Table, Text, TextInput, Title,
} from '@mantine/core';
import { APIProblemView } from '../../components/api-problem';
import { providerLabel } from '../../lib/provider-label';
import { useLaunchProjects, type TemplateRevision } from '../templates/use-templates';
import styles from './launch-project-picker.module.css';

const PAGE_SIZE = 10;

export function LaunchProjectPicker({ userID, admin, onReview }: {
  userID: string;
  admin: boolean;
  onReview: (project: TemplateRevision) => void;
}) {
  const projects = useLaunchProjects(userID, admin);
  const [search, setSearch] = useState('');
  const [view, setView] = useState<'cards' | 'table'>('cards');
  const [page, setPage] = useState(1);
  const searchInput = useRef<HTMLInputElement>(null);
  const query = search.trim().toLowerCase();
  const matches = projects.data?.filter(project => project.name.toLowerCase().includes(query)) ?? [];
  const pageCount = Math.ceil(matches.length / PAGE_SIZE);
  const currentPage = Math.max(1, Math.min(page, pageCount));
  const start = (currentPage - 1) * PAGE_SIZE;
  const end = Math.min(start + PAGE_SIZE, matches.length);

  function changeSearch(value: string) {
    setSearch(value);
    setPage(1);
  }

  return (
    <Stack component="section" aria-labelledby="launch-projects-title" className={styles.surface} gap="md" miw={0}>
      <Title id="launch-projects-title" order={2} size="h3">Choose a project</Title>
      <Group justify="space-between" align="flex-end">
        <TextInput ref={searchInput} type="search" label="Search projects" placeholder="Search by project name"
          className={styles.search} size="md" value={search} onChange={event => changeSearch(event.currentTarget.value)}
          leftSection={<svg width="18" height="18" viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
            <circle cx="8.5" cy="8.5" r="5" /><path d="m12 12 4 4" />
          </svg>}
          rightSection={search ? <CloseButton aria-label="Clear search" onClick={() => {
            changeSearch('');
            searchInput.current?.focus();
          }} /> : null} />
        <SegmentedControl aria-label="Project view" size="md" value={view} onChange={setView}
          data={[{ label: 'Cards', value: 'cards' }, { label: 'Table', value: 'table' }]} />
      </Group>

      {projects.isError ? <Stack align="flex-start">
        <APIProblemView error={projects.error} />
        <Button variant="default" loading={projects.isFetching} onClick={() => void projects.refetch()}>Retry</Button>
      </Stack> : projects.isPending ? <Text role="status">Loading projects…</Text> : matches.length === 0 ? (
        <Text role="status" c="dimmed">{query ? 'No projects match your search.' : 'No projects are available to launch.'}</Text>
      ) : view === 'cards' ? (
        <>
          <Text size="sm" c="dimmed" role="status">{matches.length} {matches.length === 1 ? 'project' : 'projects'}</Text>
          <SimpleGrid type="container" cols={{ base: 1, '36rem': 2, '56rem': 3, '76rem': 4 }}>
            {matches.map(project => <ProjectCard key={project.id} project={project} onReview={onReview} />)}
          </SimpleGrid>
        </>
      ) : (
        <Stack gap={0}>
          <Table.ScrollContainer minWidth={680}>
            <Table aria-label="Projects" withTableBorder highlightOnHover verticalSpacing="sm">
              <Table.Thead><Table.Tr>
                <Table.Th scope="col">Project</Table.Th>
                <Table.Th scope="col">Repository</Table.Th>
                <Table.Th scope="col">Resource profile</Table.Th>
                <Table.Th scope="col" ta="right">Action</Table.Th>
              </Table.Tr></Table.Thead>
              <Table.Tbody>{matches.slice(start, end).map(project => (
                <Table.Tr key={project.id}>
                  <Table.Th scope="row">
                    <Text size="sm" fw={600}>{project.name}</Text>
                    <Text size="xs" c="dimmed">Revision {project.currentRevision}</Text>
                  </Table.Th>
                  <Table.Td>
                    <Text size="sm">{repositoryName(project)}</Text>
                    <Text size="xs" c="dimmed">{project.repositoryRef || 'Default branch'}</Text>
                  </Table.Td>
                  <Table.Td>
                    <Text size="sm">{project.resourceProfileName || 'Unavailable'}</Text>
                    {project.providerKind ? <Text size="xs" c="dimmed">{providerLabel(project.providerKind)}</Text> : null}
                  </Table.Td>
                  <Table.Td ta="right">
                    <Button variant="subtle" size="compact-sm" aria-label={`Review launch for ${project.name}`}
                      aria-haspopup="dialog" onClick={() => onReview(project)}>Review launch</Button>
                  </Table.Td>
                </Table.Tr>
              ))}</Table.Tbody>
            </Table>
          </Table.ScrollContainer>
          <Group justify="space-between" py="xs">
            <Text size="sm" c="dimmed" role="status">{start + 1}–{end} of {matches.length} {matches.length === 1 ? 'project' : 'projects'}</Text>
            <Pagination aria-label="Project pages" total={pageCount} value={currentPage} onChange={setPage} hideWithOnePage
              size="sm" siblings={0} getItemProps={value => ({ 'aria-label': `Page ${value}` })}
              getControlProps={control => ({ 'aria-label': control === 'next' ? 'Next page' : 'Previous page' })} />
          </Group>
        </Stack>
      )}
    </Stack>
  );
}

function ProjectCard({ project, onReview }: { project: TemplateRevision; onReview: (project: TemplateRevision) => void }) {
  const detailsId = `launch-project-${project.id}`;
  return (
    <Card component="button" type="button" withBorder padding="lg" className={styles.card}
      aria-label={`Review launch for ${project.name}`} aria-describedby={detailsId} aria-haspopup="dialog"
      onClick={() => onReview(project)}>
      <Text component="span" size="lg" fw={600}>{project.name}</Text>
      <Stack component="span" id={detailsId} gap="sm" mt={4}>
        <Text component="span" size="xs" c="dimmed">Revision {project.currentRevision}</Text>
        <Stack component="span" gap={2}>
          <Text component="span" size="sm" fw={500}>{repositoryName(project)}</Text>
          <Text component="span" size="xs" c="dimmed">{project.repositoryRef || 'Default branch'}</Text>
        </Stack>
        <Text component="span" size="sm" c="dimmed">
          {project.resourceProfileName || 'Profile unavailable'}{project.providerKind ? ` · ${providerLabel(project.providerKind)}` : ''}
        </Text>
      </Stack>
      <Group component="span" className={styles.cardAction} justify="space-between" gap="xs" wrap="nowrap">
        <Text component="span" size="sm" fw={600}>Review launch</Text>
        <svg width="18" height="18" viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
          <path d="M4 10h11m-5-5 5 5-5 5" />
        </svg>
      </Group>
    </Card>
  );
}

function repositoryName(project: TemplateRevision) {
  return project.repositoryUrl?.replace(/^https:\/\/github\.com\//i, '') || 'Repository unavailable';
}
