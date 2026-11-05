/**
 * CIDetailSections — advanced, collapsible sections for the CI detail page
 * (spec §18 progressive disclosure): instance fields, field provenance /
 * manual overrides, lifecycle, impact analysis and metrics. Each is an expandable
 * <details> so normal users never see the metadata model.
 */
import { ChevronRight, Plus } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  useInstanceFields,
  useUpsertInstanceField,
  useDeleteInstanceField,
  useFieldProvenance,
  useSetOverride,
  useClearOverride,
  useBlastRadius,
  useLifecycleOptions,
  useLifecycleTransition,
  useEntityHistory,
  useLocationTree,
} from '../../api/cmdbHooks';
import type { LocationTreeNode } from '../../api/cmdb';
import { DynamicForm, validateAll } from '../form/DynamicForm';
import { ApiError } from '../../api/client';
import type { MetricPoint } from '../../api/client';
import { useCIMetrics, useHasPermission } from '../../api/hooks';
import { ProvenanceTooltip } from './ProvenanceTooltip';
import { fieldValueText } from './fieldValue';
import type { FieldDefinition, FieldError } from '../../lib/fieldmeta';
import { Badge } from '../ui/Badge';
import { cn } from '../ui/utils';
import { Button } from '../ui/Button';
import { Input } from '../ui/Input';
import { Select } from '../ui/Select';
import { Modal } from '../ui/Modal';
import { SkeletonList } from '../ui/Skeleton';
import { ImpactPanel } from '../graph/ImpactPanel';
import { useEntitlements } from '../../hooks/useEntitlements';
import { ErrorState } from '../ui/ErrorState';

/** One fact of a definition list: a fixed label column, the value beside it. */
export function DetailRow({
  label,
  value,
  mono,
  compact,
}: {
  label: string;
  value?: string;
  mono?: boolean;
  /** A narrower label column for panels in a side column. */
  compact?: boolean;
}) {
  return (
    <div
      className={cn(
        'grid grid-cols-1 gap-0.5 py-1.5 sm:gap-4',
        compact ? 'sm:grid-cols-[6.5rem_minmax(0,1fr)]' : 'sm:grid-cols-[10rem_minmax(0,1fr)]',
      )}
    >
      <dt className="text-sm text-fg-muted">{label}</dt>
      <dd className={cn('min-w-0 text-sm text-fg [overflow-wrap:anywhere]', mono && 'font-mono')}>
        {value || <span className="text-fg-subtle">—</span>}
      </dd>
    </div>
  );
}

// ─── Location names and times (CI-06, DB-05) ────────────────────────────────

/** formatDateTime shows a timestamp in the UI language; no value is undefined. */
export function formatDateTime(value: string | undefined, language: string): string | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(language, { dateStyle: 'medium', timeStyle: 'short' });
}

/**
 * formatDate shows a calendar date (YYYY-MM-DD, e.g. a purchase date) in the
 * UI language. The date has no time zone, so it is formatted as UTC and
 * shows the same day everywhere.
 */
