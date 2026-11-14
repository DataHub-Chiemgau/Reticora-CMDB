import { useMemo, useState } from 'react';
import type { FormEvent } from 'react';
import { Link, useParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { Entitlement } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Textarea } from '../components/ui/Textarea';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { ScrollRegion } from '../components/ScrollRegion';
import { OperatorApiError, operatorApi } from './operatorClient';
import { planPreview, quotaFeature } from './planPreview';

const PLANS = ['essential', 'standard', 'pro', 'enterprise'];
const ORIGINS = ['plan', 'addon', 'manual'] as const;

function planName(t: TFunction, plan: string) {
  return t(`discoverability.plans.${plan}`, { defaultValue: plan });
}
function featureName(t: TFunction, key: string) {
  return t(`planPage.features.${key}`, { defaultValue: key });
}
function quotaName(t: TFunction, quota: string) {
  if (quota.startsWith('max_targets_')) {
    return t('planPage.quotaNames.maxTargetsOfType', { type: quota.slice('max_targets_'.length) });
  }
  return t(`planPage.quotaNames.${quota}`, { defaultValue: quota });
}
function limitText(t: TFunction, value: number | null | undefined) {
  return value === null || value === undefined ? t('planPage.quotas.unlimited') : String(value);
}

/**
 * errorMessage maps an operator API error to the text of a dialog: 422 at
 * its field (returned separately), 500 as the failed audit without a
 * success message (F-208).
 */
function dialogError(t: TFunction, err: unknown): { field?: string; message: string } {
  if (err instanceof OperatorApiError) {
    if (err.status === 422 || err.status === 400) {
      return { field: err.field, message: t('operator.detail.invalid', { detail: err.message }) };
    }
    if (err.status >= 500) return { message: t('operator.detail.failed') };
  }
  return { message: t('operator.detail.failed') };
}

/**
 * OrganizationDetailPage shows one organization for the operator (PKG-07,
 * WP-888): plan and plan_changed_at, the rows grouped by origin with status
 * and valid_until, use and origin of every limit, and the history of the
 * organization from operator_audit. The dialogs change the plan (with a
 * preview of the change and a mandatory reason), book and cancel add-ons and
 * override a limit; every action is audited on the server.
 */
