import { useEffect, useMemo, useState } from 'react';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError } from '../api/client';
import type { CI, CICreateRequest, CIUpdateRequest } from '../api/client';
import { localizedText } from '../api/cmdb';
import { useCIType, useCITypes } from '../api/cmdbHooks';
import { useCreateCI, useUpdateCI } from '../api/hooks';
import { DynamicForm, validateAll } from '../components/form/DynamicForm';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';
import { evaluateField } from '../lib/fieldmeta';

interface CIFormModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  ci?: CI | null;
  onSuccess?: (ci: CI) => void;
}

interface CIFormValues {
  name: string;
  ci_type_id: string;
  status: string;
  manufacturer: string;
  model: string;
  serial_number: string;
  management_ip: string;
  firmware_version: string;
}

const statusValues = ['active', 'inactive', 'maintenance', 'decommissioned'];

function toFormValues(ci?: CI | null): CIFormValues {
  return {
    name: ci?.name ?? '',
    ci_type_id: ci?.ci_type_id ?? '',
    status: ci?.status ?? 'active',
    manufacturer: ci?.manufacturer ?? '',
    model: ci?.model ?? '',
    serial_number: ci?.serial_number ?? '',
    management_ip: ci?.management_ip ?? '',
    firmware_version: ci?.firmware_version ?? '',
  };
}

/** The optional typed fields of the form; PATCH clears them with null. */
const optionalFields = [
  'manufacturer',
  'model',
  'serial_number',
  'management_ip',
  'firmware_version',
] as const;

function optionalValue(value: string) {
  const trimmed = value.trim();
  return trimmed ? trimmed : undefined;
}

function isBlank(value: unknown) {
  return value === undefined || value === null || (typeof value === 'string' && !value.trim());
}

