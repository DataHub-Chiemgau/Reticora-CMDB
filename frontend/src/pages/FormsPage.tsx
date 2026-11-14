/**
 * FormsPage lists the active form definitions (WFL-01), renders the chosen
 * one for a submission (FormFill over FormRenderer: conditions after
 * MET-45, signatures) and shows its submissions grouped by schema version,
 * each with the schema of its own version (WP-1133). "Neu" and
 * "Bearbeiten" lead into the form editor, only with form:write.
 */
import { FileText, Pencil, Plus } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import {
  useAllFormSubmissions,
  useFormDefinitions,
  useFormVersions,
  useHasPermission,
  useSubmitForm,
} from '../api/hooks';
import type { FormDefinition, FormSubmission } from '../api/client';
import { FormFill, SubmissionView, parseForm } from '../components/form/FormRenderer';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
import { SkeletonList } from '../components/ui/Skeleton';
import { cn } from '../components/ui/utils';

export function FormsPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const canWrite = useHasPermission('form:write') === true;
  const forms = useFormDefinitions();
  const [selectedId, setSelectedId] = useState('');
  const selected = useMemo(
    () => forms.data?.data.find((form) => form.id === selectedId) ?? forms.data?.data[0],
    [forms.data, selectedId],
  );

  const header = (
    <PageHeader
      title={t('forms.title')}
      description={t('forms.subtitle')}
      actions={
        canWrite ? (
          <Button icon={<Plus />} onClick={() => void navigate('/forms/new')}>
            {t('forms.new')}
          </Button>
        ) : null
      }
    />
  );

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
          {selected ? <SelectedForm key={selected.id} form={selected} canWrite={canWrite} /> : null}
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

function SelectedForm({ form, canWrite }: { form: FormDefinition; canWrite: boolean }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const submit = useSubmitForm();
  const fields = useMemo(() => parseForm(form.schema, form.ui_hints), [form]);
  return (
    <div className="min-w-0 space-y-4">
      <Card
        title={`${t('forms.render')} · ${form.name}`}
        actions={
          canWrite ? (
            <Button
              size="sm"
              variant="secondary"
              icon={<Pencil />}
              onClick={() => void navigate(`/forms/${form.id}/edit`)}
            >
              {t('forms.edit')}
            </Button>
          ) : null
        }
      >
        <FormFill
          key={form.current_version_id ?? form.id}
          fields={fields}
          submitting={submit.isPending}
          // A client form has no link target here: an unlinked signature
          // document would be visible organization-wide.
          signatureUnavailable={!!form.client_id}
          onSubmit={(values) => submit.mutateAsync({ id: form.id, values })}
        />
      </Card>
      <Submissions form={form} />
    </div>
  );
}

/** Submissions lists the submissions of a form grouped by schema version, newest first. */
function Submissions({ form }: { form: FormDefinition }) {
  const { t, i18n } = useTranslation();
  const submissions = useAllFormSubmissions(form.id);
  const versions = useFormVersions(form.id);
  const fieldsOf = useMemo(() => {
    const byVersion = new Map<number, ReturnType<typeof parseForm>>();
    for (const v of versions.data ?? []) byVersion.set(v.version, parseForm(v.schema, v.ui_hints));
    return byVersion;
  }, [versions.data]);
  const groups = useMemo(() => {
    const map = new Map<number, FormSubmission[]>();
    for (const s of submissions.data ?? []) {
      const n = s.form_version ?? 0;
      map.set(n, [...(map.get(n) ?? []), s]);
    }
    return [...map.entries()].sort((a, b) => b[0] - a[0]);
  }, [submissions.data]);
  const current = parseForm(form.schema, form.ui_hints);

  return (
    <Card title={t('forms.submissions.title')}>
      {submissions.isLoading || versions.isLoading ? (
        <SkeletonList rows={2} label={t('forms.loading')} />
      ) : submissions.isError || versions.isError ? (
        <ErrorState
          title={t('forms.submissions.loadFailed')}
          retryLabel={t('common.retry')}
          onRetry={() => {
            void submissions.refetch();
            void versions.refetch();
          }}
        />
      ) : groups.length === 0 ? (
        <EmptyState title={t('forms.submissions.empty')} icon={<FileText />} className="py-6" />
      ) : (
        <div className="space-y-4">
          {groups.map(([version, rows]) => (
            <section
              key={version}
              aria-label={t('forms.submissions.version', { version })}
              data-testid={`submissions-v${version}`}
            >
              <h3 className="mb-2 text-sm font-semibold text-fg">
                {version
                  ? t('forms.submissions.version', { version })
                  : t('forms.submissions.noVersion')}
              </h3>
              <ul className="space-y-3">
                {rows.map((s) => (
                  <li key={s.id} className="rounded-lg border border-line p-3">
                    <p className="mb-1 text-xs text-fg-muted">
                      {new Date(s.created_at).toLocaleString(i18n.language)}
                    </p>
                    <SubmissionView fields={fieldsOf.get(version) ?? current} values={s.values} />
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      )}
    </Card>
  );
}
