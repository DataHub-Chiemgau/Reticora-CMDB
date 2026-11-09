# Gate G1 – Checkliste (Fixture „declared“)

Fixture der Selbsttests von `tests/rep01-structure.test.sh` (WP-831): das
Ergebnis „bedingt bestanden“ nach E-76 Option 2 mit dem Beschluss nach E-103.
Die erklärten IDs stehen ohne Frist in der Ausnahmeliste, ihre Spalte
`entscheidung` nennt E-103 und ihre Spalte `lieferung` lautet „entfällt
(E-103)“. Jede andere Bedingung von „bestanden“ ist erfüllt. Die Inhalte sind
erfunden und belegen nichts; sie folgen nur dem Format von
`docs/acceptance/g1-gate.md`.

## Ergebnis

**Ergebnis: bedingt bestanden.** Die erklärten IDs sind durch die Erklärung
unten nachgewiesen (E-103); nichts wird nachgereicht.

### Freigabe nach E-76

Erklärung des Auftraggebers vom 2000-01-01: „Fixture-Erklärung.“ Beschluss
des Auftraggebers vom 2000-01-02: Die Erklärung gilt als Nachweis für G1 und
G2 (E-103). Die Freigabe gilt für AUT-05, AUT-06, NET-04, NFR-08, NFR-09,
ABN-01, GATE-03 (Pentest-Anteil) und GATE-02. Eine Lieferungstabelle gibt es
nicht.

## GATE-01 – Kein offenes Critical

**Status:** PASS.

## GATE-02 – Alle Anforderungen der Phase sind PASS

**Status:** offen.

### N/P und WARN

| ID | Status | Begründung |
|---|---|---|
| GATE-04 | N/P | Gate-Regel für GA, nicht für G1 (Fixture). |

### Offen: Erklärung des Auftraggebers als Nachweis (E-103)

| ID | Status | Begründung |
|---|---|---|
| ABN-01 | offen | Erklärung des Auftraggebers als Nachweis für G1 und G2 (E-103, Fixture). |
| AUT-05 | offen | Erklärung des Auftraggebers als Nachweis für G1 und G2 (E-103, Fixture). |
| AUT-06 | offen | Erklärung des Auftraggebers als Nachweis für G1 und G2 (E-103, Fixture). |
| GATE-02 | offen | Folgt aus den übrigen Zeilen (Fixture). |
| GATE-03 | offen | Pentest-Anteil: Erklärung des Auftraggebers als Nachweis für G1 und G2 (E-103, Fixture). |
| NET-04 | offen | Erklärung des Auftraggebers als Nachweis für G1 und G2 (E-103, Fixture). |
| NFR-08 | offen | Erklärung des Auftraggebers als Nachweis für G1 und G2 (E-103, Fixture). |
| NFR-09 | offen | Erklärung des Auftraggebers als Nachweis für G1 und G2 (E-103, Fixture). |

### Abgleich offener Befunde

| Befund | ID | Begründung |
|---|---|---|
| SR-08 | NFR-03 | Restrisiko zur Reserve; die Schwelle der Anforderung ist eingehalten (Fixture). |

## GATE-03 – Zusatzbedingungen G1

| Bedingung | Status | Nachweis |
|---|---|---|
| Lasttest (NFR-10) bestanden | PASS | Fixture |
| Pentest ohne offene High/Critical | offen | Erklärung des Auftraggebers als Nachweis für G1 und G2 (E-103, Fixture) |
| Isolationstest (TEN-09) vollständig | PASS | Fixture |
| Install-Smoke auf beiden Plattformen | PASS | Fixture |

**Status:** offen.

## GATE-05 – Kein Gate wird durch [O]-Punkte beeinflusst

**Status:** PASS.

## Anhang: Status je [B]/[Q]-ID

| ID | Tag | Status | Nachweis | WP |
|---|---|---|---|---|
| ABN-01 | [Q] | offen | Ausnahme (erklaerung, E-103) | WP-000 |
| AUT-05 | [B] | offen | Ausnahme (erklaerung, E-103) | WP-000 |
| AUT-06 | [B] | offen | Ausnahme (erklaerung, E-103) | WP-000 |
| CI-13 | [B] | PASS | Fixture | WP-000 |
| GATE-01 | [Q] | PASS | Fixture | WP-000 |
| GATE-02 | [Q] | offen | Ausnahme (erklaerung, E-103) | WP-000 |
| GATE-03 | [Q] | offen | Ausnahme (erklaerung, E-103) | WP-000 |
| GATE-04 | [Q], Bezug GA | N/P | Ausnahme (np) | WP-000 |
| GATE-05 | [Q] | PASS | Fixture | WP-000 |
| NET-04 | [B] | offen | Ausnahme (erklaerung, E-103) | WP-000 |
| NFR-03 | [B] | PASS | Fixture | WP-000 |
| NFR-08 | [B] | offen | Ausnahme (erklaerung, E-103) | WP-000 |
| NFR-09 | [B] | offen | Ausnahme (erklaerung, E-103) | WP-000 |
| TEN-09 | [Q] | PASS | Fixture | WP-000 |