export function CIFormModal({ open, onOpenChange, ci, onSuccess }: CIFormModalProps) {
  const { t, i18n } = useTranslation();
  const createMutation = useCreateCI();
  const updateMutation = useUpdateCI();
  const isEditMode = Boolean(ci);
  const [values, setValues] = useState<CIFormValues>(() => toFormValues(ci));
  const [errors, setErrors] = useState<Partial<Record<keyof CIFormValues, string>>>({});
  const [attributes, setAttributes] = useState<Record<string, unknown>>(() => ci?.attributes ?? {});
  const [attributeErrors, setAttributeErrors] = useState<Record<string, string | null>>({});

  useEffect(() => {
    if (open) {
      setValues(toFormValues(ci));
      setErrors({});
      setAttributes(ci?.attributes ?? {});
      setAttributeErrors({});
    }
  }, [ci, open]);

  // The types come from the metamodel (GET /ci-types: the active system types
  // and the organization's own, MET-04), read again at every opening so a type
  // created at runtime is offered without a reload or release. The option value
  // is the type id, which POST /cis expects as ci_type_id (CI-03).
  const ciTypes = useCITypes({ limit: 200 }, { enabled: open, staleTime: 0 });
  // The chosen type's schema: its attributes are rendered below the fields.
  const selectedType = useCIType(values.ci_type_id || undefined, { enabled: open, staleTime: 0 });
  const typeFields = useMemo(
    () => (selectedType.data?.id === values.ci_type_id ? (selectedType.data.fields ?? []) : []),
    [selectedType.data, values.ci_type_id],
  );

  const typeOptions = useMemo(() => {
    const types = ciTypes.data?.data ?? [];
    const stored = selectedType.data;
    // Editing keeps the stored type selectable after it was deactivated.
    const offered =
      isEditMode &&
      stored &&
      stored.id === ci?.ci_type_id &&
      !types.some((ty) => ty.id === stored.id)
        ? [...types, stored]
        : types;
    return [
      { value: '', label: t('form.selectPlaceholder') },
      ...offered
        .map((ty) => ({
          value: ty.id,
          label: localizedText(ty.display_name, i18n.language) || ty.key,
        }))
        .sort((a, b) => a.label.localeCompare(b.label, i18n.language)),
    ];
  }, [ciTypes.data, selectedType.data, isEditMode, ci?.ci_type_id, t, i18n.language]);

  const statusOptions = useMemo(
    () =>
      statusValues.map((value) => ({
        value,
        label: t(`ci.status${value.charAt(0).toUpperCase()}${value.slice(1)}`),
      })),
    [t],
  );

  const serverError = (createMutation.error || updateMutation.error) as Error | null;
  const isSubmitting = createMutation.isPending || updateMutation.isPending;

  // Server-side field validation (RFC 7807 + `violations`) is authoritative:
  // the CI type's field metadata is only fully known to the backend, so a
  // rejection can name fields the client never validated. Violations that map
  // onto a known input are rendered inline; anything else stays in the banner
  // so no server complaint is silently swallowed.
  const serverViolations =
    serverError instanceof ApiError
      ? serverError.violationsByField()
      : ({} as Record<string, string>);

  const fieldErrors: Partial<Record<keyof CIFormValues, string>> = { ...errors };
  const attributeFieldErrors: Record<string, string | null> = { ...attributeErrors };
  const attributeNames = new Set(typeFields.map((field) => field.name));
  const unmappedViolations: string[] = [];
  for (const [field, detail] of Object.entries(serverViolations)) {
    const attribute = field.startsWith('attributes.') ? field.slice('attributes.'.length) : field;
    if (field in values) {
      const key = field as keyof CIFormValues;
      if (!fieldErrors[key]) fieldErrors[key] = detail;
    } else if (attributeNames.has(attribute)) {
      if (!attributeFieldErrors[attribute]) attributeFieldErrors[attribute] = detail;
    } else {
      unmappedViolations.push(`${field}: ${detail}`);
    }
  }

  const bannerMessage = serverError
    ? unmappedViolations.length > 0
      ? unmappedViolations.join('; ')
      : Object.keys(serverViolations).length > 0
        ? t('form.validation.serverRejected', 'Bitte korrigieren Sie die markierten Felder.')
        : serverError.message
    : null;

  function setValue<Key extends keyof CIFormValues>(key: Key, value: CIFormValues[Key]) {
    setValues((current) => ({ ...current, [key]: value }));
    setErrors((current) => ({ ...current, [key]: undefined }));
  }

  function setAttribute(name: string, value: unknown) {
    setAttributes((current) => ({ ...current, [name]: value }));
    setAttributeErrors((current) => ({ ...current, [name]: null }));
  }

  // The values of the chosen type's attributes that apply in the current state;
  // hidden fields and values left from another type are not sent. Editing sends
  // only changed values (PATCH merges the attributes); a stored value the user
  // cleared is sent as null, which removes it (RFC 7396, CI-04).
  function attributePayload(): Record<string, unknown> | undefined {
    const payload: Record<string, unknown> = {};
    for (const def of typeFields) {
      const value = attributes[def.name];
      if (!evaluateField(def, attributes).visible) continue;
      const stored = ci?.attributes?.[def.name];
      if (isBlank(value)) {
        if (isEditMode && !isBlank(stored)) payload[def.name] = null;
        continue;
      }
      if (isEditMode && JSON.stringify(value) === JSON.stringify(stored)) {
        continue;
      }
      payload[def.name] = value;
    }
    return Object.keys(payload).length > 0 ? payload : undefined;
  }

  // The edit payload holds only the members whose value differs from the
  // loaded CI (F-228): a stale editor that saves one field does not resend
  // the others, so If-Match refuses it only when that field changed in the
  // meantime (API-07). A cleared optional field is sent as null, which the
  // merge patch stores as NULL (F-081, CI-04); omitting it kept the old value.
  function updatePayload(): CIUpdateRequest {
    const payload: CIUpdateRequest = {};
    const name = values.name.trim();
    if (name !== (ci?.name ?? '')) payload.name = name;
    if (values.status !== (ci?.status ?? '')) payload.status = values.status;
    for (const key of optionalFields) {
      const next = optionalValue(values[key]);
      if (next !== optionalValue(ci?.[key] ?? '')) payload[key] = next ?? null;
    }
    const attrs = attributePayload();
    if (attrs) payload.attributes = attrs;
    return payload;
  }

  function validate() {
    const nextErrors: Partial<Record<keyof CIFormValues, string>> = {};

    if (!values.name.trim()) {
      nextErrors.name = t('form.validation.nameRequired');
    }

    if (!values.ci_type_id.trim()) {
      nextErrors.ci_type_id = t('form.validation.ciTypeRequired');
    }

    const nextAttributeErrors = validateAll(typeFields, attributes);

    setErrors(nextErrors);
    setAttributeErrors(nextAttributeErrors);
    return (
      Object.keys(nextErrors).length === 0 &&
      Object.values(nextAttributeErrors).every((error) => !error)
    );
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!validate()) {
      return;
    }

    const createPayload: CICreateRequest = {
      name: values.name.trim(),
      ci_type_id: values.ci_type_id,
      status: values.status,
      manufacturer: optionalValue(values.manufacturer),
      model: optionalValue(values.model),
      serial_number: optionalValue(values.serial_number),
      management_ip: optionalValue(values.management_ip),
      firmware_version: optionalValue(values.firmware_version),
      attributes: attributePayload(),
      discovery_source: 'manual',
    };

    const changes = isEditMode ? updatePayload() : {};
    if (isEditMode && Object.keys(changes).length === 0) {
      // Nothing changed: no request, no new version.
      onOpenChange(false);
      return;
    }

    try {
      const saved =
        isEditMode && ci
          ? await updateMutation.mutateAsync({
              id: ci.id,
              data: changes,
              version: ci.version,
            })
          : await createMutation.mutateAsync(createPayload);
      onSuccess?.(saved);
      onOpenChange(false);
    } catch {
      // Errors are surfaced through mutation state.
    }
  }

  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      title={isEditMode ? t('form.editCI') : t('form.createCI')}
      description={t('form.ciDescription')}
    >
      <form className="space-y-4" onSubmit={handleSubmit}>
        <Input
          label={t('form.fields.name')}
          value={values.name}
          onChange={(event) => setValue('name', event.target.value)}
          error={fieldErrors.name}
          required
        />
        <Select
          label={t('form.fields.ciType')}
          value={values.ci_type_id}
          onChange={(event) => {
            setValue('ci_type_id', event.target.value);
            setAttributeErrors({});
          }}
          options={typeOptions}
          error={
            fieldErrors.ci_type_id ?? (ciTypes.isError ? t('form.typesLoadFailed') : undefined)
          }
          disabled={isEditMode}
          required
        />
        <Select
          label={t('form.fields.status')}
          value={values.status}
          onChange={(event) => setValue('status', event.target.value)}
          options={statusOptions}
          error={fieldErrors.status}
        />
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Input
            label={t('form.fields.manufacturer')}
            value={values.manufacturer}
            onChange={(event) => setValue('manufacturer', event.target.value)}
            error={fieldErrors.manufacturer}
          />
          <Input
            label={t('form.fields.model')}
            value={values.model}
            onChange={(event) => setValue('model', event.target.value)}
            error={fieldErrors.model}
          />
          <Input
            label={t('form.fields.serialNumber')}
            value={values.serial_number}
            onChange={(event) => setValue('serial_number', event.target.value)}
            error={fieldErrors.serial_number}
          />
          <Input
            label={t('form.fields.managementIp')}
            value={values.management_ip}
            onChange={(event) => setValue('management_ip', event.target.value)}
            error={fieldErrors.management_ip}
          />
          <Input
            label={t('form.fields.firmwareVersion')}
            value={values.firmware_version}
            onChange={(event) => setValue('firmware_version', event.target.value)}
            error={fieldErrors.firmware_version}
            className="sm:col-span-2"
          />
        </div>

        {typeFields.length > 0 ? (
          <div className="space-y-3 border-t border-gray-200 pt-4 dark:border-gray-700">
            <h3 className="text-sm font-semibold text-gray-700 dark:text-gray-200">
              {t('form.typeAttributes')}
            </h3>
            <DynamicForm
              fields={typeFields}
              values={attributes}
              errors={attributeFieldErrors}
              onChange={setAttribute}
            />
          </div>
        ) : null}
        {selectedType.isError ? (
          <p className="text-sm text-red-600 dark:text-red-400">{t('form.typeSchemaLoadFailed')}</p>
        ) : null}

        {bannerMessage ? (
          <p role="alert" className="text-sm text-red-600 dark:text-red-400">
            {bannerMessage}
          </p>
        ) : null}

        <div className="flex justify-end gap-3 pt-2">
          <Button variant="secondary" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" loading={isSubmitting}>
            {isEditMode ? t('common.save') : t('common.create')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
