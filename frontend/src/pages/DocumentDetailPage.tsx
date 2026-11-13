import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { Download, Link2, Plus, Upload, X } from 'lucide-react';
import { assetApi, type Document, type DocumentLink, type DocumentLinkTarget } from '../api/client';
import {
  useCI,
  useDocument,
  useDocumentLinks,
  useDocumentVersions,
  useLinkDocument,
  useUnlinkDocument,
  useUpdateDocument,
} from '../api/hooks';
import {
  ScanStatusBadge,
  documentErrorMessage,
  scanNotice,
  useDocumentDownload,
} from '../components/documents/LinkedDocuments';
import { formatDateTime, useLocationNames } from '../components/cmdb/CIDetailSections';
import { LocationPicker, type PickedLocation } from '../components/pickers/LocationPicker';
import { ObjectPicker, type PickedObject } from '../components/pickers/ObjectPicker';
import { FileUpload, formatBytes } from '../components/upload/FileUpload';
import { Button, IconButton } from '../components/ui/Button';
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
  tdMonoClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import { ScrollRegion } from '../components/ScrollRegion';
import { DOCUMENT_CATEGORIES } from './DocumentListPage';

/** LINK_ENTITY_TYPES are the targets of a document link (DocumentLink.entity_type, WP-267). */
export const LINK_ENTITY_TYPES = [
  'ci',
  'asset',
  'ticket',
  'location',
  'disposal_record',
  'stocktake',
  'contract',
  'stock_movement',
] as const;

/** The targets this page links; the others are linked on their own pages. */
type LinkKind = 'object' | 'location';

/**
 * DocumentDetailPage shows one document (UI-14, WP-321): its metadata to
 * edit, the versions with scan status and download, a new version through
 * FileUpload and the links. Download and new links need clean content; a
 * pending scan is read again every 5 s until it ends.
 */
export function DocumentDetailPage() {
  const { t } = useTranslation();
  const { id = '' } = useParams();
  const doc = useDocument(id);

  if (doc.isLoading) return <SkeletonList rows={4} label={t('app.loading')} />;
  if (doc.error || !doc.data) {
    return (
      <ErrorState
        title={t('app.error')}
        description={doc.error ? documentErrorMessage(doc.error, t) : undefined}
        retryLabel={t('common.retry')}
        onRetry={() => void doc.refetch()}
      />
    );
  }
  return <DocumentDetail document={doc.data} />;
}

function DocumentDetail({ document: doc }: { document: Document }) {
  const { t } = useTranslation();
  const notice = scanNotice(doc.scan_status, t);
  return (
    <div className="space-y-4">
      <PageHeader
        title={doc.title}
        description={doc.file_name}
        breadcrumbs={[{ label: t('document.title'), to: '/documents' }, { label: doc.title }]}
        meta={<ScanStatusBadge status={doc.scan_status} />}
      />
      {notice ? (
        <p
          role="status"
          className={cn(
            'rounded-lg border px-4 py-3 text-sm',
            doc.scan_status === 'infected'
              ? 'border-red-600/30 bg-red-50 text-red-800 dark:bg-red-500/10 dark:text-red-300'
              : 'border-line bg-panel text-fg',
          )}
        >
          {notice}
        </p>
      ) : null}
      <MetadataCard document={doc} />
      <VersionsCard document={doc} />
      <LinksCard document={doc} />
    </div>
  );
}

