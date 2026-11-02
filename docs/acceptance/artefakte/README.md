# Eingefrorene Abnahme-Artefakte

Eingefrorener Stand der Abnahmeszenarien ABN-01 (d) und (e) für G1 (WP-212).
Das Protokoll steht in `docs/acceptance/abn-01.md`.

| Datei | Inhalt |
|---|---|
| `abn-01-e-konflikt.json` | Daten des Konflikt-Szenarios (e). `frontend/e2e-real/abn-01-konflikt.spec.ts` spielt sie gegen den echten Stack ein und schreibt sie aus dem Lauf neu (unten); `frontend/e2e/conflict.spec.ts` liest sie direkt. |
| `SHA256SUMS` | Prüfsummen des eingefrorenen Stands: Szenario-Tests (Onboarding, Konflikt gemockt und im Real-Stack), Onboarding-Fixture und Daten. Die Pfade sind relativ zum Repository. |

`tests/rep01-structure.test.sh` prüft die Prüfsummen und dass jede dieser
Dateien in `SHA256SUMS` steht. Ändert sich ein eingefrorenes Szenario,
scheitert der Test. Eine gewollte Änderung braucht eine neue Abnahme:
Protokoll mit datiertem Vermerk aktualisieren, Prüfsummen neu erzeugen und
beides im selben PR begründen.

```sh
sha256sum frontend/e2e/onboarding.spec.ts frontend/e2e/conflict.spec.ts \
  frontend/e2e-real/abn-01-konflikt.spec.ts \
  frontend/e2e/fixtures/setup-onboarding.sh \
  docs/acceptance/artefakte/abn-01-e-konflikt.json > docs/acceptance/artefakte/SHA256SUMS
```

## `abn-01-e-konflikt.json` aus dem Real-Lauf

`frontend/e2e-real/abn-01-konflikt.spec.ts` läuft im Workflow
`install-smoke.yml`, Job `install-cloud-smoke (ubuntu-24.04)`, Schritt „Run
the real-stack E2E suite against the expected-failure list“ (WP-338). Nach
einem grünen Lauf schreibt die Spec die Datei aus dem neu, was der Lauf über
die API gelesen hat: IDs von CI, CI-Typ, Organisation und Reviews, deren
Zeitpunkte und unter `lauf` Workflow, Job, Run-ID und Commit. Die
Szenariowerte (Namen, Felder, Werte, Aktionen) muss der Lauf unverändert
reproduzieren, sonst ist der Test rot. Die Datei liegt im Artefakt `e2e-real`
des Laufs im Testverzeichnis der Spec unter `frontend/test-results/`
(`abn-01-konflikt-…/abn-01-e-konflikt.json`) und hängt am Playwright-Bericht
(`e2e-real.json`).

Übernahme in das Repository:

1. Artefakt `e2e-real` des grünen Laufs laden und die Datei nach
   `docs/acceptance/artefakte/abn-01-e-konflikt.json` kopieren.
2. Prüfsummen mit dem Befehl oben neu erzeugen;
   `sha256sum -c docs/acceptance/artefakte/SHA256SUMS` meldet OK.
3. In `docs/acceptance/abn-01.md` (e) Run-ID und Datum des Laufs eintragen
   (datierter Vermerk).

Die Originalszenarien (a) und (b) kommen nach E-02 als
`abn-01-a-cmdb.md` und `abn-01-b-system.md` hinzu.
