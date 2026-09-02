/**
 * CIDetailSections — advanced, collapsible sections for the CI detail page
 * (spec §18 progressive disclosure): instance fields, field provenance /
 * manual overrides, lifecycle, and impact analysis. Each is an expandable
 * <details> so normal users never see the metadata model.
 */
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  useInstanceFields,
  useUpsertInstanceField,
  useDeleteInstanceField,
  useFieldValues,
  useSetOverride,
  useClearOverride,
  useBlastRadius,
  useLifecycleDefinitions,
  useLifecycleTransition,
} from '../../api/cmdbHooks';
import { DynamicForm, validateAll } from '../form/DynamicForm';
import { ApiError } from '../../api/client';
import type { FieldDefinition } from '../../lib/fieldmeta';
import { Badge } from '../ui/Badge';
import { Button } from '../ui/Button';
import { Input } from '../ui/Input';
import { Select } from '../ui/Select';
import { Modal } from '../ui/Modal';
import { SkeletonList } from '../ui/Skeleton';

function Section({
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
    <details open={defaultOpen} className="rounded-lg border border-gray-200 dark:border-gray-700">
      <summary className="cursor-pointer select-none px-4 py-3 text-sm font-semibold text-gray-700 hover:bg-gray-50 dark:text-gray-200 dark:hover:bg-gray-800">
        {title}
        {count !== undefined ? (
          <Badge variant="neutral" className="ml-2">
            {count}
          </Badge>
        ) : null}
      </summary>
      <div className="border-t border-gray-200 px-4 py-3 dark:border-gray-700">{children}</div>
    </details>
  );
}

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
  const upsert = useUpsertInstanceField(ciId);
  const remove = useDeleteInstanceField(ciId);
  const [showAdd, setShowAdd] = useState(false);
  const [draft, setDraft] = useState<FieldDefinition>({ name: '', data_type: 'text' });
  const [localErrors, setLocalErrors] = useState<Record<string, string | null>>({});
  // Local draft of the attribute values. The saved CI stays the source of
  // truth, but edits are rendered from the draft so in-flight (and invalid)
  // input is not overwritten by the server response mid-typing.
  const [valueDraft, setValueDraft] = useState<Record<string, unknown> | null>(null);
  const draftValues = valueDraft ?? attributes;

  const serverViolations =
    serverError instanceof ApiError ? serverError.violationsByField() : ({} as Record<string, string>);

  const defs = fields.data?.data ?? [];
  return (
    <Section title={t('ci.instanceFields', 'Instanzfelder')} count={defs.length}>
      <p className="mb-3 text-xs text-gray-500">
        {t(
          'ci.instanceFieldsHint',
          'Felder nur für dieses Gerät (z. B. iLO-IP, SAP-SID) — ohne den Typ zu ändern.',
        )}
      </p>
      {fields.isLoading ? <SkeletonList rows={2} label="…" /> : null}
      <ul className="mb-3 space-y-1 text-sm">
        {defs.map((f) => (
          <li key={f.name} className="flex items-center justify-between">
            <span>
              <span className="font-medium">{f.label || f.name}</span>
              <span className="ml-2 text-xs text-gray-400">{f.data_type}</span>
            </span>
            <button
              className="text-xs text-red-600 hover:underline"
              onClick={() => remove.mutate(f.name)}
            >
              {t('common.delete', 'Löschen')}
            </button>
          </li>
        ))}
        {defs.length === 0 ? (
          <li className="text-sm text-gray-500">
            {t('ci.noInstanceFields', 'Keine Instanzfelder.')}
          </li>
        ) : null}
      </ul>
      <Button size="sm" variant="secondary" onClick={() => setShowAdd(true)}>
        + {t('ci.addInstanceField', 'Instanzfeld hinzufügen')}
      </Button>

      {serverError && Object.keys(serverViolations).length === 0 ? (
        <p role="alert" className="mt-2 text-sm text-red-600 dark:text-red-400">
          {serverError.message}
        </p>
      ) : null}

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
              'textarea',
              'integer',
              'decimal',
              'boolean',
              'date',
              'datetime',
              'enum',
              'ip',
              'mac',
              'url',
              'email',
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
      {defs.length > 0 ? (
        <div className="mt-4 border-t border-gray-100 pt-3 dark:border-gray-800">
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
        </div>
      ) : null}
    </Section>
  );
}

// ─── Field provenance / overrides (§13) ──────────────────────────────────────

