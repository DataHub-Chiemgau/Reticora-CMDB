import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  useCITypes,
  useCITypeTemplates,
  useCreateCIType,
  useCloneCIType,
  useSetCITypeActive,
  useUpsertCITypeField,
  useDeleteCITypeField,
} from '../api/cmdbHooks';
import type { CIType } from '../api/cmdb';
import type { FieldDefinition } from '../lib/fieldmeta';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Select } from '../components/ui/Select';
import { Modal } from '../components/ui/Modal';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

const FIELD_TYPES = [
  'text',
  'textarea',
  'integer',
  'decimal',
  'boolean',
  'date',
  'datetime',
  'enum',
  'multi_enum',
  'ip',
  'mac',
  'url',
  'email',
  'json',
  'file',
  'user',
  'team',
  'location',
  'ci_ref',
  'asset_ref',
  'contract_ref',
];

export function CITypeAdminPage() {
  const { t } = useTranslation();
  const [search, setSearch] = useState('');
  const [showInactive, setShowInactive] = useState(false);
  const [selectedId, setSelectedId] = useState<string | undefined>();
  const [showTemplates, setShowTemplates] = useState(false);
  const [showFieldEditor, setShowFieldEditor] = useState(false);
  const [newTypeName, setNewTypeName] = useState('');
  const [showCreate, setShowCreate] = useState(false);
  const [fieldDraft, setFieldDraft] = useState<FieldDefinition>({ name: '', data_type: 'text' });

  const { data, isLoading, error, refetch } = useCITypes({
    search,
    include_inactive: showInactive,
  });
  const templates = useCITypeTemplates();
  const createType = useCreateCIType();
  const cloneType = useCloneCIType();
  const setActive = useSetCITypeActive();
  const upsertField = useUpsertCITypeField();
  const deleteField = useDeleteCITypeField();

  const selected = (data?.data ?? []).find((ty) => ty.id === selectedId);

  const instantiateTemplate = (tpl: CIType) => {
    createType.mutate(
      {
        key: tpl.key,
        name: tpl.name,
        display_name: tpl.display_name,
        description: tpl.description,
        category: tpl.category,
        is_logical: tpl.is_logical,
        capabilities: tpl.capabilities,
        allowed_relationship_types: tpl.allowed_relationship_types,
        fields: tpl.fields,
      },
      { onSuccess: () => setShowTemplates(false) },
    );
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('citypes.title', 'CI-Typen')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
            {t(
              'citypes.subtitle',
              'Metadaten-getriebene Typen: Felder, Gruppen, Validierung, Lebenszyklus.',
            )}
          </p>
        </div>
        <div className="flex gap-2">
          <Button size="sm" variant="secondary" onClick={() => setShowTemplates(true)}>
            {t('citypes.fromTemplate', 'Aus Vorlage')}
          </Button>
          <Button size="sm" onClick={() => setShowCreate(true)}>
            {t('citypes.create', 'Typ anlegen')}
          </Button>
        </div>
      </div>

      <Card>
        <div className="flex flex-wrap items-center gap-3">
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('common.search', 'Suchen')}
            className="max-w-xs"
            aria-label={t('common.search', 'Suchen')}
          />
          <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input
              type="checkbox"
              checked={showInactive}
              onChange={(e) => setShowInactive(e.target.checked)}
            />
            {t('citypes.showInactive', 'Deaktivierte anzeigen')}
          </label>
        </div>
      </Card>

      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <div className="space-y-2 lg:col-span-1">
          {(data?.data ?? []).map((ty) => (
            <button
              key={ty.id}
              onClick={() => setSelectedId(ty.id)}
              className={`w-full rounded-lg border px-4 py-3 text-left transition ${
                selectedId === ty.id
                  ? 'border-primary bg-primary/5'
                  : 'border-gray-200 hover:border-gray-300 dark:border-gray-700'
              }`}
            >
              <div className="flex items-center justify-between">
                <span className="font-medium text-gray-900 dark:text-gray-100">
                  {ty.display_name || ty.name}
                </span>
                <div className="flex gap-1">
                  {ty.is_system ? <Badge variant="info">system</Badge> : null}
                  {!ty.is_active ? (
                    <Badge variant="warning">{t('citypes.inactive', 'deaktiviert')}</Badge>
                  ) : null}
                  {ty.is_logical ? (
                    <Badge variant="neutral">{t('citypes.logical', 'logisch')}</Badge>
                  ) : null}
                </div>
              </div>
              <p className="mt-0.5 text-xs text-gray-500">
                {ty.key} · v{ty.version}
                {ty.category ? ` · ${ty.category}` : ''}
              </p>
            </button>
          ))}
        </div>

        <div className="lg:col-span-2">
          {selected ? (
            <CITypeDetail
              type={selected}
              onClone={() => cloneType.mutate({ id: selected.id })}
              onToggleActive={() =>
                setActive.mutate({ id: selected.id, active: !selected.is_active })
              }
              onAddField={() => {
                setFieldDraft({ name: '', data_type: 'text', ui_group: 'general' });
                setShowFieldEditor(true);
              }}
              onDeleteField={(name) => deleteField.mutate({ typeId: selected.id, name })}
              t={t}
            />
          ) : (
            <Card>
              <p className="text-sm text-gray-500">
                {t('citypes.select', 'Typ auswählen, um Felder zu bearbeiten.')}
              </p>
            </Card>
          )}
        </div>
      </div>

      {/* Templates modal */}
      <Modal
        open={showTemplates}
        onOpenChange={setShowTemplates}
        title={t('citypes.templates', 'Typvorlagen')}
      >
        <div className="grid max-h-96 grid-cols-1 gap-2 overflow-y-auto md:grid-cols-2">
          {(templates.data?.data ?? []).map((tpl) => (
            <button
              key={tpl.key}
              onClick={() => instantiateTemplate(tpl)}
              className="rounded-lg border border-gray-200 px-3 py-2 text-left hover:border-primary dark:border-gray-700"
            >
              <p className="font-medium">{tpl.display_name || tpl.name}</p>
              <p className="text-xs text-gray-500">
                {tpl.category}
                {tpl.is_logical ? ' · logisch' : ''} · {(tpl.fields ?? []).length} Felder
              </p>
            </button>
          ))}
        </div>
      </Modal>

      {/* Create modal */}
      <Modal
        open={showCreate}
        onOpenChange={setShowCreate}
        title={t('citypes.create', 'Typ anlegen')}
      >
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            createType.mutate(
              { name: newTypeName },
              {
                onSuccess: () => {
                  setShowCreate(false);
                  setNewTypeName('');
                },
              },
            );
          }}
        >
          <Input
            label={t('citypes.name', 'Name')}
            value={newTypeName}
            onChange={(e) => setNewTypeName(e.target.value)}
            required
          />
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button type="submit" disabled={createType.isPending}>
              {t('common.create', 'Anlegen')}
            </Button>
          </div>
        </form>
      </Modal>

      {/* Field editor modal */}
      <Modal
        open={showFieldEditor}
        onOpenChange={setShowFieldEditor}
        title={t('citypes.addField', 'Feld hinzufügen')}
      >
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!selected) return;
            upsertField.mutate(
              { typeId: selected.id, field: fieldDraft },
              { onSuccess: () => setShowFieldEditor(false) },
            );
          }}
        >
          <Input
            label={t('citypes.fieldName', 'Feldname')}
            value={fieldDraft.name}
            required
            onChange={(e) => setFieldDraft({ ...fieldDraft, name: e.target.value })}
          />
          <Input
            label={t('citypes.fieldLabel', 'Anzeigename')}
            value={fieldDraft.label ?? ''}
            onChange={(e) => setFieldDraft({ ...fieldDraft, label: e.target.value })}
          />
          <Select
            label={t('citypes.fieldType', 'Typ')}
            options={FIELD_TYPES.map((ft) => ({ value: ft, label: ft }))}
            value={fieldDraft.data_type}
            onChange={(e) =>
              setFieldDraft({
                ...fieldDraft,
                data_type: e.target.value as FieldDefinition['data_type'],
              })
            }
          />
          <Input
            label={t('citypes.fieldGroup', 'Gruppe')}
            value={fieldDraft.ui_group ?? ''}
            onChange={(e) => setFieldDraft({ ...fieldDraft, ui_group: e.target.value })}
          />
          {fieldDraft.data_type === 'enum' || fieldDraft.data_type === 'multi_enum' ? (
            <Input
              label={t('citypes.enumValues', 'Werte (kommagetrennt)')}
              value={(fieldDraft.enum_values ?? []).join(', ')}
              onChange={(e) =>
                setFieldDraft({
                  ...fieldDraft,
                  enum_values: e.target.value
                    .split(',')
                    .map((s) => s.trim())
                    .filter(Boolean),
                })
              }
            />
          ) : null}
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={!!fieldDraft.required}
              onChange={(e) => setFieldDraft({ ...fieldDraft, required: e.target.checked })}
            />
            {t('citypes.required', 'Pflichtfeld')}
          </label>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={() => setShowFieldEditor(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button type="submit" disabled={upsertField.isPending}>
              {t('common.save', 'Speichern')}
            </Button>
          </div>
        </form>
      </Modal>
    </div>
  );
}