export function OrganizationDetailPage() {
  const { id = '' } = useParams();
  const { t, i18n } = useTranslation();
  const formatDate = useMemo(
    () => new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium', timeStyle: 'short' }),
    [i18n.language],
  );
  const queryClient = useQueryClient();
  const entitlements = useQuery({
    queryKey: ['operator', 'entitlements', id],
    queryFn: () => operatorApi.entitlements(id),
  });
  const usage = useQuery({
    queryKey: ['operator', 'usage', id],
    queryFn: () => operatorApi.usage(id),
  });
  const plans = useQuery({ queryKey: ['operator', 'plans'], queryFn: () => operatorApi.plans() });
  const history = useQuery({
    queryKey: ['operator', 'history', id],
    queryFn: () => operatorApi.history(id),
  });
  const [dialog, setDialog] = useState<'plan' | 'addon' | 'limit' | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const refresh = () =>
    Promise.all(
      ['entitlements', 'usage', 'history', 'orgs'].map((k) =>
        queryClient.invalidateQueries({ queryKey: ['operator', k] }),
      ),
    );

  const cancelAddon = useMutation({
    mutationFn: (key: string) => operatorApi.cancelAddon(id, key),
    onSuccess: async () => {
      setNotice(t('operator.detail.addonCancelled'));
      await refresh();
    },
    onError: (err) => setNotice(dialogError(t, err).message),
  });

  const error = entitlements.error ?? usage.error ?? plans.error;
  if (error) {
    const notFound = error instanceof OperatorApiError && error.status === 404;
    return (
      <div className="space-y-6">
        <PageHeader title={t('operator.detail.title')} />
        <ErrorState
          title={notFound ? t('operator.detail.notFound') : t('operator.detail.loadError')}
        />
      </div>
    );
  }
  if (!entitlements.data || !usage.data || !plans.data) {
    return <SkeletonList />;
  }

  const plan = entitlements.data.plan;
  const rows = entitlements.data.data;
  const grouped = ORIGINS.map((origin) => ({
    origin,
    rows: rows.filter((r) => (r.origin ?? 'plan') === origin),
  }));

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('operator.detail.title')}
        breadcrumbs={[{ label: t('operator.orgs.title'), to: '/operator/orgs' }]}
        description={
          entitlements.data.plan_changed_at
            ? t('operator.detail.planSince', {
                plan: planName(t, plan),
                date: formatDate.format(new Date(entitlements.data.plan_changed_at)),
              })
            : t('operator.detail.planOfCreation', { plan: planName(t, plan) })
        }
        actions={
          <div className="flex flex-wrap gap-2">
            <Button size="sm" onClick={() => setDialog('plan')}>
              {t('operator.detail.changePlan')}
            </Button>
            <Button size="sm" variant="secondary" onClick={() => setDialog('addon')}>
              {t('operator.detail.bookAddon')}
            </Button>
            <Button size="sm" variant="secondary" onClick={() => setDialog('limit')}>
              {t('operator.detail.overrideLimit')}
            </Button>
          </div>
        }
      />
      {notice && (
        <p role="status" className="text-sm">
          {notice}
        </p>
      )}

      {grouped.map(({ origin, rows: list }) => (
        <Card key={origin} title={t(`operator.detail.origins.${origin}`)}>
          {list.length === 0 ? (
            <p className="text-sm text-fg-muted">{t('operator.detail.noRows')}</p>
          ) : (
            <ul
              className="divide-y divide-gray-100 dark:divide-gray-800"
              aria-label={t(`operator.detail.origins.${origin}`)}
            >
              {list.map((row) => (
                <li
                  key={row.feature_key}
                  className="flex flex-wrap items-center gap-3 py-2 text-sm"
                >
                  <span className="font-medium">{featureName(t, row.feature_key)}</span>
                  <Badge variant={row.enabled ? 'success' : 'neutral'}>
                    {row.enabled ? t('operator.detail.active') : t('operator.detail.inactive')}
                  </Badge>
                  {row.valid_until && (
                    <span className="text-fg-muted">
                      {t('operator.detail.validUntil', {
                        date: formatDate.format(new Date(row.valid_until)),
                      })}
                    </span>
                  )}
                  {origin === 'addon' && row.enabled && (
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => cancelAddon.mutate(row.feature_key)}
                      disabled={cancelAddon.isPending}
                    >
                      {t('operator.detail.cancelAddon', { name: featureName(t, row.feature_key) })}
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Card>
      ))}

      <Card title={t('planPage.quotas.title')}>
        <ScrollRegion label={t('planPage.quotas.title')}>
          <table className={tableClasses}>
            <thead className={theadClasses}>
              <tr>
                <th scope="col" className={thClasses}>
                  {t('planPage.quotas.quota')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('planPage.quotas.usage')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('planPage.quotas.origin')}
                </th>
              </tr>
            </thead>
            <tbody>
              {usage.data.data.map((u) => (
                <tr key={u.quota} className={trClasses}>
                  <td className={tdPrimaryClasses}>{quotaName(t, u.quota)}</td>
                  <td className={tdClasses}>
                    {u.used === null
                      ? t('planPage.quotas.limitOnly', { limit: limitText(t, u.limit) })
                      : t('planPage.quotas.usedOf', { used: u.used, limit: limitText(t, u.limit) })}
                  </td>
                  <td className={tdClasses}>{t(`planPage.origins.${u.origin}`)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </ScrollRegion>
      </Card>

      <Card title={t('operator.detail.history')}>
        {!history.data || history.data.data.length === 0 ? (
          <p className="text-sm text-fg-muted">{t('operator.detail.noHistory')}</p>
        ) : (
          <ul className="space-y-1 text-sm" aria-label={t('operator.detail.history')}>
            {history.data.data.map((e) => (
              <li key={e.id}>
                {t('operator.detail.historyEntry', {
                  date: formatDate.format(new Date(e.timestamp)),
                  action: t(`operator.actions.${e.action}`, { defaultValue: e.action }),
                  operator: e.operator_id,
                })}
              </li>
            ))}
          </ul>
        )}
      </Card>

      <PlanDialog
        open={dialog === 'plan'}
        onClose={() => setDialog(null)}
        orgId={id}
        current={plan}
        rows={rows}
        matrix={plans.data.data}
        onDone={async (message) => {
          setNotice(message);
          await refresh();
        }}
      />
      <AddonDialog
        open={dialog === 'addon'}
        onClose={() => setDialog(null)}
        orgId={id}
        addons={plans.data.addons.map((a) => a.key)}
        onDone={async (message) => {
          setNotice(message);
          await refresh();
        }}
      />
      <LimitDialog
        open={dialog === 'limit'}
        onClose={() => setDialog(null)}
        orgId={id}
        plan={plan}
        rows={rows}
        quotas={usage.data.data.map((u) => u.quota)}
        onDone={async (message) => {
          setNotice(message);
          await refresh();
        }}
      />
      <p className="text-sm">
        <Link to="/operator/orgs" className="text-accent hover:underline">
          {t('operator.detail.back')}
        </Link>
      </p>
    </div>
  );
}

interface DialogProps {
  open: boolean;
  onClose: () => void;
  orgId: string;
  onDone: (message: string) => Promise<void>;
}

function PlanDialog({
  open,
  onClose,
  orgId,
  current,
  rows,
  matrix,
  onDone,
}: DialogProps & {
  current: string;
  rows: Entitlement[];
  matrix: Parameters<typeof planPreview>[3];
}) {
  const { t } = useTranslation();
  const [target, setTarget] = useState(current);
  const [reason, setReason] = useState('');
  const [error, setError] = useState<{ field?: string; message: string } | null>(null);
  const preview = planPreview(current, target, rows, matrix);
  const change = useMutation({
    mutationFn: () => operatorApi.changePlan(orgId, target, reason),
    onSuccess: async () => {
      onClose();
      setReason('');
      await onDone(t('operator.detail.planChanged', { plan: planName(t, target) }));
    },
    onError: (err) => setError(dialogError(t, err)),
  });
  function submit(event: FormEvent) {
    event.preventDefault();
    setError(null);
    change.mutate();
  }
  return (
    <Modal
      open={open}
      onOpenChange={(o) => !o && onClose()}
      title={t('operator.detail.changePlan')}
      size="md"
    >
      <form onSubmit={submit} className="space-y-4">
        <Select
          label={t('operator.detail.newPlan')}
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          options={PLANS.map((p) => ({ value: p, label: planName(t, p) }))}
          error={error?.field === 'plan' ? error.message : undefined}
        />
        <section aria-label={t('operator.detail.preview')} className="space-y-2 text-sm">
          <p className="font-medium">{t('operator.detail.preview')}</p>
          {target === current ? (
            <p>{t('operator.detail.previewUnchanged')}</p>
          ) : (
            <>
              <p>
                {t('operator.detail.previewUnlocked')}:{' '}
                {preview.unlocked.map((f) => featureName(t, f)).join(', ') || '–'}
              </p>
              <p>
                {t('operator.detail.previewLocked')}:{' '}
                {preview.locked.map((f) => featureName(t, f)).join(', ') || '–'}
              </p>
              <p>{t('operator.detail.previewLimits')}:</p>
              <ul className="list-inside list-disc">
                {preview.limits.length === 0 && <li>–</li>}
                {preview.limits.map((l) => (
                  <li key={l.quota}>
                    {t('operator.detail.previewLimit', {
                      quota: quotaName(t, l.quota),
                      from: limitText(t, l.from),
                      to: limitText(t, l.to),
                    })}
                  </li>
                ))}
              </ul>
            </>
          )}
        </section>
        <Textarea
          label={t('operator.detail.reason')}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          maxLength={500}
          rows={3}
          required
          error={error?.field === 'reason' ? error.message : undefined}
        />
        {error && error.field !== 'reason' && error.field !== 'plan' && (
          <p role="alert" className="text-sm text-danger">
            {error.message}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" disabled={change.isPending}>
            {t('operator.detail.confirmPlan')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function AddonDialog({ open, onClose, orgId, addons, onDone }: DialogProps & { addons: string[] }) {
  const { t } = useTranslation();
  const [key, setKey] = useState(addons[0] ?? '');
  const [until, setUntil] = useState('');
  const [error, setError] = useState<{ field?: string; message: string } | null>(null);
  const book = useMutation({
    mutationFn: () =>
      operatorApi.bookAddon(
        orgId,
        key,
        until ? new Date(`${until}T23:59:59Z`).toISOString() : undefined,
      ),
    onSuccess: async () => {
      onClose();
      setUntil('');
      await onDone(t('operator.detail.addonBooked', { name: featureName(t, key) }));
    },
    onError: (err) => setError(dialogError(t, err)),
  });
  return (
    <Modal
      open={open}
      onOpenChange={(o) => !o && onClose()}
      title={t('operator.detail.bookAddon')}
      size="sm"
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          setError(null);
          book.mutate();
        }}
        className="space-y-4"
      >
        <Select
          label={t('operator.detail.addon')}
          value={key}
          onChange={(e) => setKey(e.target.value)}
          options={addons.map((a) => ({ value: a, label: featureName(t, a) }))}
          error={error?.field === 'key' ? error.message : undefined}
        />
        <Input
          type="date"
          label={t('operator.detail.term')}
          value={until}
          onChange={(e) => setUntil(e.target.value)}
          error={error?.field === 'valid_until' ? error.message : undefined}
        />
        {error && error.field !== 'key' && error.field !== 'valid_until' && (
          <p role="alert" className="text-sm text-danger">
            {error.message}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" disabled={book.isPending || !key}>
            {t('operator.detail.confirmAddon')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function LimitDialog({
  open,
  onClose,
  orgId,
  plan,
  rows,
  quotas,
  onDone,
}: DialogProps & { plan: string; rows: Entitlement[]; quotas: string[] }) {
  const { t } = useTranslation();
  const [quota, setQuota] = useState(quotas[0] ?? '');
  const [value, setValue] = useState('');
  const [error, setError] = useState<{ field?: string; message: string } | null>(null);
  const grant = useMutation({
    mutationFn: () => {
      const feature = quotaFeature(quota) ?? '';
      const row = rows.find((r) => r.feature_key === feature);
      return operatorApi.grant(orgId, {
        feature_key: feature,
        plan: row?.plan ?? plan,
        enabled: row?.enabled ?? true,
        limits: { ...(row?.limits ?? {}), [quota]: Number(value) },
        valid_until: row?.valid_until,
        source: row?.source,
      });
    },
    onSuccess: async () => {
      onClose();
      setValue('');
      await onDone(t('operator.detail.limitSaved', { quota: quotaName(t, quota) }));
    },
    onError: (err) => setError(dialogError(t, err)),
  });
  return (
    <Modal
      open={open}
      onOpenChange={(o) => !o && onClose()}
      title={t('operator.detail.overrideLimit')}
      size="sm"
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          setError(null);
          grant.mutate();
        }}
        className="space-y-4"
      >
        <Select
          label={t('planPage.quotas.quota')}
          value={quota}
          onChange={(e) => setQuota(e.target.value)}
          options={quotas.map((q) => ({ value: q, label: quotaName(t, q) }))}
        />
        <Input
          type="number"
          min={0}
          required
          label={t('operator.detail.limitValue')}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          error={error?.field === 'limits' ? error.message : undefined}
        />
        {error && error.field !== 'limits' && (
          <p role="alert" className="text-sm text-danger">
            {error.message}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" disabled={grant.isPending || value === ''}>
            {t('operator.detail.confirmLimit')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
