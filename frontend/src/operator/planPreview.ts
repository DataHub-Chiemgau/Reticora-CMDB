import type { Entitlement, PlanInfo } from '../api/client';

/**
 * The feature whose row carries a quota (entitlement.QuotaFeature on the
 * server); max_targets_<type> belongs to monitoring.
 */
export function quotaFeature(quota: string): string | undefined {
  if (quota.startsWith('max_targets_')) return 'monitoring';
  return (
    {
      max_cis: 'cmdb_core',
      max_users: 'cmdb_core',
      max_collectors: 'discovery',
      max_api_keys: 'api_access',
      max_storage_mb: 'documents',
      max_monitored_targets: 'monitoring',
      max_probes_per_minute: 'monitoring',
      min_probe_interval_s: 'monitoring',
    } as Record<string, string>
  )[quota];
}

/** A quota whose value changes with the plan change; null is unlimited. */
export interface LimitChange {
  quota: string;
  from: number | null;
  to: number | null;
}

export interface PlanPreview {
  /** Features the change unlocks (sorted). */
  unlocked: string[];
  /** Features the change locks (their rows stay, disabled). */
  locked: string[];
  limits: LimitChange[];
}

/**
 * planPreview computes what a change from `from` to `to` does to the rows of
 * the organization, with the rule of entitlement.PlanTransition (WP-882):
 * only rows of origin plan change; a feature of the new plan is enabled, one
 * outside it disabled; a quota of a row inside the new plan that is missing
 * or equal to the default of the old plan takes the default of the new plan
 * (none: unlimited), an override stays.
 */
export function planPreview(
  from: string,
  to: string,
  rows: Entitlement[],
  matrix: PlanInfo[],
): PlanPreview {
  const fromInfo = matrix.find((p) => p.plan === from);
  const toInfo = matrix.find((p) => p.plan === to);
  if (!fromInfo || !toInfo) return { unlocked: [], locked: [], limits: [] };
  const target = new Set(toInfo.features);
  const planRows = rows.filter((r) => (r.origin ?? 'plan') === 'plan');
  const enabledNow = new Set(planRows.filter((r) => r.enabled).map((r) => r.feature_key));
  // A feature an add-on or a manual row already grants stays available.
  const otherwise = new Set(
    rows.filter((r) => r.origin !== 'plan' && r.enabled).map((r) => r.feature_key),
  );

  const unlocked = toInfo.features.filter((f) => !enabledNow.has(f) && !otherwise.has(f)).sort();
  const locked = [...enabledNow].filter((f) => !target.has(f) && !otherwise.has(f)).sort();

  const limits: LimitChange[] = [];
  const quotas = new Set([
    ...Object.keys(fromInfo.quota_defaults),
    ...Object.keys(toInfo.quota_defaults),
    ...planRows.flatMap((r) => Object.keys(r.limits ?? {})),
  ]);
  for (const quota of [...quotas].sort()) {
    const feature = quotaFeature(quota);
    if (!feature || !target.has(feature)) continue;
    const row = planRows.find((r) => r.feature_key === feature);
    const stored = row?.limits?.[quota];
    const oldDefault = fromInfo.quota_defaults[quota];
    const follows = stored === undefined || (oldDefault !== undefined && stored === oldDefault);
    if (!follows) continue;
    const next = toInfo.quota_defaults[quota] ?? null;
    const current = stored ?? null;
    if (current !== next) limits.push({ quota, from: current, to: next });
  }
  return { unlocked, locked, limits };
}