export function formatDate(value: string | undefined, language: string): string | undefined {
  if (!value) return undefined;
  const date = new Date(/^\d{4}-\d{2}-\d{2}$/.test(value) ? `${value}T00:00:00Z` : value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleDateString(language, { dateStyle: 'medium', timeZone: 'UTC' });
}

export interface LocationNames {
  /** The name of a node of the location tree (site, room, …). */
  name: (id?: string) => string | undefined;
  /** The path of a node from its site: Site › Gebäude › Raum › Rack. */
  path: (id?: string) => string | undefined;
}

/**
 * useLocationNames names the nodes of the location tree (GET /locations/tree,
 * the nodes visible to the user). The tree is read only when enabled, i.e.
 * when there is a location to name. A node the tree does not hold (not
 * loaded, outside the user's scope or deleted) keeps its id.
 */
export function useLocationNames(enabled: boolean): LocationNames {
  const tree = useLocationTree({ enabled });
  return useMemo(() => {
    const nodes = new Map<string, LocationTreeNode>();
    const walk = (list: LocationTreeNode[] | undefined) => {
      for (const node of list ?? []) {
        nodes.set(node.id, node);
        walk(node.children);
      }
    };
    walk(tree.data?.data);
    return {
      name: (id) => (id ? (nodes.get(id)?.name ?? id) : undefined),
      path: (id) => {
        if (!id) return undefined;
        const names: string[] = [];
        // A node whose parent is outside the scope is a root of the tree.
        for (
          let node = nodes.get(id);
          node;
          node = node.parent_id ? nodes.get(node.parent_id) : undefined
        ) {
          names.unshift(node.name);
          if (names.length > nodes.size) break;
        }
        return names.length > 0 ? names.join(' › ') : id;
      },
    };
  }, [tree.data]);
}

/** The location fields of a CI whose values are nodes of the location tree. */
const LOCATION_FIELDS = new Set(['location_id', 'site_id', 'room_id']);

/**
 * CollapsibleSection is the quiet panel of the advanced sections of a CI: a
 * native <details> (open and closed state stay with the browser, the summary
 * is reachable by keyboard) with a chevron that turns when it opens.
 */
export function CollapsibleSection({
  title,
  children,
  defaultOpen = false,
  count,
}: {
  title: string;
  children: React.ReactNode;
  defaultOpen?: boolean;
  count?: number;
}) {
  return (
    <details open={defaultOpen} className="group rounded-xl border border-line bg-panel">
      <summary className="flex min-h-11 cursor-pointer select-none list-none items-center gap-2 rounded-xl px-4 py-2.5 text-sm font-medium text-fg outline-none transition-colors hover:bg-fg/[0.03] focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent group-open:rounded-b-none [&::-webkit-details-marker]:hidden">
        <ChevronRight
          aria-hidden="true"
          className="h-4 w-4 shrink-0 text-fg-subtle transition-transform group-open:rotate-90"
        />
        {title}
        {count !== undefined ? <Badge variant="neutral">{count}</Badge> : null}
      </summary>
      <div className="border-t border-line px-4 py-4">{children}</div>
    </details>
  );
}

const Section = CollapsibleSection;

// ─── Instance fields (§2) ────────────────────────────────────────────────────

export function InstanceFieldsSection({
  ciId,
  attributes,
  onChanged,
  serverError,
}: {
  ciId: string;
  attributes: Record<string, unknown>;
  onChanged: (attrs: Record<string, unknown>) => void;
  /** Rejection from the last attribute save, so 422 violations render inline. */
  serverError?: Error | null;
}) {
  const { t } = useTranslation();
  const fields = useInstanceFields(ciId);
  // Defining and deleting instance fields needs ci_instance_attribute:manage
  // (PUT/DELETE /cis/{id}/field-definitions). Without it (fail closed, also
  // while the rights load) the section shows only the values, without the
  // metamodel terms (UI-12, WP-804).
  const canManage = useHasPermission('ci_instance_attribute:manage') === true;
  const upsert = useUpsertInstanceField(ciId);
  const remove = useDeleteInstanceField(ciId);
  const [showAdd, setShowAdd] = useState(false);
  const [draft, setDraft] = useState<FieldDefinition>({ name: '', data_type: 'text' });
  const [localErrors, setLocalErrors] = useState<Record<string, FieldError | null>>({});
  // Local draft of the attribute values. The saved CI stays the source of
  // truth, but edits are rendered from the draft so in-flight (and invalid)
  // input is not overwritten by the server response mid-typing.
  const [valueDraft, setValueDraft] = useState<Record<string, unknown> | null>(null);
  const draftValues = valueDraft ?? attributes;

  const serverViolations =
    serverError instanceof ApiError
      ? serverError.violationsByField()
      : ({} as Record<string, string>);

  const defs = fields.data?.data ?? [];

  const valuesForm =
    defs.length > 0 ? (
      <DynamicForm
        fields={defs}
        values={draftValues}
        errors={{ ...localErrors, ...serverViolations }}
        onChange={(name, value) => {
          // Render from a local draft so the keystroke stays visible even
          // when it is invalid. Previously an invalid value was dropped on
          // the floor with no message, so the input appeared frozen. Only
          // values that pass client validation are propagated to the save
          // handler; the server re-validates and can still reject them.
          const next = { ...draftValues, [name]: value };
          setValueDraft(next);
          const errs = validateAll(defs, next);
          setLocalErrors((current) => ({ ...current, [name]: errs[name] ?? null }));
          if (!errs[name]) onChanged(next);
        }}
      />
    ) : null;
  const saveError =
    serverError && Object.keys(serverViolations).length === 0 ? (
      <p role="alert" className="mt-2 text-sm text-danger">
        {serverError.message}
      </p>
    ) : null;

  if (!canManage) {
    // A CI without instance fields has nothing more to show.
    if (defs.length === 0) return null;
    return (
      <Section title={t('ci.additionalDetails')}>
        {valuesForm}
        {saveError}
      </Section>
    );
  }

  return (
    <Section title={t('ci.instanceFields', 'Instanzfelder')} count={defs.length}>
      <p className="mb-3 text-sm text-fg-muted">
        {t(
          'ci.instanceFieldsHint',
          'Felder nur für dieses Gerät (z. B. iLO-IP, SAP-SID) — ohne den Typ zu ändern.',
        )}
      </p>
      {fields.isLoading ? <SkeletonList rows={2} label="…" /> : null}
      <ul className="mb-3 divide-y divide-line text-sm">
        {defs.map((f) => (
          <li key={f.name} className="flex items-center justify-between gap-3 py-1.5">
            <span className="flex min-w-0 flex-wrap items-baseline gap-x-2">
              <span className="font-medium text-fg [overflow-wrap:anywhere]">
                {f.label || f.name}
              </span>
              <span className="font-mono text-xs text-fg-muted">{f.data_type}</span>
            </span>
            <Button
              size="sm"
              variant="ghost"
              className="text-danger hover:text-danger"
              onClick={() => remove.mutate(f.name)}
            >
              {t('common.delete', 'Löschen')}
            </Button>
          </li>
        ))}
        {defs.length === 0 ? (
          <li className="py-1.5 text-sm text-fg-muted">
            {t('ci.noInstanceFields', 'Keine Instanzfelder.')}
          </li>
        ) : null}
      </ul>
      <Button size="sm" variant="secondary" icon={<Plus />} onClick={() => setShowAdd(true)}>
        {t('ci.addInstanceField', 'Instanzfeld hinzufügen')}
      </Button>

      {saveError}

      <Modal
        open={showAdd}
        onOpenChange={setShowAdd}
        title={t('ci.addInstanceField', 'Instanzfeld hinzufügen')}
      >
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            upsert.mutate(draft, { onSuccess: () => setShowAdd(false) });
          }}
        >
          <Input
            label={t('citypes.fieldName', 'Feldname')}
            value={draft.name}
            required
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
          />
          <Input
            label={t('citypes.fieldLabel', 'Anzeigename')}
            value={draft.label ?? ''}
            onChange={(e) => setDraft({ ...draft, label: e.target.value })}
          />
          <Select
            label={t('citypes.fieldType', 'Typ')}
            options={[
              'text',
              'multiline',
              'integer',
              'decimal',
              'boolean',
              'date',
              'datetime',
              'enum',
              'multiselect',
              'ip',
              'mac',
              'url',
              'email',
              'uuid',
              'json',
            ].map((ft) => ({ value: ft, label: ft }))}
            value={draft.data_type}
            onChange={(e) =>
              setDraft({ ...draft, data_type: e.target.value as FieldDefinition['data_type'] })
            }
          />
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={() => setShowAdd(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button type="submit" disabled={upsert.isPending}>
              {t('common.save', 'Speichern')}
            </Button>
          </div>
        </form>
      </Modal>

      {/* Instance-field values edit inline through the same DynamicForm. */}
      {valuesForm ? <div className="mt-4 border-t border-line pt-4">{valuesForm}</div> : null}
    </Section>
  );
}

// ─── Field provenance / overrides (§13) ──────────────────────────────────────

/** jsonValue renders a provenance value as JSON; no value is "—". */
function jsonValue(value: unknown): string {
  return JSON.stringify(value) ?? '—';
}

export function ProvenanceSection({ ciId }: { ciId: string }) {
  const { t } = useTranslation();
  // Every typed field with its source, time and selection reason (REC-10).
  const values = useFieldProvenance(ciId);
  const setOverride = useSetOverride(ciId);
  const clearOverride = useClearOverride(ciId);
  const [overrideField, setOverrideField] = useState<string | undefined>();
  const [draft, setDraft] = useState({ value: '', reason: '' });

  const rows = values.data?.data ?? [];
  const diverged = rows.filter((r) => r.diverged);
  return (
    <Section
      title={t('ci.provenance', 'Herkunft & Overrides')}
      count={values.data ? diverged.length : undefined}
    >
      <p className="mb-3 text-sm text-fg-muted">
        {t(
          'ci.provenanceHint',
          'Discovery-Wert vs. effektiver Wert. Geschützte manuelle Overrides werden nie von Discovery überschrieben.',
        )}
      </p>
      {values.isLoading ? <SkeletonList rows={2} label="…" /> : null}
      {/* A failed request is no empty provenance (F-350). */}
      {values.isError ? (
        <ErrorState
          title={t('ci.provenanceLoadError', 'Die Herkunftsdaten konnten nicht geladen werden')}
          retryLabel={t('common.retry', 'Erneut versuchen')}
          onRetry={() => void values.refetch()}
        />
      ) : null}
      <ul className="divide-y divide-line text-sm">
        {rows.map((fv) => (
          <li key={fv.field} className="py-3 first:pt-0 last:pb-0">
            {/* Values without a break opportunity (an identity field as JSON)
                wrap anywhere, and the three columns stack on a phone: before,
                a row was wider than the 412 px of mobile-chrome and every
                click on its actions landed on the neighbouring row (WP-646). */}
            <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-1">
              <span className="flex min-w-0 items-center gap-1 font-medium text-fg [overflow-wrap:anywhere]">
                {fv.field}
                <ProvenanceTooltip provenance={fv} />
              </span>
              <div className="flex flex-wrap items-center gap-2">
                {fv.diverged ? (
                  <Badge variant="warning">{t('ci.diverged', 'abweichend')}</Badge>
                ) : null}
                {fv.protected ? (
                  <Badge variant="info">{t('ci.protected', 'geschützt')}</Badge>
                ) : null}
                {fv.reason === 'override' ? (
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-danger hover:text-danger"
                    onClick={() => clearOverride.mutate(fv.field)}
                  >
                    {t('ci.clearOverride', 'Override aufheben')}
                  </Button>
                ) : (
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-accent hover:text-accent"
                    onClick={() => {
                      setOverrideField(fv.field);
                      setDraft({ value: '', reason: '' });
                    }}
                  >
                    {t('ci.setOverride', 'Override setzen')}
                  </Button>
                )}
              </div>
            </div>
            <dl className="mt-2 grid grid-cols-1 gap-2 text-xs sm:grid-cols-3 [&>div]:min-w-0 [&_dd]:[overflow-wrap:anywhere]">
              <div>
                <dt className="text-fg-muted">{t('ci.discovered', 'Discovery')}</dt>
                <dd className="font-mono text-fg">
                  {fieldValueText(
                    t,
                    fv.field,
                    fv.reason === 'override' ? fv.discovered_value : fv.value,
                    jsonValue,
                  )}
                </dd>
              </div>
              <div>
                <dt className="text-fg-muted">{t('ci.override', 'Override')}</dt>
                <dd className="font-mono text-fg">
                  {fv.reason === 'override'
                    ? fieldValueText(t, fv.field, fv.value, jsonValue)
                    : '—'}
                </dd>
              </div>
              <div>
                <dt className="text-fg-muted">{t('ci.effective', 'Effektiv')}</dt>
                <dd className="font-mono font-semibold text-fg">
                  {fieldValueText(t, fv.field, fv.value, jsonValue)}
                </dd>
              </div>
            </dl>
            <p className="mt-1 text-xs text-fg-muted">
              {t('ci.source', 'Quelle')}: {fv.source}
            </p>
          </li>
        ))}
        {rows.length === 0 && !values.isLoading && !values.isError ? (
          <li className="text-sm text-fg-muted">
            {t(
              'ci.noProvenance',
              'Noch keine Provenienz-Daten (erstellt bei der nächsten Discovery).',
            )}
          </li>
        ) : null}
      </ul>

      <Modal
        open={!!overrideField}
        onOpenChange={(o) => !o && setOverrideField(undefined)}
        title={`${t('ci.setOverride', 'Override setzen')}: ${overrideField ?? ''}`}
      >
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!overrideField) return;
            setOverride.mutate(
              { field: overrideField, value: draft.value, reason: draft.reason },
              { onSuccess: () => setOverrideField(undefined) },
            );
          }}
        >
          <Input
            label={t('ci.overrideValue', 'Wert')}
            value={draft.value}
            required
            onChange={(e) => setDraft({ ...draft, value: e.target.value })}
          />
          <Input
            label={t('ci.overrideReason', 'Begründung')}
            value={draft.reason}
            required
            onChange={(e) => setDraft({ ...draft, reason: e.target.value })}
          />
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={() => setOverrideField(undefined)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button type="submit" disabled={setOverride.isPending}>
              {t('common.save', 'Speichern')}
            </Button>
          </div>
        </form>
      </Modal>
    </Section>
  );
}

