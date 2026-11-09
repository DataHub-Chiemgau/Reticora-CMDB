import { Lock } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import type { AppPage } from '../components/CommandPalette';
import { lockedHint } from '../components/shell/navigation';
import { Button } from '../components/ui/Button';
import { EmptyState } from '../components/ui/EmptyState';
import { PageHeader } from '../components/ui/PageHeader';

/**
 * FeatureLockedPage stands in for a page whose feature the organization's
 * plan does not include (WP-857): it names the function and the plan
 * instead of letting the page run into 403 errors. A purchase or upgrade
 * path is not part of it.
 */
export function FeatureLockedPage({ page, plan }: { page: AppPage; plan?: string }) {
  const { t } = useTranslation();
  const name = t(`nav.${page}`);
  return (
    <div className="space-y-6">
      <PageHeader title={name} description={lockedHint(t, plan)} />
      <EmptyState
        icon={<Lock />}
        title={t('discoverability.locked.title', { name })}
        description={t('discoverability.locked.description')}
        action={
          <Button asChild variant="secondary" size="sm">
            <Link to="/dashboard">{t('discoverability.locked.back')}</Link>
          </Button>
        }
      />
    </div>
  );
}
