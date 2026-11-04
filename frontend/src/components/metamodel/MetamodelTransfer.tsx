/**
 * MetamodelTransfer exports and imports the metamodel (MET-54, MET-53,
 * UI-11, WP-800) in the head of CITypeAdminPage.
 *
 * Export: GET /api/v1/ci-types/export?format_version=2 (WP-262) over
 * fetchAPI with the bearer header of the session, saved as a JSON file from
 * a Blob (URL.createObjectURL); a link would carry no token.
 *
 * Import (only with citype:manage): reads a JSON file and sends it to
 * POST /api/v1/metamodel/import (WP-722). 201 makes the new draft the
 * active one and opens /ci-types/drafts/{draft_id}, where
 * ImportReportNotice shows the report (items, conflicts, notes) handed over
 * in the navigation state; 200 with report.unchanged is a note without a
 * draft; 413 and 422 show the problem detail with the conflict report and
 * the trace id.
 */
import { useRef, useState, type ChangeEvent } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  metamodelDraftApi,
  type MetamodelImportReport,
  type MetamodelImportResult,
} from '../../api/cmdb';
import { useExportMetamodel, useImportMetamodel } from '../../api/cmdbHooks';
import { ApiError } from '../../api/client';
import { useHasPermission } from '../../api/hooks';
import { useMetamodelDraftStore } from '../../stores/metamodelDraft';
import { Button } from '../ui/Button';
import { ProblemNotice } from './MigrateDialog';

/** The actions of an import item; labels templates.transfer.actions.*. */
export const IMPORT_ACTIONS = ['create', 'update', 'unchanged', 'resolved'] as const;
/** The conflict codes of an import; labels templates.transfer.conflictCodes.*. */
export const IMPORT_CONFLICT_CODES = [
  'datatype-conflict',
  'missing-reference',
  'system-key-collision',
] as const;
/** The note codes of an import; labels templates.transfer.noteCodes.*. */
export const IMPORT_NOTE_CODES = [
  'history-skipped',
  'not-imported',
  'field-permissions-skipped',
] as const;

/** The navigation state the import hands to the draft page. */
export interface ImportNavigationState {
  importReport?: MetamodelImportReport;
}

/** The name of the export file: metamodel-<date>.json. */
export function exportFileName(now = new Date()): string {
  return `metamodel-${now.toISOString().slice(0, 10)}.json`;
}

function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = filename;
  anchor.rel = 'noopener';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 30_000);
}

/** readText reads a file as text (FileReader: also where File.text is missing). */
function readText(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result ?? ''));
    reader.onerror = () => reject(reader.error ?? new Error('read failed'));
    reader.readAsText(file);
  });
}

/** The report a refused import (422) carries next to its problem detail. */
function reportOf(error: unknown): MetamodelImportReport | undefined {
  if (!(error instanceof ApiError)) return undefined;
  const report = error.problem.report;
  return report && typeof report === 'object' ? (report as MetamodelImportReport) : undefined;
}