// ─── Lifecycle (§8) ──────────────────────────────────────────────────────────

/**
 * LifecycleSection offers the transitions the effective lifecycle model of
 * the entity allows from its current state (GET …/lifecycle-transitions,
 * WP-723) and applies one with POST …/lifecycle-transitions (WP-724); the
 * required fields of the chosen transition are asked for as its context,
 * and a refusal (guard violations, a transition someone else applied in
 * between) is shown with the violated fields.
 * A CI type without lifecycle model answers 422: the section is left out.
 */
export function LifecycleSection({
  entityType,
  entityId,
  currentState,
}: {
  entityType: 'assets' | 'cis';
  entityId: string;
  currentState?: string;
}) {
  const { t } = useTranslation();
  const options = useLifecycleOptions(entityType, entityId);
  const transition = useLifecycleTransition(entityType, entityId);
  const [target, setTarget] = useState('');
  const [reason, setReason] = useState('');
  const [context, setContext] = useState<Record<string, string>>({});

  if (options.isLoading) return null;
  // No lifecycle model for the type (DB-05): no lifecycle state to show.
  if (options.error instanceof ApiError && options.error.status === 422) return null;
  if (options.isError || !options.data) {
    return (
      <Section title={t('lifecycle.title', 'Lebenszyklus')}>
        <ErrorState
          title={String(options.error ?? '')}
          retryLabel={t('common.retry', 'Erneut versuchen')}
          onRetry={() => void options.refetch()}
        />
      </Section>
    );
  }

  const current = options.data.current_state ?? currentState ?? '';
  const targets = new Map<string, { label: string; required: string[] }>();
  // A body without transitions offers none (no crash of the detail page).
  for (const tr of options.data.transitions ?? []) {
    const known = targets.get(tr.to_state);
    targets.set(tr.to_state, {
      label: known?.label ?? tr.label,
      required: [...new Set([...(known?.required ?? []), ...(tr.required_fields ?? [])])],
    });
  }
  const required = targets.get(target)?.required ?? [];
  const error = transition.error;
  const violated =
    error instanceof ApiError ? error.violations.map((v) => v.field).filter(Boolean) : [];

  const apply = () => {
    const values: Record<string, unknown> = {};
    for (const field of required) {
      if (context[field]) values[field] = context[field];
    }
    transition.mutate(
      { toState: target, context: values, reason },
      {
        onSuccess: () => {
          setTarget('');
          setReason('');
          setContext({});
        },
      },
    );
  };

  return (
    <Section title={t('lifecycle.title', 'Lebenszyklus')}>
      <div className="mb-3 flex items-center gap-2">
        <span className="text-sm text-fg-muted">{t('lifecycle.current', 'Aktuell')}:</span>
        <Badge variant="info">{current || '—'}</Badge>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <Select
          label={t('lifecycle.transitionTo', 'Übergang nach')}
          options={[
            { value: '', label: '—' },
            ...[...targets].map(([key, tr]) => ({ value: key, label: tr.label || key })),
          ]}
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          className="w-56 max-w-full"
        />
        {required.map((field) => (
          <Input
            key={field}
            label={field}
            required
            value={context[field] ?? ''}
            onChange={(e) => setContext((c) => ({ ...c, [field]: e.target.value }))}
            className="w-56 max-w-full"
          />
        ))}
        <Input
          label={t('lifecycle.reason', 'Grund')}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          className="w-56 max-w-full"
        />
        <Button variant="secondary" disabled={!target || transition.isPending} onClick={apply}>
          {t('lifecycle.apply', 'Anwenden')}
        </Button>
      </div>
      {error ? (
        <p role="alert" className="mt-2 text-sm text-danger">
          {String(error.message || error)}
          {violated.length > 0 ? `: ${violated.join(', ')}` : null}
        </p>
      ) : null}
    </Section>
  );
}

