import { useId, useRef, useState } from 'react';
import type { DragEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useQueryClient } from '@tanstack/react-query';
import { RotateCcw, Trash2, Upload } from 'lucide-react';
import {
  ApiError,
  PROBLEM_ENTITLEMENT_LIMIT,
  PROBLEM_MIME_MISMATCH,
  documentApi,
  type Document,
  type DocumentVersion,
} from '../../api/client';
import { Button } from '../ui/Button';
import { cn } from '../ui/utils';

/**
 * DOCUMENT_MIME_TYPES are the content types PUT /documents/{id}/content and
 * POST /documents/{id}/versions accept (WP-268, api/openapi.yaml). The
 * check in the browser only spares a doomed transfer; the server checks
 * type, signature and size again.
 */
export const DOCUMENT_MIME_TYPES = [
  'application/pdf',
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
  'application/zip',
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  'application/msword',
  'application/vnd.ms-excel',
  'application/vnd.ms-powerpoint',
  'application/xml',
  'application/json',
  'text/csv',
  'text/plain',
] as const;

/** The default of RETICORA_DOCUMENT_MAX_UPLOAD_MB (WP-268): 50 MiB. */
export const DEFAULT_MAX_UPLOAD_BYTES = 50 * 1024 * 1024;

/** UploadResult is what a finished upload hands to onUploaded. */
export interface UploadResult {
  /** The document; after a new version as it was before the version. */
  document?: Document;
  /** The stored version after POST /documents/{id}/versions. */
  version?: DocumentVersion;
  documentId: string;
}

export interface FileUploadProps {
  /** accept lists the allowed MIME types or file extensions (".pdf"). */
  accept?: readonly string[];
  /** maxBytes is the largest file offered for transfer. */
  maxBytes?: number;
  /** documentId makes the upload a new version of this document (WP-731). */
  documentId?: string;
  /** category of a new document; default general. */
  category?: string;
  /** entityType/entityId link a new document in POST /documents (E-61, WP-811). */
  entityType?: string;
  entityId?: string;
  onUploaded?: (result: UploadResult) => void;
  /** onError receives every failure of the request, e.g. to flag the storage limit. */
  onError?: (error: unknown) => void;
  /** label names the drop zone; default "Datei hochladen". */
  label?: string;
}

/**
 * uploadErrorMessage words a failed creation or transfer (WP-268, WP-731,
 * WP-733): too large, type not allowed, content not matching its type, the
 * storage limit of the organization, a busy server; any other failure (a
 * 403 of a scoped principal, E-61) with its detail and the trace id.
 */
export function uploadErrorMessage(error: unknown, t: TFunction): string {
  if (error instanceof ApiError) {
    if (error.status === 413) return t('upload.errors.tooLarge');
    if (error.status === 415) return t('upload.errors.unsupportedType');
    if (error.status === 422 && error.type === PROBLEM_MIME_MISMATCH) {
      return t('upload.errors.mimeMismatch');
    }
    if (error.status === 403 && error.type === PROBLEM_ENTITLEMENT_LIMIT) {
      return t('upload.errors.storageLimit');
    }
    if (error.status === 503) return t('upload.errors.unavailable');
    if (error.status === 0) return t('upload.errors.network');
    const detail = error.detail ?? error.title ?? error.message;
    if (error.status === 403 && !error.type) return detail;
    return error.traceId
      ? t('upload.errors.withTrace', { detail, traceId: error.traceId })
      : detail;
  }
  return error instanceof Error ? error.message : String(error);
}

/** isStorageLimit tells a 403 of the storage limit (entitlement-limit) apart. */
export function isStorageLimit(error: unknown): boolean {
  return (
    error instanceof ApiError && error.status === 403 && error.type === PROBLEM_ENTITLEMENT_LIMIT
  );
}

/** formatBytes shows a size in the UI language (B, KB, MB, GB in 1024 steps). */
export function formatBytes(bytes: number, language: string): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = unit === 0 ? 0 : 1;
  return `${value.toLocaleString(language, { maximumFractionDigits: digits, minimumFractionDigits: digits })} ${units[unit]}`;
}

function accepted(file: File, accept: readonly string[]): boolean {
  const name = file.name.toLowerCase();
  return accept.some((entry) => {
    const value = entry.trim().toLowerCase();
    if (value.startsWith('.')) return name.endsWith(value);
    if (value.endsWith('/*')) return file.type.startsWith(value.slice(0, -1));
    return file.type === value;
  });
}

/** titleOf is the file name without its extension, the title of a new document. */
function titleOf(fileName: string): string {
  const dot = fileName.lastIndexOf('.');
  return dot > 0 ? fileName.slice(0, dot) : fileName;
}

type Phase =
  | { kind: 'idle' }
  | { kind: 'uploading'; percent: number; fileName: string }
  | { kind: 'failed'; message: string; file?: File; orphanId?: string }
  | { kind: 'done'; fileName: string };

/**
 * FileUpload uploads one file as a new document or as a new version of a
 * document (UI-14, WP-321), by drag and drop or with the file dialog, which
 * the button opens from the keyboard. A new document is created with
 * POST /documents (title and file name from the file, its type, the
 * category and, with entityType/entityId, the link) and its content sent to
 * PUT /documents/{id}/content; a new version goes to
 * POST /documents/{id}/versions. The transfer reports its progress as a
 * progressbar; when the content fails after the document was created, the
 * upload can be repeated or the document discarded.
 */
