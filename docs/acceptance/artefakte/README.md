# Eingefrorene Abnahme-Artefakte

Eingefrorener Stand der Abnahmeszenarien ABN-01 (d) und (e) für G1 (WP-212).
Das Protokoll steht in `docs/acceptance/abn-01.md`.

| Datei | Inhalt |
|---|---|
| `abn-01-e-konflikt.json` | Daten des Konflikt-Szenarios (e). `frontend/e2e/conflict.spec.ts` liest sie direkt. |
| `SHA256SUMS` | Prüfsummen des eingefrorenen Stands: Szenario-Tests, Onboarding-Fixture und Daten. Die Pfade sind relativ zum Repository. |

`tests/rep01-structure.test.sh` prüft die Prüfsummen. Ändert sich ein
eingefrorenes Szenario, scheitert der Test. Eine gewollte Änderung braucht eine
neue Abnahme: Protokoll aktualisieren, Prüfsummen neu erzeugen und beides im
selben PR begründen.

```sh
sha256sum frontend/e2e/onboarding.spec.ts frontend/e2e/conflict.spec.ts \
  frontend/e2e/fixtures/setup-onboarding.sh \
  docs/acceptance/artefakte/abn-01-e-konflikt.json > docs/acceptance/artefakte/SHA256SUMS
```

Die Originalszenarien (a) und (b) kommen nach E-02 als
`abn-01-a-cmdb.md` und `abn-01-b-system.md` hinzu.
