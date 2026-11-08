import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Download, FileText, Link2, Plus } from 'lucide-react';
import {
  ApiError,
  PROBLEM_DOCUMENT_PENDING_SCAN,
  PROBLEM_DOCUMENT_QUARANTINED,
  documentApi,
  type Document,
  type ScanStatus,
} from '../../api/client';
import { useDocumentList, useLinkDocument } from '../../api/hooks';
import { PickerCombobox, PICKER_LIMIT, useDebouncedValue } from '../pickers/ObjectPicker';
import { FileUpload } from '../upload/FileUpload';
import { Badge } from '../ui/Badge';
import { Button } from '../ui/Button';
import { Card } from '../ui/Card';
import { Modal } from '../ui/Modal';

const SCAN_VARIANTS: Record<ScanStatus, 'success' | 'warning' | 'danger' | 'info'> = {
  pending_scan: 'info',
  clean: 'success',
  infected: 'danger',
  scan_failed: 'warning',
};

/** ScanStatusBadge shows the malware scan status of a version (SEC-09, WP-733). */
export function ScanStatusBadge({ status }: { status?: ScanStatus }) {
  const { t } = useTranslation();
  if (!status) return <Badge variant="neutral">{t('document.scan.none')}</Badge>;
  return <Badge variant={SCAN_VARIANTS[status]}>{t(`document.scanStatuses.${status}`)}</Badge>;
}

/**
 * scanNotice explains why a version cannot be downloaded: the quarantine of
 * infected, the lock of scan_failed, the running scan of pending_scan; a
 * clean version has none.
 */
export function scanNotice(status: ScanStatus | undefined, t: TFunction): string | undefined {
  if (status === 'infected') return t('document.scan.quarantined');
  if (status === 'scan_failed') return t('document.scan.failed');
  if (status === 'pending_scan') return t('document.scan.pending');
  return undefined;
}

/**
 * documentErrorMessage words a failed download or link: the 409 of a
 * pending or failed scan and of the quarantine read as the scan notice
 * (fail-closed, E-42); any other failure as its detail.
 */
export function documentErrorMessage(error: unknown, t: TFunction): string {
  if (error instanceof ApiError) {
    if (error.status === 409 && error.type === PROBLEM_DOCUMENT_QUARANTINED) {
      return t('document.scan.quarantined');
    }
    if (error.status === 409 && error.type === PROBLEM_DOCUMENT_PENDING_SCAN) {
      return t('document.scan.pending');
    }
    return error.detail ?? error.title ?? error.message;
  }
  return error instanceof Error ? error.message : String(error);
}

/**
 * useDocumentDownload fetches the presigned URL of a version
 * (GET /documents/{id}/content?version=n) and opens it; a refused download
 * is kept as message.
 */
export function useDocumentDownload() {
  const { t } = useTranslation();
  const [error, setError] = useState<string | undefined>(undefined);
  async function download(id: string, version?: number) {
    setError(undefined);
    try {
      const { download_url: url } = await documentApi.contentUrl(id, version);
      window.open(url, '_blank', 'noopener,noreferrer');
    } catch (failure) {
      setError(documentErrorMessage(failure, t));
    }
  }
  return { download, error };
}

export interface LinkedDocumentsProps {
  /** entityType is the link target type, e.g. ticket, contract or asset. */
  entityType: string;
  entityId: string;
  /**
   * load reads the documents of the object; the object pages read them on
   * different paths (ticket attachments, the filter of GET /documents, the
   * aggregate of an asset).
   */
  load: () => Promise<Document[]>;
  /** title of the card; default "Dokumente". */
  title?: string;
  /** canWrite offers upload and linking; the server decides each request. */
  canWrite?: boolean;
}

/**
 * LinkedDocuments lists the documents of an object with scan status and
 * download (UI-14, WP-321); a download is offered only for clean content.
 * A new document is uploaded with FileUpload and carries the object as
 * `links` in POST /documents (E-61, WP-811), so a client- or site-scoped
 * principal can create it as well. An existing document is linked with
 * POST /documents/{id}/links, also while its scan is pending; the server
 * refuses only a quarantined one (409 document-quarantined, WP-733).
 */
