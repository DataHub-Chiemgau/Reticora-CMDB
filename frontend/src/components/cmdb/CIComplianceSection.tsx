import { useTranslation } from 'react-i18next';
import { Badge } from '../ui/Badge';
import { CIP3Section, severityVariant } from './CIFindingsSection';

/** The order of the result states: failed rules first (WP-1137 AK4). */
const STATUS_ORDER: Record<string, number> = { fail: 0, pass: 1, not_applicable: 2 };

/**
 * CIComplianceSection shows the compliance score of the CI and its result
 * per rule (SEC-03, WP-1137 AK4): status, severity and the time of the last
 * change of the status, the failed rules first.
 */
export function CIComplianceSection({ ciId }: { ciId: string }) {
  const { t, i18n } = useTranslation();
  const time = (value: string) =>
    new Date(value).toLocaleString(i18n.language, { dateStyle: 'medium', timeStyle: 'short' });
  return (
    <CIP3Section
      ciId={ciId}
      module="compliance"
      field="complianceResults"
      title={t('ciP3.compliance.title')}
      summary={(ci) =>
        ci.complianceScore ? (
          <Badge variant={ci.complianceScore.failed > 0 ? 'warning' : 'success'}>
            {`${Math.round(ci.complianceScore.score)}%`}
          </Badge>
        ) : null
      }
    >
      {(ci) => {
        const results = [...ci.complianceResults].sort(
          (a, b) =>
            (STATUS_ORDER[a.status] ?? 3) - (STATUS_ORDER[b.status] ?? 3) ||
            a.ruleName.localeCompare(b.ruleName),
        );
        return (
          <div className="space-y-3">
            {ci.complianceScore ? (
              <p className="text-sm text-fg">
                {t('ciP3.compliance.score', {
                  score: Math.round(ci.complianceScore.score),
                  passed: ci.complianceScore.passed,
                  failed: ci.complianceScore.failed,
                })}
              </p>
            ) : null}
            {results.length === 0 ? (
              <p className="text-sm text-fg-muted">{t('ciP3.compliance.empty')}</p>
            ) : (
              <ul className="space-y-2" data-testid="compliance-results">
                {results.map((r) => (
                  <li key={r.ruleId} className="flex flex-wrap items-center gap-2 text-sm">
                    <Badge
                      variant={
                        r.status === 'fail' ? 'danger' : r.status === 'pass' ? 'success' : 'neutral'
                      }
                    >
                      {t(`compliance.resultStatuses.${r.status}`, r.status)}
                    </Badge>
                    <span className="font-medium text-fg">{r.ruleName}</span>
                    <Badge variant={severityVariant(r.severity)}>
                      {t(`compliance.severities.${r.severity}`, r.severity)}
                    </Badge>
                    <span className="text-xs text-fg-muted">
                      {t('ciP3.compliance.changedAt', { time: time(r.statusChangedAt) })}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        );
      }}
    </CIP3Section>
  );
}
