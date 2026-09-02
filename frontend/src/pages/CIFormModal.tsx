import { useEffect, useMemo, useState } from 'react';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError } from '../api/client';
import type { CI, CICreateRequest, CIUpdateRequest } from '../api/client';
import { useCreateCI, useUpdateCI } from '../api/hooks';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';

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

const ciTypeValues = ['server', 'switch', 'router', 'firewall', 'pdu', 'ups', 'nas', 'client'];
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

function optionalValue(value: string) {
  const trimmed = value.trim();
  return trimmed ? trimmed : undefined;
}

export function CIFormModal({ open, onOpenChange, ci, onSuccess }: CIFormModalProps) {
  const { t } = useTranslation();
  const createMutation = useCreateCI();
  const updateMutation = useUpdateCI();
  const isEditMode = Boolean(ci);
  const [values, setValues] = useState<CIFormValues>(() => toFormValues(ci));
  const [errors, setErrors] = useState<Partial<Record<keyof CIFormValues, string>>>({});

  useEffect(() => {
    if (open) {
      setValues(toFormValues(ci));
      setErrors({});
    }
  }, [ci, open]);

  const typeOptions = useMemo(
    () => [
      { value: '', label: t('form.selectPlaceholder') },
      ...ciTypeValues.map((value) => ({
        value,
        label: value.charAt(0).toUpperCase() + value.slice(1),
      })),
    ],
    [t],
  );

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
    serverError instanceof ApiError ? serverError.violationsByField() : ({} as Record<string, string>);

  const fieldErrors: Partial<Record<keyof CIFormValues, string>> = { ...errors };
  const unmappedViolations: string[] = [];
  for (const [field, detail] of Object.entries(serverViolations)) {
    if (field in values) {
      const key = field as keyof CIFormValues;
      if (!fieldErrors[key]) fieldErrors[key] = detail;
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

  function validate() {
    const nextErrors: Partial<Record<keyof CIFormValues, string>> = {};

    if (!values.name.trim()) {
      nextErrors.name = t('form.validation.nameRequired');
    }

    if (!values.ci_type_id.trim()) {
      nextErrors.ci_type_id = t('form.validation.ciTypeRequired');
    }

    setErrors(nextErrors);
    return Object.keys(nextErrors).length === 0;
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
      discovery_source: 'manual',
    };

    const updatePayload: CIUpdateRequest = {
      name: values.name.trim(),
      status: values.status,
      manufacturer: optionalValue(values.manufacturer),
      model: optionalValue(values.model),
      serial_number: optionalValue(values.serial_number),
      management_ip: optionalValue(values.management_ip),
      firmware_version: optionalValue(values.firmware_version),
    };

    try {
      const saved =
        isEditMode && ci
          ? await updateMutation.mutateAsync({ id: ci.id, data: updatePayload })
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
          onChange={(event) => setValue('ci_type_id', event.target.value)}
          options={typeOptions}
          error={fieldErrors.ci_type_id}
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
