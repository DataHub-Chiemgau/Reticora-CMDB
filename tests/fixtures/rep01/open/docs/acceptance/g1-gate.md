# Gate G1 – Checkliste (Fixture „open“)

Fixture der Selbsttests von `tests/rep01-structure.test.sh` (WP-451): der
Übergangsstand nach E-76, GATE-01 bis GATE-03 offen. Die Inhalte sind
erfunden und belegen nichts; sie folgen nur dem Format von
`docs/acceptance/g1-gate.md`.

## Ergebnis

**Ergebnis: offen.** Ausstehend sind die Nachweise der Fixture.

## GATE-01 – Kein offenes Critical

**Status:** offen.

## GATE-02 – Alle Anforderungen der Phase sind PASS

**Status:** offen.

### N/P und WARN

| ID | Status | Begründung |
|---|---|---|
| GATE-04 | N/P | Gate-Regel für GA, nicht für G1 (Fixture). |

### Offen

| ID | Status | Begründung |
|---|---|---|
| ABN-01 | offen | Erklärung des Auftraggebers, Nachweis ausstehend (Fixture). |
| AUT-05 | offen | Erklärung des Auftraggebers, Nachweis ausstehend (Fixture). |
| AUT-06 | offen | Erklärung des Auftraggebers, Nachweis ausstehend (Fixture). |
| CI-13 | offen | Befund SR-11 ist offen (Fixture). |
| GATE-01 | offen | Pentest-Nachweis fehlt (Fixture). |
| GATE-02 | offen | Folgt aus den übrigen Zeilen (Fixture). |
| GATE-03 | offen | Pentest-Nachweis und Isolationstest fehlen (Fixture). |
| NET-04 | offen | Erklärung des Auftraggebers, Nachweis ausstehend (Fixture). |
| NFR-08 | offen | Erklärung des Auftraggebers, Nachweis ausstehend (Fixture). |
| NFR-09 | offen | Erklärung des Auftraggebers, Nachweis ausstehend (Fixture). |
| TEN-09 | offen | Der Rack-Befund fehlt im Review (Fixture). |

### Abgleich offener Befunde

| Befund | ID | Begründung |
|---|---|---|
| SR-08 | NFR-03 | Restrisiko zur Reserve; die Schwelle der Anforderung ist eingehalten (Fixture). |

## GATE-03 – Zusatzbedingungen G1

| Bedingung | Status | Nachweis |
|---|---|---|
| Lasttest (NFR-10) bestanden | PASS | Fixture |
| Pentest ohne offene High/Critical | offen | Erklärung des Auftraggebers, Nachweis ausstehend (Fixture) |
| Isolationstest (TEN-09) vollständig | offen | Fixture |
| Install-Smoke auf beiden Plattformen | PASS | Fixture |

**Status:** offen.

## GATE-05 – Kein Gate wird durch [O]-Punkte beeinflusst

**Status:** PASS.

## Anhang: Status je [B]/[Q]-ID

| ID | Tag | Status | Nachweis | WP |
|---|---|---|---|---|
| ABN-01 | [Q] | offen | Ausnahme (erklaerung) | WP-000 |
| AUT-05 | [B] | offen | Ausnahme (erklaerung) | WP-000 |
| AUT-06 | [B] | offen | Ausnahme (erklaerung) | WP-000 |
| CI-13 | [B] | offen | Fixture | WP-000 |
| GATE-01 | [Q] | offen | Fixture | WP-000 |
| GATE-02 | [Q] | offen | Ausnahme (erklaerung) | WP-000 |
| GATE-03 | [Q] | offen | Ausnahme (erklaerung) | WP-000 |
| GATE-04 | [Q], Bezug GA | N/P | Ausnahme (np) | WP-000 |
| GATE-05 | [Q] | PASS | Fixture | WP-000 |
| NET-04 | [B] | offen | Ausnahme (erklaerung) | WP-000 |
| NFR-03 | [B] | PASS | Fixture | WP-000 |
| NFR-08 | [B] | offen | Ausnahme (erklaerung) | WP-000 |
| NFR-09 | [B] | offen | Ausnahme (erklaerung) | WP-000 |
| TEN-09 | [Q] | offen | Fixture | WP-000 |
