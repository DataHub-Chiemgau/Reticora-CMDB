/**
 * FormEditorPage is the form editor of /forms/new and /forms/:id/edit
 * (WFL-01, WP-1133). Fields are added, moved and deleted; each has a key
 * (unique, ^[a-z][a-z0-9_]*$), a title in German and English, a type
 * (string, text, number, integer, boolean, enum with values, date,
 * signature) and required. visible_when, required_when and readonly_when
 * are MET-45 conditions set with the RuleBuilder over the fields of the
 * form; a condition on the field itself or on a deleted field is an error
 * at the field before saving, a 422 of the server appears at the field of
 * its path. The editor writes JSON schema and ui_hints (buildForm). Saving
 * creates with POST /forms or changes with PATCH /forms/{id} and If-Match
 * on version; a 412 points to the newer version with a reload. The live
 * preview renders the edited fields with FormRenderer; the versions of the
 * form (GET /forms/{id}/versions) list date and author, and an older
 * version opens read only in the preview. Without form:write the editor is
 * read only, conditions included; so is a definition with parts the editor
 * cannot represent (nested objects, arrays, conditions it cannot parse),
 * which it would otherwise overwrite. The keys of schema and ui_hints it
 * does not model stay on saving (buildForm with formExtras). A client form
 * takes no signature field: its document would be visible
 * organization-wide (FormFill).
 */
import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react';
import { useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams } from 'react-router';
import { ApiError, type FormDefinition, type FormDefinitionVersion } from '../api/client';
import {
  useFormDefinition,
  useFormVersions,
  useHasPermission,
  useSaveForm,
  useUser,
} from '../api/hooks';
import {
  CONDITION_KINDS,
  FIELD_KEY_PATTERN,
  FORM_FIELD_TYPES,
  FormRenderer,
  buildForm,
  conditionFields,
  formExtras,
  parseForm,
  type ConditionKind,
  type FormField,
  type FormFieldType,
} from '../components/form/FormRenderer';
import { RuleBuilder } from '../components/rules/RuleBuilder';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Textarea } from '../components/ui/Textarea';
import { CONDITION_MODE, type ConditionCheck, type FieldSpec } from '../rules/builderModes';

/** A field of the editor with an id that stays while its key changes. */
interface EditedField extends FormField {
  uid: string;
}

/** The editor of a value in the conditions per field type. */
function conditionSpec(f: EditedField): FieldSpec {
  const kind: FieldSpec['kind'] =
    f.type === 'number' || f.type === 'integer'
      ? 'number'
      : f.type === 'boolean'
        ? 'bool'
        : f.type === 'date'
          ? 'time'
          : 'text';
  const editor: FieldSpec['editor'] =
    f.type === 'enum'
      ? 'enum'
      : kind === 'number'
        ? 'number'
        : kind === 'bool'
          ? 'bool'
          : f.type === 'date'
            ? 'date'
            : 'text';
  return {
    field: f.key,
    source: 'attribute',
    kind,
    editor,
    definition: {
      key: f.key,
      display_name: { de: f.title.de || f.key, en: f.title.en || f.title.de || f.key },
      enum_values: f.enumValues,
    } as unknown as FieldSpec['definition'],
  };
}

export function FormEditorPage() {
  const { id } = useParams();
  const { t } = useTranslation();
  const definition = useFormDefinition(id);
  const [saved, setSaved] = useState(false);
  const header = (
    <PageHeader
      title={id ? t('forms.editor.editTitle') : t('forms.editor.newTitle')}
      description={t('forms.editor.subtitle')}
      actions={
        <Link to="/forms" className="text-sm text-accent underline underline-offset-2">
          {t('forms.editor.back')}
        </Link>
      }
    />
  );
  if (id && definition.isLoading) {
    return (
      <div className="space-y-4">
        {header}
        <SkeletonList rows={4} label={t('forms.loading')} />
      </div>
    );
  }
  if (id && (definition.isError || !definition.data)) {
    return (
      <div className="space-y-4">
        {header}
        <ErrorState
          title={t('forms.editor.loadFailed')}
          retryLabel={t('common.retry')}
          onRetry={() => void definition.refetch()}
        />
      </div>
    );
  }
  return (
    <div className="space-y-6">
      {header}
      {saved && (
        <p role="status" className="text-sm text-fg">
          {t('forms.editor.saved')}
        </p>
      )}
      <Editor
        key={`${definition.data?.id ?? 'new'}-${definition.data?.version ?? 0}`}
        form={definition.data}
        onReload={() => {
          setSaved(false);
          void definition.refetch();
        }}
        onSaved={() => {
          setSaved(true);
          void definition.refetch();
        }}
      />
    </div>
  );
}