function MetadataCard({ document: doc }: { document: Document }) {
  const { t } = useTranslation();
  const update = useUpdateDocument(doc.id);
  // A field the field permissions leave out (RBA-07) is absent: the
  // category select is hidden then, and the save sends only the fields the
  // user changed, so a hidden field is never written back.
  const categoryReadable = doc.category !== undefined;
  const [form, setForm] = useState({
    title: doc.title,
    description: doc.description ?? '',
    category: doc.category ?? '',
  });
  const changes: { title?: string; description?: string; category?: string } = {};
  if (form.title !== doc.title) changes.title = form.title.trim();
  if (form.description !== (doc.description ?? '')) changes.description = form.description;
  if (categoryReadable && form.category !== doc.category) changes.category = form.category;
  const changed = Object.keys(changes).length > 0;

  return (
    <Card title={t('document.metadata')}>
      <form
        className="grid gap-4 sm:grid-cols-2"
        onSubmit={(event) => {
          event.preventDefault();
          if (!form.title.trim()) return;
          update.mutate(changes);
        }}
      >
        <Input
          label={t('document.docTitle')}
          value={form.title}
          onChange={(event) => setForm({ ...form, title: event.target.value })}
          required
        />
        {categoryReadable ? (
          <Select
            label={t('document.category')}
            value={form.category}
            onChange={(event) => setForm({ ...form, category: event.target.value })}
            options={DOCUMENT_CATEGORIES.map((value) => ({
              value,
              label: t(`document.categories.${value}`),
            }))}
          />
        ) : null}
        <div className="sm:col-span-2">
          <Textarea
            label={t('document.description')}
            value={form.description}
            rows={3}
            onChange={(event) => setForm({ ...form, description: event.target.value })}
          />
        </div>
        <div className="flex items-center justify-end gap-3 sm:col-span-2">
          {update.error ? (
            <p role="alert" className="text-sm text-danger">
              {documentErrorMessage(update.error, t)}
            </p>
          ) : null}
          {update.isSuccess && !changed ? (
            <p role="status" className="text-sm text-fg-muted">
              {t('document.saved')}
            </p>
          ) : null}
          <Button type="submit" disabled={!changed || update.isPending}>
            {t('common.save')}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function VersionsCard({ document: doc }: { document: Document }) {
  const { t, i18n } = useTranslation();
  const versions = useDocumentVersions(doc.id);
  const { download, error: downloadError } = useDocumentDownload();
  const [uploading, setUploading] = useState(false);
  const rows = versions.data?.pages.flatMap((page) => page.data) ?? [];

  return (
    <Card
      className="p-0 overflow-hidden"
      title={<span className="block px-5 pt-5">{t('document.versions')}</span>}
      actions={
        <div className="px-5 pt-5">
          <Button size="sm" icon={<Upload />} onClick={() => setUploading(true)}>
            {t('document.newVersion')}
          </Button>
        </div>
      }
    >
      {downloadError ? (
        <p role="alert" className="px-5 pb-3 text-sm text-danger">
          {downloadError}
        </p>
      ) : null}
      {versions.error ? (
        <p role="alert" className="px-5 pb-3 text-sm text-danger">
          {documentErrorMessage(versions.error, t)}
        </p>
      ) : null}
      <ScrollRegion label={t('document.versions')}>
        <table className={tableClasses}>
          <thead className={theadClasses}>
            <tr>
              <th scope="col" className={thClasses}>
                {t('document.version')}
              </th>
              <th scope="col" className={cn(thClasses, 'text-right')}>
                {t('document.size')}
              </th>
              <th scope="col" className={thClasses}>
                {t('document.mimeType')}
              </th>
              <th scope="col" className={thClasses}>
                {t('document.checksum')}
              </th>
              <th scope="col" className={thClasses}>
                {t('document.uploadedAt')}
              </th>
              <th scope="col" className={thClasses}>
                {t('document.scanStatus')}
              </th>
              <th scope="col" className={cn(thClasses, 'text-right')}>
                {t('common.actions')}
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((version) => (
              <tr key={version.id} className={trClasses}>
                <td className={cn(tdClasses, 'tabular-nums')}>
                  {t('document.versionLabel', { version: version.version })}
                </td>
                {/* A field the field permissions leave out (RBA-07) is absent. */}
                <td className={cn(tdClasses, 'whitespace-nowrap text-right tabular-nums')}>
                  {version.size === undefined ? '—' : formatBytes(version.size, i18n.language)}
                </td>
                <td className={tdMonoClasses}>{version.mime_type ?? '—'}</td>
                <td className={tdMonoClasses}>
                  {version.checksum_sha256 ? (
                    <span title={version.checksum_sha256}>
                      {version.checksum_sha256.slice(0, 12)}
                    </span>
                  ) : (
                    t('document.noChecksum')
                  )}
                </td>
                <td className={cn(tdClasses, 'whitespace-nowrap')}>
                  {formatDateTime(version.created_at, i18n.language)}
                </td>
                <td className={tdClasses}>
                  <ScanStatusBadge status={version.scan_status} />
                </td>
                <td className={cn(tdClasses, 'text-right')}>
                  {version.scan_status === 'clean' ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      icon={<Download />}
                      aria-label={t('document.downloadVersion', { version: version.version })}
                      onClick={() => void download(doc.id, version.version)}
                    >
                      {t('document.download')}
                    </Button>
                  ) : (
                    <span className="text-xs text-fg-muted">{t('document.downloadLocked')}</span>
                  )}
                </td>
              </tr>
            ))}
            {versions.data && rows.length === 0 ? (
              <tr>
                <td colSpan={7} className={cn(tdClasses, 'text-fg-muted')}>
                  {t('document.noVersions')}
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </ScrollRegion>
      {versions.hasNextPage ? (
        <div className="flex justify-center p-3">
          <Button
            size="sm"
            variant="secondary"
            disabled={versions.isFetchingNextPage}
            onClick={() => void versions.fetchNextPage()}
          >
            {t('document.moreVersions')}
          </Button>
        </div>
      ) : null}

      <Modal open={uploading} onOpenChange={setUploading} title={t('document.newVersion')}>
        <FileUpload
          documentId={doc.id}
          label={t('document.newVersionFile')}
          onUploaded={() => setUploading(false)}
        />
      </Modal>
    </Card>
  );
}

function LinksCard({ document: doc }: { document: Document }) {
  const { t } = useTranslation();
  const links = useDocumentLinks(doc.id);
  const unlink = useUnlinkDocument();
  const [adding, setAdding] = useState(false);
  const rows = links.data ?? [];
  const locationNames = useLocationNames(rows.some((link) => link.entity_type === 'location'));
  const clean = doc.scan_status === 'clean';

  return (
    <Card
      title={t('document.links')}
      actions={
        clean ? (
          <Button size="sm" icon={<Plus />} onClick={() => setAdding(true)}>
            {t('document.addLink')}
          </Button>
        ) : undefined
      }
    >
      {links.error ? (
        <p role="alert" className="text-sm text-danger">
          {documentErrorMessage(links.error, t)}
        </p>
      ) : null}
      {unlink.error ? (
        <p role="alert" className="text-sm text-danger">
          {documentErrorMessage(unlink.error, t)}
        </p>
      ) : null}
      {links.data && rows.length === 0 ? (
        <p className="text-sm text-fg-muted">{t('document.noLinks')}</p>
      ) : null}
      {rows.length > 0 ? (
        <ul aria-label={t('document.links')} className="divide-y divide-line">
          {rows.map((link) => (
            <li key={link.id} className="flex items-center gap-3 py-2">
              <span className="w-28 shrink-0 text-xs text-fg-muted">
                {t(`document.linkTypes.${link.entity_type}`, link.entity_type)}
              </span>
              <span className="min-w-0 flex-1 truncate text-sm text-fg">
                <LinkTargetName link={link} locationPath={locationNames.path} />
              </span>
              <IconButton
                size="sm"
                icon={<X />}
                label={t('document.unlink', {
                  type: t(`document.linkTypes.${link.entity_type}`, link.entity_type),
                })}
                onClick={() => unlink.mutate({ id: doc.id, linkId: link.id })}
              />
            </li>
          ))}
        </ul>
      ) : null}
      <Modal open={adding} onOpenChange={setAdding} title={t('document.addLink')}>
        <AddLinkForm documentId={doc.id} onDone={() => setAdding(false)} />
      </Modal>
    </Card>
  );
}

/** LinkTargetName names the target of a link: a CI links to its page, a location shows its path. */
function LinkTargetName({
  link,
  locationPath,
}: {
  link: DocumentLink;
  locationPath: (id?: string) => string | undefined;
}) {
  if (link.entity_type === 'ci') return <CIName id={link.entity_id} />;
  if (link.entity_type === 'asset') return <AssetName id={link.entity_id} />;
  if (link.entity_type === 'location') return <>{locationPath(link.entity_id)}</>;
  return <span className="font-mono text-xs">{link.entity_id}</span>;
}

function CIName({ id }: { id: string }) {
  const ci = useCI(id);
  return (
    <Link to={`/cmdb/${id}`} className="hover:underline">
      {ci.data?.name ?? id}
    </Link>
  );
}

function AssetName({ id }: { id: string }) {
  const asset = useQuery({ queryKey: ['assets', 'detail', id], queryFn: () => assetApi.get(id) });
  return <>{asset.data ? asset.data.name : id}</>;
}

function AddLinkForm({ documentId, onDone }: { documentId: string; onDone: () => void }) {
  const { t } = useTranslation();
  const link = useLinkDocument();
  const [kind, setKind] = useState<LinkKind>('object');
  const [objects, setObjects] = useState<PickedObject[]>([]);
  const [location, setLocation] = useState<PickedLocation | null>(null);

  let target: DocumentLinkTarget | undefined;
  const object = objects[0];
  if (kind === 'object' && object) target = { entity_type: object.kind, entity_id: object.id };
  if (kind === 'location' && location) target = { entity_type: 'location', entity_id: location.id };

  return (
    <form
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        if (!target) return;
        link.mutate({ id: documentId, target }, { onSuccess: onDone });
      }}
    >
      <Select
        label={t('document.linkTarget')}
        value={kind}
        onChange={(event) => setKind(event.target.value as LinkKind)}
        options={[
          { value: 'object', label: t('document.linkKinds.object') },
          { value: 'location', label: t('document.linkKinds.location') },
        ]}
      />
      {kind === 'object' ? (
        <ObjectPicker
          label={t('document.linkObject')}
          kinds={['ci', 'asset']}
          value={objects}
          onChange={setObjects}
        />
      ) : (
        <LocationPicker
          label={t('document.linkTypes.location')}
          value={location?.id ?? null}
          onChange={setLocation}
        />
      )}
      {link.error ? (
        <p role="alert" className="text-sm text-danger">
          {documentErrorMessage(link.error, t)}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={onDone}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" icon={<Link2 />} disabled={!target || link.isPending}>
          {t('document.link')}
        </Button>
      </div>
    </form>
  );
}
