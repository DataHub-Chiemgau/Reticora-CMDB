import { AlertTriangle, FileText, Search, Upload } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { useDocumentList, useDeleteDocument, useDocumentUsage } from '../api/hooks';
import type { DocumentUsage } from '../api/client';
import { ScanStatusBadge } from '../components/documents/LinkedDocuments';
import { FileUpload, formatBytes, isStorageLimit } from '../components/upload/FileUpload';
import { Card } from '../components/ui/Card';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';
import { Badge } from '../components/ui/Badge';
import { ErrorState } from '../components/ui/ErrorState';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Toolbar, ToolbarEnd } from '../components/ui/Toolbar';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import { ScrollRegion } from '../components/ScrollRegion';
import { TableEmptyRow } from './ListParts';

// Stored document categories (English keys); the labels come from the catalog.
export const DOCUMENT_CATEGORIES = [
  'general',
  'contract',
  'invoice',
  'manual',
  'diagram',
  'certificate',
  'policy',
  'other',
] as const;

/** STORAGE_WARNING_RATIO is the share of the storage limit from which the page warns. */
export const STORAGE_WARNING_RATIO = 0.9;

const MIB = 1024 * 1024;

/**
 * StorageUsage shows the stored bytes of the organization against its limit
 * max_storage_mb (WP-731) as a bar, without a limit only the usage; from 90 %
 * and after a refused upload (403 entitlement-limit) with a warning.
 */
function StorageUsage({ usage, limitReached }: { usage?: DocumentUsage; limitReached: boolean }) {
  const { t, i18n } = useTranslation();
  if (!usage) return null;
  const used = formatBytes(usage.storage_bytes, i18n.language);
  const limitBytes = usage.limit_mb === null ? undefined : usage.limit_mb * MIB;
  const ratio = limitBytes ? usage.storage_bytes / limitBytes : 0;
  const percent = Math.min(100, Math.round(ratio * 100));
  const warn = limitReached || (limitBytes !== undefined && ratio >= STORAGE_WARNING_RATIO);
  return (
    <Card className="space-y-2">
      <div className="flex flex-wrap items-baseline justify-between gap-2 text-sm">
        <span className="font-medium text-fg">{t('document.usage.title')}</span>
        <span className="tabular-nums text-fg-muted">
          {limitBytes !== undefined
            ? t('document.usage.ofLimit', {
                used,
                limit: formatBytes(limitBytes, i18n.language),
                percent,
              })
            : t('document.usage.unlimited', { used })}
        </span>
      </div>
      {limitBytes !== undefined ? (
        <div
          role="meter"
          aria-label={t('document.usage.title')}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={percent}
          aria-valuetext={t('document.usage.ofLimit', {
            used,
            limit: formatBytes(limitBytes, i18n.language),
            percent,
          })}
          className="h-2 overflow-hidden rounded-full bg-accent-soft"
        >
          <div
            className={cn('h-full', warn ? 'bg-amber-500' : 'bg-accent')}
            style={{ width: `${percent}%` }}
          />
        </div>
      ) : null}
      {warn ? (
        <p role="status" className="flex items-center gap-2 text-sm text-fg">
          <AlertTriangle aria-hidden="true" className="h-4 w-4 text-amber-600" />
          {limitReached ? t('document.usage.limitReached') : t('document.usage.nearLimit')}
        </p>
      ) : null}
    </Card>
  );
}

