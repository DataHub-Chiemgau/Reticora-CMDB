import { useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  ApiError,
  IMPACT_PANEL_INCLUDE,
  isProjectedNode,
  type NodeKind,
  type TopologyGraphData,
  type TopologyNode,
} from '../api/client';
import { localizedText, type RelationshipType } from '../api/cmdb';
import { useCITypes, useRelationshipTypes } from '../api/cmdbHooks';
import { MAX_IMPACT_CIS, useTopology, useImpactMany, useSites } from '../api/hooks';
import { ImpactPanel } from '../components/graph/ImpactPanel';
import { SPOF_COLOR, TopologyGraph } from '../components/graph/TopologyGraph';
import type { GraphEdge, GraphNode } from '../components/graph/TopologyGraph';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { useEntitlements } from '../hooks/useEntitlements';
import { getStatusBadgeVariant, getStatusTranslationKey } from './ciStatus';

const statusColors: Record<string, string> = {
  active: '#16a34a',
  inactive: '#6b7280',
  maintenance: '#d97706',
  decommissioned: '#dc2626',
};

/**
 * The look of the projected subnets and VLANs (IMP-11): their own color and a
 * smaller size than a CI, whose color follows its status. The legend of the
 * page explains both.
 */
export const projectedNodeStyles: Record<
  Exclude<NodeKind, 'ci'>,
  { color: string; size: number }
> = {
  subnet: { color: '#0891b2', size: 6 },
  vlan: { color: '#7c3aed', size: 6 },
};

/** nodeKindOf reads the kind of a node; absent means ci (API-01). */
export function nodeKindOf(node: Pick<TopologyNode, 'node_kind'>): NodeKind {
  return node.node_kind ?? 'ci';
}

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
    nodes: graph.nodes.map((node) => {
      const kind = nodeKindOf(node);
      if (kind !== 'ci') {
        return { id: node.id, label: node.name, ...projectedNodeStyles[kind] };
      }
      return {
        id: node.id,
        label: node.name,
        color: statusColors[node.status ?? ''] ?? '#4f46e5',
      };
    }),
    edges: [...edgesByPair.values()],
  };
}

/** The impact direction of a relationship type (REL-01, CH27, E-18). */
export type ImpactDirection = RelationshipType['impact_direction'];

/** impactDirections maps each relationship type key onto its impact direction. */
export function impactDirections(types: RelationshipType[]): Map<string, ImpactDirection> {
  return new Map(types.map((type) => [type.key, type.impact_direction]));
}

/**
 * Computes the outage-impact set of failed nodes along the impact direction
 * of each relationship type, as the server does (IMP-01, F-254): over a
 * source_depends_on_target edge (powered_by, hosted_on, depends_on) the
 * failure of the target reaches the source, over target_depends_on_source
 * (powers, contains) the failure of the source reaches the target, an
 * undirected edge (connected_to, the network heuristic of IMP-03) carries it
 * both ways and a type with direction none never. A type without a known
 * direction counts as source_depends_on_target, the server's reading. Edges
 * carry their relationship types in the label of buildGraphModel; relType
 * narrows the walk to one type (the facet filter). The failed nodes are not
 * part of the set (IMP-05). The page shows this set only, marked as a
 * preview, while the server analysis (/impact, with redundancy) loads; when
 * the analysis fails it shows the error instead (F-326).
 */
export function computeOutageImpact(
  failed: string | string[],
  edges: GraphEdge[],
  directions: ReadonlyMap<string, ImpactDirection>,
  relType = '',
): Set<string> {
  const failedIds = new Set(Array.isArray(failed) ? failed : [failed]);
  const impacted = new Set<string>();
  const queue = [...failedIds];
  const reach = (to: string) => {
    if (!failedIds.has(to) && !impacted.has(to)) {
      impacted.add(to);
      queue.push(to);
    }
  };
  while (queue.length > 0) {
    const current = queue.pop() as string;
    edges.forEach((edge) => {
      if (edge.source !== current && edge.target !== current) return;
      (edge.label ?? '').split(', ').forEach((type) => {
        if (relType && type !== relType) return;
        switch (directions.get(type) ?? 'source_depends_on_target') {
          case 'source_depends_on_target':
            if (edge.target === current) reach(edge.source);
            break;
          case 'target_depends_on_source':
            if (edge.source === current) reach(edge.target);
            break;
          case 'undirected':
            reach(edge.source === current ? edge.target : edge.source);
            break;
          case 'none':
            break;
        }
      });
    });
  }
  return impacted;
}

/**
 * parseSimulated reads the failed CIs of the simulation from the URL. A
 * projected subnet or VLAN (subnet:{id}, vlan:{id}) is no CI and cannot
 * fail (IMP-11): a stale link naming one is ignored.
 */
