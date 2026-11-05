import { FileText } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useFormDefinitions, useSubmitForm } from '../api/hooks';
import type { FormDefinition } from '../api/client';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { PageHeader } from '../components/ui/PageHeader';
import { SkeletonList } from '../components/ui/Skeleton';
import { cn } from '../components/ui/utils';

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

  const header = <PageHeader title={t('forms.title')} description={t('forms.subtitle')} />;

  if (forms.isLoading)
    return (
      <div className="space-y-4">
        {header}
        <SkeletonList rows={5} label={t('forms.loading')} />
      </div>
    );
  if (forms.isError)
    return (
      <div className="space-y-4">
        {header}
        <ErrorState
          title={t('forms.errorTitle')}
          description={forms.error.message}
          retryLabel={t('common.retry')}
          onRetry={() => forms.refetch()}
        />
      </div>
    );

  return (
    <div className="space-y-6">
      {header}
      {forms.data?.data.length ? (
        <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,20rem)_minmax(0,1fr)]">
          <section aria-labelledby="forms-definitions">
            <Card className="overflow-hidden p-0">
              <h2
                id="forms-definitions"
                className="border-b border-line px-4 py-3 text-base font-semibold text-fg"
              >
                {t('forms.definitions')}
              </h2>
              <ul className="divide-y divide-line">
                {forms.data.data.map((form) => (
                  <li key={form.id}>
                    <button
                      type="button"
                      aria-current={selected?.id === form.id ? 'true' : undefined}
                      className={cn(
                        'block w-full px-4 py-3 text-left text-sm transition-colors hover:bg-fg/[0.04] focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent',
                        selected?.id === form.id && 'bg-accent-soft hover:bg-accent-soft',
                      )}
                      onClick={() => setSelectedId(form.id)}
                    >
                      <span className="font-medium text-fg">{form.name}</span>
                      <span className="mt-0.5 block text-fg-muted">
                        {form.description || t('forms.noDescription')}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </Card>
          </section>
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
      ) : (
        <EmptyState
          icon={<FileText />}
          title={t('forms.emptyTitle')}
          description={t('forms.emptyHint')}
        />
      )}
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
    <div className="max-w-md space-y-4">
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