export function FileUpload({
  accept = DOCUMENT_MIME_TYPES,
  maxBytes = DEFAULT_MAX_UPLOAD_BYTES,
  documentId,
  category = 'general',
  entityType,
  entityId,
  onUploaded,
  onError,
  label,
}: FileUploadProps) {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const inputId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [phase, setPhase] = useState<Phase>({ kind: 'idle' });
  const [dragging, setDragging] = useState(false);
  const busy = phase.kind === 'uploading';

  const progress = (fileName: string) => (loaded: number, total: number) =>
    setPhase({
      kind: 'uploading',
      fileName,
      percent: total > 0 ? Math.min(100, Math.round((loaded / total) * 100)) : 0,
    });

  function fail(error: unknown, file?: File, orphanId?: string) {
    onError?.(error);
    setPhase({ kind: 'failed', message: uploadErrorMessage(error, t), file, orphanId });
  }

  function finished(file: File, result: UploadResult) {
    void queryClient.invalidateQueries({ queryKey: ['documents'] });
    setPhase({ kind: 'done', fileName: file.name });
    onUploaded?.(result);
  }

  /** sendContent transfers the content of a created document. */
  async function sendContent(id: string, file: File) {
    setPhase({ kind: 'uploading', fileName: file.name, percent: 0 });
    try {
      const document = await documentApi.uploadContent(id, file, {
        onProgress: progress(file.name),
      });
      finished(file, { document, documentId: id });
    } catch (error) {
      fail(error, file, id);
    }
  }

  async function start(file: File) {
    if (!accepted(file, accept)) {
      setPhase({ kind: 'failed', message: t('upload.errors.unsupportedType') });
      return;
    }
    if (file.size > maxBytes) {
      setPhase({
        kind: 'failed',
        message: t('upload.errors.tooLargeLocal', { max: formatBytes(maxBytes, i18n.language) }),
      });
      return;
    }
    if (documentId) {
      setPhase({ kind: 'uploading', fileName: file.name, percent: 0 });
      try {
        const version = await documentApi.createVersion(documentId, file, {
          onProgress: progress(file.name),
        });
        finished(file, { version, documentId });
      } catch (error) {
        fail(error, file);
      }
      return;
    }
    setPhase({ kind: 'uploading', fileName: file.name, percent: 0 });
    let created: Document;
    try {
      created = await documentApi.create({
        title: titleOf(file.name),
        file_name: file.name,
        mime_type: file.type,
        category,
        ...(entityType && entityId
          ? { links: [{ entity_type: entityType, entity_id: entityId }] }
          : {}),
      });
    } catch (error) {
      fail(error);
      return;
    }
    await sendContent(created.id, file);
  }

  function take(files: FileList | null) {
    const file = files?.[0];
    if (file && !busy) void start(file);
    if (inputRef.current) inputRef.current.value = '';
  }

  async function discard(id: string) {
    try {
      await documentApi.delete(id);
      void queryClient.invalidateQueries({ queryKey: ['documents'] });
      setPhase({ kind: 'idle' });
    } catch (error) {
      fail(error, undefined, id);
    }
  }

  function onDrop(event: DragEvent<HTMLDivElement>) {
    event.preventDefault();
    setDragging(false);
    take(event.dataTransfer.files);
  }

  const zoneLabel = label ?? t('upload.label');

  return (
    <div className="space-y-3">
      <div
        onDragOver={(event) => {
          event.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={onDrop}
        data-testid="file-drop-zone"
        className={cn(
          'flex flex-col items-center gap-2 rounded-lg border border-dashed border-line px-4 py-6 text-center',
          dragging && 'border-accent bg-accent/5',
        )}
      >
        <Upload aria-hidden="true" className="h-6 w-6 text-fg-muted" />
        <label htmlFor={inputId} className="text-sm font-medium text-fg">
          {zoneLabel}
        </label>
        <p className="text-xs text-fg-muted">
          {t('upload.hint', { max: formatBytes(maxBytes, i18n.language) })}
        </p>
        <input
          ref={inputRef}
          id={inputId}
          type="file"
          className="sr-only"
          accept={accept.join(',')}
          disabled={busy}
          onChange={(event) => take(event.target.files)}
        />
        <Button
          variant="secondary"
          size="sm"
          disabled={busy}
          onClick={() => inputRef.current?.click()}
        >
          {t('upload.choose')}
        </Button>
      </div>

      {phase.kind === 'uploading' ? (
        <div className="space-y-1">
          <div className="flex justify-between text-xs text-fg-muted">
            <span className="truncate">{phase.fileName}</span>
            <span className="tabular-nums">{t('upload.percent', { percent: phase.percent })}</span>
          </div>
          <div
            role="progressbar"
            aria-label={t('upload.progress', { name: phase.fileName })}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={phase.percent}
            className="h-2 overflow-hidden rounded-full bg-accent-soft"
          >
            <div
              className="h-full bg-accent transition-[width]"
              style={{ width: `${phase.percent}%` }}
            />
          </div>
        </div>
      ) : null}

      {phase.kind === 'done' ? (
        <p role="status" className="text-sm text-fg-muted">
          {t('upload.done', { name: phase.fileName })}
        </p>
      ) : null}

      {phase.kind === 'failed' ? (
        <div className="space-y-2">
          <p role="alert" className="text-sm text-danger">
            {phase.message}
          </p>
          {phase.orphanId ? (
            <div className="flex flex-wrap gap-2">
              {phase.file ? (
                <Button
                  size="sm"
                  variant="secondary"
                  icon={<RotateCcw />}
                  onClick={() => {
                    if (phase.orphanId && phase.file) void sendContent(phase.orphanId, phase.file);
                  }}
                >
                  {t('upload.retry')}
                </Button>
              ) : null}
              <Button
                size="sm"
                variant="ghost"
                icon={<Trash2 />}
                onClick={() => {
                  if (phase.orphanId) void discard(phase.orphanId);
                }}
              >
                {t('upload.discard')}
              </Button>
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
