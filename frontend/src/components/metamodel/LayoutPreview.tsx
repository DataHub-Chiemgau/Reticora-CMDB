/**
 * LayoutPreview is the preview of the form builder (MET-52, UI-11, WP-803):
 * the form as role X with template Y, resolved by the server (GET
 * /ci-types/{id}/form?template_id&role_id&preview=true, WP-719) and
 * rendered by DynamicForm with its layout. The inputs start with the
 * defaults and change local state only: the preview never writes.
 *
 * The preview as another role requires citype:manage; without it the role
 * selection is off. A 403 for a role shows a note and resolves with the
 * roles of the caller. With an active draft (WP-798) the preview reads the
 * state of the draft (draft=<id>, E-113), else the published state.
 */
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useResolvedTypeForm } from '../../api/cmdbHooks';
import { useHasPermission, useRoleList } from '../../api/hooks';
import { localizedText, type ResolvedForm, type Template } from '../../api/cmdb';
import { ApiError } from '../../api/client';
import { useMetamodelDraftStore } from '../../stores/metamodelDraft';
import type { FieldDataType, FieldDefinition } from '../../lib/fieldmeta';
import { DynamicForm, type DynamicFormSection } from '../form/DynamicForm';
import { ErrorState } from '../ui/ErrorState';
import { Select } from '../ui/Select';
import { Skeleton } from '../ui/Skeleton';

export interface LayoutPreviewProps {
  /** The CI type of the form builder. */
  ciTypeId: string;
  /** The templates of the type chain (the target list of the builder). */
  templates: Template[];
  /** The target and role the builder edits: the starting selection. */
  initialTemplateId?: string;
  initialRoleId?: string;
  /** The editor holds changes that are not saved yet. */
  unsaved?: boolean;
}

const isForbidden = (error: unknown) => error instanceof ApiError && error.status === 403;

/** fieldsOf maps the definitions of a resolved form to DynamicForm fields, once per name. */
export function fieldsOf(form: ResolvedForm): FieldDefinition[] {
  const seen = new Set<string>();
  const fields: FieldDefinition[] = [];
  for (const section of form.sections) {
    for (const field of section.fields) {
      if (seen.has(field.name)) continue;
      seen.add(field.name);
      const d = field.definition;
      fields.push({
        ...(d as unknown as FieldDefinition),
        name: field.name,
        data_type: d.data_type as FieldDataType,
      });
    }
  }
  return fields;
}

/** layoutOf maps the sections of a resolved form to the layout of DynamicForm. */
export function layoutOf(form: ResolvedForm): DynamicFormSection[] {
  return form.sections.map((s) => ({
    key: s.key,
    title: s.title,
    visible: s.visible,
    fields: s.fields.map((f) => ({
      name: f.name,
      width: f.width,
      state: {
        visible: f.state.visible,
        required: f.state.required,
        readOnly: f.state.read_only,
        allowedValues: f.state.allowed_values,
      },
    })),
  }));
}

/** defaultOf turns the default_value (a string) into a value of the data type. */
function defaultOf(field: FieldDefinition): unknown {
  const raw = field.default_value;
  if (raw === undefined || raw === '') return undefined;
  switch (field.data_type) {
    case 'boolean':
      return raw === 'true';
    case 'integer':
    case 'decimal':
    case 'number': {
      const n = Number(raw);
      return Number.isFinite(n) ? n : raw;
    }
    case 'json':
      try {
        return JSON.parse(raw) as unknown;
      } catch {
        return raw;
      }
    default:
      return raw;
  }
}

export function defaultsOf(fields: FieldDefinition[]): Record<string, unknown> {
  const values: Record<string, unknown> = {};
  for (const field of fields) {
    const value = defaultOf(field);
    if (value !== undefined) values[field.name] = value;
  }
  return values;
}

/** PreviewForm holds the local values; a new form starts again with its defaults. */
function PreviewForm({ form }: { form: ResolvedForm }) {
  const [fields] = useState(() => fieldsOf(form));
  const [layout] = useState(() => layoutOf(form));
  const [values, setValues] = useState(() => defaultsOf(fields));
  return (
    <DynamicForm
      fields={fields}
      layout={layout}
      values={values}
      onChange={(name, value) => setValues((v) => ({ ...v, [name]: value }))}
    />
  );
}

