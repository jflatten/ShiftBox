import { useRef, useState, type ReactNode } from 'react';
import {
  Badge, Button, Card, CloseButton, Group, Pagination, SegmentedControl, SimpleGrid, Stack, Switch, Table, Text,
  TextInput, Title, UnstyledButton,
} from '@mantine/core';
import { APIProblemView } from '../../components/api-problem';
import { providerLabel } from '../../lib/provider-label';
import type { useProjectCatalog, TemplateRevision } from './use-templates';
import styles from './project-catalog.module.css';

const PAGE_SIZE = 10;

interface ProjectManagement {
  userID: string;
  archived: boolean;
  canCreate: boolean;
  onArchivedChange?: (archived: boolean) => void;
  actions: (project: TemplateRevision) => ReactNode;
}

type ProjectAction = (project: TemplateRevision, control: HTMLButtonElement) => void;

export function ProjectCatalog({ catalog, onProjectAction, management }: {
  catalog: ReturnType<typeof useProjectCatalog>;
  onProjectAction: ProjectAction;
  management?: ProjectManagement;
}) {
  const [search, setSearch] = useState('');
  const [view, setView] = useState<'cards' | 'table'>('cards');
  const [page, setPage] = useState(1);
  const searchInput = useRef<HTMLInputElement>(null);
  const query = search.trim().toLowerCase();
  const matches = catalog.data?.filter(project => project.name.toLowerCase().includes(query) ||
    (management && project.repositoryUrl?.toLowerCase().includes(query))) ?? [];
  const empty = management ? management.archived ? 'No archived projects.' : 'No projects yet.' : 'No projects are available to launch.';
  const pageCount = Math.ceil(matches.length / PAGE_SIZE);
  const currentPage = Math.max(1, Math.min(page, pageCount));
  const start = (currentPage - 1) * PAGE_SIZE;
  const end = Math.min(start + PAGE_SIZE, matches.length);

  function changeSearch(value: string) {
    setSearch(value);
    setPage(1);
  }

  return (
    <Stack component="section" aria-label={management ? 'Project catalog' : 'Choose a project'} className={styles.surface} gap="md" miw={0}>
      {!management ? <Title order={2} size="h3">Choose a project</Title> : null}
      <Group justify="space-between" align="flex-end">
        <TextInput ref={searchInput} type="search" label="Search projects" placeholder={management ? 'Name or repository' : 'Search by project name'}
          className={styles.search} size="md" value={search} onChange={event => changeSearch(event.currentTarget.value)}
          leftSection={<svg width="18" height="18" viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
            <circle cx="8.5" cy="8.5" r="5" /><path d="m12 12 4 4" />
          </svg>}
          rightSection={search ? <CloseButton aria-label="Clear search" onClick={() => {
            changeSearch('');
            searchInput.current?.focus();
          }} /> : null} />
        <Group gap="sm">
          {management?.onArchivedChange ? <Switch label="Archived projects" checked={management.archived} onChange={event => {
            setPage(1);
            management.onArchivedChange?.(event.currentTarget.checked);
          }} /> : null}
          {management ? <Button variant="default" size="md" loading={catalog.isFetching} onClick={() => void catalog.refetch()}>Refresh</Button> : null}
          <SegmentedControl aria-label="Project view" size="md" value={view} onChange={setView}
            data={[{ label: 'Cards', value: 'cards' }, { label: 'Table', value: 'table' }]} />
        </Group>
      </Group>
      {management?.archived ? <Text size="sm" c="dimmed">Restore a project to allow new launches. Existing workspaces and grants are preserved.</Text> : null}

      {catalog.isError ? <Stack align="flex-start">
        <APIProblemView error={catalog.error} />
        <Button variant="default" loading={catalog.isFetching} onClick={() => void catalog.refetch()}>Retry</Button>
      </Stack> : catalog.isPending ? <Text role="status">Loading projects…</Text> : matches.length === 0 ? (
        <Stack gap={4} role="status">
          <Text c="dimmed">{query ? 'No projects match your search.' : empty}</Text>
          {!query && management && !management.archived ? <Text size="sm" c="dimmed">{management.canCreate
            ? 'Create a project to define a repeatable workspace configuration.' : 'Ask a project owner to share launch access with you.'}</Text> : null}
        </Stack>
      ) : view === 'cards' ? (
        <>
          <Text size="sm" c="dimmed" role="status">{matches.length} {matches.length === 1 ? 'project' : 'projects'}</Text>
          <SimpleGrid type="container" cols={{ base: 1, '36rem': 2, '56rem': 3, '76rem': 4 }}>
            {matches.map(project => <ProjectCard key={project.id} project={project} onProjectAction={onProjectAction} management={management} />)}
          </SimpleGrid>
        </>
      ) : (
        <Stack gap={0}>
          <Table.ScrollContainer minWidth={management ? 760 : 680}>
            <Table aria-label="Projects" className={styles.table} withTableBorder highlightOnHover verticalSpacing="sm">
              <Table.Thead><Table.Tr>
                <Table.Th scope="col">Project</Table.Th>
                <Table.Th scope="col">Repository</Table.Th>
                <Table.Th scope="col">Resource profile</Table.Th>
                {management ? <Table.Th scope="col">Owner</Table.Th> : null}
                <Table.Th scope="col" ta="right">Actions</Table.Th>
              </Table.Tr></Table.Thead>
              <Table.Tbody>{matches.slice(start, end).map(project => (
                <Table.Tr key={project.id}>
                  <Table.Th scope="row">
                    <Text size="sm" fw={600}>{project.name}</Text>
                    <Text size="xs" c="dimmed">Revision {project.currentRevision}</Text>
                    {management ? <ProjectState project={project} /> : null}
                  </Table.Th>
                  <Table.Td>
                    <Text size="sm">{repositoryName(project)}</Text>
                    <Text size="xs" c="dimmed">{project.repositoryRef || 'Default branch'}</Text>
                  </Table.Td>
                  <Table.Td>
                    <ProjectResources project={project} showImage={!!management} />
                  </Table.Td>
                  {management ? <Table.Td><Text size="sm">{ownerName(project, management.userID)}</Text></Table.Td> : null}
                  <Table.Td ta="right">
                    <Group justify="flex-end" gap="xs">
                      <Button variant="subtle" size="compact-sm" aria-label={actionLabel(project)}
                        aria-haspopup="dialog" onClick={event => onProjectAction(project, event.currentTarget)}>{actionText(project)}</Button>
                      {management?.actions(project)}
                    </Group>
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

function ProjectCard({ project, onProjectAction, management }: {
  project: TemplateRevision;
  onProjectAction: ProjectAction;
  management?: ProjectManagement;
}) {
  const detailsId = `project-summary-${project.id}`;
  return (
    <Card component="article" aria-label={project.name} withBorder padding={0} className={styles.card}>
      <UnstyledButton className={styles.cardButton} aria-label={actionLabel(project)} aria-describedby={detailsId}
        aria-haspopup="dialog" onClick={event => onProjectAction(project, event.currentTarget)}>
        <Text component="span" size="lg" fw={600}>{project.name}</Text>
        <Stack component="span" id={detailsId} gap="sm" mt={4}>
          <Group component="span" gap="xs">
            <Text component="span" size="xs" c="dimmed">Revision {project.currentRevision}</Text>
            {management ? <ProjectState project={project} /> : null}
          </Group>
          <Stack component="span" gap={2}>
            <Text component="span" size="sm" fw={500}>{repositoryName(project)}</Text>
            <Text component="span" size="xs" c="dimmed">{project.repositoryRef || 'Default branch'}</Text>
          </Stack>
          <ProjectResources project={project} showImage={!!management} />
        </Stack>
        <Group component="span" className={styles.cardAction} justify="space-between" gap="xs" wrap="nowrap">
          <Text component="span" size="sm" fw={600}>{actionText(project)}</Text>
          <svg width="18" height="18" viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
            <path d="M4 10h11m-5-5 5 5-5 5" />
          </svg>
        </Group>
      </UnstyledButton>
      {management ? <Group className={styles.cardFooter} px="lg" py="xs" justify="space-between" gap="xs">
        <Text size="sm" c="dimmed" style={{ overflowWrap: 'anywhere' }}>{ownerName(project, management.userID)}</Text>
        {management.actions(project)}
      </Group> : null}
    </Card>
  );
}

function ProjectResources({ project, showImage }: { project: TemplateRevision; showImage: boolean }) {
  return <Stack component="span" gap={2}>
    <Text component="span" size="sm" c="dimmed">{project.resourceProfileName || 'Profile unavailable'}</Text>
    {project.providerKind ? <Text component="span" size="xs" c="dimmed">{providerLabel(project.providerKind)}</Text> : null}
    {showImage && project.platformImageName ? <Text component="span" size="xs" c="dimmed">{project.platformImageName}</Text> : null}
  </Stack>;
}

function ProjectState({ project }: { project: TemplateRevision }) {
  const archived = project.state === 'archived';
  return <Badge component="span" size="xs" variant="light" color={archived ? 'gray' : 'green'}>{archived ? 'Archived' : 'Active'}</Badge>;
}

function ownerName(project: TemplateRevision, userID: string) {
  return project.ownerId === userID ? 'You' : project.ownerName || 'Project owner';
}

function actionText(project: TemplateRevision) {
  return project.state === 'archived' ? 'View details' : 'Launch workspace';
}

function actionLabel(project: TemplateRevision) {
  return project.state === 'archived' ? `View ${project.name}` : `${actionText(project)} for ${project.name}`;
}

function repositoryName(project: TemplateRevision) {
  return project.repositoryUrl?.replace(/^https:\/\/github\.com\//i, '') || 'Repository unavailable';
}