export function ProvenanceSection({ ciId }: { ciId: string }) {
  const { t } = useTranslation();
  const values = useFieldValues(ciId);
  const setOverride = useSetOverride(ciId);
  const clearOverride = useClearOverride(ciId);
  const [overrideField, setOverrideField] = useState<string | undefined>();
  const [draft, setDraft] = useState({ value: '', reason: '' });

  const rows = values.data?.data ?? [];
  const diverged = rows.filter((r) => r.diverged);
  return (
    <Section title={t('ci.provenance', 'Herkunft & Overrides')} count={diverged.length}>
      <p className="mb-3 text-xs text-gray-500">
        {t(
          'ci.provenanceHint',
          'Discovery-Wert vs. effektiver Wert. Geschützte manuelle Overrides werden nie von Discovery überschrieben.',
        )}
      </p>
      {values.isLoading ? <SkeletonList rows={2} label="…" /> : null}
      <ul className="space-y-2 text-sm">
        {rows.map((fv) => (
          <li
            key={fv.field_name}
            className="rounded-md border border-gray-100 px-3 py-2 dark:border-gray-800"
          >
            <div className="flex items-center justify-between">
              <span className="font-medium">{fv.field_name}</span>
              <div className="flex items-center gap-2">
                {fv.diverged ? (
                  <Badge variant="warning">{t('ci.diverged', 'abweichend')}</Badge>
                ) : null}
                {fv.protected ? (
                  <Badge variant="info">{t('ci.protected', 'geschützt')}</Badge>
                ) : null}
                {fv.override_value !== undefined && fv.override_value !== null ? (
                  <button
                    className="text-xs text-red-600 hover:underline"
                    onClick={() => clearOverride.mutate(fv.field_name)}
                  >
                    {t('ci.clearOverride', 'Override aufheben')}
                  </button>
                ) : (
                  <button
                    className="text-xs text-primary hover:underline"
                    onClick={() => {
                      setOverrideField(fv.field_name);
                      setDraft({ value: '', reason: '' });
                    }}
                  >
                    {t('ci.setOverride', 'Override setzen')}
                  </button>
                )}
              </div>
            </div>
            <dl className="mt-1 grid grid-cols-3 gap-2 text-xs">
              <div>
                <dt className="text-gray-400">{t('ci.discovered', 'Discovery')}</dt>
                <dd className="font-mono">{JSON.stringify(fv.discovered_value) ?? '—'}</dd>
              </div>
              <div>
                <dt className="text-gray-400">{t('ci.override', 'Override')}</dt>
                <dd className="font-mono">
                  {fv.override_value !== undefined && fv.override_value !== null
                    ? JSON.stringify(fv.override_value)
                    : '—'}
                </dd>
              </div>
              <div>
                <dt className="text-gray-400">{t('ci.effective', 'Effektiv')}</dt>
                <dd className="font-mono font-semibold">
                  {JSON.stringify(fv.effective_value) ?? '—'}
                </dd>
              </div>
            </dl>
            {fv.discovered_source ? (
              <p className="mt-1 text-xs text-gray-400">
                {t('ci.source', 'Quelle')}: {fv.discovered_source}
              </p>
            ) : null}
          </li>
        ))}
        {rows.length === 0 && !values.isLoading ? (
          <li className="text-sm text-gray-500">
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
  const defs = useLifecycleDefinitions();
  const transition = useLifecycleTransition(entityType, entityId);
  const [target, setTarget] = useState('');
  const [reason, setReason] = useState('');

  // Use the default physical asset lifecycle (or the first definition).
  const def =
    (defs.data?.data ?? []).find((d) => d.key === 'physical_asset') ?? (defs.data?.data ?? [])[0];
  const states = def?.states ?? [];
  const current = currentState || states.find((s) => s.is_initial)?.key || '';

  return (
    <Section title={t('lifecycle.title', 'Lebenszyklus')}>
      <div className="mb-3 flex items-center gap-2">
        <span className="text-sm text-gray-500">{t('lifecycle.current', 'Aktuell')}:</span>
        <Badge variant="info">{current || '—'}</Badge>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <Select
          label={t('lifecycle.transitionTo', 'Übergang nach')}
          options={[
            { value: '', label: '—' },
            ...states
              .filter((s) => s.key !== current)
              .map((s) => ({ value: s.key, label: s.label })),
          ]}
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          className="max-w-xs"
        />
        <Input
          label={t('lifecycle.reason', 'Grund')}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          className="max-w-xs"
        />
        <Button
          size="sm"
          disabled={!target || transition.isPending}
          onClick={() => transition.mutate({ toState: target, reason })}
        >
          {t('lifecycle.apply', 'Anwenden')}
        </Button>
      </div>
      {transition.error ? (
        <p className="mt-2 text-sm text-red-600">{String(transition.error)}</p>
      ) : null}
    </Section>
  );
}

// ─── Impact analysis (§15) ───────────────────────────────────────────────────

export function ImpactSection({ ciId }: { ciId: string }) {
  const { t } = useTranslation();
  const [enabled, setEnabled] = useState(false);
  const blast = useBlastRadius(ciId, enabled);

  return (
    <Section title={t('impact.title', 'Auswirkungsanalyse')}>
      {!enabled ? (
        <Button size="sm" variant="secondary" onClick={() => setEnabled(true)}>
          {t('impact.compute', 'Blast Radius berechnen')}
        </Button>
      ) : blast.isLoading ? (
        <SkeletonList rows={3} label="…" />
      ) : blast.data ? (
        <div className="space-y-3">
          <div className="flex gap-4 text-sm">
            <span>
              {t('impact.affected', 'Betroffene CIs')}: <strong>{blast.data.affected_count}</strong>
            </span>
            <span>
              {t('impact.clients', 'Kunden')}: <strong>{blast.data.client_ids?.length ?? 0}</strong>
            </span>
            <span>
              {t('impact.sites', 'Standorte')}: <strong>{blast.data.site_ids?.length ?? 0}</strong>
            </span>
            <span className="text-red-600">
              {t('impact.spof', 'SPOF')}:{' '}
              <strong>{blast.data.single_points_of_failure?.length ?? 0}</strong>
            </span>
          </div>
          {(blast.data.single_points_of_failure?.length ?? 0) > 0 ? (
            <div>
              <p className="text-xs font-semibold text-red-600">
                {t('impact.spofList', 'Single Points of Failure:')}
              </p>
              <ul className="text-sm">
                {blast.data.single_points_of_failure!.map((n) => (
                  <li key={n.id}>• {n.name}</li>
                ))}
              </ul>
            </div>
          ) : null}
          <div>
            <p className="text-xs font-semibold text-gray-500">
              {t('impact.affectedList', 'Betroffen:')}
            </p>
            <ul className="max-h-40 overflow-y-auto text-sm">
              {(blast.data.affected_cis ?? []).map((n) => (
                <li key={n.id}>
                  • {n.name} <span className="text-xs text-gray-400">({n.ci_type})</span>
                </li>
              ))}
            </ul>
          </div>
        </div>
      ) : null}
    </Section>
  );
}
