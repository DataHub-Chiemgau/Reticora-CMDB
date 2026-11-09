# Gate G2 – Checkliste (Fixture „g2-conditional“)

Fixture der Selbsttests der G2-Stufe von `tests/rep01-structure.test.sh`:
das Ergebnis „bedingt bestanden“ nach einer Freigabe des Auftraggebers.
SRC-02 und GATE-02 sind offen und Ausnahmen der Art `erklaerung` mit Frist,
MET-20 eine Ausnahme der Art `np` (`docs/acceptance/g2-ausnahmen.csv`). Die
Inhalte sind erfunden und belegen nichts; sie folgen nur dem Format von
`docs/acceptance/g2-gate.md`.

## Ergebnis

**Ergebnis: bedingt bestanden.** Fixture.

### Freigabe des Auftraggebers

Entscheidung des Auftraggebers vom 2026-01-01 (Fixture): „Gate G2 ist
freigegeben.“ Die Freigabe gilt für SRC-02 und GATE-02.

## GATE-01 – Kein offenes Critical

| Quelle | Critical offen | Nachweis |
|---|---:|---|
| (1) Sicherheitsreview Phase 2 | 0 | Fixture |
| (3) Pentest G1 | 0 laut Erklärung | PASS (Erklärung des Auftraggebers vom 2026-10-15) |

| ID | Severity | Status | Nachweis |
|---|---|---|---|
| AST-01 | Critical | geschlossen | WP-999; `fixture_test.go` |
| AST-05 | Critical | geschlossen | WP-999; `fixture_test.go` |
| MGT-02 | Critical | geschlossen | WP-999; `fixture_test.go` |
| SRC-03 | Critical | geschlossen | WP-999; `fixture_test.go` |
| EXP-01 | Critical | geschlossen | WP-999; `fixture_test.go` |
| GQL-04 | Critical | geschlossen | WP-999; `fixture_test.go` |
| MET-14 | Critical | geschlossen | WP-999; `fixture_test.go` |
| MGT-01 | Critical | geschlossen | WP-999; `fixture_test.go` |
| SRC-01 | Critical | geschlossen | WP-999; `fixture_test.go` |
| SRC-04 | Critical | geschlossen | WP-999; `fixture_test.go` |
| TKT-01 | Critical | geschlossen | WP-999; `fixture_test.go` |
| LOC-10 | Critical | geschlossen | WP-999; `fixture_test.go` |
| LOC-11 | Critical | **behoben** | WP-999; `fixture_test.go` |
| X-SCOPE | Critical | geschlossen | WP-999; `fixture_test.go` |
| X-MANUAL | Critical | geschlossen | WP-999; `fixture_test.go` |
| AGT-05 | Critical | abgeschaltet (E-03) | `TestFixture` |
| MGT-04 | Critical | abgeschaltet (E-03) | `TestFixture` |
| WFL-01 | Critical | abgeschaltet (E-03) | `TestFixture` |
| WFL-02 | Critical | abgeschaltet (E-03) | `TestFixture` |

**Status:** PASS.

## GATE-02 – Alle Anforderungen der Phase sind PASS

**Status:** offen.

### N/P und WARN

| ID | Status | Begründung |
|---|---|---|
| MET-20 | N/P | Anforderungstext nicht geliefert, Regel aus E-37 (Fixture). |

### FAIL und offen

| ID | Status | Begründung |
|---|---|---|
| SRC-02 | offen | Offener Fixture-Befund, freigegeben als Ausnahme mit Frist. |
| GATE-02 | offen | Folgt aus SRC-02 (Fixture). |

### Abnahme durch Erklärung

| ID | Status | Begründung |
|---|---|---|
| NFR-08 | PASS | Fixture-Zeile für die Erklärung nach E-103. |

## GATE-05 – Kein Gate wird durch [O]-Punkte beeinflusst

TKT-03 und die Textsyntax von SRC-03 ohne Gate-Wirkung (Fixture).

**Status:** PASS.

## SEQ-01 – Reihenfolge der Phase 2

Metamodell → Feldrechte → Dokumente → Verträge, Asset, Lager, Stocktake →
Ticketing → Import, Bulk, Views → G2 (Fixture).

**Status:** PASS.

## DOD-01 und GLO-11

Fixture.

**Status:** PASS.

## Anhang: Status je ID

| ID | Tag | Status | Nachweis | WP |
|---|---|---|---|---|
| DOD-01 | [Q] | PASS | `tests/fixture.test.sh` | WP-999 |
| GATE-01 | [Q] | PASS | `tests/fixture.test.sh` | WP-999 |
| GATE-02 | [Q] | offen | `tests/fixture.test.sh` | WP-999 |
| GATE-05 | [Q] | PASS | `tests/fixture.test.sh` | WP-999 |
| GLO-11 | [Q] | PASS | `tests/fixture.test.sh` | WP-999 |
| IMP-IO-01 | [P2] | PASS | `fixture_test.go` | WP-999 |
| MET-20 | nicht geliefert | N/P | ohne Abschluss-WP (E-37) | – |
| SEQ-01 | [Q] | PASS | `tests/fixture.test.sh` | WP-999 |
| SRC-02 | [P2] | offen | `fixture_test.go` | WP-999 |