function CITypeDetail({
  type,
  onClone,
  onToggleActive,
  onAddField,
  onDeleteField,
  t,
}: {
  type: CIType;
  onClone: () => void;
  onToggleActive: () => void;
  onAddField: () => void;
  onDeleteField: (name: string) => void;
  t: TFunction;
}) {
  const fields = type.fields ?? [];
  return (
    <Card
      title={type.display_name || type.name}
      actions={
        <div className="flex gap-2">
          <Button size="sm" variant="secondary" onClick={onClone}>
            {t('citypes.clone', 'Klonen')}
          </Button>
          <Button
            size="sm"
            variant={type.is_active ? 'danger' : 'primary'}
            onClick={onToggleActive}
          >
            {type.is_active
              ? t('citypes.deactivate', 'Deaktivieren')
              : t('citypes.activate', 'Aktivieren')}
          </Button>
        </div>
      }
    >
      {type.description ? (
        <p className="text-sm text-gray-600 dark:text-gray-300">{type.description}</p>
      ) : null}
      <div className="mt-4 flex items-center justify-between">
        <h3 className="text-sm font-semibold">
          {t('citypes.fields', 'Felder')} ({fields.length})
        </h3>
        <Button size="sm" variant="secondary" onClick={onAddField}>
          {t('citypes.addField', 'Feld hinzufügen')}
        </Button>
      </div>
      <ul className="mt-2 divide-y divide-gray-100 dark:divide-gray-800">
        {fields.map((f) => (
          <li key={f.name} className="flex items-center justify-between py-2 text-sm">
            <div>
              <span className="font-medium">{f.label || f.name}</span>
              <span className="ml-2 text-xs text-gray-500">
                {f.data_type}
                {f.ui_group ? ` · ${f.ui_group}` : ''}
              </span>
              {f.required ? (
                <Badge variant="warning" className="ml-2">
                  *
                </Badge>
              ) : null}
              {f.conditional ? (
                <Badge variant="info" className="ml-2">
                  {t('citypes.conditional', 'bedingt')}
                </Badge>
              ) : null}
            </div>
            {!type.is_system ? (
              <button
                className="text-xs text-red-600 hover:underline"
                onClick={() => onDeleteField(f.name)}
              >
                {t('common.delete', 'Löschen')}
              </button>
            ) : null}
          </li>
        ))}
        {fields.length === 0 ? (
          <li className="py-2 text-sm text-gray-500">
            {t('citypes.noFields', 'Keine Felder definiert.')}
          </li>
        ) : null}
      </ul>
    </Card>
  );
}