export function parseSimulated(value: string | null): string[] {
  return (value ?? '')
    .split(',')
    .map((id) => id.trim())
    .filter(
      (id, index, all) => id !== '' && !/^(subnet|vlan):/.test(id) && all.indexOf(id) === index,
    );
}

/**
 * relTypeFilterOptions lists the relationship types of the facet filter
 * (WP-818): from GET /relationship-types, labelled with display_name, without
 * the types whose impact direction is none, since a failure never passes
 * them and the simulation would stay empty.
 */
export function relTypeFilterOptions(
  types: RelationshipType[],
): { value: string; label: string }[] {
  return types
    .filter((type) => type.impact_direction !== 'none')
    .map((type) => ({ value: type.key, label: type.display_name || type.key }));
}

const depthValues = ['1', '2', '3', '4', '5'];

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
  // The projected subnets and VLANs of IPAM (IMP-11): off by default.
  const includeNetwork = searchParams.get('network') === '1';
  const { data: sites } = useSites();
  const { isEnabled } = useEntitlements();

  const { data, isLoading, error, refetch } = useTopology({
    root_ci_id: rootCiId || undefined,
    ci_type: ciType || undefined,
    site_id: siteId || undefined,
    depth: rootCiId ? Number(depth) : undefined,
    include: includeNetwork ? 'network' : undefined,
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
  // A node carries its CI type id; the list shows the localized label of
  // the type, the id only for a type the catalogue does not list (F-344).
  const typeLabels = new Map(typeOptions.map((option) => [option.value, option.label]));

  const model = useMemo(() => (data ? buildGraphModel(data) : { nodes: [], edges: [] }), [data]);

  // Server-side impact analysis (spec §4): the backend computes the impacted
  // set authoritatively, faceted by the selected relationship class. The
  // client-side walk along the impact direction of the relationship types
  // (F-254) is a preview, marked as such, only while the analysis loads;
  // without the types the page shows the loading state only. A failed
  // analysis shows its error and no client-side set (F-326): the client walk
  // knows neither redundancy nor the CIs outside the drawn graph. Several CIs
  // can fail together (IMP-05, maintenance windows and redundancy): the
  // server evaluates them jointly.
  const {
    data: impact,
    error: impactError,
    isError: impactFailed,
    refetch: refetchImpact,
  } = useImpactMany(simulated, relTypeFacet, IMPACT_PANEL_INCLUDE);
  // More CIs than one analysis takes: 422 on ci_ids (IMP-05).
  const tooManyFailed =
    impactError instanceof ApiError && impactError.status === 422 && impactError.field === 'ci_ids';
  // The types label the facet filter and give the preview its directions.
  const { data: relationshipTypes } = useRelationshipTypes();
  const relTypeOptions = useMemo(
    () => relTypeFilterOptions(relationshipTypes?.data ?? []),
    [relationshipTypes],
  );
  const directions = useMemo(
    () => (relationshipTypes ? impactDirections(relationshipTypes.data) : undefined),
    [relationshipTypes],
  );

  const outage = useMemo(() => {
    if (simulated.length === 0 || impactFailed) return undefined;
    if (impact && Array.isArray(impact.impacted)) {
      return { impacted: new Set(impact.impacted.map((n) => n.id)), preview: false };
    }
    if (!directions) return undefined;
    return {
      impacted: computeOutageImpact(simulated, model.edges, directions, relTypeFacet),
      preview: true,
    };
  }, [simulated, impact, impactFailed, directions, model.edges, relTypeFacet]);
  const outageImpact = outage?.impacted;
  // The SPOF of the affected services (service_spofs, IMP-08), not the
  // direct dependents without an alternative (single_points_of_failure).
  const spofIds = useMemo(
    () =>
      simulated.length > 0 && !impactFailed && impact?.service_spofs
        ? new Set(impact.service_spofs.map((spof) => spof.id))
        : undefined,
    [simulated, impact, impactFailed],
  );

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
              ...relTypeOptions,
            ]}
          />
          <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
            <input
              type="checkbox"
              checked={includeNetwork}
              onChange={(event) => updateParam('network', event.target.checked ? '1' : '')}
              className="h-4 w-4 rounded border-gray-300 text-primary focus:ring-primary"
            />
            {t('topology.filterNetwork')}
          </label>
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
          {simulated.length > 0 && !outageImpact ? (
            <Card
              className="border-red-300 bg-red-50 dark:border-red-900 dark:bg-red-950"
              title={t('topology.simulation.active')}
            >
              {impactFailed ? (
                <ErrorState
                  title={t('topology.simulation.unavailable')}
                  description={
                    tooManyFailed
                      ? t('topology.simulation.tooMany', { max: MAX_IMPACT_CIS })
                      : t('topology.simulation.failedHint')
                  }
                  retryLabel={t('common.retry')}
                  onRetry={() => void refetchImpact()}
                />
              ) : (
                <p className="text-sm text-red-900 dark:text-red-100">
                  {t('topology.simulation.loading')}
                </p>
              )}
            </Card>
          ) : null}
          {simulated.length > 0 && outageImpact ? (
            <Card
              className="border-red-300 bg-red-50 dark:border-red-900 dark:bg-red-950"
              title={t('topology.simulation.active')}
            >
              {outage?.preview ? (
                <p className="mb-2 flex flex-wrap items-center gap-2 text-xs text-gray-700 dark:text-gray-300">
                  <Badge variant="warning">{t('topology.simulation.preview')}</Badge>
                  <span>{t('topology.simulation.provisionalLoading')}</span>
                </p>
              ) : null}
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
              {outageImpact.size === 0 ? (
                // Nothing depends on the failed CIs: the plain hint, also with the server analysis
                // (ABN-01 d, frozen onboarding scenario).
                <p className="mt-2 text-sm text-gray-600 dark:text-gray-300">
                  {t('topology.simulation.noImpact')}
                </p>
              ) : !outage?.preview && impact && isEnabled('topology') ? (
                // The server analysis with the blocks of WP-297 (UI-14).
                <div className="mt-3">
                  <ImpactPanel
                    impact={{ ...impact, affected: impact.impacted }}
                    typeLabel={(type) => typeLabels.get(type) ?? type}
                  />
                </div>
              ) : (
                <ul className="mt-2 flex flex-wrap gap-1 text-xs">
                  {[...outageImpact].map((id) => (
                    <li key={id}>
                      <Badge variant="danger">{nameOf(id)}</Badge>
                    </li>
                  ))}
                </ul>
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
                spofNodes={spofIds}
              />
            </div>
            {includeNetwork || (spofIds?.size ?? 0) > 0 ? (
              <TopologyLegend network={includeNetwork} spof={(spofIds?.size ?? 0) > 0} />
            ) : null}
          </Card>

          <Card title={t('topology.nodeList')}>
            <p className="mb-3 text-sm text-gray-600 dark:text-gray-300">
              {t('topology.nodeListHint')}
            </p>
            <ul className="space-y-2 text-sm">
              {data.nodes.map((node) =>
                isProjectedNode(node) ? (
                  // A projected subnet or VLAN is no CI: no page, no
                  // simulation, no focus (IMP-11).
                  <li
                    key={node.id}
                    className="flex flex-wrap items-center justify-between gap-2 border-b border-gray-100 pb-2 last:border-0 dark:border-gray-800"
                  >
                    <span className="font-medium text-gray-900 dark:text-gray-100">
                      {node.name}
                    </span>
                    <Badge variant="neutral">
                      {t(`topology.nodeKinds.${nodeKindOf(node)}`, nodeKindOf(node))}
                    </Badge>
                  </li>
                ) : (
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
                      {spofIds?.has(node.id) ? (
                        <Badge variant="warning">{t('impact.spofBadge')}</Badge>
                      ) : null}
                      <Badge variant="info">
                        {typeLabels.get(node.ci_type ?? '') ?? node.ci_type}
                      </Badge>
                      <Badge variant={getStatusBadgeVariant(node.status ?? '')}>
                        {t(getStatusTranslationKey(node.status ?? ''))}
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
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => updateParam('root', node.id)}
                      >
                        {t('topology.focus')}
                      </Button>
                    </span>
                  </li>
                ),
              )}
            </ul>
          </Card>
        </>
      ) : null}
    </div>
  );
}

