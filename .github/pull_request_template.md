<!--
Ein PR = ein Arbeitspaket (DOD-01, 0,5–2 PT). Die CI liest die WP-Nummern aus
dem PR-Titel und aus der Zeile „Arbeitspakete:“ und verlangt alle
Traceability-Zeilen, die der Plan des WPs für sie vorsieht:
docs/plan/implementierungsplan.md, der Korrekturplan
docs/plan/g1-korrektur/implementierungsplan.md (WP-336 ff.) oder der
Phase-2-Plan docs/plan/phase2/implementierungsplan.md.
Die Zeile „Arbeitspakete:“ enthält nur WP-Nummern oder das Wort „keine“;
jede weitere WP-Nummer auf dieser Zeile wird als Arbeitspaket des PRs
gewertet. Reine Planungs- und Dokumentations-PRs ohne Arbeitspaket schreiben
„Arbeitspakete: keine“ und tragen keine WP-Nummer im Titel; fehlt die Zeile
oder nennt ein Planungs-PR trotzdem eine WP-Nummer, scheitert die Prüfung.
Die nach E-03 verschobenen Nummern WP-215 bis WP-239 dürfen weder im Titel
noch in der Zeile „Arbeitspakete:“ stehen (E-46).
Sammel-PRs: je ein Commit pro WP mit Präfix „WP-nnn:“. Ein Bereich in
„Arbeitspakete:“ (z. B. „WP-243 – WP-246“) umfasst nur WPs dieses PRs, weil
die CI ihn expandiert.
-->

**Arbeitspakete:** WP-nnn

## Zusammenfassung

<!-- Was ändert sich und warum? Befundquelle und Anforderungs-IDs nennen.
Korrektur-WPs (docs/plan/g1-korrektur/) nennen die behobenen Befund-IDs (F-nnn
aus audit/phase1-final/PHASE1_FINDINGS.md) und belegen deren Retest. -->

## Definition of Done (DOD-01)

Nicht zutreffende Punkte bitte mit Begründung als „entfällt“ markieren.

- [ ] **OpenAPI:** neue oder geänderte Routen zuerst in `api/openapi.yaml`; `TestRoutesAndSpecificationAreInParity` grün; `npm run generate:api` ausgeführt
- [ ] **Implementierung:** vollständig für die Anforderungen des WPs; Repository-Fehler über `api.WriteRepoError`
- [ ] **RLS:** neue Mandantentabellen mit ENABLE + FORCE RLS und Policies (USING und WITH CHECK); RLS-Katalogtest ohne neue Ausnahme
- [ ] **Audit/Events:** schreibende Pfade erzeugen Audit-Einträge und Events
- [ ] **i18n:** neue UI-Texte in allen Sprachdateien, keine hartkodierten Texte
- [ ] **Tests:** Unit-/Integrationstests ergänzt; Migrationen mit `.up.sql` und `.down.sql`, Roundtrip-Test grün
- [ ] **Lint:** `make lint` bzw. CI-Lint ohne Befunde
- [ ] **Dokumentation:** betroffene Doku aktualisiert (u. a. `docs/schema-baseline.md` bei Schemaänderungen)
- [ ] **Traceability-Eintrag:** Zeilen des WPs in `docs/traceability.csv` mit tatsächlich angelegter Testdatei
- [ ] **WP-Nummer:** in Titel und Zeile „Arbeitspakete“ angegeben; Voraussetzungen sind gemergt (Planungs-/Dokumentations-PR ohne WP: „Arbeitspakete: keine“)
