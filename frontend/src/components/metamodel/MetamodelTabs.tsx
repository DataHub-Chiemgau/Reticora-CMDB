/**
 * MetamodelTabs are the tabs of the metamodel administration (UI-11,
 * WP-314) in the head of CITypeAdminPage and TemplatesPage: links with
 * aria-current, since components/ui has no tabs component. The menu entry
 * "CI types" stays active through its prefix match; there is no menu entry
 * of its own. "Templates" and "Drafts" (WP-799) belong to the module
 * metamodel (E-105) and are hidden while the module is off; an unknown
 * module counts as on.
 */
import { NavLink } from 'react-router';
import { useTranslation } from 'react-i18next';
import { useModules } from '../../hooks/useEntitlements';
import { cn } from '../ui/utils';

const tabClass = ({ isActive }: { isActive: boolean }) =>
  cn(
    '-mb-px shrink-0 whitespace-nowrap border-b-2 px-3.5 py-2.5 text-sm font-medium transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent',
    isActive ? 'border-accent text-fg' : 'border-transparent text-fg-muted hover:text-fg',
  );

export function MetamodelTabs() {
  const { t } = useTranslation();
  const modules = useModules();
  const templatesOn = modules.isModuleEnabled('metamodel');
  return (
    <nav
      aria-label={t('templates.tabs.label')}
      className="flex gap-1 overflow-x-auto border-b border-line"
    >
      <NavLink to="/ci-types" end className={tabClass}>
        {t('templates.tabs.types')}
      </NavLink>
      {templatesOn ? (
        <>
          <NavLink to="/ci-types/templates" className={tabClass}>
            {t('templates.tabs.templates')}
          </NavLink>
          <NavLink to="/ci-types/drafts" className={tabClass}>
            {t('templates.tabs.drafts')}
          </NavLink>
        </>
      ) : null}
    </nav>
  );
}