// ─── Impact analysis (§15, UI-14: ImpactPanel, WP-818) ───────────────────────────────────────────────────

export function ImpactSection({ ciId }: { ciId: string }) {
  const { t } = useTranslation();
  const { isEnabled } = useEntitlements();
  const [enabled, setEnabled] = useState(false);
  const topology = isEnabled('topology');
  const blast = useBlastRadius(ciId, enabled && topology);
  // blast-radius hangs on the entitlement topology (gatedSuffixes): without
  // it the section is not shown (UI-14, WP-818).
  if (!topology) return null;

  return (
    <Section title={t('impact.title', 'Auswirkungsanalyse')}>
      {!enabled ? (
        <Button size="sm" variant="secondary" onClick={() => setEnabled(true)}>
          {t('impact.compute', 'Blast Radius berechnen')}
        </Button>
      ) : blast.isLoading ? (
        <SkeletonList rows={3} label="…" />
      ) : blast.data ? (
        // Of the nodes the CI detail uses id, name and ci_type only.
        <ImpactPanel impact={{ ...blast.data, affected: blast.data.affected_cis }} />
      ) : null}
    </Section>
  );
}

// ─── Metrics (MON-02) ────────────────────────────────────────────────────────

/** sparklinePath draws the points of a series as one SVG polyline path. */
export function sparklinePath(points: MetricPoint[], width: number, height: number): string {
  if (points.length === 0) return '';
  const values = points.map((p) => p.value);
  const min = Math.min(...values);
  const span = Math.max(...values) - min || 1;
  const stepX = points.length > 1 ? width / (points.length - 1) : 0;
  return points
    .map((p, i) => {
      const x = i * stepX;
      const y = height - ((p.value - min) / span) * height;
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(' ');
}

/**
 * CIMetricsSection shows the collector series of a CI (MON-02): one line per
 * metric over the last 24 hours, loaded on demand. Without the monitoring
 * feature the API answers 403 and the section says so.
 */
export function CIMetricsSection({ ciId }: { ciId: string }) {
  const { t } = useTranslation();
  const [enabled, setEnabled] = useState(false);
  const metrics = useCIMetrics(ciId, enabled);
  const forbidden = metrics.error instanceof ApiError && metrics.error.status === 403;

  return (
    <Section title={t('monitoring.ciTitle', 'Metriken')}>
      {!enabled ? (
        <Button size="sm" variant="secondary" onClick={() => setEnabled(true)}>
          {t('monitoring.ciLoad', 'Metriken laden')}
        </Button>
      ) : metrics.isLoading ? (
        <SkeletonList rows={2} label="…" />
      ) : forbidden ? (
        <p className="text-sm text-fg-muted">
          {t(
            'monitoring.ciNotLicensed',
            'Monitoring ist für diese Organisation nicht freigeschaltet.',
          )}
        </p>
      ) : metrics.error ? (
        <p className="text-sm text-danger">{(metrics.error as Error).message}</p>
      ) : (metrics.data?.series.length ?? 0) === 0 ? (
        <p className="text-sm text-fg-muted">
          {t('monitoring.ciNone', 'Keine Messwerte in den letzten 24 Stunden.')}
        </p>
      ) : (
        <ul className="space-y-2">
          {metrics.data!.series.map((series) => {
            const latest = series.points[series.points.length - 1];
            return (
              <li key={series.name} className="flex items-center gap-3 text-sm">
                <span className="w-56 truncate font-mono text-xs text-fg">{series.name}</span>
                <svg
                  role="img"
                  aria-label={series.name}
                  width={160}
                  height={28}
                  className="text-accent"
                >
                  <path
                    d={sparklinePath(series.points, 160, 28)}
                    fill="none"
                    stroke="currentColor"
                    strokeWidth={1.5}
                  />
                </svg>
                <span className="text-xs text-fg-muted">
                  {t('monitoring.ciLatest', 'Aktuell')}:{' '}
                  <strong>{latest ? latest.value : '–'}</strong>
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </Section>
  );
}

// ─── History (CI-06) ─────────────────────────────────────────────────────────

/** historyValue renders an old or new value of a field change; empty is "—". */
function historyValue(value: unknown): string {
  if (value === null || value === undefined || value === '') return '—';
  return typeof value === 'string' ? value : JSON.stringify(value);
}

/**
 * CIHistorySection lists the unified history of a CI (CI-06): changes,
 * history rows, location changes and audit entries, newest first, each with
 * its time and the site of the CI at that time. A CI change is one entry per
 * change set (AUD-03) with a line per changed field: name, old and new
 * value; a row written before the change sets is shown as before. Without
 * audit:read the server leaves the audit entries out (E-78); the section
 * then says so.
 */
export function CIHistorySection({ ciId }: { ciId: string }) {
  const { t, i18n } = useTranslation();
  const history = useEntityHistory('ci', ciId);
  const entries = history.data?.data ?? [];
  // Sites and location changes are shown with the names of the location
  // tree (CI-06), the location as its path.
  const locations = useLocationNames(
    entries.some(
      (entry) =>
        !!entry.site_id ||
        !!entry.field_changes?.some((change) => LOCATION_FIELDS.has(change.field)),
    ),
  );
  const changeText =
    (field: string) =>
    (value: unknown): string =>
      LOCATION_FIELDS.has(field) && typeof value === 'string' && value !== ''
        ? ((field === 'location_id' ? locations.path(value) : locations.name(value)) ?? value)
        : historyValue(value);
  return (
    <Section title={t('ci.history')} count={history.data?.total}>
      {history.isLoading ? (
        <SkeletonList rows={3} label={t('app.loading')} />
      ) : history.error ? (
        <p className="text-sm text-danger">{t('ci.historyLoadError')}</p>
      ) : entries.length === 0 ? (
        <p className="text-sm text-fg-muted">{t('ci.historyEmpty')}</p>
      ) : (
        <ol className="divide-y divide-line text-sm" aria-label={t('ci.history')}>
          {entries.map((entry) => (
            <li
              key={`${entry.source ?? 'change'}-${entry.id}`}
              className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-2.5 first:pt-0 last:pb-0"
            >
              <time dateTime={entry.created_at} className="text-fg-muted">
                {formatDateTime(entry.created_at, i18n.language)}
              </time>
              <span className="font-medium text-fg">
                {entry.change_type}
                {entry.field_name ? ` · ${entry.field_name}` : ''}
              </span>
              {entry.source ? (
                <Badge variant="neutral">{t(`ci.historySource.${entry.source}`)}</Badge>
              ) : null}
              {entry.site_id ? (
                <span className="text-fg-muted">
                  {t('ci.historySite', { site: locations.name(entry.site_id) })}
                </span>
              ) : null}
              {entry.change_set_id && entry.field_changes?.length ? (
                <ul className="basis-full space-y-0.5 pl-4 text-fg">
                  {entry.field_changes.map((change) => (
                    <li key={change.field} className="break-all">
                      {`${change.field}: ${fieldValueText(t, change.field, change.old, changeText(change.field))} → ${fieldValueText(t, change.field, change.new, changeText(change.field))}`}
                    </li>
                  ))}
                </ul>
              ) : null}
            </li>
          ))}
        </ol>
      )}
      {history.data?.audit_included === false ? (
        <p className="mt-2 text-xs text-fg-muted">{t('ci.historyAuditHint')}</p>
      ) : null}
    </Section>
  );
}
