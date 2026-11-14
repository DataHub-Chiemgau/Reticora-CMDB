import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useSearchParams } from 'react-router';
import type { TFunction } from 'i18next';
import { ArrowLeft, ArrowRight, Download, Play, RotateCcw, Save, Trash2 } from 'lucide-react';
import {
  ApiError,
  PROBLEM_DOCUMENT_PENDING_SCAN,
  PROBLEM_DOCUMENT_QUARANTINED,
  PROBLEM_FEATURE_NOT_ENTITLED,
  PROBLEM_IMPORT_FILE_TOO_COMPLEX,
  PROBLEM_IMPORT_FILE_TOO_LARGE,
  PROBLEM_IMPORT_FORMAT_UNSUPPORTED,
  PROBLEM_IMPORT_REPORT_NOT_READY,
  PROBLEM_IMPORT_SHEET_NOT_FOUND,
  importApi,
  importJobResult,
  type ImportMapping,
  type ImportPreview,
  type ImportProfile,
} from '../api/client';
import { localizedText } from '../api/cmdb';
import {
  useCreateImportProfile,
  useDeleteImportProfile,
  useDocument,
  useHasPermission,
  useImportErrors,
  useImportPreview,
  useImportProfiles,
  useImportRun,
  useStartImport,
  useUpdateImportProfile,
} from '../api/hooks';
import {
  useApplicableAttributeDefinitions,
  useCITypes,
  useRelationshipTypes,
} from '../api/cmdbHooks';
import {
  IMPORT_ENTITIES,
  IMPORT_MODES,
  MappingEditor,
  emptyMapping,
  importFieldSpecs,
  type ImportEntity,
  type ImportMode,
} from '../components/import/MappingEditor';
import { ScanStatusBadge } from '../components/documents/LinkedDocuments';
import { DEFAULT_MAX_UPLOAD_BYTES, FileUpload } from '../components/upload/FileUpload';
import { fieldSpecLabel } from '../components/rules/AttributePicker';
import { ScrollRegion } from '../components/ScrollRegion';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import {
  tableClasses,
  tdClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import { isActive } from '../hooks/useJobs';

/** The steps of the assistant, in order. */
export const IMPORT_STEPS = ['file', 'mapping', 'check', 'run'] as const;
type Step = (typeof IMPORT_STEPS)[number];

/** The file types of the assistant (E-49): CSV and XLSX, checked by extension first. */
export const IMPORT_ACCEPT = ['.csv', '.xlsx'] as const;

/** The default of RETICORA_DOCUMENT_MAX_UPLOAD_MB and the file limit of E-49: 50 MiB. */
export const IMPORT_MAX_BYTES = DEFAULT_MAX_UPLOAD_BYTES;

/** The API import (WP-303): rows per request. */
export const API_IMPORT_MAX_ROWS = 5000;

/**
 * importErrorMessage words a refused preview, start or report request: the
 * scan states of the document (409), the file problems of E-49 (422), a
 * missing right or entitlement (403) and the detail of anything else.
 */
export function importErrorMessage(error: unknown, t: TFunction): string {
  if (error instanceof ApiError) {
    switch (error.type) {
      case PROBLEM_DOCUMENT_QUARANTINED:
        return t('import.file.infected');
      case PROBLEM_DOCUMENT_PENDING_SCAN:
        return t('import.file.notClean');
      case PROBLEM_IMPORT_FORMAT_UNSUPPORTED:
        return t('import.errors.import-format-unsupported');
      case PROBLEM_IMPORT_SHEET_NOT_FOUND:
        return t('import.errors.import-sheet-not-found');
      case PROBLEM_IMPORT_FILE_TOO_COMPLEX:
        return t('import.errors.import-file-too-complex');
      case PROBLEM_IMPORT_FILE_TOO_LARGE:
        return t('import.errors.import-file-too-large');
      case PROBLEM_IMPORT_REPORT_NOT_READY:
        return t('import.report.notReady');
      case PROBLEM_FEATURE_NOT_ENTITLED:
        return t('import.errors.notEntitled');
    }
    if (error.status === 403) return t('import.errors.forbidden');
    const detail = error.detail ?? error.title ?? error.message;
    return error.field ? t('import.errors.field', { field: error.field, detail }) : detail;
  }
  return error instanceof Error ? error.message : String(error);
}

/** The profile being edited: the fields of the request without format and sheet. */
interface Draft {
  name: string;
  entity: ImportEntity;
  mode: ImportMode;
  mapping: ImportMapping;
  identityKeys: string[];
}

const newDraft = (): Draft => ({
  name: '',
  entity: 'ci',
  mode: 'both',
  mapping: emptyMapping(),
  identityKeys: [],
});

function draftOf(profile: ImportProfile): Draft {
  return {
    name: profile.name,
    entity: profile.entity,
    mode: profile.mode,
    mapping: {
      columns: { ...profile.mapping.columns },
      defaults: { ...(profile.mapping.defaults ?? {}) },
    },
    identityKeys: [...profile.identity_keys],
  };
}

/** Stepper shows the steps; a finished step can be opened again. */
function Stepper({
  current,
  reached,
  onOpen,
}: {
  current: Step;
  reached: number;
  onOpen: (step: Step) => void;
}) {
  const { t } = useTranslation();
  return (
    <nav aria-label={t('import.steps.label')}>
      <ol className="flex flex-wrap gap-2">
        {IMPORT_STEPS.map((step, index) => (
          <li key={step}>
            <button
              type="button"
              disabled={index > reached}
              aria-current={step === current ? 'step' : undefined}
              onClick={() => onOpen(step)}
              className={cn(
                'rounded-full border px-3 py-1 text-sm transition-colors disabled:cursor-not-allowed disabled:opacity-60',
                step === current
                  ? 'border-accent bg-accent-soft font-medium text-fg'
                  : 'border-line text-fg-muted hover:text-fg',
              )}
            >
              {t('import.steps.item', { index: index + 1, name: t(`import.steps.${step}`) })}
            </button>
          </li>
        ))}
      </ol>
    </nav>
  );
}

/** PermissionHint names the right an action needs while it is missing. */
function PermissionHint({ allowed, text }: { allowed: boolean | undefined; text: string }) {
  if (allowed !== false) return null;
  return <p className="text-xs text-fg-muted">{text}</p>;
}

/**
 * ImportErrorList shows the row errors of a finished run page by page (GET
 * /imports/{id}/errors): line, field, code and message, the next page over
 * next_cursor, the previous one from the cursors already read.
 */
function ImportErrorList({ jobId }: { jobId: string }) {
  const { t } = useTranslation();
  // cursors[i] is the cursor of page i (undefined: the first page).
  const [cursors, setCursors] = useState<(string | undefined)[]>([undefined]);
  const pageIndex = cursors.length - 1;
  const errors = useImportErrors(jobId, cursors[pageIndex], true);

  if (errors.isError) {
    return (
      <p role="alert" className="text-sm text-danger">
        {importErrorMessage(errors.error, t)}
      </p>
    );
  }
  const page = errors.data;
  if (!page) {
    return (
      <p role="status" className="text-sm text-fg-muted">
        {t('import.report.errorList.loading')}
      </p>
    );
  }
  if (page.total === 0) {
    return <p className="text-sm text-fg-muted">{t('import.report.errorList.empty')}</p>;
  }
  const next = page.has_more ? page.next_cursor : undefined;
  const from = page.offset + 1;
  const to = page.offset + page.data.length;
  return (
    <section
      className="space-y-2"
      aria-label={t('import.report.errorList.title')}
      data-testid="import-errors"
    >
      <h3 className="text-sm font-semibold text-fg">{t('import.report.errorList.title')}</h3>
      <ScrollRegion label={t('import.report.errorList.table')}>
        <table className={tableClasses}>
          <thead className={theadClasses}>
            <tr>
              <th scope="col" className={thClasses}>
                {t('import.report.errorList.line')}
              </th>
              <th scope="col" className={thClasses}>
                {t('import.report.errorList.field')}
              </th>
              <th scope="col" className={thClasses}>
                {t('import.report.errorList.code')}
              </th>
              <th scope="col" className={thClasses}>
                {t('import.report.errorList.message')}
              </th>
            </tr>
          </thead>
          <tbody>
            {page.data.map((error, index) => (
              <tr key={`${page.offset + index}`} className={trClasses}>
                <td className={cn(tdClasses, 'tabular-nums')}>
                  {error.line ?? t('import.report.errorList.wholeFile')}
                </td>
                <td className={tdClasses}>{error.field || '—'}</td>
                <td className={tdClasses}>
                  <code className="text-xs">{error.code}</code>
                </td>
                <td className={tdClasses}>{error.message}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </ScrollRegion>
      <div className="flex flex-wrap items-center gap-2">
        <p className="text-xs text-fg-muted" aria-live="polite">
          {t('import.report.errorList.range', { from, to, total: page.total })}
        </p>
        <Button
          variant="secondary"
          size="sm"
          icon={<ArrowLeft />}
          disabled={pageIndex === 0 || errors.isFetching}
          onClick={() => setCursors((list) => list.slice(0, -1))}
        >
          {t('import.report.errorList.previous')}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          icon={<ArrowRight />}
          disabled={!next || errors.isFetching}
          loading={errors.isFetching && errors.isPlaceholderData}
          onClick={() => setCursors((list) => [...list, next])}
        >
          {t('import.report.errorList.next')}
        </Button>
      </div>
    </section>
  );
}

/**
 * ImportReport follows a run (useImportRun) and shows its report: progress
 * while it runs, then the counters (total, created, updated, skipped,
 * failed) with a partial failure marked as such, the error code of a
 * failed run, its row errors page by page (ImportErrorList) and the CSV
 * report for download, which lists every row with its outcome or error
 * (Zeile;Feld;Code;Meldung).
 */
function ImportReport({ jobId, dryRun }: { jobId: string; dryRun: boolean }) {
  const { t } = useTranslation();
  const { job, run } = useImportRun(jobId);
  const [downloadError, setDownloadError] = useState<string>();
  const [downloading, setDownloading] = useState(false);

  if (job.isError) {
    return (
      <p role="alert" className="text-sm text-danger">
        {importErrorMessage(job.error, t)}
      </p>
    );
  }
  const current = job.data;
  if (!current) {
    return (
      <p role="status" className="text-sm text-fg-muted">
        {t('import.report.loading')}
      </p>
    );
  }

  const status = current.status;
  const statusLabel = t(`jobs.statuses.${status}`, status);
  if (isActive(current)) {
    return (
      <div className="space-y-2" data-testid="import-progress">
        <p role="status" className="text-sm text-fg">
          {t('import.report.running', { status: statusLabel, progress: current.progress })}
        </p>
        <div
          role="progressbar"
          aria-label={t('import.report.progress')}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={current.progress}
          className="h-2 overflow-hidden rounded-full bg-accent-soft"
        >
          <div
            className="h-full bg-accent transition-[width]"
            style={{ width: `${current.progress}%` }}
          />
        </div>
      </div>
    );
  }

  const counters = importJobResult(current.result);
  const total = counters?.total ?? run.data?.rows_total ?? 0;
  const failed = counters?.failed ?? run.data?.rows_failed ?? 0;
  const errorCode = run.data?.error_code ?? undefined;

  async function download() {
    setDownloadError(undefined);
    setDownloading(true);
    try {
      const link = await importApi.reportUrl(jobId);
      window.open(link.download_url, '_blank', 'noopener');
    } catch (error) {
      setDownloadError(importErrorMessage(error, t));
    } finally {
      setDownloading(false);
    }
  }

  const outcome =
    status !== 'succeeded'
      ? undefined
      : failed === 0
        ? { variant: 'success' as const, text: t('import.report.ok') }
        : failed < total
          ? { variant: 'warning' as const, text: t('import.report.partial', { failed, total }) }
          : { variant: 'danger' as const, text: t('import.report.allFailed') };

  const summary: { key: string; count: number | undefined }[] = [
    { key: 'total', count: total },
    { key: 'created', count: counters?.created },
    { key: 'updated', count: counters?.updated },
    { key: 'skipped', count: counters?.skipped },
    { key: 'failed', count: failed },
  ];

  return (
    <section
      className="space-y-3"
      aria-label={t('import.report.title')}
      data-testid="import-report"
    >
      <div className="flex flex-wrap items-center gap-2">
        <Badge
          variant={
            status === 'succeeded' ? 'success' : status === 'cancelled' ? 'neutral' : 'danger'
          }
        >
          {statusLabel}
        </Badge>
        {outcome ? <Badge variant={outcome.variant}>{outcome.text}</Badge> : null}
        {dryRun ? <Badge variant="neutral">{t('import.report.dryRun')}</Badge> : null}
      </div>
      {status === 'failed' ? (
        <p role="alert" className="text-sm text-danger">
          {errorCode
            ? t(`import.errors.${errorCode}`, run.data?.error ?? current.error ?? errorCode)
            : (run.data?.error ?? current.error ?? t('import.report.failed'))}
        </p>
      ) : null}
      {status === 'cancelled' ? (
        <p className="text-sm text-fg-muted">{t('import.report.cancelled')}</p>
      ) : null}
      {status === 'succeeded' ? (
        <>
          <dl className="grid grid-cols-2 gap-3 sm:grid-cols-5">
            {summary.map((item) => (
              <div key={item.key} className="rounded-lg border border-line px-3 py-2">
                <dt className="text-xs text-fg-muted">{t(`import.report.counters.${item.key}`)}</dt>
                <dd className="text-lg font-semibold tabular-nums text-fg">
                  {item.count ?? t('import.report.unknown')}
                </dd>
              </div>
            ))}
          </dl>
          {dryRun ? <p className="text-sm text-fg-muted">{t('import.report.dryRunNote')}</p> : null}
          {failed > 0 ? (
            <p className="text-sm text-fg-muted">
              {t('import.report.rowErrors', { count: failed })}
            </p>
          ) : null}
        </>
      ) : null}
      {status === 'succeeded' || status === 'failed' ? <ImportErrorList jobId={jobId} /> : null}
      {status !== 'cancelled' ? (
        <div className="space-y-1">
          <Button
            variant="secondary"
            size="sm"
            icon={<Download />}
            loading={downloading}
            onClick={() => void download()}
          >
            {t('import.report.download')}
          </Button>
          {downloadError ? (
            <p role="alert" className="text-sm text-danger">
              {downloadError}
            </p>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

/** PreviewTable shows the header and the sample rows of the file. */
function PreviewTable({ preview }: { preview: ImportPreview }) {
  const { t } = useTranslation();
  return (
    <ScrollRegion label={t('import.preview.title')}>
      <table className={tableClasses}>
        <thead className={theadClasses}>
          <tr>
            {preview.columns.map((column, i) => (
              <th key={`${column}-${i}`} scope="col" className={thClasses}>
                {column}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {preview.sample_rows.map((row, r) => (
            <tr key={r} className={trClasses}>
              {preview.columns.map((column, c) => (
                <td key={`${column}-${c}`} className={tdClasses}>
                  {row[c] ?? ''}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </ScrollRegion>
  );
}

/**
 * ImportPage is the import assistant (UI-11, UI-14, WP-322): upload of a CSV
 * or XLSX file as a document with progress and scan status (FileUpload of
 * WP-321), preview of the columns (POST /imports/preview), choice or
 * editing of a mapping profile with the MappingEditor, a dry run and the
 * run itself as import.run job with their report. Running needs import:run,
 * saving a profile import:manage (E-106); the server decides every request.
 * The query parameter profile (WP-922, e.g. from the IPAM dialog) chooses a
 * stored profile in advance; an unknown one leaves the assistant without a
 * choice.
 */
export function ImportPage() {
  const { t, i18n } = useTranslation();
  const canRun = useHasPermission('import:run');
  const canManage = useHasPermission('import:manage');

  const [step, setStep] = useState<Step>('file');
  const [reached, setReached] = useState(0);
  const [uploadKey, setUploadKey] = useState(0);
  const [documentId, setDocumentId] = useState<string>();
  const [sheet, setSheet] = useState<string>();
  const [profileId, setProfileId] = useState<string>('');
  const [draft, setDraft] = useState<Draft>(newDraft);
  const [dirty, setDirty] = useState(false);
  const [profileMessage, setProfileMessage] = useState<{ ok: boolean; text: string }>();
  const [dryRunJob, setDryRunJob] = useState<string | null>(null);
  const [runJob, setRunJob] = useState<string | null>(null);
  const [startError, setStartError] = useState<string>();

  const uploaded = useDocument(documentId ?? '');
  const scan = uploaded.data?.scan_status;
  const preview = useImportPreview(documentId, sheet, step !== 'file' && scan === 'clean');
  // ?profile=<id> chooses a stored profile once its list is loaded (WP-922).
  const [params] = useSearchParams();
  const presetId = params.get('profile') ?? '';
  const [presetDone, setPresetDone] = useState(false);
  const profiles = useImportProfiles(step !== 'file' || (presetId !== '' && !presetDone));
  const createProfile = useCreateImportProfile();
  const updateProfile = useUpdateImportProfile();
  const deleteProfile = useDeleteImportProfile();
  const startImport = useStartImport();

  const ciTypes = useCITypes(
    { limit: 200 },
    { enabled: step === 'mapping' && draft.entity === 'ci' },
  );
  const ciTypeKey = draft.entity === 'ci' ? (draft.mapping.defaults?.ci_type ?? '') : '';
  const ciType = (ciTypes.data?.data ?? []).find((type) => type.key === ciTypeKey);
  const definitions = useApplicableAttributeDefinitions(
    step === 'mapping' && draft.entity === 'ci' ? ciType?.id : undefined,
  );
  const relationTypes = useRelationshipTypes(step === 'mapping' && draft.entity === 'ci');
  const fields = useMemo(
    () =>
      importFieldSpecs(draft.entity, {
        definitions: definitions.data?.data ?? [],
        relationTypes: (relationTypes.data?.data ?? []).map((type) => type.key),
      }),
    [draft.entity, definitions.data, relationTypes.data],
  );

  const profileList = profiles.data?.data ?? [];
  const savedProfile = profileList.find((p) => p.id === profileId);
  // The preset applies once, while rendering (no effect): a known profile
  // is chosen as if picked, an unknown one is ignored.
  if (presetId && !presetDone && (profiles.isSuccess || profiles.isError)) {
    setPresetDone(true);
    const preset = profileList.find((p) => p.id === presetId);
    if (preset) {
      setProfileId(preset.id);
      setDraft(draftOf(preset));
      if (preset.sheet) setSheet(preset.sheet);
      setDirty(false);
    }
  }

  function open(next: Step) {
    setStep(next);
    setReached((r) => Math.max(r, IMPORT_STEPS.indexOf(next)));
  }

  function restart() {
    setStep('file');
    setReached(0);
    setUploadKey((k) => k + 1);
    setDocumentId(undefined);
    setSheet(undefined);
    setDryRunJob(null);
    setRunJob(null);
    setStartError(undefined);
  }

  function edit(change: Partial<Draft>) {
    setDraft((d) => ({ ...d, ...change }));
    setDirty(true);
    setProfileMessage(undefined);
    setDryRunJob(null);
    setRunJob(null);
  }

  function chooseProfile(id: string) {
    setProfileId(id);
    const profile = profileList.find((p) => p.id === id);
    setDraft(profile ? draftOf(profile) : newDraft());
    if (profile?.sheet) setSheet(profile.sheet);
    setDirty(false);
    setProfileMessage(undefined);
    setDryRunJob(null);
    setRunJob(null);
  }

  const mappedTargets = [
    ...new Set(Object.values(draft.mapping.columns).map((column) => column.target)),
  ];
  const format = preview.data?.format ?? 'csv';
  const chosenSheet =
    format === 'xlsx' ? (sheet ?? preview.data?.sheets[0] ?? undefined) : undefined;

  async function saveProfile() {
    setProfileMessage(undefined);
    const identity = draft.identityKeys.filter((key) => mappedTargets.includes(key));
    const body = {
      name: draft.name.trim(),
      entity: draft.entity,
      format,
      mode: draft.mode,
      sheet: chosenSheet ?? null,
      mapping: draft.mapping,
      identity_keys: identity,
    };
    try {
      const saved = profileId
        ? await updateProfile.mutateAsync({ id: profileId, data: body })
        : await createProfile.mutateAsync(body);
      setProfileId(saved.id);
      setDraft(draftOf(saved));
      setDirty(false);
      setProfileMessage({ ok: true, text: t('import.profile.saved', { name: saved.name }) });
    } catch (error) {
      setProfileMessage({ ok: false, text: importErrorMessage(error, t) });
    }
  }

  async function removeProfile() {
    if (!profileId) return;
    try {
      await deleteProfile.mutateAsync(profileId);
      chooseProfile('');
    } catch (error) {
      setProfileMessage({ ok: false, text: importErrorMessage(error, t) });
    }
  }

  async function start(dryRun: boolean) {
    if (!documentId || !profileId) return;
    setStartError(undefined);
    try {
      const accepted = await startImport.mutateAsync({
        document_id: documentId,
        profile_id: profileId,
        mode: draft.mode,
        dry_run: dryRun,
      });
      if (dryRun) setDryRunJob(accepted.job_id);
      else setRunJob(accepted.job_id);
    } catch (error) {
      setStartError(importErrorMessage(error, t));
    }
  }

  const dryRunDone = useImportRun(dryRunJob);
  const dryRunSucceeded = dryRunDone.job.data?.status === 'succeeded';
  const profileReady = !!profileId && !dirty;
  const canSave =
    canManage === true &&
    draft.name.trim() !== '' &&
    Object.keys(draft.mapping.columns).length > 0 &&
    !createProfile.isPending &&
    !updateProfile.isPending;

  return (
    <div className="space-y-6">
      <PageHeader title={t('import.title')} description={t('import.subtitle')} />
      <Stepper current={step} reached={reached} onOpen={setStep} />

      {step === 'file' ? (
        <Card title={t('import.file.title')} className="space-y-4">
          <p className="text-sm text-fg-muted">{t('import.file.hint')}</p>
          {savedProfile && !dirty ? (
            <p role="status" className="text-sm text-fg" data-testid="import-preset">
              {t('import.file.preset', {
                name: savedProfile.name,
                entity: t(`import.entities.${savedProfile.entity}`),
              })}
            </p>
          ) : null}
          <FileUpload
            key={uploadKey}
            accept={IMPORT_ACCEPT}
            maxBytes={IMPORT_MAX_BYTES}
            label={t('import.file.label')}
            onUploaded={(result) => {
              setDocumentId(result.documentId);
              setSheet(undefined);
              setDryRunJob(null);
              setRunJob(null);
            }}
          />
          {documentId ? (
            <div className="space-y-3" data-testid="import-scan">
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <span className="text-fg-muted">{t('import.file.scanStatus')}</span>
                <ScanStatusBadge status={scan} />
              </div>
              {uploaded.isError ? (
                <p role="alert" className="text-sm text-danger">
                  {importErrorMessage(uploaded.error, t)}
                </p>
              ) : null}
              {!scan || scan === 'pending_scan' ? (
                <p role="status" className="text-sm text-fg-muted">
                  {t('import.file.scanPending')}
                </p>
              ) : null}
              {scan === 'infected' || scan === 'scan_failed' ? (
                <div className="space-y-2">
                  <p role="alert" className="text-sm text-danger">
                    {scan === 'infected' ? t('import.file.infected') : t('import.file.scanFailed')}
                  </p>
                  <Button variant="secondary" icon={<RotateCcw />} onClick={restart}>
                    {t('import.file.another')}
                  </Button>
                </div>
              ) : null}
              {scan === 'clean' ? (
                <Button iconAfter={<ArrowRight />} onClick={() => open('mapping')}>
                  {t('import.next.mapping')}
                </Button>
              ) : null}
            </div>
          ) : null}
        </Card>
      ) : null}

      {step === 'mapping' ? (
        <>
          <Card title={t('import.preview.title')} className="space-y-4">
            {preview.isPending ? (
              <p role="status" className="text-sm text-fg-muted">
                {t('import.preview.loading')}
              </p>
            ) : null}
            {preview.isError ? (
              <p role="alert" className="text-sm text-danger">
                {importErrorMessage(preview.error, t)}
              </p>
            ) : null}
            {preview.data ? (
              <>
                <div className="flex flex-wrap items-end gap-4 text-sm">
                  <p className="text-fg-muted">
                    {t('import.preview.summary', {
                      format: t(`import.formats.${preview.data.format}`),
                      columns: preview.data.columns.length,
                      rows: preview.data.sample_rows.length,
                    })}
                  </p>
                  {preview.data.format === 'xlsx' && preview.data.sheets.length > 0 ? (
                    <div className="w-full sm:w-64">
                      <Select
                        label={t('import.preview.sheet')}
                        value={chosenSheet ?? ''}
                        onChange={(event) => {
                          setSheet(event.target.value);
                          setDryRunJob(null);
                        }}
                        options={preview.data.sheets.map((name) => ({ value: name, label: name }))}
                      />
                    </div>
                  ) : null}
                </div>
                <PreviewTable preview={preview.data} />
              </>
            ) : null}
          </Card>

          <Card title={t('import.profile.title')} className="space-y-4">
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
              <Select
                label={t('import.profile.choose')}
                value={profileId}
                onChange={(event) => chooseProfile(event.target.value)}
                options={[
                  { value: '', label: t('import.profile.new') },
                  ...profileList.map((p) => ({
                    value: p.id,
                    label: t('import.profile.option', {
                      name: p.name,
                      entity: t(`import.entities.${p.entity}`),
                    }),
                  })),
                ]}
              />
              <Input
                label={t('import.profile.name')}
                value={draft.name}
                disabled={canManage !== true}
                onChange={(event) => edit({ name: event.target.value })}
              />
              <Select
                label={t('import.profile.entity')}
                value={draft.entity}
                disabled={canManage !== true}
                onChange={(event) =>
                  edit({
                    entity: event.target.value as ImportEntity,
                    mapping: emptyMapping(),
                    identityKeys: [],
                  })
                }
                options={IMPORT_ENTITIES.map((entity) => ({
                  value: entity,
                  label: t(`import.entities.${entity}`),
                }))}
              />
              <Select
                label={t('import.profile.mode')}
                value={draft.mode}
                disabled={canManage !== true}
                onChange={(event) => edit({ mode: event.target.value as ImportMode })}
                options={IMPORT_MODES.map((mode) => ({
                  value: mode,
                  label: t(`import.modes.${mode}`),
                }))}
              />
              {draft.entity === 'ci' ? (
                <Select
                  label={t('import.profile.ciType')}
                  value={ciTypeKey}
                  disabled={canManage !== true}
                  onChange={(event) => {
                    const defaults = { ...(draft.mapping.defaults ?? {}) };
                    if (event.target.value) defaults.ci_type = event.target.value;
                    else delete defaults.ci_type;
                    edit({ mapping: { ...draft.mapping, defaults } });
                  }}
                  options={[
                    { value: '', label: t('import.profile.ciTypeFromColumn') },
                    ...(ciTypeKey && !ciType ? [{ value: ciTypeKey, label: ciTypeKey }] : []),
                    ...(ciTypes.data?.data ?? []).map((type) => ({
                      value: type.key,
                      label:
                        localizedText(type.display_name, i18n.language) || type.name || type.key,
                    })),
                  ]}
                />
              ) : null}
            </div>
            <p className="text-xs text-fg-muted">
              {t('import.profile.format', { format: t(`import.formats.${format}`) })}
            </p>

            {preview.data ? (
              <MappingEditor
                columns={preview.data.columns}
                sampleRows={preview.data.sample_rows}
                fields={fields}
                value={draft.mapping}
                disabled={canManage !== true}
                onChange={(mapping) => edit({ mapping })}
              />
            ) : null}

            <fieldset className="space-y-2">
              <legend className="text-sm font-medium text-fg">
                {t('import.profile.identityKeys')}
              </legend>
              <p className="text-xs text-fg-muted">{t('import.profile.identityHint')}</p>
              {mappedTargets.length === 0 ? (
                <p className="text-sm text-fg-muted">{t('import.profile.identityEmpty')}</p>
              ) : (
                <div className="flex flex-wrap gap-x-4 gap-y-2">
                  {mappedTargets.map((target) => {
                    const spec = fields.find((f) => f.field === target) ?? {
                      field: target,
                      source: 'column' as const,
                      kind: 'text' as const,
                      editor: 'text' as const,
                      column: target,
                    };
                    return (
                      <label key={target} className="flex items-center gap-2 text-sm text-fg">
                        <input
                          type="checkbox"
                          disabled={canManage !== true}
                          checked={draft.identityKeys.includes(target)}
                          onChange={(event) =>
                            edit({
                              identityKeys: event.target.checked
                                ? [...draft.identityKeys, target]
                                : draft.identityKeys.filter((key) => key !== target),
                            })
                          }
                        />
                        {fieldSpecLabel(t, i18n.language, spec)}
                      </label>
                    );
                  })}
                </div>
              )}
            </fieldset>

            <div className="flex flex-wrap items-center gap-2">
              <Button
                icon={<Save />}
                disabled={!canSave}
                loading={createProfile.isPending || updateProfile.isPending}
                onClick={() => void saveProfile()}
              >
                {profileId ? t('import.profile.update') : t('import.profile.create')}
              </Button>
              {profileId ? (
                <Button
                  variant="ghost"
                  icon={<Trash2 />}
                  disabled={canManage !== true || deleteProfile.isPending}
                  onClick={() => void removeProfile()}
                >
                  {t('import.profile.delete')}
                </Button>
              ) : null}
            </div>
            <PermissionHint allowed={canManage} text={t('import.permissions.manage')} />
            {profileMessage ? (
              <p
                role={profileMessage.ok ? 'status' : 'alert'}
                className={cn('text-sm', profileMessage.ok ? 'text-fg-muted' : 'text-danger')}
              >
                {profileMessage.text}
              </p>
            ) : null}
            {dirty && profileId ? (
              <p className="text-xs text-fg-muted">{t('import.profile.unsaved')}</p>
            ) : null}
          </Card>

          <div className="flex flex-wrap gap-2">
            <Button variant="secondary" icon={<ArrowLeft />} onClick={() => setStep('file')}>
              {t('import.back')}
            </Button>
            <Button
              iconAfter={<ArrowRight />}
              disabled={!profileReady}
              onClick={() => open('check')}
            >
              {t('import.next.check')}
            </Button>
          </div>
        </>
      ) : null}

      {step === 'check' ? (
        <Card title={t('import.check.title')} className="space-y-4">
          <p className="text-sm text-fg-muted">
            {t('import.check.hint', { name: savedProfile?.name ?? draft.name })}
          </p>
          <div className="space-y-1">
            <Button
              icon={<Play />}
              disabled={canRun !== true || startImport.isPending || !profileReady}
              loading={startImport.isPending && !dryRunJob}
              onClick={() => void start(true)}
            >
              {t('import.check.start')}
            </Button>
            <PermissionHint allowed={canRun} text={t('import.permissions.run')} />
          </div>
          {startError ? (
            <p role="alert" className="text-sm text-danger">
              {startError}
            </p>
          ) : null}
          {dryRunJob ? <ImportReport jobId={dryRunJob} dryRun /> : null}
          <div className="flex flex-wrap gap-2">
            <Button variant="secondary" icon={<ArrowLeft />} onClick={() => setStep('mapping')}>
              {t('import.back')}
            </Button>
            <Button
              iconAfter={<ArrowRight />}
              disabled={!dryRunSucceeded}
              onClick={() => {
                setStartError(undefined);
                open('run');
              }}
            >
              {t('import.next.run')}
            </Button>
          </div>
        </Card>
      ) : null}

      {step === 'run' ? (
        <Card title={t('import.run.title')} className="space-y-4">
          <p className="text-sm text-fg-muted">
            {t('import.run.hint', { mode: t(`import.modes.${draft.mode}`) })}
          </p>
          <div className="space-y-1">
            <Button
              icon={<Play />}
              disabled={canRun !== true || startImport.isPending || !!runJob}
              loading={startImport.isPending}
              onClick={() => void start(false)}
            >
              {t('import.run.start')}
            </Button>
            <PermissionHint allowed={canRun} text={t('import.permissions.run')} />
          </div>
          {startError ? (
            <p role="alert" className="text-sm text-danger">
              {startError}
            </p>
          ) : null}
          {runJob ? <ImportReport jobId={runJob} dryRun={false} /> : null}
          <div className="flex flex-wrap gap-2">
            <Button variant="secondary" icon={<ArrowLeft />} onClick={() => setStep('check')}>
              {t('import.back')}
            </Button>
            {runJob ? (
              <Button variant="secondary" icon={<RotateCcw />} onClick={restart}>
                {t('import.run.again')}
              </Button>
            ) : null}
          </div>
        </Card>
      ) : null}

      <Card title={t('import.api.title')}>
        <p className="text-sm text-fg-muted">
          {t('import.api.hint', { max: API_IMPORT_MAX_ROWS.toLocaleString(i18n.language) })}
        </p>
      </Card>
    </div>
  );
}