export function DocumentListPage() {
  const { t, i18n } = useTranslation();
  const categoryValues = DOCUMENT_CATEGORIES.map((value) => ({
    value,
    label: t(`document.categories.${value}`, value),
  }));
  const categoryOptions = [{ value: '', label: t('common.allCategories') }, ...categoryValues];
  const [search, setSearch] = useState('');
  const [category, setCategory] = useState('');
  const [showCreate, setShowCreate] = useState(false);
  const [uploadCategory, setUploadCategory] = useState('general');
  const [limitReached, setLimitReached] = useState(false);

  const { data, isLoading, error, refetch } = useDocumentList({ search, category });
  const usage = useDocumentUsage();
  const deleteMutation = useDeleteDocument();

  const documents = data?.data ?? [];
  const filtered = Boolean(search || category);

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('document.title', 'Dokumente')}
        description={t('document.summary')}
        actions={
          <Button icon={<Upload />} onClick={() => setShowCreate(true)}>
            {t('document.upload')}
          </Button>
        }
      />

      <StorageUsage usage={usage.data} limitReached={limitReached} />

      <Toolbar>
        <Input
          type="text"
          icon={<Search />}
          placeholder={t('common.search', 'Suche')}
          aria-label={t('common.search', 'Suche')}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-full sm:w-64"
        />
        <Select
          aria-label={t('document.category', 'Kategorie')}
          value={category}
          onChange={(e) => setCategory(e.target.value)}
          options={categoryOptions}
          className="w-40 sm:w-44"
        />
        {data ? (
          <ToolbarEnd>
            <span className="text-sm text-fg-muted tabular-nums">
              {data.total} {t('common.entries')}
            </span>
          </ToolbarEnd>
        ) : null}
      </Toolbar>

      {isLoading ? <SkeletonList rows={5} label={t('app.loading', 'Laden...')} /> : null}
      {error ? (
        <ErrorState
          title={t('app.error')}
          description={error instanceof Error ? error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      {data ? (
        <Card className="p-0 overflow-hidden">
          <ScrollRegion label={t('document.title', 'Dokumente')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('document.docTitle', 'Titel')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('document.fileName', 'Dateiname')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('document.category', 'Kategorie')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('document.size', 'Größe')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('document.version', 'Version')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('document.scanStatus')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('common.actions', 'Aktionen')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {documents.map((doc) => (
                  <tr key={doc.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>
                      <Link
                        to={`/documents/${doc.id}`}
                        className="hover:underline underline-offset-2"
                      >
                        {doc.title}
                      </Link>
                    </td>
                    {/* A field the field permissions leave out (RBA-07) is absent. */}
                    <td className={tdMonoClasses}>{doc.file_name ?? '—'}</td>
                    <td className={tdClasses}>
                      {doc.category ? (
                        <Badge variant="neutral">
                          {t(`document.categories.${doc.category}`, doc.category)}
                        </Badge>
                      ) : (
                        '—'
                      )}
                    </td>
                    <td className={cn(tdClasses, 'whitespace-nowrap text-right tabular-nums')}>
                      {doc.file_size === undefined
                        ? '—'
                        : formatBytes(doc.file_size, i18n.language)}
                    </td>
                    <td className={cn(tdClasses, 'tabular-nums')}>
                      {t('document.versionLabel', { version: doc.version })}
                    </td>
                    <td className={tdClasses}>
                      <ScanStatusBadge status={doc.scan_status} />
                    </td>
                    <td className={cn(tdClasses, 'text-right')}>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => deleteMutation.mutate(doc.id)}
                      >
                        {t('common.delete', 'Löschen')}
                      </Button>
                    </td>
                  </tr>
                ))}
                {documents.length === 0 ? (
                  <TableEmptyRow
                    colSpan={7}
                    icon={<FileText />}
                    title={filtered ? t('common.noResults') : t('document.emptyTitle')}
                    description={filtered ? t('common.noResultsHint') : t('document.emptyHint')}
                  />
                ) : null}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}

      <Modal open={showCreate} onOpenChange={setShowCreate} title={t('document.upload')}>
        <div className="space-y-4">
          <Select
            label={t('document.category', 'Kategorie')}
            value={uploadCategory}
            onChange={(e) => setUploadCategory(e.target.value)}
            options={categoryValues}
          />
          {/* The storage key is generated by the server when the content is
              uploaded (PUT /documents/{id}/content, TEN-06, WP-560). */}
          <FileUpload
            category={uploadCategory}
            onUploaded={() => {
              setLimitReached(false);
              setShowCreate(false);
            }}
            onError={(failure) => {
              if (isStorageLimit(failure)) setLimitReached(true);
            }}
          />
        </div>
      </Modal>
    </div>
  );
}
