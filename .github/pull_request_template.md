<!--
Ein PR = ein Arbeitspaket (DOD-01, 0,5–2 PT). Die CI liest die WP-Nummern aus
dem PR-Titel und aus der Zeile „Arbeitspakete:“ und verlangt alle
Traceability-Zeilen, die docs/plan/implementierungsplan.md für sie vorsieht.
-->

**Arbeitspakete:** WP-nnn

## Zusammenfassung

<!-- Was ändert sich und warum? Befundquelle und Anforderungs-IDs nennen. -->

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
- [ ] **WP-Nummer:** in Titel und Zeile „Arbeitspakete“ angegeben; Voraussetzungen sind gemergt