export function LayoutPreview({
  ciTypeId,
  templates,
  initialTemplateId = '',
  initialRoleId = '',
  unsaved = false,
}: LayoutPreviewProps) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const draft = useMetamodelDraftStore((s) => s.draft);
  const canManage = useHasPermission('citype:manage') === true;
  const roles = useRoleList({ limit: 200 }).data?.data ?? [];

  const [templateId, setTemplateId] = useState(initialTemplateId);
  const [chosenRole, setChosenRole] = useState(initialRoleId);
  // The preview as another role only with citype:manage (the server checks).
  const roleId = canManage ? chosenRole : '';
  const draftId = draft?.id;

  const asRole = useResolvedTypeForm(
    ciTypeId,
    {
      template_id: templateId || undefined,
      role_id: roleId,
      preview: true,
      draft: draftId,
    },
    { enabled: !!roleId },
  );
  const roleForbidden = !!roleId && isForbidden(asRole.error);
  // Without a role, or after a 403 for the role: the roles of the caller.
  const own = useResolvedTypeForm(
    ciTypeId,
    { template_id: templateId || undefined, draft: draftId },
    { enabled: !roleId || roleForbidden },
  );
  const query = roleId && !roleForbidden ? asRole : own;
  const form = query.data;
  const formKey = [
    templateId,
    roleForbidden ? '' : roleId,
    draftId ?? '',
    query.dataUpdatedAt,
  ].join(':');

  const templateName = (tpl: Template) => localizedText(tpl.display_name, lang) || tpl.key;
  const notes = [
    draft
      ? t('formBuilder.preview.draftState', { name: draft.name })
      : t('formBuilder.preview.publishedState'),
    ...(canManage ? [] : [t('formBuilder.preview.roleLocked')]),
    ...(roleForbidden ? [t('formBuilder.preview.roleForbidden')] : []),
    ...(unsaved ? [t('formBuilder.preview.unsaved')] : []),
  ];

  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2">
        <Select
          label={t('formBuilder.preview.template')}
          value={templateId}
          onChange={(e) => setTemplateId(e.target.value)}
          options={[
            { value: '', label: t('formBuilder.preview.noTemplate') },
            ...templates.map((tpl) => ({ value: tpl.id, label: templateName(tpl) })),
          ]}
        />
        <Select
          label={t('formBuilder.preview.role')}
          value={roleId}
          disabled={!canManage}
          onChange={(e) => setChosenRole(e.target.value)}
          options={[
            { value: '', label: t('formBuilder.preview.ownRoles') },
            ...roles.map((r) => ({ value: r.id, label: r.name })),
          ]}
        />
      </div>
      {/* One live region: the loading state is aria-busy on it, the skeleton
          below has no role of its own (a second status would make the region
          ambiguous for assistive technology and for tests). */}
      <div
        role="status"
        aria-busy={query.isLoading || undefined}
        className="space-y-1 text-sm text-fg-muted"
      >
        {notes.map((note) => (
          <p key={note}>{note}</p>
        ))}
      </div>
      {query.error && !roleForbidden ? (
        <ErrorState
          title={t('formBuilder.preview.error')}
          description={query.error instanceof Error ? query.error.message : undefined}
          retryLabel={t('common.retry', 'Erneut versuchen')}
          onRetry={() => void query.refetch()}
        />
      ) : null}
      {query.isLoading ? (
        <div className="space-y-2" aria-hidden="true">
          {[0, 1, 2].map((row) => (
            <Skeleton key={row} className="h-10 w-full" />
          ))}
        </div>
      ) : null}
      {form ? (
        <form
          aria-label={t('formBuilder.preview.form')}
          className="rounded-xl border border-dashed border-line p-4"
          onSubmit={(e) => e.preventDefault()}
        >
          <PreviewForm key={formKey} form={form} />
        </form>
      ) : null}
    </div>
  );
}
