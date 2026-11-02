# Internes Sicherheitsreview G1 (Fixture „conditional“)

Fixture der Selbsttests von `tests/rep01-structure.test.sh` (WP-595): kein
offenes High oder Critical. Die Befunde sind erfunden; sie folgen nur dem
Format von `docs/security/review-g1.md`.

## Neue Befunde

| ID | Severity | Status | Befund | Maßnahme |
|---|---|---|---|---|
| SR-12 | Critical | **behoben** (WP-000) | Site-Scope-Bypass im Rack-Kanal (TEN-09, Fixture). | Fixture. |
| SR-01 | High | **behoben** (WP-000) | Fixture-Befund ohne Katalog-ID. | Fixture. |
| SR-11 | High | **behoben** (WP-000) | „reaktivieren“ aus CI-13 war in der Produktionsverdrahtung nicht nutzbar. | Fixture. |
| SR-08 | Low | offen | Die Reserve bei NFR-03 ist knapp; die Schwelle ist eingehalten. | Folge-WP. |

## Anhang: Critical/High-Befunde des Audits

| ID | Tag | Severity | Status | WPs |
|---|---|---|---|---|
| TEN-06 | [B] | Critical | geschlossen | WP-000 |
| AST-01 | [P2] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-000 |
| CI-13 | [B] | High | geschlossen | WP-000 |
| AGT-01 | [P4] | High | verschoben (E-03) | WP-000 |
