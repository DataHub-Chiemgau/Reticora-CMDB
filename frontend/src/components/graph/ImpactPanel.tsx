import { useTranslation } from 'react-i18next';
import type { components } from '../../api/generated/schema';
import { useClients } from '../../api/cmdbHooks';
import { useSites } from '../../api/hooks';
import { useEntitlements } from '../../hooks/useEntitlements';
import { Badge } from '../ui/Badge';

type BlastCounts = components['schemas']['BlastCounts'];
type ImpactSummary = components['schemas']['ImpactSummary'];
type ServiceSPOF = components['schemas']['ServiceSPOF'];

/** An affected CI or service as both answers carry it: id, name and type. */
export interface ImpactPanelCI {
  id: string;
  name?: string;
  ci_type?: string;
  heuristic?: boolean;
}

/**
 * The impact as the panel shows it (UI-14, WP-818). The blast radius of
 * GET /cis/{id}/blast-radius and the answer of GET /impact with
 * include=services,clients,sites,spof (WP-297) both fit: the CI detail
 * passes the former, the topology page the latter.
 */
export interface ImpactPanelData {
  /** The affected CIs; heuristic hits (IMP-03) carry heuristic true. */
  affected: ImpactPanelCI[];
  affected_count?: number;
  heuristic_count?: number;
  affected_services?: ImpactPanelCI[];
  client_ids?: string[];
  site_ids?: string[];
  /** The SPOF of the affected services (IMP-08). */
  service_spofs?: ServiceSPOF[];
  spof_truncated?: boolean;
  /** The direct dependents without an alternative provider (IMP-04). */
  single_points_of_failure?: ImpactPanelCI[];
  protected_by_redundancy?: ImpactPanelCI[];
  blast_radius?: BlastCounts;
  summary?: ImpactSummary;
  truncated?: boolean;
}

export interface ImpactPanelProps {
  impact: ImpactPanelData;
  /** The label of a CI type id; without it the id is shown. */
  typeLabel?: (ciType: string) => string;
}

/**
 * ImpactPanel shows the impact of a failure (UI-14, IMP-08): the affected
 * CIs (certain and heuristic apart, IMP-03), the affected services, clients
 * and sites by name, the counts of the blast radius, the impacted CIs per
 * relationship category and the single points of failure of the services
 * (service_spofs), apart from the direct dependents without an alternative
 * and those protected by redundancy. Without the entitlement topology the
 * panel is not rendered: /impact and blast-radius need it.
 */