export function MetamodelTransfer() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const canManage = useHasPermission('citype:manage') === true;
  const setDraft = useMetamodelDraftStore((s) => s.setDraft);
  const exportMetamodel = useExportMetamodel();
  const importMetamodel = useImportMetamodel();
  const fileInput = useRef<HTMLInputElement>(null);
  const [fileError, setFileError] = useState<string | null>(null);
  const [unchanged, setUnchanged] = useState(false);

  const runExport = () => {
    exportMetamodel.mutate(undefined, {
      onSuccess: (blob) => saveBlob(blob, exportFileName()),
    });
  };

  const onImported = async (result: MetamodelImportResult, fileName: string) => {
    if (!result.draft_id) {
      setUnchanged(true);
      return;
    }
    const id = result.draft_id;
    // The draft name for the banner; the draft is active either way.
    let name = fileName;
    try {
      name = (await metamodelDraftApi.get(id)).name;
    } catch {
      // The banner reads the draft itself.
    }
    setDraft({ id, name });
    const state: ImportNavigationState = { importReport: result.report };
    navigate(`/ci-types/drafts/${id}`, { state });
  };

  const onFile = async (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (!file) return;
    setFileError(null);
    setUnchanged(false);
    importMetamodel.reset();
    let text: string;
    try {
      text = await readText(file);
      JSON.parse(text);
    } catch {
      setFileError(t('templates.transfer.notJson', { name: file.name }));
      return;
    }
    importMetamodel.mutate(text, { onSuccess: (result) => void onImported(result, file.name) });
  };

  const refusedReport = reportOf(importMetamodel.error);

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          variant="secondary"
          disabled={exportMetamodel.isPending}
          onClick={runExport}
        >
          {t('templates.transfer.export')}
        </Button>
        {canManage ? (
          <>
            <Button
              size="sm"
              variant="secondary"
              disabled={importMetamodel.isPending}
              onClick={() => fileInput.current?.click()}
            >
              {t('templates.transfer.import')}
            </Button>
            <input
              ref={fileInput}
              type="file"
              accept="application/json,.json"
              className="sr-only"
              tabIndex={-1}
              aria-label={t('templates.transfer.importFile')}
              onChange={(e) => void onFile(e)}
            />
          </>
        ) : null}
      </div>
      {exportMetamodel.error ? (
        <ProblemNotice
          error={exportMetamodel.error}
          prefix={t('templates.transfer.exportFailed')}
        />
      ) : null}
      {fileError ? (
        <p role="alert" className="text-sm text-red-600 dark:text-red-400">
          {fileError}
        </p>
      ) : null}
      {unchanged ? (
        <p role="status" className="text-sm text-gray-700 dark:text-gray-300">
          {t('templates.transfer.unchanged')}
        </p>
      ) : null}
      {importMetamodel.error ? (
        <ProblemNotice error={importMetamodel.error} prefix={t('templates.transfer.importFailed')}>
          {refusedReport ? <ImportReportView report={refusedReport} /> : null}
        </ProblemNotice>
      ) : null}
    </div>
  );
}

/**
 * ImportReportNotice shows the report of the import that created the draft
 * of the page, handed over in the navigation state; nothing otherwise.
 */
export function ImportReportNotice() {
  const { t } = useTranslation();
  const location = useLocation();
  const report = (location.state as ImportNavigationState | null)?.importReport;
  if (!report) return null;
  return (
    <section
      aria-label={t('templates.transfer.reportTitle')}
      className="rounded-lg border border-gray-200 p-3 text-sm dark:border-gray-700"
    >
      <h3 className="font-semibold">{t('templates.transfer.reportTitle')}</h3>
      <ImportReportView report={report} />
    </section>
  );
}

/** ImportReportView lists the items, conflicts and notes of an import report. */
export function ImportReportView({ report }: { report: MetamodelImportReport }) {
  const { t } = useTranslation();
  const changed = report.items.filter((item) => item.action !== 'unchanged');
  const owner = (value: string | undefined) => (value ? ` (${value})` : '');
  return (
    <div className="mt-2 space-y-2">
      {report.conflicts.length > 0 ? (
        <div>
          <h4 className="font-medium">{t('templates.transfer.conflicts')}</h4>
          <ul aria-label={t('templates.transfer.conflicts')} className="list-disc pl-5">
            {report.conflicts.map((c, i) => (
              <li key={`${c.code}-${c.kind}-${c.key}-${i}`}>
                {t(`templates.transfer.conflictCodes.${c.code}`)}: {c.kind} {c.key}
                {owner(c.owner)}
                {c.reference_key ? ` → ${c.reference ?? ''} ${c.reference_key}` : ''} — {c.detail}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {changed.length > 0 ? (
        <div>
          <h4 className="font-medium">{t('templates.transfer.items')}</h4>
          <ul aria-label={t('templates.transfer.items')} className="list-disc pl-5">
            {changed.map((item, i) => (
              <li key={`${item.kind}-${item.key}-${i}`}>
                {t(`templates.transfer.actions.${item.action}`)}: {item.kind} {item.key}
                {owner(item.owner)}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {report.notes.length > 0 ? (
        <div>
          <h4 className="font-medium">{t('templates.transfer.notes')}</h4>
          <ul aria-label={t('templates.transfer.notes')} className="list-disc pl-5">
            {report.notes.map((n, i) => (
              <li key={`${n.code}-${n.kind}-${n.key}-${i}`}>
                {t(`templates.transfer.noteCodes.${n.code}`)}: {n.kind} {n.key} — {n.detail}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}
