# Gate G1 – Checkliste (Fixture „final“)

Fixture der Selbsttests von `tests/rep01-structure.test.sh` (WP-451): ein
vollständiges Gate, jede GATE-Zeile PASS. Die Inhalte sind erfunden und
belegen nichts; sie folgen nur dem Format von `docs/acceptance/g1-gate.md`.

## Ergebnis

**Ergebnis: bestanden.**

## GATE-01 – Kein offenes Critical

**Status:** PASS.

## GATE-02 – Alle Anforderungen der Phase sind PASS

**Status:** PASS.

### N/P und WARN

| ID | Status | Begründung |
|---|---|---|
| GATE-04 | N/P | Gate-Regel für GA, nicht für G1 (Fixture). |

### Abgleich offener Befunde

| Befund | ID | Begründung |
|---|---|---|
| SR-08 | NFR-03 | Restrisiko zur Reserve; die Schwelle der Anforderung ist eingehalten (Fixture). |

### Abgleich v2-Texte

| ID | Textbestandteil | Test | Ergebnis |
|---|---|---|---|
| AUT-05 | Fixture | `tests/v2-abgleich.test.sh` | bestanden |
| AUT-06 | Fixture | `tests/v2-abgleich.test.sh` | bestanden |
| NET-04 | Fixture | `tests/v2-abgleich.test.sh` | bestanden |

## GATE-03 – Zusatzbedingungen G1

| Bedingung | Status | Nachweis |
|---|---|---|
| Lasttest (NFR-10) bestanden | PASS | Fixture |
| Pentest ohne offene High/Critical | PASS | `docs/security/pentest-g1.md` |
| Isolationstest (TEN-09) vollständig | PASS | Fixture |
| Install-Smoke auf beiden Plattformen | PASS | Fixture |

**Status:** PASS.

## GATE-05 – Kein Gate wird durch [O]-Punkte beeinflusst

**Status:** PASS.

## Anhang: Status je [B]/[Q]-ID

| ID | Tag | Status | Nachweis | WP |
|---|---|---|---|---|
| ABN-01 | [Q] | PASS | `docs/acceptance/abn-01-a-cmdb.md`, `docs/acceptance/abn-01-b-system.md` | WP-000 |
| AUT-05 | [B] | PASS | `tests/v2-abgleich.test.sh` | WP-000 |
| AUT-06 | [B] | PASS | `tests/v2-abgleich.test.sh` | WP-000 |
| CI-13 | [B] | PASS | Fixture | WP-000 |
| GATE-01 | [Q] | PASS | Fixture | WP-000 |
| GATE-02 | [Q] | PASS | Fixture | WP-000 |
| GATE-03 | [Q] | PASS | Fixture | WP-000 |
| GATE-04 | [Q], Bezug GA | N/P | Ausnahme (np) | WP-000 |
| GATE-05 | [Q] | PASS | Fixture | WP-000 |
| NET-04 | [B] | PASS | `tests/v2-abgleich.test.sh` | WP-000 |
| NFR-03 | [B] | PASS | Fixture | WP-000 |
| NFR-08 | [B] | PASS | `docs/security/pentest-g1.md` | WP-000 |
| NFR-09 | [B] | PASS | `docs/legal/subprozessoren.md` | WP-000 |
| TEN-09 | [Q] | PASS | Fixture | WP-000 |