export function ImpactPanel({ impact, typeLabel = (type) => type }: ImpactPanelProps) {
  const { t } = useTranslation();
  const { isEnabled } = useEntitlements();
  const clientIds = impact.client_ids ?? [];
  const siteIds = impact.site_ids ?? [];
  const clients = useClients(isEnabled('topology') && clientIds.length > 0);
  const sites = useSites();
  if (!isEnabled('topology')) return null;

  const certain = impact.affected.filter((n) => !n.heuristic);
  const heuristic = impact.affected.filter((n) => n.heuristic);
  const counts = impact.blast_radius;
  const services = impact.affected_services ?? [];
  const spofs = impact.service_spofs ?? [];
  const direct = impact.single_points_of_failure ?? [];
  const redundant = impact.protected_by_redundancy ?? [];
  const byCategory = Object.entries(impact.summary?.by_category ?? {});

  // Without a readable name a client or site shows its id.
  const clientNames = new Map(
    (clients.data?.data ?? []).map((client) => [client.id, client.name] as const),
  );
  const siteNames = new Map((sites.data?.data ?? []).map((site) => [site.id, site.name] as const));
  const serviceNames = new Map(services.map((service) => [service.id, service.name] as const));
  const nameOf = (names: Map<string, string | undefined>, id: string) => names.get(id) || id;

  const ciItem = (n: ImpactPanelCI) => (
    <>
      • {n.name || n.id}
      {n.ci_type ? (
        <>
          {' '}
          <span className="text-xs text-gray-500 dark:text-gray-400">({typeLabel(n.ci_type)})</span>
        </>
      ) : null}
    </>
  );

  return (
    <div className="space-y-3" data-testid="impact-panel">
      <div className="flex flex-wrap gap-4 text-sm">
        <span>
          {t('impact.affected')}:{' '}
          <strong>{counts?.cis ?? impact.affected_count ?? certain.length}</strong>
        </span>
        <span>
          {t('impact.heuristic')}:{' '}
          <strong>{impact.heuristic_count ?? impact.summary?.heuristic ?? heuristic.length}</strong>
        </span>
        <span>
          {t('impact.services')}: <strong>{counts?.services ?? services.length}</strong>
          {(counts?.services_heuristic ?? 0) > 0 ? (
            <> ({t('impact.servicesHeuristic', { count: counts?.services_heuristic })})</>
          ) : null}
        </span>
        <span>
          {t('impact.clients')}: <strong>{counts?.clients ?? clientIds.length}</strong>
        </span>
        <span>
          {t('impact.sites')}: <strong>{counts?.sites ?? siteIds.length}</strong>
        </span>
        <span className="text-red-600 dark:text-red-400">
          {t('impact.spof')}: <strong>{counts?.spof ?? spofs.length}</strong>
        </span>
      </div>
      {impact.truncated ? (
        <p role="note" className="text-xs text-amber-700 dark:text-amber-300">
          {t('impact.truncated')}
        </p>
      ) : null}

      {services.length > 0 ? (
        <div>
          <p className="text-xs font-semibold text-gray-500 dark:text-gray-400">
            {t('impact.servicesList')}
          </p>
          <ul className="max-h-40 overflow-y-auto text-sm">
            {services.map((n) => (
              <li key={n.id}>
                {ciItem(n)}
                {n.heuristic ? (
                  <>
                    {' '}
                    <Badge variant="warning">{t('impact.heuristicMarker')}</Badge>
                  </>
                ) : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {clientIds.length > 0 ? (
        <div>
          <p className="text-xs font-semibold text-gray-500 dark:text-gray-400">
            {t('impact.clientsList')}
          </p>
          <ul className="text-sm">
            {clientIds.map((id) => (
              <li key={id}>• {nameOf(clientNames, id)}</li>
            ))}
          </ul>
        </div>
      ) : null}

      {siteIds.length > 0 ? (
        <div>
          <p className="text-xs font-semibold text-gray-500 dark:text-gray-400">
            {t('impact.sitesList')}
          </p>
          <ul className="text-sm">
            {siteIds.map((id) => (
              <li key={id}>• {nameOf(siteNames, id)}</li>
            ))}
          </ul>
        </div>
      ) : null}

      {spofs.length > 0 || impact.spof_truncated ? (
        <div>
          <p className="text-xs font-semibold text-red-600 dark:text-red-400">
            {t('impact.spofList')}
          </p>
          {impact.spof_truncated ? (
            <p role="note" className="text-xs text-amber-700 dark:text-amber-300">
              {t('impact.spofTruncated')}
            </p>
          ) : null}
          <ul className="text-sm">
            {spofs.map((n) => (
              <li key={n.id}>
                {ciItem(n)} <Badge variant="danger">{t('impact.spofBadge')}</Badge>
                {n.service_ids.length > 0 ? (
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    {' '}
                    {t('impact.spofFor', {
                      services: n.service_ids.map((id) => nameOf(serviceNames, id)).join(', '),
                    })}
                  </span>
                ) : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {direct.length > 0 ? (
        <div>
          <p className="text-xs font-semibold text-gray-500 dark:text-gray-400">
            {t('impact.directList')}
          </p>
          <ul className="text-sm">
            {direct.map((n) => (
              <li key={n.id}>{ciItem(n)}</li>
            ))}
          </ul>
        </div>
      ) : null}

      {redundant.length > 0 ? (
        <div>
          <p className="text-xs font-semibold text-gray-500 dark:text-gray-400">
            {t('impact.redundantList')}
          </p>
          <ul className="text-sm">
            {redundant.map((n) => (
              <li key={n.id}>{ciItem(n)}</li>
            ))}
          </ul>
        </div>
      ) : null}

      {byCategory.length > 0 ? (
        <div>
          <p className="text-xs font-semibold text-gray-500 dark:text-gray-400">
            {t('impact.byCategory')}
          </p>
          <ul className="flex flex-wrap gap-x-4 text-sm">
            {byCategory.map(([category, count]) => (
              <li key={category}>
                {t(`impact.categories.${category}`, category)}: <strong>{count}</strong>
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      <div>
        <p className="text-xs font-semibold text-gray-500 dark:text-gray-400">
          {t('impact.affectedList')}
        </p>
        {certain.length > 0 ? (
          <ul className="max-h-40 overflow-y-auto text-sm">
            {certain.map((n) => (
              <li key={n.id}>{ciItem(n)}</li>
            ))}
          </ul>
        ) : (
          <p className="text-sm text-gray-500 dark:text-gray-400">{t('impact.noneAffected')}</p>
        )}
      </div>
      {heuristic.length > 0 ? (
        <div>
          <p className="text-xs font-semibold text-gray-500 dark:text-gray-400">
            {t('impact.heuristicList')}
          </p>
          <p className="text-xs text-gray-500 dark:text-gray-400">{t('impact.heuristicHint')}</p>
          <ul className="max-h-40 overflow-y-auto text-sm">
            {heuristic.map((n) => (
              <li key={n.id}>
                {ciItem(n)} <Badge variant="warning">{t('impact.heuristicMarker')}</Badge>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}
