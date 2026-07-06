import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useDocumentList, useCreateDocument, useDeleteDocument } from '../api/hooks';
import { Card } from '../components/ui/Card';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';
import { Badge } from '../components/ui/Badge';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';

const categoryOptions = [
  { value: '', label: 'Alle' },
  { value: 'general', label: 'Allgemein' },
  { value: 'contract', label: 'Vertrag' },
  { value: 'invoice', label: 'Rechnung' },
  { value: 'manual', label: 'Handbuch' },
  { value: 'diagram', label: 'Diagramm' },
  { value: 'certificate', label: 'Zertifikat' },
  { value: 'policy', label: 'Richtlinie' },
  { value: 'other', label: 'Sonstiges' },
];

function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function DocumentListPage() {
  const { t } = useTranslation();
  const [search, setSearch] = useState('');
  const [category, setCategory] = useState('');
  const [showCreate, setShowCreate] = useState(false);

  const { data, isLoading } = useDocumentList({ search, category });
  const createMutation = useCreateDocument();
  const deleteMutation = useDeleteDocument();

  const [form, setForm] = useState({ title: '', file_name: '', storage_key: '', category: 'general' });

  const handleCreate = () => {
    if (!form.title || !form.file_name || !form.storage_key) return;
    createMutation.mutate({ ...form, file_size: 0 }, {
      onSuccess: () => { setShowCreate(false); setForm({ title: '', file_name: '', storage_key: '', category: 'general' }); },
    });
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">{t('document.title', 'Dokumente')}</h1>
        <Button onClick={() => setShowCreate(true)}>{t('common.create', 'Erstellen')}</Button>
      </div>

      <div className="flex gap-4 flex-wrap">
        <Input label={t('common.search', 'Suche')} value={search} onChange={(e) => setSearch(e.target.value)} />
        <Select label={t('document.category', 'Kategorie')} value={category} onChange={(e) => setCategory(e.target.value)} options={categoryOptions} />
      </div>

      {isLoading ? (
        <p className="text-gray-500 dark:text-gray-400">{t('app.loading', 'Laden...')}</p>
      ) : (
        <Card title={`${t('document.title', 'Dokumente')} (${data?.total ?? 0})`}>
          <div className="overflow-x-auto">
            <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
              <thead>
                <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
                  <th className="pb-2">{t('document.docTitle', 'Titel')}</th>
                  <th className="pb-2">{t('document.fileName', 'Dateiname')}</th>
                  <th className="pb-2">{t('document.category', 'Kategorie')}</th>
                  <th className="pb-2">{t('document.size', 'Größe')}</th>
                  <th className="pb-2">{t('document.version', 'Version')}</th>
                  <th className="pb-2">{t('common.actions', 'Aktionen')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {data?.data.map((doc) => (
                  <tr key={doc.id} className="text-sm text-gray-700 dark:text-gray-300">
                    <td className="py-2 font-medium">{doc.title}</td>
                    <td className="py-2 font-mono text-xs">{doc.file_name}</td>
                    <td className="py-2"><Badge variant="neutral">{doc.category}</Badge></td>
                    <td className="py-2">{formatFileSize(doc.file_size)}</td>
                    <td className="py-2">v{doc.version}</td>
                    <td className="py-2">
                      <Button variant="ghost" size="sm" onClick={() => deleteMutation.mutate(doc.id)}>
                        {t('common.delete', 'Löschen')}
                      </Button>
                    </td>
                  </tr>
                ))}
                {(!data?.data || data.data.length === 0) && (
                  <tr><td colSpan={6} className="py-4 text-center text-gray-400">{t('common.noData', 'Keine Daten')}</td></tr>
                )}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      <Modal open={showCreate} onOpenChange={setShowCreate} title={t('document.upload', 'Dokument erstellen')}>
        <div className="space-y-4">
          <Input label={t('document.docTitle', 'Titel')} value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} required />
          <Input label={t('document.fileName', 'Dateiname')} value={form.file_name} onChange={(e) => setForm({ ...form, file_name: e.target.value })} required />
          <Input label={t('document.storageKey', 'Storage-Key')} value={form.storage_key} onChange={(e) => setForm({ ...form, storage_key: e.target.value })} required />
          <Select label={t('document.category', 'Kategorie')} value={form.category} onChange={(e) => setForm({ ...form, category: e.target.value })} options={categoryOptions.slice(1)} />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>{t('common.cancel', 'Abbrechen')}</Button>
            <Button onClick={handleCreate}>{t('common.save', 'Speichern')}</Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
