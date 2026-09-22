## Zusammenfassung

Prüfstand 2026-09-22; Dokumentationsbasis `914ccf33e5b5b86684781044f4c19f588fe7f97a`, Produktcode unverändert. Der ausdrücklich bestätigte vollständige Auftrag wird unverändert als Prüfmaßstab verwendet; kein zusätzlicher Katalogtext wird unterstellt.

Zwischenstand: Kontakte/CI-Kontakte inventarisiert; Audit-/PII-/Sicherheitsgegenprüfung und Testauswertung laufen. Nur dieser Bericht wird angelegt, keine Produktkorrekturen.

## Ergebnis je Anforderung

**Umfang:** Die 16 ausdrücklich genannten IDs AUD-01/02/03/04/05/07/09, SEC-01/05/06/07/08/10/11, EVT-01/02; CH16 als gemeinsame Grundlage zu SEC-05. Nicht genannte IDs werden nicht ergänzt. Zusammen genannte Anforderungen ohne Einzelsatzzuordnung bleiben gemeinsame Prüfblöcke: AUD-01/02, AUD-05/07/09, EVT-01/02; insgesamt zwölf Bewertungszeilen, keine erfundene Unterteilung.

**Bewertung:** Schema aus `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/README.md:13–15`. Tags/Phasen sind im aktuellen Auftrag nicht angegeben; SEC-05 ist durch CH16 ausdrücklich Phase1, ansonsten keine erfundenen Gatezuordnungen. „FAIL“ bezeichnet einen vollständig fehlenden ausdrücklich beauftragten Schutz/Nachweis, nicht automatisch ein behauptetes Phasen-Gate. Nicht gelieferte exakte Kontaktrollen, change_kind-Sollmenge, Ausschlussfeldliste und Retention-Fristen werden als N/P-Teilprüfung markiert; belegte Abweichungen von expliziten Kriterien bleiben bewertbar. Aufwand S ≤0,5 Tag, M 0,5–2 Tage, L >2 Tage/aufzuteilen.

| ID | Tag/Phase | Status | Evidenz | Tests | Befund | Severity | Aufwand |
|---|---|---|---|---|---|---|---|
| AUD-01 / AUD-02 | nicht angegeben | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000016_contacts.up.sql:4–26`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/contact/model.go:7–19,46–69`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/contact/pg_repository.go:45–56,109–151` | Vier Contact-Handler-/Memorytests; keine PII-Vault-Roundtripprüfung. | contact/ci_contact und validierte Linkrollen vorhanden; Name/E-Mail/Telefon stehen unmittelbar in contact statt ausschließlich im PII-Vault. Exakter Sollrollenabgleich mangels gelieferter Rollenliste N/P, Istmenge dokumentiert. | High | L |

### Tests und CI

Bestehende gezielte Backendtests/Build ohne Dependencyinstallation beauftragt; Ergebnisse werden nachgetragen. Kontakt-Tests sind `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/contact/handler_test.go:38–132`: TestContactCRUD, TestContactUnauthorized, TestContactTenantIsolation, TestCIContactLinkUnlink. Sie prüfen Memory-CRUD, Tenantkontext und Linkvalidierung, nicht DB-PII-Ablage oder Vaultlöschung.

Erneut über GitHub Actions abgefragt: [CI 35731844252](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/35731844252), Commit `b0f0ee2302e3af5afa41fbeaf738687fe1492504`, insgesamt **failure**. Jobs backend und k8s-manifests success; migrations, lint-backend und frontend failure; e2e skipped. Im Migrationsjob up/down/up erfolgreich, PostgreSQL-Integration fehlgeschlagen, anschließende RLS-Assertions übersprungen. Erfolgreiche Cloud-Agent-Läufe sind keine Produkt-CI- oder Scan-Nachweise; keine neue CI ausgelöst.

## Befunde im Detail

### AUD-01/AUD-02 — High: Personenwerte unmittelbar in contact

**Beschreibung/Dateien:** Schema `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000016_contacts.up.sql:4–26` enthält display_name, email, phone als Text, role ebenfalls freier Text. Repository liest/schreibt diese Werte direkt, ohne Surrogat-/Vaultauflösung (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/contact/pg_repository.go:45–56,109–151`); Handler übernimmt sie aus Create/Update (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/contact/handler.go:61–94`).

**Istrollen exakt:** ci_contact.relationship_type erlaubt **responsible, owner, operator, vendor, escalation**, Default **responsible**. SQL-CHECK und Go-ValidRelationshipTypes stimmen überein; UNIQUE(ci_id,contact_id,relationship_type) vorhanden. contact.role selbst besitzt keinen solchen Enum-/CHECK-Vertrag. Quellen: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000016_contacts.up.sql:11,23–26`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/contact/model.go:62–69`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/contact/handler.go:175–194`. Ohne Sollrollenliste werden weder fehlende noch überzählige Rollen erfunden.

CI-Link speichert IDs statt kopierter Personenwerte; dies ist positiv, heilt aber nicht den Klartextkontakt außerhalb des geforderten Vaults. Kontakt-Klartext allein wird hier High bewertet; unveränderliche Klartext-Auditdaten sind gesondert nach CH16/SEC-05 Critical zu beurteilen, sofern konkreter Schreibpfad belegt.

**Empfohlene Korrektur, nicht umgesetzt:** Personenwerte ausschließlich in den PII-Vault verlagern, Kontakt-/CI-Linkmodelle mit Surrogatreferenzen führen und autorisierte Auflösung/Löschung mit Persistenztests prüfen. Exakte Sollrollen vor einer Konformitätsbehauptung festlegen; bestehende Istrollen nicht ohne Anforderungsgrundlage austauschen.

## Offene Fragen

- Bestätigter Auftrag bleibt vollständig; dennoch nennt er die „exakten“ Rollen/change_kind-Werte und „genannten“ Retention-Fristen nicht als Wertelisten. Ihr exakter Sollvergleich ist daher N/P, keine Annahme eines fehlenden Promptteils.
- AUD-05/07/09 sind gemeinsam beschrieben; dieser Bericht bewertet den gemeinsamen Block, ohne die einzelnen Sätze willkürlich einer ID zuzuweisen.
- EVT-01/02 nennt wörtlich RETICORA_EVENTS und „Subjects events.“. Prüfbar sind Streamname und events.-Namensraum; vollständiges Subjectmuster/Tokenlayout, Zustellgarantie und Retention werden nicht ergänzt.
- Allgemeine Phasentags fehlen im aktuellen Auftrag; keine Status-/Gatezuordnung allein aus der Nummer ableiten. CH16/SEC-05 hat belegten Phase1-Bezug in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/katalog-v3/00-grundlagen.md`.

## Stand

UNVOLLSTÄNDIG – fortsetzen ab ID AUD-03.