let uidSeq = 0;
const nextUID = () => `f${++uidSeq}`;

function Editor({
  form,
  onReload,
  onSaved,
}: {
  form?: FormDefinition;
  onReload: () => void;
  onSaved: () => void;
}) {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const canWrite = useHasPermission('form:write') === true;
  const save = useSaveForm();
  const versions = useFormVersions(form?.id);
  const [name, setName] = useState(form?.name ?? '');
  const [description, setDescription] = useState(form?.description ?? '');
  const [fields, setFields] = useState<EditedField[]>(() =>
    parseForm(form?.schema, form?.ui_hints).map((f) => ({ ...f, uid: nextUID() })),
  );
  const extras = useMemo(() => formExtras(form?.schema, form?.ui_hints), [form]);
  // A definition the editor cannot represent stays read only (no loss).
  const unsupported = fields.some((f) => f.unsupported);
  const readOnly = !canWrite || unsupported;
  // The raw text of the value lists while typing (the comma must stay).
  const [enumText, setEnumText] = useState<Record<string, string>>({});
  // The checks of the condition builders per field and kind.
  const checks = useRef<Record<string, Partial<Record<ConditionKind, ConditionCheck>>>>({});
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [nameError, setNameError] = useState<string>();
  const [message, setMessage] = useState<string>();
  const [stale, setStale] = useState(false);
  const [previewValues, setPreviewValues] = useState<Record<string, unknown>>({});
  const [previewVersion, setPreviewVersion] = useState<FormDefinitionVersion>();

  const update = (uid: string, change: Partial<FormField>) => {
    setFields((list) => list.map((f) => (f.uid === uid ? { ...f, ...change } : f)));
  };
  const move = (index: number, step: number) =>
    setFields((list) => {
      const next = [...list];
      const target = index + step;
      if (target < 0 || target >= next.length) return list;
      [next[index], next[target]] = [next[target]!, next[index]!];
      return next;
    });
  const remove = (uid: string) => {
    setFields((list) => list.filter((f) => f.uid !== uid));
    delete checks.current[uid];
  };
  const add = () => {
    const uid = nextUID();
    let n = fields.length + 1;
    while (fields.some((f) => f.key === `field_${n}`)) n++;
    setFields((list) => [
      ...list,
      {
        uid,
        key: `field_${n}`,
        title: { de: '', en: '' },
        type: 'string',
        required: false,
        enumValues: [],
        conditions: {},
      },
    ]);
  };

  const specs = useMemo(() => fields.map(conditionSpec), [fields]);

  /** validate checks keys and conditions before saving (AK1, AK2). */
  const validate = (): Record<string, string> => {
    const out: Record<string, string> = {};
    const keys = new Map<string, number>();
    for (const f of fields) keys.set(f.key, (keys.get(f.key) ?? 0) + 1);
    for (const f of fields) {
      if (!FIELD_KEY_PATTERN.test(f.key)) out[f.uid] = t('forms.editor.keyInvalid');
      else if ((keys.get(f.key) ?? 0) > 1) out[f.uid] = t('forms.editor.keyTaken');
      else if (f.type === 'enum' && f.enumValues.length === 0)
        out[f.uid] = t('forms.editor.enumEmpty');
      else if (f.type === 'signature' && form?.client_id)
        out[f.uid] = t('forms.editor.signatureClientForm');
      if (out[f.uid]) continue;
      for (const kind of CONDITION_KINDS) {
        const check = checks.current[f.uid]?.[kind];
        if (check && !check.ok) {
          out[f.uid] = t('forms.editor.conditionIncomplete', {
            kind: t(`forms.editor.kinds.${kind}`),
          });
          break;
        }
        const named = conditionFields(f.conditions[kind]);
        if (named.includes(f.key)) {
          out[f.uid] = t('forms.editor.conditionSelf', { kind: t(`forms.editor.kinds.${kind}`) });
          break;
        }
        const unknown = named.find((n) => !keys.has(n));
        if (unknown) {
          out[f.uid] = t('forms.editor.conditionUnknown', {
            kind: t(`forms.editor.kinds.${kind}`),
            field: unknown,
          });
          break;
        }
      }
    }
    return out;
  };

  /** fieldOfPath names the field of a server path (ui_hints.fields.x.kind, schema.properties.x…). */
  const fieldOfPath = (path: string | undefined) => {
    const m = /^(?:ui_hints\.fields|schema\.properties)\.([a-z][a-z0-9_]*)/.exec(path ?? '');
    return m ? fields.find((f) => f.key === m[1]) : undefined;
  };

  const submit = () => {
    if (readOnly) return;
    setMessage(undefined);
    setNameError(undefined);
    const found = validate();
    setErrors(found);
    if (!name.trim()) setNameError(t('forms.editor.nameRequired'));
    if (Object.keys(found).length > 0 || !name.trim()) {
      setOpen((o) => ({
        ...o,
        ...Object.fromEntries(Object.keys(found).map((uid) => [uid, true])),
      }));
      return;
    }
    const built = buildForm(fields, extras);
    const data = { name: name.trim(), description: description.trim(), ...built };
    // mutateAsync: the save refreshes the definition, which mounts the
    // editor anew; the callbacks of mutate would not run any more.
    save
      .mutateAsync({
        id: form?.id,
        data: form ? data : { ...data, active: true },
        version: form?.version,
      })
      .then((stored) => {
        if (!form) {
          void navigate(`/forms/${stored.id}/edit`, { replace: true });
          return;
        }
        onSaved();
      })
      .catch((err: unknown) => {
        if (err instanceof ApiError && err.status === 412) {
          setStale(true);
          return;
        }
        if (err instanceof ApiError && (err.status === 422 || err.status === 400)) {
          const target = fieldOfPath(err.field);
          if (target) {
            setErrors({ [target.uid]: err.detail ?? t('forms.editor.saveFailed') });
            setOpen((o) => ({ ...o, [target.uid]: true }));
            return;
          }
          if (err.field === 'name') {
            setNameError(err.detail ?? t('forms.editor.saveFailed'));
            return;
          }
        }
        setMessage(
          err instanceof ApiError && err.status === 403
            ? t('forms.editor.forbidden')
            : t('forms.editor.saveFailed'),
        );
      });
  };

  const previewFields = previewVersion
    ? parseForm(previewVersion.schema, previewVersion.ui_hints)
    : fields;

  return (
    <div className="grid items-start gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,26rem)]">
      <div className="min-w-0 space-y-4">
        {stale && (
          <div
            role="alert"
            className="flex flex-wrap items-center gap-3 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-fg"
          >
            <span>{t('forms.editor.stale')}</span>
            <Button size="sm" variant="secondary" onClick={onReload}>
              {t('forms.editor.reload')}
            </Button>
          </div>
        )}
        {unsupported && canWrite ? (
          <p role="note" className="text-sm text-fg-muted">
            {t('forms.editor.unsupported')}
          </p>
        ) : readOnly ? (
          <p className="text-sm text-fg-muted">{t('forms.editor.readOnly')}</p>
        ) : null}
        <Card title={t('forms.editor.properties')}>
          <div className="grid gap-3 sm:grid-cols-2">
            <Input
              label={t('forms.editor.name')}
              value={name}
              onChange={(e) => setName(e.target.value)}
              error={nameError}
              readOnly={readOnly}
              required
            />
            <Textarea
              label={t('forms.editor.description')}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              readOnly={readOnly}
              rows={2}
            />
          </div>
        </Card>
        <Card
          title={t('forms.editor.fields')}
          actions={
            !readOnly ? (
              <Button size="sm" variant="secondary" icon={<Plus />} onClick={add}>
                {t('forms.editor.addField')}
              </Button>
            ) : null
          }
        >
          {fields.length === 0 ? (
            <EmptyState title={t('forms.editor.noFields')} className="py-6" />
          ) : (
            <ol className="space-y-3">
              {fields.map((f, index) => (
                <li key={f.uid}>
                  <fieldset
                    className="space-y-3 rounded-lg border border-line p-3"
                    aria-label={t('forms.editor.fieldLabel', { n: index + 1, key: f.key })}
                    data-invalid={errors[f.uid] ? 'true' : undefined}
                  >
                    <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                      <Input
                        label={t('forms.editor.key')}
                        value={f.key}
                        onChange={(e) => update(f.uid, { key: e.target.value })}
                        readOnly={readOnly}
                      />
                      <Input
                        label={t('forms.editor.titleDe')}
                        value={f.title.de ?? ''}
                        onChange={(e) =>
                          update(f.uid, { title: { ...f.title, de: e.target.value } })
                        }
                        readOnly={readOnly}
                      />
                      <Input
                        label={t('forms.editor.titleEn')}
                        value={f.title.en ?? ''}
                        onChange={(e) =>
                          update(f.uid, { title: { ...f.title, en: e.target.value } })
                        }
                        readOnly={readOnly}
                      />
                      <Select
                        label={t('forms.editor.type')}
                        value={f.type}
                        onChange={(e) => update(f.uid, { type: e.target.value as FormFieldType })}
                        disabled={readOnly}
                        options={FORM_FIELD_TYPES.map((type) => ({
                          value: type,
                          label: t(`forms.editor.types.${type}`),
                        }))}
                      />
                    </div>
                    {f.type === 'enum' && (
                      <Input
                        label={t('forms.editor.enumValues')}
                        value={enumText[f.uid] ?? f.enumValues.join(', ')}
                        onChange={(e) => {
                          const text = e.target.value;
                          setEnumText((m) => ({ ...m, [f.uid]: text }));
                          update(f.uid, {
                            enumValues: text
                              .split(',')
                              .map((v) => v.trim())
                              .filter(Boolean),
                          });
                        }}
                        onBlur={() =>
                          setEnumText((m) => {
                            const next = { ...m };
                            delete next[f.uid];
                            return next;
                          })
                        }
                        readOnly={readOnly}
                      />
                    )}
                    <div className="flex flex-wrap items-center gap-3">
                      <label className="flex items-center gap-2 text-sm text-fg">
                        <input
                          type="checkbox"
                          checked={f.required}
                          disabled={readOnly}
                          onChange={(e) => update(f.uid, { required: e.target.checked })}
                        />
                        {t('forms.editor.required')}
                      </label>
                      <Button
                        size="sm"
                        variant="ghost"
                        aria-expanded={open[f.uid] ?? false}
                        onClick={() => setOpen((o) => ({ ...o, [f.uid]: !o[f.uid] }))}
                      >
                        {t('forms.editor.conditions', { key: f.key })}
                      </Button>
                      {!readOnly && (
                        <span className="ml-auto flex gap-1">
                          <IconButton
                            size="sm"
                            label={t('forms.editor.moveUp', { key: f.key })}
                            icon={<ArrowUp />}
                            disabled={index === 0}
                            onClick={() => move(index, -1)}
                          />
                          <IconButton
                            size="sm"
                            label={t('forms.editor.moveDown', { key: f.key })}
                            icon={<ArrowDown />}
                            disabled={index === fields.length - 1}
                            onClick={() => move(index, 1)}
                          />
                          <IconButton
                            size="sm"
                            label={t('forms.editor.remove', { key: f.key })}
                            icon={<Trash2 />}
                            onClick={() => remove(f.uid)}
                          />
                        </span>
                      )}
                    </div>
                    {errors[f.uid] && (
                      <p role="alert" className="text-sm text-danger">
                        {errors[f.uid]}
                      </p>
                    )}
                    {open[f.uid] && (
                      // disabled: without form:write the conditions are read only.
                      <fieldset
                        disabled={readOnly}
                        aria-label={t('forms.editor.conditions', { key: f.key })}
                        className="space-y-3 border-t border-line pt-3"
                      >
                        {CONDITION_KINDS.map((kind) => (
                          <div
                            key={kind}
                            className="space-y-1"
                            data-testid={`condition-${f.key}-${kind}`}
                          >
                            <RuleBuilder
                              key={`${f.uid}-${kind}`}
                              mode={CONDITION_MODE}
                              initialValue={f.conditions[kind] ?? null}
                              fields={specs}
                              label={t(`forms.editor.kinds.${kind}`)}
                              onChange={(check: ConditionCheck) => {
                                if (readOnly) return;
                                checks.current[f.uid] = { ...checks.current[f.uid], [kind]: check };
                                if (!check.ok) return;
                                const next = check.condition ?? undefined;
                                setFields((list) => {
                                  const current = list.find((x) => x.uid === f.uid);
                                  if (
                                    !current ||
                                    JSON.stringify(current.conditions[kind]) ===
                                      JSON.stringify(next)
                                  ) {
                                    return list;
                                  }
                                  return list.map((x) =>
                                    x.uid === f.uid
                                      ? { ...x, conditions: { ...x.conditions, [kind]: next } }
                                      : x,
                                  );
                                });
                              }}
                            />
                          </div>
                        ))}
                      </fieldset>
                    )}
                  </fieldset>
                </li>
              ))}
            </ol>
          )}
        </Card>
        {message && (
          <p role="alert" className="text-sm text-danger">
            {message}
          </p>
        )}
        {!readOnly && (
          <div className="flex justify-end">
            <Button onClick={submit} loading={save.isPending}>
              {t('forms.editor.save')}
            </Button>
          </div>
        )}
      </div>
      <div className="min-w-0 space-y-4">
        <Card title={t('forms.editor.preview')}>
          <section
            aria-label={t('forms.editor.preview')}
            data-testid="form-preview"
            className="space-y-3"
          >
            {previewVersion && (
              <div className="flex flex-wrap items-center gap-2 text-sm text-fg-muted">
                <span>{t('forms.editor.previewVersion', { version: previewVersion.version })}</span>
                <Button size="sm" variant="ghost" onClick={() => setPreviewVersion(undefined)}>
                  {t('forms.editor.previewCurrent')}
                </Button>
              </div>
            )}
            {previewFields.length === 0 ? (
              <p className="text-sm text-fg-muted">{t('forms.editor.noFields')}</p>
            ) : (
              <FormRenderer
                fields={previewFields}
                values={previewValues}
                onChange={setPreviewValues}
                readOnly={!!previewVersion}
              />
            )}
          </section>
        </Card>
        {form && (
          <Card title={t('forms.editor.versions')}>
            {versions.isLoading ? (
              <SkeletonList rows={2} label={t('forms.loading')} />
            ) : versions.isError ? (
              <ErrorState
                title={t('forms.editor.versionsFailed')}
                retryLabel={t('common.retry')}
                onRetry={() => void versions.refetch()}
              />
            ) : (
              <ul className="divide-y divide-line text-sm" aria-label={t('forms.editor.versions')}>
                {(versions.data ?? []).map((v) => (
                  <li key={v.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
                    <span>
                      <span className="font-medium text-fg">
                        {t('forms.editor.version', { version: v.version })}
                      </span>
                      <span className="block text-xs text-fg-muted">
                        {new Date(v.created_at).toLocaleString(i18n.language)}
                        {' · '}
                        <Author id={v.created_by} />
                      </span>
                    </span>
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => {
                        setPreviewValues({});
                        setPreviewVersion(v);
                      }}
                    >
                      {t('forms.editor.openVersion', { version: v.version })}
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        )}
      </div>
    </div>
  );
}

/** Author names the user who stored a version; its id without user:read. */
function Author({ id }: { id?: string }) {
  const { t } = useTranslation();
  const canRead = useHasPermission('user:read') === true;
  const user = useUser(id, { enabled: canRead });
  if (!id) return <>{t('forms.editor.noAuthor')}</>;
  return <>{user.data?.display_name || user.data?.email || id}</>;
}
