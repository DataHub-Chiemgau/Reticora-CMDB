/**
 * LabelPrintDialog — printing labels (INV-03, UI-14, E-52, E-135, WP-813).
 * With an asset it loads the PDF label of that asset (GET
 * /assets/{id}/label?format=pdf&template=…) through fetchBlob, with the
 * bearer token, and opens it as an object URL. With an area it renders the
 * labels of the inventory area as job labels.batch (POST /labels/batch with
 * template, kind and location_id): the progress comes from useJobs, the
 * result {labels, pages, expires_at} from useJobResult, and GET
 * /labels/batch/{job_id}/download names a short-lived signed URL the dialog
 * opens directly. A refused request (409 result not ready, 503 no blob
 * storage) shows the detail of the problem.
 *
 * The dialog is offered only with the feature barcode (E-44); the caller
 * checks it with useLabelGate.
 */
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Printer } from 'lucide-react';
import {
  ApiError,
  labelDownloadApi,
  type LabelBatchResult,
  type LabelTemplate,
} from '../../api/client';
import { useEntitlements } from '../../hooks/useEntitlements';
import { useJobResult, useJobs, type Job } from '../../hooks/useJobs';
import { formatDateTime } from '../cmdb/ObjectHistory';
import { Button } from '../ui/Button';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';

/** LABEL_TEMPLATES are the templates of a label PDF (E-52); labels.templates.* name them. */
export const LABEL_TEMPLATES = ['asset_50x25', 'bin_100x50', 'a4_24'] as const;
/** ASSET_LABEL_TEMPLATES are the templates of the label of one asset. */
export const ASSET_LABEL_TEMPLATES = ['asset_50x25', 'a4_24'] as const;
/** LABEL_KINDS are the object kinds of a label batch; labels.kinds.* name them. */
export const LABEL_KINDS = ['asset', 'consumable', 'location'] as const;

type AssetTemplate = (typeof ASSET_LABEL_TEMPLATES)[number];
type LabelKind = (typeof LABEL_KINDS)[number];

/** LABEL_BATCH_KIND is the job kind of a label batch (WP-763). */
export const LABEL_BATCH_KIND = 'labels.batch';
/** OBJECT_URL_TTL_MS keeps the object URL of a label PDF alive for the opened tab. */
const OBJECT_URL_TTL_MS = 60_000;

/** useLabelGate tells whether labels are printed: the feature barcode of the plan. */
export function useLabelGate(): boolean {
  const { isEnabled } = useEntitlements();
  return isEnabled('barcode');
}

/** errorDetail is the detail of a refused request, else its message. */
function errorDetail(error: unknown): string {
  if (error instanceof ApiError) return error.detail ?? error.message;
  return error instanceof Error ? error.message : String(error);
}

/**
 * openURL opens a URL in a new tab; without one (pop-up blocked) it returns
 * false and the dialog offers the link instead.
 */
function openURL(url: string): boolean {
  const opened = window.open(url, '_blank');
  if (!opened) return false;
  opened.opener = null;
  return true;
}

export interface LabelPrintDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** asset prints the label of this asset. */
  asset?: { id: string; name: string };
  /** areaId prints the labels of the inventory area below this location. */
  areaId?: string;
  /** areaName names the area in the dialog. */
  areaName?: string;
}

export function LabelPrintDialog({
  open,
  onOpenChange,
  asset,
  areaId,
  areaName,
}: LabelPrintDialogProps) {
  const { t } = useTranslation();
  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      title={t('labels.title')}
      description={
        asset
          ? t('labels.assetDescription', { name: asset.name })
          : t('labels.batchDescription', { name: areaName ?? '' })
      }
    >
      {open && asset ? <AssetLabel asset={asset} onClose={() => onOpenChange(false)} /> : null}
      {open && !asset && areaId ? (
        <BatchLabels areaId={areaId} onClose={() => onOpenChange(false)} />
      ) : null}
    </Modal>
  );
}

