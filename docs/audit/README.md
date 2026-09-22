# Audit gegen Anforderungskatalog v3

## Zweck und Arbeitsweise

Dieses Audit prüft Reticora (CMDB-/Asset-Plattform) gegen den Anforderungskatalog v3. Teil 0 sichert die gemeinsamen Grundlagen und dokumentiert den belegbaren Repository- und Schema-Iststand; er ersetzt keine Einzelprüfung oder Gate-Abnahme. Teile 1–9 prüfen die Fachbereiche in eigenen Sessions, Teil 10 konsolidiert die Befunde.

Die unveränderten GRUNDLAGEN stehen in `docs/spec/katalog-v3/00-grundlagen.md`. Darin nur referenzierte v2-Anforderungen werden nicht rekonstruiert: fehlende Ausgangsdokumente sind in `docs/audit/00-bestandsaufnahme.md` ausgewiesen. Nachlieferungen des Katalogs sind Voraussetzung für eine vollständige Wortlautprüfung.

Jede Bewertung benötigt Anforderungs-ID, Phase/Gate, konkrete Pfad-/Symbolbelege und Testbelege beziehungsweise eine benannte Prüflücke. Vorhandener Code allein ist kein PASS; eine fehlende Testumgebung ist kein Beweis für fehlerhafte Fachlogik. Schemaaussagen in `docs/audit/00-schema-ist.md` beruhen ausschließlich auf Migrationen, nicht auf einem geprüften Live-Schema. Produktivcode, Migrationen und Tests bleiben in Teil 0 unverändert.

## Bewertungsschema

Status: PASS (vollständig, korrekt, durch Test belegt) | PARTIAL (teilweise umgesetzt oder ohne Test) | ABWEICHEND (umgesetzt, widerspricht aber Wortlaut oder Entscheidung CH8–CH30) | FAIL (fehlt, obwohl die Phase fällig ist) | OFFEN (spätere Phase, noch kein Code) | N/P (nicht prüfbar, Grund angeben)
Severity je Befund: Critical (Mandanten-/Scope-Isolation, stiller Datenverlust, Secrets/Krypto, Auth-Bypass; vgl. TEN-09, OVR-01) | High (Kernfunktion falsch oder fehlend, blockiert ein Gate) | Medium | Low
Aufwand je Befund: S (≤ 0,5 Tag) | M (0,5–2 Tage) | L (&gt; 2 Tage, muss für die Umsetzung aufgeteilt werden)

## Geplante Berichte

- `docs/audit/00-bestandsaufnahme.md` und `docs/audit/00-schema-ist.md` – dieser Teil
- `docs/audit/01-installation-stack.md` – Technologie-Stack, Monorepo, Installation & Betrieb
- `docs/audit/02-mandanten-auth-entitlements.md` – Mandanten/Scopes/RLS, Authentifizierung, Autorisierung, Entitlements
- `docs/audit/03-datenmodell-standorte-metamodell.md` – Datenbank-Konventionen, Standorte & Lagerorte, Metamodell
- `docs/audit/04-ci-netz-rack-beziehungen-impact.md` – CI-Kern, Lifecycle & Health, Netzwerk/IPAM, Rack, Beziehungen, Topologie & Impact
- `docs/audit/05-collector-discovery-reconciliation.md` – Collector, Discovery, Ingest/Identity-Resolution/Reconciliation, Overrides
- `docs/audit/06-audit-sicherheit-events.md` – Kontakte/Historie/Audit, Sicherheit & DSGVO, Events & Webhooks
- `docs/audit/07-api-suche-jobs-lebenszyklus.md` – REST-API, GraphQL-BFF, Suche/Views/Bulk/Export, Mandantenlebenszyklus, Benachrichtigungen, Jobs
- `docs/audit/08-frontend-monitoring-nfr-tests.md` – Monitoring-Baseline, Frontend & UX, NFR, Seed/Simulation/Tests/DoD/Gates, Abnahme, Traceability
- `docs/audit/09-module-phase2plus.md` – Module ab Phase 2 und Add-ons (Asset, Lager, Stocktake, Management, Tickets, Agent, Workflows, IGA, KI)
- `docs/audit/99-gesamtbericht.md` und `docs/audit/befunde.csv` – Konsolidierung (Teil 10)

## Stand

Teil 0: siehe `docs/audit/00-bestandsaufnahme.md` und `docs/audit/00-schema-ist.md`. Die oben genannten Folgeberichte sind geplant, nicht durch diesen Teil angelegt oder geprüft.

Pfadangaben in diesen Dokumenten sind repository-relativ zur Prüfkopie `/home/runner/work/Reticora-CMDB/Reticora-CMDB/`, sofern nicht ausdrücklich absolut angegeben.
