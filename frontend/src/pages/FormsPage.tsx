import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useFormDefinitions, useSubmitForm } from '../api/hooks';
import type { FormDefinition } from '../api/client';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { SkeletonList } from '../components/ui/Skeleton';

type JsonSchema = {
  properties?: Record<string, { type?: string; title?: string; enum?: string[] }>;
  required?: string[];
};

export function FormsPage() {
  const { t } = useTranslation();
  const forms = useFormDefinitions();
  const submit = useSubmitForm();
  const [selectedId, setSelectedId] = useState('');
  const [values, setValues] = useState<Record<string, unknown>>({});
  const selected = useMemo(
    () => forms.data?.data.find((form) => form.id === selectedId) ?? forms.data?.data[0],
    [forms.data, selectedId],
  );

  if (forms.isLoading) return <SkeletonList rows={5} label={t('forms.loading')} />;
  if (forms.isError)
    return (
      <ErrorState
        title={t('forms.errorTitle')}
        description={forms.error.message}
        retryLabel={t('common.retry')}
        onRetry={() => forms.refetch()}
      />
    );

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">{t('forms.title')}</h1>
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('forms.subtitle')}</p>
      </div>
      <Card title={t('forms.definitions')}>
        {forms.data?.data.length ? (
          <div className="grid gap-3 md:grid-cols-2">
            {forms.data.data.map((form) => (
              <button
                key={form.id}
                className="rounded-lg border border-gray-200 p-3 text-left text-sm hover:border-primary dark:border-gray-800"
                onClick={() => setSelectedId(form.id)}
              >
                <span className="font-medium">{form.name}</span>
                <p className="text-gray-500">{form.description || t('forms.noDescription')}</p>
              </button>
            ))}
          </div>
        ) : (
          <EmptyState title={t('forms.emptyTitle')} description={t('forms.emptyHint')} />
        )}
      </Card>
      {selected ? (
        <Card title={`${t('forms.render')} · ${selected.name}`}>
          <FormRenderer
            form={selected}
            values={values}
            setValues={setValues}
            onSubmit={() =>
              submit.mutate({ id: selected.id, values }, { onSuccess: () => setValues({}) })
            }
            submitting={submit.isPending}
          />
        </Card>
      ) : null}
    </div>
  );
}

function FormRenderer({
  form,
  values,
  setValues,
  onSubmit,
  submitting,
}: {
  form: FormDefinition;
  values: Record<string, unknown>;
  setValues: (v: Record<string, unknown>) => void;
  onSubmit: () => void;
  submitting: boolean;
}) {
  const { t } = useTranslation();
  const schema = form.schema as JsonSchema;
  const properties = Object.entries(schema.properties ?? {});
  return (
    <div className="space-y-4">
      {properties.map(([name, prop]) => (
        <Input
          key={name}
          label={prop.title || name}
          type={prop.type === 'number' || prop.type === 'integer' ? 'number' : 'text'}
          required={schema.required?.includes(name)}
          value={String(values[name] ?? '')}
          onChange={(event) =>
            setValues({
              ...values,
              [name]:
                prop.type === 'number' || prop.type === 'integer'
                  ? Number(event.target.value)
                  : event.target.value,
            })
          }
        />
      ))}
      <Button onClick={onSubmit} loading={submitting}>
        {t('forms.submit')}
      </Button>
    </div>
  );
}