function AssetLabel({
  asset,
  onClose,
}: {
  asset: { id: string; name: string };
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [template, setTemplate] = useState<AssetTemplate>('asset_50x25');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [fallback, setFallback] = useState<string>();

  useEffect(
    () => () => {
      if (fallback) URL.revokeObjectURL(fallback);
    },
    [fallback],
  );

  const load = async () => {
    setBusy(true);
    setError(undefined);
    try {
      const blob = await labelDownloadApi.assetLabel(asset.id, template);
      const url = URL.createObjectURL(blob);
      if (openURL(url)) {
        setTimeout(() => URL.revokeObjectURL(url), OBJECT_URL_TTL_MS);
      } else {
        setFallback(url);
      }
    } catch (err) {
      setError(errorDetail(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      <Select
        label={t('labels.template')}
        value={template}
        onChange={(event) => setTemplate(event.target.value as AssetTemplate)}
        options={ASSET_LABEL_TEMPLATES.map((value) => ({
          value,
          label: t(`labels.templates.${value}`),
        }))}
      />
      {error ? (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      ) : null}
      {fallback ? (
        <p className="text-sm">
          <a href={fallback} target="_blank" rel="noreferrer" className="text-accent underline">
            {t('labels.openPdf')}
          </a>
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          {t('labels.close')}
        </Button>
        <Button icon={<Printer />} loading={busy} onClick={() => void load()}>
          {t('labels.printAsset')}
        </Button>
      </div>
    </div>
  );
}

function BatchLabels({ areaId, onClose }: { areaId: string; onClose: () => void }) {
  const { t, i18n } = useTranslation();
  const [template, setTemplate] = useState<LabelTemplate>('a4_24');
  const [kind, setKind] = useState<LabelKind>('asset');
  const [jobId, setJobId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [fallback, setFallback] = useState<string>();

  const jobs = useJobs({ kind: LABEL_BATCH_KIND });
  const job: Job | undefined = jobId ? jobs.data?.data.find((row) => row.id === jobId) : undefined;
  const succeeded = job?.status === 'succeeded';
  const result = useJobResult(succeeded ? jobId : null);
  const summary = result.data as LabelBatchResult | undefined;

  const start = async () => {
    setBusy(true);
    setError(undefined);
    setFallback(undefined);
    try {
      const created = await labelDownloadApi.batch({ template, kind, location_id: areaId });
      setJobId(created.job_id);
      void jobs.refetch();
    } catch (err) {
      setError(errorDetail(err));
    } finally {
      setBusy(false);
    }
  };

  const download = async () => {
    if (!jobId) return;
    setBusy(true);
    setError(undefined);
    try {
      const res = await labelDownloadApi.batchDownload(jobId);
      if (!openURL(res.download_url)) setFallback(res.download_url);
    } catch (err) {
      setError(errorDetail(err));
    } finally {
      setBusy(false);
    }
  };

  let progress: string | undefined;
  if (jobId && !job) progress = t('labels.queued');
  else if (job) {
    progress = t('labels.progress', {
      status: t(`jobs.statuses.${job.status}`, job.status),
      progress: job.progress,
    });
  }

  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <Select
          label={t('labels.template')}
          value={template}
          onChange={(event) => setTemplate(event.target.value as LabelTemplate)}
          options={LABEL_TEMPLATES.map((value) => ({
            value,
            label: t(`labels.templates.${value}`),
          }))}
        />
        <Select
          label={t('labels.kind')}
          value={kind}
          onChange={(event) => setKind(event.target.value as LabelKind)}
          options={LABEL_KINDS.map((value) => ({ value, label: t(`labels.kinds.${value}`) }))}
        />
      </div>
      <div aria-live="polite" className="space-y-1 text-sm text-fg-muted">
        {progress ? <p>{progress}</p> : null}
        {job?.status === 'failed' && job.error ? <p className="text-danger">{job.error}</p> : null}
        {summary ? (
          <p>
            {t('labels.result', {
              labels: summary.labels,
              pages: summary.pages,
              expires: formatDateTime(summary.expires_at, i18n.language),
            })}
          </p>
        ) : null}
      </div>
      {error ? (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      ) : null}
      {fallback ? (
        <p className="text-sm">
          <a href={fallback} target="_blank" rel="noreferrer" className="text-accent underline">
            {t('labels.openPdf')}
          </a>
        </p>
      ) : null}
      <div className="flex flex-wrap justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          {t('labels.close')}
        </Button>
        {succeeded ? (
          <Button icon={<Printer />} loading={busy} onClick={() => void download()}>
            {t('labels.download')}
          </Button>
        ) : (
          <Button
            icon={<Printer />}
            loading={busy}
            disabled={!!job && (job.status === 'queued' || job.status === 'running')}
            onClick={() => void start()}
          >
            {t('labels.startBatch')}
          </Button>
        )}
      </div>
    </div>
  );
}