/**
 * TopologyLegend explains the marks of the graph (WP-818): a CI in the color
 * of its status, the projected subnets and VLANs (IMP-11) in their own color
 * and smaller, and the SPOF of the affected services.
 */
function TopologyLegend({ network, spof }: { network: boolean; spof: boolean }) {
  const { t } = useTranslation();
  const swatch = (color: string, size: number) => (
    <span
      aria-hidden="true"
      className="inline-block rounded-full"
      style={{ backgroundColor: color, width: size, height: size }}
    />
  );
  return (
    <div className="border-t border-gray-100 px-4 py-3 dark:border-gray-800">
      <p className="text-xs font-semibold text-gray-500 dark:text-gray-400">
        {t('topology.legend.title')}
      </p>
      <ul aria-label={t('topology.legend.title')} className="mt-1 flex flex-wrap gap-4 text-xs">
        <li className="flex items-center gap-2">
          {swatch(statusColors.active!, 12)}
          {t('topology.legend.ci')}
        </li>
        {network
          ? (['subnet', 'vlan'] as const).map((kind) => (
              <li key={kind} className="flex items-center gap-2">
                {swatch(projectedNodeStyles[kind].color, 9)}
                {t(`topology.legend.${kind}`)}
              </li>
            ))
          : null}
        {spof ? (
          <li className="flex items-center gap-2">
            {swatch(SPOF_COLOR, 13)}
            {t('topology.legend.spof')}
          </li>
        ) : null}
      </ul>
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
