import { useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { ApiError, type TopologyGraphData } from '../api/client';
import { localizedText } from '../api/cmdb';
import { useCITypes } from '../api/cmdbHooks';
import { useTopology, useImpactMany, useSites } from '../api/hooks';
import { TopologyGraph } from '../components/graph/TopologyGraph';
import type { GraphEdge, GraphNode } from '../components/graph/TopologyGraph';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { getStatusBadgeVariant, getStatusTranslationKey } from './ciStatus';

const statusColors: Record<string, string> = {
  active: '#16a34a',
  inactive: '#6b7280',
  maintenance: '#d97706',
  decommissioned: '#dc2626',
};

export interface GraphModel {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

/**
 * Maps the API topology graph onto the renderer model. Edges whose endpoints
 * are not part of the returned node set are dropped, because the renderer
 * cannot place an edge without both of its nodes. Two CIs may be related in
 * several ways at once (for example `connected_to` and `powered_by`); the
 * renderer graph is not a multigraph, so parallel edges are collapsed into one
 * edge whose label lists every relationship type.
 */
export function buildGraphModel(graph: TopologyGraphData): GraphModel {
  const nodeIds = new Set(graph.nodes.map((node) => node.id));
  const edgesByPair = new Map<string, GraphEdge>();

  graph.edges
    .filter((edge) => nodeIds.has(edge.source_ci_id) && nodeIds.has(edge.target_ci_id))
    .forEach((edge) => {
      const key = `${edge.source_ci_id}\u0000${edge.target_ci_id}`;
      const existing = edgesByPair.get(key);

      if (!existing) {
        edgesByPair.set(key, {
          source: edge.source_ci_id,
          target: edge.target_ci_id,
          label: edge.rel_type,
        });
        return;
      }

      const labels = (existing.label ?? '').split(', ');
      if (!labels.includes(edge.rel_type)) {
        existing.label = [...labels, edge.rel_type].join(', ');
      }
    });

  return {
    nodes: graph.nodes.map((node) => ({
      id: node.id,
      label: node.name,
      color: statusColors[node.status] ?? '#4f46e5',
    })),
    edges: [...edgesByPair.values()],
  };
}

/**
 * Computes the outage-impact set of failed nodes: every node reachable from
 * a failure via outbound edges (things that depend on a failed CI). Nodes
 * reachable only over an already-impacted path are impacted as well; the
 * failed nodes themselves are not part of the set (IMP-05).
 */
export function computeOutageImpact(failed: string | string[], edges: GraphEdge[]): Set<string> {
  const failedIds = new Set(Array.isArray(failed) ? failed : [failed]);
  const impacted = new Set<string>();
  const queue = [...failedIds];
  while (queue.length > 0) {
    const current = queue.pop() as string;
    edges.forEach((edge) => {
      if (edge.source === current && !failedIds.has(edge.target) && !impacted.has(edge.target)) {
        impacted.add(edge.target);
        queue.push(edge.target);
      }
    });
  }
  return impacted;
}

/** parseSimulated reads the failed CIs of the simulation from the URL. */
export function parseSimulated(value: string | null): string[] {
  return (value ?? '')
    .split(',')
    .map((id) => id.trim())
    .filter((id, index, all) => id !== '' && all.indexOf(id) === index);
}

const depthValues = ['1', '2', '3', '4', '5'];
// Dependency classes for the failure-simulation facet filter (spec §5.5).
const relTypeValues = ['connected_to', 'powered_by', 'hosted_on', 'depends_on', 'member_of'];

export function TopologyPage() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  // The type filter sends the CI type id, the value of ci.ci_type_id (IMP-06).
  const { data: ciTypes } = useCITypes();
  const [searchParams, setSearchParams] = useSearchParams();

  const rootCiId = searchParams.get('root') ?? '';
  const ciType = searchParams.get('type') ?? '';
  const siteId = searchParams.get('site') ?? '';
  const depth = searchParams.get('depth') ?? '2';
  const simulated = useMemo(() => parseSimulated(searchParams.get('simulate')), [searchParams]);
  const relTypeFacet = searchParams.get('rel_type') ?? '';
  const { data: sites } = useSites();

  const { data, isLoading, error, refetch } = useTopology({
    root_ci_id: rootCiId || undefined,
    ci_type: ciType || undefined,
    site_id: siteId || undefined,
    depth: rootCiId ? Number(depth) : undefined,
  });
  // More than 2,000 CIs without a filter: the server answers 422 and the
  // page asks for a site or type first (UI-06).
  const tooLarge = error instanceof ApiError && error.status === 422;
  // A start CI the server does not show (deleted, of another tenant or out
  // of scope, e.g. a stale deep link) is 404 (F-161): the page says so and
  // leads back to the full view instead of reporting a load error.
  const rootNotFound = rootCiId !== '' && error instanceof ApiError && error.status === 404;
  const typeOptions = (ciTypes?.data ?? []).map((type) => ({
    value: type.id,
    label: localizedText(type.display_name, i18n.language) || type.name,
  }));
  const siteOptions = (sites?.data ?? []).map((site) => ({ value: site.id, label: site.name }));

  const model = useMemo(() => (data ? buildGraphModel(data) : { nodes: [], edges: [] }), [data]);

  // Server-side impact analysis (spec §4): the backend computes the impacted
  // set authoritatively, faceted by the selected relationship class. The
  // client-side BFS below remains as the instant feedback while it loads.
  // Several CIs can fail together (IMP-05, maintenance windows and
  // redundancy): the server evaluates them jointly.
  const { data: impact } = useImpactMany(simulated, relTypeFacet);

  const outageImpact = useMemo(() => {
    if (simulated.length === 0) return undefined;
    if (impact && Array.isArray(impact.impacted)) {
      return new Set(impact.impacted.map((n) => n.id));
    }
    return computeOutageImpact(simulated, model.edges);
  }, [simulated, impact, model.edges]);

  // The facet filter also narrows the rendered edges to the selected
  // relationship class so the simulated dependency path is visible.
  const visibleEdges = useMemo(
    () =>
      relTypeFacet
        ? model.edges.filter((e) => e.label?.split(', ').includes(relTypeFacet))
        : model.edges,
    [model.edges, relTypeFacet],
  );

  function updateParams(values: Record<string, string>) {
    const next = new URLSearchParams(searchParams);
    for (const [key, value] of Object.entries(values)) {
      if (value) {
        next.set(key, value);
      } else {
        next.delete(key);
      }
    }
    setSearchParams(next, { replace: true });
  }

  function updateParam(key: string, value: string) {
    updateParams({ [key]: value });
  }

  function toggleSimulated(id: string) {
    const next = simulated.includes(id)
      ? simulated.filter((entry) => entry !== id)
      : [...simulated, id];
    updateParam('simulate', next.join(','));
  }

  const nameOf = (id: string) => data?.nodes.find((node) => node.id === id)?.name ?? id;

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('nav.topology')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('topology.subtitle')}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          {simulated.length > 0 ? (
            <Button variant="danger" onClick={() => updateParam('simulate', '')}>
              {t('topology.simulation.stop')}
            </Button>
          ) : null}
          {rootCiId ? (
            <Button variant="secondary" onClick={() => updateParam('root', '')}>
              {t('topology.clearRoot')}
            </Button>
          ) : null}
        </div>
      </div>

      <Card>
        <div className="flex flex-wrap gap-3">
          <Select
            value={ciType}
            onChange={(event) => updateParam('type', event.target.value)}
            aria-label={t('topology.filterType')}
            className="max-w-xs"
            options={[{ value: '', label: t('topology.allTypes') }, ...typeOptions]}
          />
          <Select
            value={siteId}
            onChange={(event) => updateParam('site', event.target.value)}
            aria-label={t('topology.filterSite')}
            className="max-w-xs"
            options={[{ value: '', label: t('topology.allSites') }, ...siteOptions]}
          />
          <Select
            value={depth}
            onChange={(event) => updateParam('depth', event.target.value)}
            aria-label={t('topology.filterDepth')}
            className="max-w-xs"
            disabled={!rootCiId}
            options={depthValues.map((value) => ({
              value,
              label: `${t('topology.depth')} ${value}`,
            }))}
          />
          <Select
            value={relTypeFacet}
            onChange={(event) => updateParam('rel_type', event.target.value)}
            aria-label={t('topology.filterRelType', 'Beziehungstyp')}
            className="max-w-xs"
            options={[
              { value: '', label: t('topology.allRelTypes', 'Alle Beziehungen') },
              ...relTypeValues.map((value) => ({
                value,
                label: t(`topology.relTypes.${value}`, value.replace(/_/g, ' ')),
              })),
            ]}
          />
        </div>
      </Card>

      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}

      {tooLarge ? (
        <TopologyStartDialog
          typeOptions={typeOptions}
          siteOptions={siteOptions}
          onStart={(type, site) => updateParams({ type, site })}
        />
      ) : rootNotFound ? (
        <EmptyState
          title={t('topology.rootNotFound')}
          description={t('topology.rootNotFoundHint')}
          action={
            <Button variant="secondary" onClick={() => updateParam('root', '')}>
              {t('topology.showAll')}
            </Button>
          }
        />
      ) : error ? (
        <ErrorState
          title={t('topology.loadError')}
          description={error instanceof Error ? error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      {data && model.nodes.length === 0 ? (
        <EmptyState title={t('topology.empty')} description={t('topology.emptyHint')} />
      ) : null}

      {data && model.nodes.length > 0 ? (
        <>
          {simulated.length > 0 && outageImpact ? (
            <Card
              className="border-red-300 bg-red-50 dark:border-red-900 dark:bg-red-950"
              title={t('topology.simulation.active')}
            >
              <p className="text-sm text-red-900 dark:text-red-100">
                {simulated.length === 1
                  ? t('topology.simulation.impact', {
                      name: nameOf(simulated[0]!),
                      count: outageImpact.size,
                    })
                  : t('topology.simulation.impactMany', {
                      names: simulated.map(nameOf).join(', '),
                      failed: simulated.length,
                      count: outageImpact.size,
                    })}
              </p>
              {outageImpact.size > 0 ? (
                <ul className="mt-2 flex flex-wrap gap-1 text-xs">
                  {[...outageImpact].map((id) => (
                    <li key={id}>
                      <Badge variant="danger">{nameOf(id)}</Badge>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="mt-2 text-sm text-gray-600 dark:text-gray-300">
                  {t('topology.simulation.noImpact')}
                </p>
              )}
            </Card>
          ) : null}

          <Card className="p-0">
            <div className="h-[28rem] w-full overflow-hidden rounded-2xl">
              <TopologyGraph
                nodes={model.nodes}
                edges={visibleEdges}
                dimmedNodes={outageImpact}
                highlightedNodes={simulated.length > 0 ? new Set(simulated) : undefined}
              />
            </div>
          </Card>

          <Card title={t('topology.nodeList')}>
            <p className="mb-3 text-sm text-gray-600 dark:text-gray-300">
              {t('topology.nodeListHint')}
            </p>
            <ul className="space-y-2 text-sm">
              {data.nodes.map((node) => (
                <li
                  key={node.id}
                  className="flex flex-wrap items-center justify-between gap-2 border-b border-gray-100 pb-2 last:border-0 dark:border-gray-800"
                >
                  <button
                    type="button"
                    onClick={() => navigate(`/cmdb/${node.id}`)}
                    className="font-medium text-primary underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
                  >
                    {node.name}
                  </button>
                  <span className="flex flex-wrap items-center gap-2">
                    <Badge variant="info">{node.ci_type}</Badge>
                    <Badge variant={getStatusBadgeVariant(node.status)}>
                      {t(getStatusTranslationKey(node.status))}
                    </Badge>
                    <Button
                      variant="ghost"
                      size="sm"
                      aria-pressed={simulated.includes(node.id)}
                      onClick={() => toggleSimulated(node.id)}
                    >
                      {simulated.includes(node.id)
                        ? t('topology.simulation.remove')
                        : simulated.length > 0
                          ? t('topology.simulation.add')
                          : t('topology.simulation.simulate')}
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => updateParam('root', node.id)}>
                      {t('topology.focus')}
                    </Button>
                  </span>
                </li>
              ))}
            </ul>
          </Card>
        </>
      ) : null}
    </div>
  );
}

/**
 * TopologyStartDialog asks for a site or CI type before a topology of more
 * than 2,000 CIs is drawn (UI-06).
 */
function TopologyStartDialog({
  typeOptions,
  siteOptions,
  onStart,
}: {
  typeOptions: { value: string; label: string }[];
  siteOptions: { value: string; label: string }[];
  onStart: (type: string, site: string) => void;
}) {
  const { t } = useTranslation();
  const [type, setType] = useState('');
  const [site, setSite] = useState('');
  return (
    <Card title={t('topology.start.title')}>
      <form
        className="space-y-3"
        aria-label={t('topology.start.title')}
        onSubmit={(event) => {
          event.preventDefault();
          if (type || site) onStart(type, site);
        }}
      >
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('topology.tooLarge')}</p>
        <div className="flex flex-wrap gap-3">
          <Select
            label={t('topology.start.site')}
            value={site}
            onChange={(event) => setSite(event.target.value)}
            options={[{ value: '', label: t('topology.allSites') }, ...siteOptions]}
          />
          <Select
            label={t('topology.start.type')}
            value={type}
            onChange={(event) => setType(event.target.value)}
            options={[{ value: '', label: t('topology.allTypes') }, ...typeOptions]}
          />
        </div>
        <Button type="submit" disabled={!type && !site}>
          {t('topology.start.show')}
        </Button>
      </form>
    </Card>
  );
}