export function LinkedDocuments({
  entityType,
  entityId,
  load,
  title,
  canWrite = true,
}: LinkedDocumentsProps) {
  const { t } = useTranslation();
  const documents = useQuery({
    queryKey: ['documents', 'linked', entityType, entityId],
    queryFn: load,
    enabled: !!entityId,
  });
  const { download, error: downloadError } = useDocumentDownload();
  const [uploading, setUploading] = useState(false);
  const [linking, setLinking] = useState(false);
  const rows = documents.data ?? [];

  return (
    <Card
      title={title ?? t('document.title')}
      actions={
        canWrite ? (
          <div className="flex gap-2">
            <Button size="sm" variant="secondary" icon={<Link2 />} onClick={() => setLinking(true)}>
              {t('document.linkExisting')}
            </Button>
            <Button size="sm" icon={<Plus />} onClick={() => setUploading(true)}>
              {t('document.add')}
            </Button>
          </div>
        ) : undefined
      }
    >
      {documents.isLoading ? <p className="text-sm text-fg-muted">{t('app.loading')}</p> : null}
      {documents.error ? (
        <p role="alert" className="text-sm text-danger">
          {documentErrorMessage(documents.error, t)}
        </p>
      ) : null}
      {downloadError ? (
        <p role="alert" className="text-sm text-danger">
          {downloadError}
        </p>
      ) : null}
      {documents.data && rows.length === 0 ? (
        <p className="flex items-center gap-2 text-sm text-fg-muted">
          <FileText aria-hidden="true" className="h-4 w-4" />
          {t('document.noLinkedDocuments')}
        </p>
      ) : null}
      {rows.length > 0 ? (
        <ul className="divide-y divide-line">
          {rows.map((doc) => (
            <li key={doc.id} className="flex flex-wrap items-center gap-3 py-2">
              <Link
                to={`/documents/${doc.id}`}
                className="min-w-0 flex-1 truncate font-medium text-fg hover:underline"
              >
                {doc.title}
              </Link>
              <span className="text-xs tabular-nums text-fg-muted">
                {t('document.versionLabel', { version: doc.version })}
              </span>
              <ScanStatusBadge status={doc.scan_status} />
              {doc.scan_status === 'clean' ? (
                <Button
                  size="sm"
                  variant="ghost"
                  icon={<Download />}
                  aria-label={t('document.downloadNamed', { name: doc.title })}
                  onClick={() => void download(doc.id)}
                >
                  {t('document.download')}
                </Button>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}

      <Modal open={uploading} onOpenChange={setUploading} title={t('document.add')}>
        <FileUpload
          entityType={entityType}
          entityId={entityId}
          onUploaded={() => {
            void documents.refetch();
          }}
        />
      </Modal>
      <Modal open={linking} onOpenChange={setLinking} title={t('document.linkExisting')}>
        <LinkExistingDocument
          entityType={entityType}
          entityId={entityId}
          linkedIds={rows.map((doc) => doc.id)}
          onLinked={() => {
            setLinking(false);
            void documents.refetch();
          }}
        />
      </Modal>
    </Card>
  );
}

interface LinkExistingDocumentProps {
  entityType: string;
  entityId: string;
  linkedIds: string[];
  onLinked: () => void;
}

/** LinkExistingDocument searches the documents by title and links the chosen one. */
function LinkExistingDocument({
  entityType,
  entityId,
  linkedIds,
  onLinked,
}: LinkExistingDocumentProps) {
  const { t } = useTranslation();
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim());
  const pending = query.trim() !== debounced;
  const search = useDocumentList(
    { search: debounced, limit: PICKER_LIMIT },
    { enabled: debounced !== '' && !pending },
  );
  const linkMutation = useLinkDocument();
  const hits = (search.data?.data ?? []).filter((doc) => !linkedIds.includes(doc.id));

  return (
    <div className="space-y-3">
      <PickerCombobox
        label={t('document.searchDocument')}
        query={query}
        onQueryChange={setQuery}
        options={hits.map((doc) => ({
          key: doc.id,
          // A field the field permissions leave out (RBA-07) is absent.
          kindLabel: doc.category ? t(`document.categories.${doc.category}`, doc.category) : '—',
          name: doc.title,
          code: doc.file_name,
        }))}
        onSelect={(index) => {
          const doc = hits[index];
          if (!doc) return;
          linkMutation.mutate(
            { id: doc.id, target: { entity_type: entityType, entity_id: entityId } },
            { onSuccess: onLinked },
          );
        }}
        searching={pending || search.isFetching}
        searched={!pending && search.isFetched}
      />
      {linkMutation.error ? (
        <p role="alert" className="text-sm text-danger">
          {documentErrorMessage(linkMutation.error, t)}
        </p>
      ) : null}
    </div>
  );
}
