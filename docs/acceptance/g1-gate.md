# Gate G1 – Checkliste

WP-213 (`g1-gate`). Die Checkliste prüft die Gate-Regeln GATE-01 bis GATE-05
(`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md`, Abschnitt „Gates“)
für Phase 1. Die Nachweise sind Tests und die Traceability
(`docs/traceability.csv`).

Prüfungen:

- `backend/internal/traceability` (`TestTraceabilityFile`) blockiert jede
  [B]/[Q]-ID ohne Traceability-Zeile, die nicht in
  `docs/acceptance/g1-ausnahmen.csv` steht (E-30, E-76).
- `tests/rep01-structure.test.sh` prüft:
  - die REP-01-Struktur
  - die Abschnitte dieser Checkliste
  - die Begründung jeder ID, die nicht PASS ist
  - [O]-IDs
  - die Workflows
  - die Konsistenz dieser Checkliste mit Sicherheitsreview,
    Pentest-Zusammenfassung und Ausnahmeliste (WP-451, unten)
- Beide laufen in jedem PR (CI-Jobs `traceability` und `gate`).
- `tests/rep01-structure.test.sh --final` prüft die Gate-Entscheidung
  (WP-595, WP-600): zusätzlich kein offenes Critical in Review und Pentest,
  kein offenes High/Critical im Pentest, jede GATE-Zeile PASS und keine
  Ausnahme der Art `erklaerung` oder `luecke`.

Konsistenzprüfung (WP-451, Normalmodus):

- Das Ergebnis steht als erste Zeile „**Ergebnis: offen**“ oder
  „**Ergebnis: bestanden**“ im Abschnitt „Ergebnis“. Das Ergebnis „bedingt
  bestanden“ nach E-76 Option 2 (Abschnitt „Freigabe nach E-76“) nimmt die
  Prüfung ab WP-595 an, mit seinen Bedingungen. Der Status einer ID ist
  die Statusspalte des Anhangs; PASS heißt, die Zelle beginnt mit PASS.
- Befundzeile ist jede Tabellenzeile mit einer Severity-Zelle in
  `docs/security/review-g1.md` (auch im Anhang) und
  `docs/security/pentest-g1.md`. Fettdruck und Groß-/Kleinschreibung zählen
  nicht; Kritisch und Hoch stehen für Critical und High. Geschlossen ist eine
  Befundzeile mit dem Status „behoben“, „geschlossen“ oder „G1-Anteil
  geschlossen“.
- Ein offenes Critical verbietet PASS von GATE-01 und das Ergebnis
  „bestanden“. Ein offenes High oder Critical im Pentest verbietet PASS von
  GATE-03.
- Ein offener SR- oder PT-Befund verbietet PASS jeder Katalog-ID, die seine
  Zeile nennt. Ausgenommen ist ein Befund unter High, für den der Abschnitt
  „Abgleich offener Befunde“ begründet, warum er die Anforderung nicht
  berührt.
- Die IDs der Ausnahmeliste sind nicht PASS. Keine Statuszelle des Anhangs
  nennt eine Erklärung, keine Statuszelle ist PASS durch Erklärung.
- GATE-02 ist erst PASS, wenn jede ID PASS ist (N/P nur mit einer Ausnahme
  der Art `np`). Das Ergebnis ist erst „bestanden“, wenn jede GATE-Zeile PASS
  ist. Jede ID, die nicht PASS ist, hat eine Begründung im Abschnitt GATE-02.
  Die Statuszeilen der Abschnitte stimmen mit dem Anhang überein.
- TEN-09 ist erst PASS, wenn Review oder Pentest den Site-Scope-Bypass im
  Rack-Kanal (F-002) als Befund führen (F-040).
- PASS einer ID mit ausstehender Lieferung verlangt die Lieferung: GATE-03
  und NFR-08 die vollständige Pentest-Zusammenfassung (A-07), NFR-09 die
  Subprozessorenliste (A-11), ABN-01 die eingefrorenen Texte (a) und (b),
  AUT-05, AUT-06 und NET-04 die v2-Texte in
  `docs/spec/v2/aut-05-aut-06-net-04.md` mit einer Zeile je ID im Abschnitt
  „Abgleich v2-Texte“ (A-02). WP-453 bis WP-457 machen diese Bedingungen
  unbedingt.
- Selbsttests mit den Fixtures in `tests/fixtures/rep01/` belegen jede Regel
  und jede Schreibweise einer offenen Befundzeile.

Stand: 2026-10-15, nach der G1-Nachbesserung (WP-332 bis WP-335,
Commit `dbbfded`). Die Erstfassung prüfte den Release-Kandidaten `main` nach
PR „Gate G1: WP-211 – WP-213“ (#62). Der Stand `dbbfded` ist lokal geprüft;
danach kamen zwei Korrekturen aus dem Gesamtreview hinzu (`58863fa`,
`dea65b7`) und am 2026-10-15 die Nachträge zu seinen nicht blockierenden
Befunden (`3f2bfd5`, `458a96d`, `111bdc4`, `f354c5a`, `0f165cc`; Bewertung in
`docs/security/review-g1.md`). Sie ändern die Werte des Nachweislaufs nicht
(Kontrollmessungen in `docs/acceptance/lasttest-g1.md`). Der Workflow
`load.yml` ist auf Commit `918a832`, dem Code-Stand mit allen Nachträgen,
grün (Run 38665599979); nach dem Merge wiederholt ihn der nächtliche Lauf auf
dem Release-Kandidaten. Der CI-Lauf auf dem Release-Kandidaten steht aus.

Nachtrag 2026-10-15: Der Auftraggeber hat E-17, E-56, E-57 und E-58 nach den
Empfehlungen entschieden, B3, B4 und B6 für erledigt erklärt und G1
freigegeben (`docs/plan/entscheidungen.md`, Nachtrag vom 2026-10-15). Die
Nachweise zu B3, B4 und B6 sind nachzureichen (Abschnitt „Ergebnis“).

Nachtrag 2026-10-25 (WP-451, `t5-gate-consistency`): Eine Erklärung des
Auftraggebers ersetzt keinen Nachweis (GATE-02 „Nachweis per
Test/Traceability“; F-120). Nach der Übergangsregel von E-76
(`docs/plan/g1-korrektur/entscheidungen.md`) stehen AUT-05, AUT-06, NET-04,
NFR-08, NFR-09, ABN-01 (a)/(b), GATE-03 (Pentest-Anteil) und GATE-02 auf
„offen“ und in `docs/acceptance/g1-ausnahmen.csv`. GATE-01, CI-13, MET-15
und TEN-09 stehen wegen offener oder fehlender Befunde des Sicherheitsreviews
auf „offen“ (F-038, F-040). Das Ergebnis lautet „offen“.

Nachtrag 2026-10-26 (E-76 Option 2): Der Auftraggeber hat E-76 entschieden.
Seine Erklärung gibt die Abweichung für die IDs der Ausnahmeliste frei; sie
ersetzt keinen Nachweis. Die IDs bleiben „offen“, die Lieferungen A-02, A-07
und A-11 sind mit Frist „vor GA“ geschuldet, und G1 darf „bedingt bestanden“
lauten, sobald jede andere Bedingung von „bestanden“ erfüllt ist (Abschnitt
„Freigabe nach E-76“). Das Ergebnis lautet weiter „offen“.

## Ergebnis

**Ergebnis: offen.** G1 ist nicht bestanden. Keine [B]/[Q]-ID steht allein
durch die Erklärung des Auftraggebers auf PASS (E-76, Option 2). Ausstehende
Nachweise:

- Pentest (B3, Lieferung A-07): Dienstleister, Zeitraum, Umfang, Befundliste
  und Nachtest in `docs/security/pentest-g1.md` (WP-453). Betrifft NFR-08,
  den Pentest-Anteil von GATE-03 und den Pentest-Anteil von GATE-01.
- v2-Texte (B4, Lieferung A-02): AUT-05, AUT-06 und NET-04 in `docs/spec/v2/`
  mit Abgleich der Umsetzung (WP-455); ABN-01 (a) und (b) als eingefrorene
  Szenariotexte mit Protokoll (WP-457).
- Subprozessorenliste (B6, Lieferung A-11) in `docs/legal/subprozessoren.md`
  (WP-454). Betrifft NFR-09.
- Sicherheitsreview (WP-452): SR-11 fortschreiben, die Befunde SR-12
  (Site-Scope-Bypass im Rack-Kanal, F-002) und SR-13 (Review-Items, F-003)
  sowie die übrigen P0-Befunde des Phase-1-Audits aufnehmen. Betrifft
  GATE-01, CI-13, MET-15 und TEN-09.
- Läufe auf dem Release-Kandidaten und endgültige Statuspflege (WP-595),
  danach `tests/rep01-structure.test.sh --final` und die Gate-Entscheidung
  (WP-600).
- GATE-02 folgt aus den übrigen Zeilen. Seine Ausnahme entfernt WP-595, wenn
  keine andere Ausnahme der Art `erklaerung` oder `luecke` besteht.

Die Spätphasen-Module sind abgeschaltet (Abschnitt „Spätphasen-Module“).

Die Blocker beim Auftragnehmer, B1 (NFR-03) und B2 (NFR-02), sind mit der
G1-Nachbesserung (WP-332 bis WP-335) geschlossen. B5 hat der Auftraggeber
(zugleich Product Owner) am 2026-10-15 entschieden. B3, B4 und B6 hat er am
2026-10-15 für erledigt erklärt; nach E-76 bleiben sie offen, bis die
Nachweise vorliegen:

| # | Blocker | Regel | Verantwortlich | Stand 2026-10-25 |
|---|---|---|---|---|
| B3 | Externer Pentest ohne offene High/Critical (NFR-08 nach E-28) | GATE-03 | Auftraggeber (E-28, A-07) | offen: Erklärung des Auftraggebers vom 2026-10-15, Nachweis ausstehend. Bericht, Befunde und Nachtest liegen im Repository nicht vor; Zusammenfassung und Befundliste sind in `docs/security/pentest-g1.md` nachzureichen (WP-453). |
| B4 | v2-Originaltexte fehlen: AUT-05, AUT-06, NET-04, ABN-01 (a)/(b) | GATE-02 | Auftraggeber (E-02, A-02) | offen: Erklärung des Auftraggebers vom 2026-10-15, Nachweis ausstehend. Die Texte liegen im Repository nicht vor und sind in `docs/spec/` nachzureichen; danach wird geprüft, ob die Umsetzung ihnen entspricht (WP-455, WP-457). |
| B5 | Skalierungsziel unbestätigt (E-17) | – (kein eigener Gate-Punkt; gemessen mit 10.000 Objekten nach CH15) | Auftraggeber | geschlossen: E-17 am 2026-10-15 entschieden (Option 1, Budgets bei 10.000 Objekten, Headroom 50.000 ohne Budgetzusage). Keine Nachmessung nötig. |
| B6 | Subprozessorenliste fehlt (NFR-09: „Subprozessoren dokumentiert und AVV-fähig“) | GATE-02 | Betreiber (E-35, A-11) | offen: Erklärung des Auftraggebers vom 2026-10-15, Nachweis ausstehend. Die Liste liegt im Repository nicht vor; `docs/legal/subprozessoren.md` ist leer und dort nachzureichen (WP-454). |

Für B3, B4 und B6 ersetzt die Erklärung des Auftraggebers den Nachweis nicht
(E-76): Die betroffenen IDs stehen auf „offen“ und in
`docs/acceptance/g1-ausnahmen.csv`, bis die Lieferung vorliegt. Diese
Checkliste erfindet keine Inhalte: Sie nennt weder Pentest-Befunde noch Texte
noch Subprozessoren.

### Freigabe nach E-76

Erklärung des Auftraggebers vom 2026-10-26: „GATE-02 darf durch meine hiermit
erteilte Erklärung abgenommen werden.“ Auf Rückfrage hat er E-76 mit Option 2
entschieden (`docs/plan/g1-korrektur/entscheidungen.md`). Die Freigabe gilt
für AUT-05, AUT-06, NET-04, NFR-08, NFR-09, ABN-01 (a)/(b), GATE-03
(Pentest-Anteil) und GATE-02:

- Die IDs bleiben „offen“ mit der Begründung „Erklärung des Auftraggebers,
  Nachweis ausstehend“ und stehen in `docs/acceptance/g1-ausnahmen.csv`; die
  Traceability zählt sie nicht als nachgewiesen.
- G1 darf „bedingt bestanden“ lauten, wenn außer diesen Ausnahmen jede
  Bedingung von „bestanden“ erfüllt ist: kein offenes Critical, kein offenes
  High/Critical im Pentest, keine Ausnahme der Art `luecke`, jede GATE-Zeile
  PASS oder selbst eine Ausnahme dieser Freigabe. Die Prüfung dafür ergänzt
  WP-595 in `tests/rep01-structure.test.sh`; die Entscheidung trifft WP-600.
- Die Lieferungen bleiben geschuldet. Mit jeder Lieferung entfernt ihr WP die
  Zeilen aus der Ausnahmeliste und setzt die IDs nach dem Nachweis auf PASS.

| Lieferung | Inhalt | IDs | WP | Frist |
|---|---|---|---|---|
| A-02 | v2-Texte zu AUT-05, AUT-06 und NET-04; Szenariotexte ABN-01 (a) und (b) | AUT-05, AUT-06, NET-04, ABN-01 | WP-455, WP-457 | vor GA |
| A-07 | Zusammenfassung, Befundliste und Nachtest des externen Pentests | NFR-08, GATE-03 (Pentest-Anteil) | WP-453 | vor GA |
| A-11 | Subprozessorenliste mit Region und AVV | NFR-09 | WP-454 | vor GA |

Das Ergebnis lautet derzeit „offen“, weil weitere Bedingungen fehlen: GATE-01
und die offenen Befunde zu CI-13, MET-15 und TEN-09 sowie die Läufe auf dem
Release-Kandidaten (WP-595).

Geschlossen mit der G1-Nachbesserung:

| # | Blocker (Erstlauf WP-206) | Regel | Geschlossen durch | Nachweis |
|---|---|---|---|---|
| B1 | Ingest-Durchsatz 58 statt 200 Records/s je Instanz (NFR-03, Befund SR-02, High) | GATE-02, GATE-03 (Lasttest bestanden), Sicherheitsreview ohne offene High | WP-332 bis WP-334 | Nachweislauf auf Commit `dbbfded` mit 4 Spuren (Standard): Abbau ab Sendeende 233 Records/s, Verzögerung Record → sichtbar höchstens 3,1 s bei 200 Records/s Angebot, Spool-Flush von 10.000 Records in 35,9 s, kein fehlender Record (`docs/acceptance/lasttest-g1.md`). SR-02 behoben (`docs/security/review-g1.md`). |
| B2 | Impact im Worst Case unter Parallellast p95 2,39 s statt < 2 s (NFR-02, SR-05) | GATE-02 | WP-335 | Nachweislauf auf Commit `dbbfded`: Impact Tiefe 20 unter Parallellast p95 477 ms in der kompakten Form der Oberfläche und 546 ms in der Standardform mit Pfaden (`docs/acceptance/lasttest-g1.md`). SR-05 behoben (`docs/security/review-g1.md`). |

Entscheidungen zu den geschlossenen Blockern (alle am 2026-10-15 vom
Auftraggeber entschieden):

- **E-58 (entschieden, Option 1):** Der Lastnachweis auf 4-vCPU-Hosts gilt
  als G1-Nachweis für GATE-03 „Lasttest bestanden“: der lokale Nachweislauf
  auf Commit `dbbfded` (4 vCPU, 15 GB RAM, Größenklasse des Mindestsizings
  nach INS-07; PostgreSQL, NATS, Redis, Server und k6 auf einem Host) und
  Run 38665599979 auf Commit `918a832` (Standard-Runner `ubuntu-latest`,
  4 CPUs, alle Schwellen eingehalten; `docs/acceptance/lasttest-g1.md`,
  Abschnitt „GitHub-Lauf“). Die Auflagen der Empfehlung gelten: Nach dem
  Merge wiederholt der nächtliche Lauf den Nachweis auf dem
  Release-Kandidaten, und ein dedizierter Referenzhost folgt vor GA (SR-06,
  bis dahin offen).
- **E-56 (entschieden, Option 1 mit Identitätssperre):** parallele Spuren mit
  Identitätssperre, Standard 4 Spuren. `RETICORA_INGEST_LANES=1` (serielle
  Verarbeitung) bleibt der Rückfall; damit erreicht der Abbau nur 182
  Records/s und verfehlt NFR-03. Die Reserve mit 4 Spuren ist auf dem
  4-vCPU-Host knapp: Frühere Läufe desselben Codes lagen bei 210–233
  Records/s.
- **E-57 (entschieden, Option A):** WP-335 ändert die Impact-Antwort nur
  additiv. Die Standardantwort behält `path`. Die Oberfläche ruft die
  kompakte Form über den Sparse-Fieldset `fields=` (API-02) ab.
- **E-17 (entschieden, Option 1):** Messbasis sind 10.000 Objekte; Listen
  und Suche über etwa 25.000 Objekten sind eine bekannte Einschränkung mit
  Folge-WP vor GA.

## GATE-01 – Kein offenes Critical

| Quelle | Critical offen | Nachweis |
|---|---:|---|
| Audit-Befunde (225 Critical/High) | 0 | `docs/security/review-g1.md`, Abschnitt „Audit-Befunde Critical/High“: 41 geschlossen, 8 mit G1-Anteil geschlossen |
| Neue Befunde des Sicherheitsreviews (SR-01 bis SR-11) | 0 | `docs/security/review-g1.md`: kein Critical. Es fehlen die Befunde SR-12 (Site-Scope-Bypass im Rack-Kanal, F-002) und SR-13 (Review-Items, F-003) und die übrigen P0-Befunde des Phase-1-Audits (WP-452). |
| P0-Befunde des Phase-1-Abnahmeaudits (F-001 bis F-016) | offen bis zum Nachweis | Die Korrektur-WPs aller 16 P0-Befunde (Tabelle „Schwere Befunde (P0/P1) und ihre Korrektur“ in `docs/plan/g1-korrektur/implementierungsplan.md`) sind gemergt. Geschlossen sind die Befunde erst mit ihrer Aufnahme ins Review (WP-452) und den Läufen auf dem Release-Kandidaten (WP-595). |
| Abhängigkeits- und Image-Scans | 0 | govulncheck, npm audit, Trivy blockierend in CI |
| Externer Pentest | unbekannt | `docs/security/pentest-g1.md` enthält keine Befundliste (A-07, WP-453). Die Erklärung des Auftraggebers vom 2026-10-15 ist kein Nachweis (E-76). |

**Status:** offen. GATE-01 bleibt offen, solange ein P0-Befund des
Phase-1-Audits offen ist (`docs/plan/g1-korrektur/implementierungsplan.md`)
und die Befundliste des Pentests fehlt. `tests/rep01-structure.test.sh`
verbietet PASS von GATE-01 und das Ergebnis „bestanden“, solange eine
Critical-Zeile in Review oder Pentest offen ist, in jeder Schreibweise und
auch im Anhang des Reviews; `--final` verlangt, dass keine offen ist. Das
gilt auch für die nachgereichte Befundliste.

## GATE-02 – Alle Anforderungen der Phase sind PASS

Für G1 gilt das für alle IDs mit Tag [B] und für die [Q]-Punkte. Für
gemischte Tags wie „[B], Teilumfang [P2]“ zählt der [B]-Anteil.

- 220 IDs sind betroffen: 219 mit Tag im Katalog und SEC-05, dessen Tag im
  Katalog fehlt und in der Traceability als [B] geführt ist. Jede hat eine
  Zeile in `docs/traceability.csv` oder steht in
  `docs/acceptance/g1-ausnahmen.csv` (E-76); `TestTraceabilityFile`
  blockiert jede andere. Stand 2026-10-25: 214 von 219 mit Zeile,
  9 Ausnahmen, davon 4 mit Zeile, aber nicht abgenommen.
- Der Status je ID steht im [Anhang](#anhang-status-je-bq-id).
- Abweichungen von PASS stehen mit Begründung in den Tabellen unten: N/P,
  offen nach E-76 und offen wegen offener Befunde. Der Abgleich offener
  Befunde begründet, warum eine ID trotz eines offenen Befunds unter High
  PASS ist. Die IDs, die mit der G1-Nachbesserung PASS wurden, stehen in der
  letzten Tabelle.

**Status:** offen. Folgt aus den Tabellen unten; die Ausnahme der Art
`erklaerung` für GATE-02 entfernt WP-595 (E-76).

### N/P und WARN

WARN ist nach GATE-02 nur für SOLL-Punkte zulässig (E-27). Phase 1 hat keine
SOLL-Punkte mit WARN. Diese ID gehört nicht zu G1:

| ID | Status | Begründung |
|---|---|---|
| GATE-04 | N/P | Gate-Regel für GA (COL-07, AI-02), nicht für G1. Die Add-ons sind nach E-03 abgeschaltet (WP-214). |

AUT-05, AUT-06, NET-04 und NFR-08 standen bis 2026-10-15 hier (B3, B4) und
danach bis 2026-10-25 auf „PASS durch Erklärung des Auftraggebers“; seit
WP-451 stehen sie in der nächsten Tabelle.

### Offen: Erklärung des Auftraggebers, Nachweis ausstehend (E-76)

Der Auftraggeber hat B3, B4 und B6 am 2026-10-15 für erledigt erklärt. Die
Nachweise liegen im Repository nicht vor. Nach der Übergangsregel von E-76
(`docs/plan/g1-korrektur/entscheidungen.md`) stehen diese IDs auf „offen“ und
in `docs/acceptance/g1-ausnahmen.csv` (Art `erklaerung`). Von 2026-10-15 bis
2026-10-25 standen sie hier als „PASS durch Erklärung des Auftraggebers“
(F-120). Jedes Lieferungs-WP setzt seine IDs nach dem Ergebnis und entfernt
ihre Zeilen aus der Ausnahmeliste.

| ID | Status | Begründung |
|---|---|---|
| AUT-05 | offen | Erklärung des Auftraggebers vom 2026-10-15, Nachweis ausstehend: v2-Originaltext in `docs/spec/` mit Abgleich der Umsetzung (B4, Aufgabe A-02, E-02; WP-455). Der Text „[B] unverändert“ verweist auf v2; nach der Übergangsregel sind keine Funktionen erfunden. |
| AUT-06 | offen | Erklärung des Auftraggebers vom 2026-10-15, Nachweis ausstehend: wie AUT-05 (B4, Aufgabe A-02, E-02; WP-455). |
| NET-04 | offen | Erklärung des Auftraggebers vom 2026-10-15, Nachweis ausstehend: v2-Text zu „cable(...) unverändert“ in `docs/spec/` mit Abgleich der Umsetzung (B4, Aufgabe A-02, E-02; WP-455). Das bestehende Kabelmodell ist unverändert. |
| ABN-01 | offen | (d) und (e) sind durch Tests belegt (`docs/acceptance/abn-01.md`). (a) und (b): Erklärung des Auftraggebers vom 2026-10-15, Nachweis ausstehend: Szenariotexte „V §65–66“ eingefroren und Schritt für Schritt protokolliert (B4, Aufgabe A-02, E-02; WP-457). (c) ist [P2]. |
| NFR-08 | offen | Erklärung des Auftraggebers vom 2026-10-15, Nachweis ausstehend: Zusammenfassung, Befundliste und Nachtest des externen Pentests in `docs/security/pentest-g1.md` (B3, Aufgabe A-07, E-28; WP-453). „Sicherheit nach SEC-11“ ist mit WP-209 belegt (`docs/security/review-g1.md`). |
| NFR-09 | offen | Erklärung des Auftraggebers vom 2026-10-15, Nachweis ausstehend: Subprozessorenliste in `docs/legal/subprozessoren.md`, die Datei ist leer (B6, Aufgabe A-11, E-35, Betreiber; WP-454). Die Datenresidenz ist belegt (`docs/betrieb/referenzmodell.md`, WP-105). |
| GATE-03 | offen | Pentest-Anteil: Erklärung des Auftraggebers vom 2026-10-15, Nachweis ausstehend (B3, Aufgabe A-07, E-28; WP-453). Außerdem ist der Isolationstest (TEN-09) offen (nächste Tabelle). Lasttest und Install-Smoke sind belegt (Abschnitt GATE-03). |
| GATE-02 | offen | Folgt aus den übrigen Zeilen: die Ausnahmen der Art `erklaerung` (A-02, A-07, A-11) und die offenen IDs der nächsten Tabelle. Die GATE-02-Zeile in `docs/acceptance/g1-ausnahmen.csv` entfernt WP-595, wenn keine andere Ausnahme der Art `erklaerung` oder `luecke` besteht. |

### Offen: offene Befunde und Korrektur-WPs

Diese IDs stehen auf „offen“, weil ein Befund in `docs/security/review-g1.md`
offen ist oder dort noch fehlt. Die Statuswerte auf dem Release-Kandidaten
pflegt WP-595 (`t5-rc-evidence`).

| ID | Status | Begründung |
|---|---|---|
| GATE-01 | offen | Die Korrektur-WPs der 16 P0-Befunde des Phase-1-Audits sind gemergt, aber die P0-Befunde, SR-12 und SR-13 stehen noch nicht im Review (WP-452), und für den Pentest liegt nur die Erklärung vor (A-07). Abschnitt GATE-01. |
| CI-13 | offen | Alle Korrektur-WPs zu WP-087 sind gemergt: WP-418 (`t5-sr11-decorator`, `423a538`) und WP-369 (`t3-resurrect-new`, `44a0db2`). SR-11 steht in `docs/security/review-g1.md` noch als offen; solange verbietet der Befund PASS (F-038). Die Fortschreibung von SR-11 übernimmt WP-452 (`t5-review-g1`). |
| MET-15 | offen | SR-11 nennt MET-15: In der Produktionsverdrahtung entfiel die serverseitige Vorabprüfung eindeutiger Attribute. WP-418 (`423a538`) reicht `UniqueConflicts` durch; SR-11 steht im Review noch als offen (WP-452). |
| TEN-09 | offen | Alle Korrektur-WPs zu WP-208 sind gemergt: WP-340 (`t1-rack-site-scope`, `5b29d6f`), WP-342 (`t1-review-scope`, `c8225dd`), WP-409 (`t1-channel-matrix`, `52ac11d`), WP-347 (`t1-export-job-visibility`, `dc1dd90`), WP-406 (`t1-err-ident-loc`, `bf46eff`) und WP-407 (`t1-err-modules`, `012abb5`). Das Review führt den Site-Scope-Bypass im Rack-Kanal (F-002) und den Review-Items-Leak (F-003) noch nicht als Befunde SR-12 und SR-13 (WP-452); bis dahin gilt der Isolationstest nicht als vollständig (F-040). |

### Abgleich offener Befunde

Ein offener Befund in `docs/security/review-g1.md` oder
`docs/security/pentest-g1.md` verbietet PASS jeder Katalog-ID, die seine Zeile
nennt (F-038). Für Befunde unter High begründet diese Tabelle, warum die
genannte ID trotzdem PASS ist; jede Begründung stützt sich auf den Befundtext,
eine Entscheidung oder den Korrekturplan. Ein High- oder Critical-Befund lässt
sich nicht abgleichen.

| Befund | ID | Begründung |
|---|---|---|
| SR-06 | GATE-03 | E-58 (entschieden am 2026-10-15, Option 1): Der Lastnachweis auf 4-vCPU-Hosts gilt als G1-Nachweis für „Lasttest bestanden“; SR-06 bleibt bis GA offen und sperrt G1 nicht. GATE-03 ist aus anderen Gründen offen. |
| SR-06 | INS-07 | Der Befund nennt INS-07 nur als Größenklasse des Lasttest-Hosts (Mindestsizing, 4 vCPU); er beschreibt keine Abweichung von INS-07. |
| SR-08 | NFR-03 | Restrisiko zur Reserve: Die Schwellen von NFR-03 sind eingehalten (233 Records/s mit 4 Spuren, `docs/acceptance/lasttest-g1.md`); der Befund beschreibt keine Abweichung von NFR-03. |
| SR-09 | ENT-03 | Der Befund beschreibt Wettlauffenster zwischen Zählung und Anlage bei parallelen Anlagen. Der Korrekturplan führt diese Klasse als bekannte Race ohne Befund (WP-472, „Nicht Bestandteil“); die Durchsetzung nach ENT-03 belegen `backend/internal/entitlement/enforce_integration_test.go` und `backend/internal/entitlement/unlicensed_integration_test.go`. |
| SR-10 | API-01 | API-01 steht nur in der Maßnahme: Eine Obergrenze im DeviceRecord-Vertrag wäre eine Vertragsänderung nach API-01. Der Befund beschreibt keine Abweichung von API-01. |
| SR-11 | CI-05 | Laut Befund nicht betroffen: „das Wiederherstellen per `POST /cis/{id}/restore` (CI-05) ist nicht betroffen“. |

### PASS mit der G1-Nachbesserung

Die Erstfassung führte diese IDs als FAIL (B1, B2). Nachweis ist der Lastlauf
auf Commit `dbbfded` mit 10.000 Objekten (`docs/acceptance/lasttest-g1.md`);
der GitHub-Lauf 38665599979 auf Commit `918a832` bestätigt ihn mit allen
Schwellen.

| ID | Status | Begründung |
|---|---|---|
| NFR-02 | PASS | Alle Budgets erfüllt (`api.js`, 60 s, alle Szenarien parallel, frischer Seed). Impact Tiefe 20 p95 477 ms in der kompakten Form der Oberfläche und 546 ms in der Standardform mit Pfaden (Erstlauf 2,39 s). 7.038 von 7.038 Checks. WP-335, B2 geschlossen. |
| NFR-03 | PASS | Mit 4 Spuren (Standard): Abbau ab Sendeende 233 Records/s (Erstlauf 58), Verzögerung Record → sichtbar höchstens 3,1 s bei 200 Records/s Angebot, Spool-Flush von 10.000 Records in 35,9 s, kein fehlender Record. Mit einer Spur 182 Records/s (E-56 entschieden: Option 1, 4 Spuren Standard, eine Spur nur als Rückfall). WP-332 bis WP-334, B1 geschlossen. |
| NFR-10 | PASS | Lasttest mit 10.000 Objekten bestanden: Alle Schwellen von `api.js` und `ingest.js` (4 Spuren) sind erfüllt, im lokalen Nachweislauf auf `dbbfded` und im GitHub-Lauf 38665599979 auf `918a832` (Workflow `load.yml`). Die Referenzumgebung ist anerkannt (E-58 entschieden, Option 1); dedizierter Referenzhost vor GA (SR-06). |

API-01 bleibt PASS: WP-335 erweitert die Antwort von `GET /impact/{ci_id}` und
`GET /impact` nur additiv (`via`, `relay`). `path` bleibt in der
Standardantwort; die kompakte Form liefert der Sparse-Fieldset `fields=`
(API-02, E-57 entschieden: Option A).

### Spätphasen-Module (E-03)

Nach E-03 sind alle Module späterer Phasen im G1-Release abgeschaltet. Sie
gehören damit nicht zum Gate-Umfang. WP-214 (`m-gates`, PR #59) ist vor WP-213
gemergt.

- **Routen nicht registriert:** Ohne `RETICORA_MODULES` (Standard des
  Release) ist kein Spätphasen-Modul aktiv. `authorizingRouter` registriert
  keine Route unter den Präfixen dieser Module (`LateModules` in
  `backend/internal/server/modules.go`). Nachweis:
  `TestG1DefaultSwitchesLateModulesOff`. `TestLateRoutesAreMarkedInSpecification`
  prüft, dass diese Routen in `api/openapi.yaml` ein `x-phase` ab P2 tragen.
- **Worker aus:** Die Worker eines Moduls starten nur, wenn das Modul aktiv
  ist (`WorkerAllowed`).
- **Oberfläche:** Das Frontend blendet Navigation und Seiten über
  `/api/v1/modules` aus (`useModules`,
  `TestModulesEndpointListsTheReleaseModules`).

Die 29 Audit-High-Befunde in diesem Code sind nach E-03 auf WP-215 bis WP-239
verschoben (`docs/security/review-g1.md`).

## GATE-03 – Zusatzbedingungen G1

| Bedingung | Status | Nachweis |
|---|---|---|
| Lasttest (NFR-10) bestanden | PASS | `docs/acceptance/lasttest-g1.md`: drei Nachweisläufe von `load.yml` auf `main` (Commit `3910aae`, Runs 40316485305, 40316488267, 40316491988), bewertet nach E-58 (Ergänzung vom 2026-10-26, Median über drei Läufe) mit `load-evidence.yml` (Run 40320307297): alle Budgets gehalten, auch NFR-02 auf den NFR-01-Volumina und die Wiederentdeckung (F-118). Umgebung nach E-58 (Option 1); dedizierter Referenzhost vor GA (SR-06). Läufe auf dem Release-Kandidaten: WP-595 |
| Pentest ohne offene High/Critical | offen | Erklärung des Auftraggebers vom 2026-10-15, Nachweis ausstehend: Zusammenfassung, Befundliste und Nachtest in `docs/security/pentest-g1.md` (B3, A-07; WP-453) |
| Isolationstest (TEN-09) vollständig | offen | Die Korrektur-WPs zu WP-208 sind gemergt (Abschnitt GATE-02, TEN-09); der Site-Scope-Bypass im Rack-Kanal (F-002) steht noch nicht als Befund im Review (WP-452). Statuspflege auf dem Release-Kandidaten: WP-595. |
| Install-Smoke auf beiden Plattformen | PASS | `docs/acceptance/g1-installation.md` (WP-211) |

**Status:** offen. `tests/rep01-structure.test.sh` verbietet PASS von GATE-03,
solange im Pentest ein High oder Critical offen ist, und PASS des
Isolationstests, solange der Rack-Befund nicht im Review steht.

## GATE-04 – GA

Diese Regel betrifft G1 nicht (siehe N/P).

## GATE-05 – Kein Gate wird durch [O]-Punkte beeinflusst

Die Gate-Prüfung wählt nur [B]- und [Q]-Tags aus (`RequiredForGate` in
`backend/internal/traceability/traceability.go`, Unit-Test
`TestRequiredForGateSelectsBaselineAndQualityTags`). [O]-IDs haben keine
G1-Zeile; `tests/rep01-structure.test.sh` prüft das. Die drei [O]-IDs des
Katalogs und die [O]-Anteile gemischter Tags (z. B. „[B], Air-Gapped [O]“)
gehen in keinen Blocker ein.

**Status:** PASS.

## SEQ-01 – Reihenfolge je Epic

SEQ-01 legt für Phase 1 fest: Epic A → Epic B ∥ Epic D → Epic C → Lasttest
(NFR-10) → Sicherheitsreview → G1.

Nach E-07 läuft M0 formal als Teil der Epics A, B und D. Jedes M0-WP trägt
sein fachliches Epic in der Spalte „Epic (Traceability)“. Die Merge-Folge auf
`main`:

| Schritt | Epic (Traceability) | PRs | gemergt | WPs |
|---|---|---|---|---|
| 1 | M0.0 Werkzeugkette (Epic A) | #40 – #43 | 2026-10-01 | WP-001 – WP-009 und Vorarbeiten der Runde 3 |
| 2 | M0.1/M0.2 Stabilisierung (Epics B, A, D je WP) | #44 – #51 | 2026-10-04 – 2026-10-12 | WP-010 – WP-091 |
| 3 | Epic A – Installation und Betrieb | #52 | 2026-10-12 | WP-093 – WP-104 |
| 4 | Epic B – Fundament | #53, #54, #55 | 2026-10-13 | WP-107 – WP-142, WP-240 – WP-242 |
| 4 | Epic D – Collector und Discovery (parallel zu B) | #56 | 2026-10-13 | WP-143 – WP-162 |
| 5 | Epic C – API, Kernfunktionen, UI | #58 | 2026-10-14 | WP-163 – WP-202 |
| – | M-S: WP-214 Feature-Gates (E-03, vor G1) | #59 | 2026-10-14 | WP-214 |
| 6 | Lasttest (NFR-10) | #60 | 2026-10-14 | WP-203 – WP-207 |
| 7 | Sicherheitsreview | #61 | 2026-10-14 | WP-208 – WP-210 |
| 8 | G1 | #62 | 2026-10-14 | WP-211 – WP-213 |

PR #57 (Planung Phase 2) enthält kein Arbeitspaket. Die einzige Abweichung von
SEQ-01 ist das Vorziehen von M0; E-07 erlaubt sie.

Nach Schritt 8 folgt die G1-Nachbesserung (WP-332 bis WP-335, Epic G1, Planung
in PR #63). Sie behebt die Befunde SR-02 und SR-05 des Sicherheitsreviews und
wiederholt den Lasttest (NFR-10).

**Status:** PASS.

## DOD-01 und GLO-11

- **DOD-01:** Die PR-Vorlage `.github/pull_request_template.md` enthält die
  Definition of Done. Die CI liest die WPs eines PRs und verlangt deren
  Traceability-Zeilen.
- **GLO-11:** Seit diesem WP blockiert die Traceability-Prüfung vollständig
  für [B]/[Q] (E-30). Fehlende [Pn]/[A]-IDs späterer Phasen werden berichtet
  und blockieren mit `TRACEABILITY_REQUIRE_COMPLETE=1`, spätestens zum Gate
  der jeweiligen Phase.

**Status:** PASS.

## REP-01

`tests/rep01-structure.test.sh` prüft:

- `backend/internal/{notify,jobs,locations,pii,reviews,rules}`
- `api/rules.schema.json`
- `docs/acceptance`
- `docs/decisions` mit Index
- `docs/schema-baseline.md`

**Status:** PASS.

## Anhang: Status je [B]/[Q]-ID

Erzeugt aus `docs/traceability.csv`. PASS heißt: Der Nachweis liegt in den genannten Tests bzw. Dokumenten, und die CI ist auf dem Release-Kandidaten grün. „offen“ heißt: Der Nachweis fehlt oder ein Befund ist offen; die Begründung steht im Abschnitt GATE-02. Eine ID der Ausnahmeliste `docs/acceptance/g1-ausnahmen.csv` steht nie auf PASS (E-76). Für NFR-02, NFR-03 und NFR-10 sind die Nachweise der lokale Lastlauf auf Commit `dbbfded` und der grüne `load.yml`-Lauf 38665599979 auf Commit `918a832`, als G1-Nachweis anerkannt (E-58 entschieden); der CI-Lauf auf dem Release-Kandidaten (PR #64) steht aus. Bis 2026-10-25 standen acht IDs auf „PASS (Erklärung des Auftraggebers)“; nach E-76 stehen sie auf „offen“ (WP-451).

| ID | Tag | Status | Nachweis | WP |
|---|---|---|---|---|
| ABN-01 | [Q] | offen | `frontend/e2e/onboarding.spec.ts`, `frontend/e2e/conflict.spec.ts` (d, e); (a), (b): Ausnahme in `docs/acceptance/g1-ausnahmen.csv` (A-02) | WP-200, WP-212 |
| ABN-02 | [Q] | PASS | `backend/internal/ci/querybudget_integration_test.go`, `tests/load/`, `backend/internal/topology/querybudget_integration_test.go` | WP-204, WP-206, WP-335 |
| ABN-03 | [Q], jeweilige Phase | PASS | `backend/internal/ci/concurrency_integration_test.go` | WP-207 |
| API-01 | [B] | PASS | `backend/internal/middleware/openapi_test.go` | WP-167 |
| API-02 | [B] | PASS | `backend/internal/api/cursor_test.go`, `backend/internal/api/listquery_test.go` | WP-164, WP-165 |
| API-03 | [B] | PASS | `backend/internal/ci/concurrency_integration_test.go`, `backend/internal/api/problem_test.go` | WP-061, WP-163 |
| API-04 | [B] | PASS | `backend/internal/middleware/middleware_test.go`, `backend/internal/entitlement/enforce_integration_test.go`, `backend/internal/middleware/ratelimit_test.go` | WP-050, WP-072, WP-166 |
| API-05 | [B] | PASS | `backend/internal/identity/apikey_integration_test.go`, `backend/internal/user/service_account_integration_test.go`, `backend/internal/operator/operator_test.go` (+11) | WP-067, WP-069, WP-070, WP-074 … |
| API-07 | [B] | PASS | `backend/internal/override/decide_test.go`, `backend/internal/ci/concurrency_integration_test.go` | WP-058, WP-061 |
| API-08 | [B] | PASS | `backend/internal/ci/bulk_test.go` | WP-181 |
| API-09 | [B] | PASS | `backend/internal/jobs/handler_test.go` | WP-182 |
| AUD-01 | [B] | PASS | `backend/internal/contact/pii_integration_test.go` | WP-242 |
| AUD-02 | [B] | PASS | `backend/internal/contact/handler_test.go` | WP-213 |
| AUD-03 | [B] | PASS | `backend/internal/discovery/last_seen_test.go`, `backend/internal/history/history_integration_test.go` | WP-090, WP-171 |
| AUD-04 | [B] | PASS | `backend/internal/audit/audit_test.go` | WP-132 |
| AUD-05 | [B] | PASS | `backend/internal/server/audit_coverage_test.go` | WP-133 |
| AUD-07 | [B] | PASS | `backend/internal/citype/export_audit_integration_test.go` | WP-134 |
| AUD-09 | [B] | PASS | `backend/internal/server/audit_coverage_test.go` | WP-133 |
| AUT-01 | [B] | PASS | `backend/internal/identity/deactivation_integration_test.go`, `backend/internal/identity/oidc_test.go` | WP-042, WP-043 |
| AUT-02 | [B] | PASS | `backend/internal/identity/deactivation_integration_test.go`, `backend/internal/identity/session_test.go` | WP-042, WP-066 |
| AUT-03 | [B] | PASS | `backend/internal/config/config_test.go` | WP-110 |
| AUT-04 | [B] | PASS | `backend/internal/identity/apikey_scope_integration_test.go`, `backend/internal/identity/apikey_integration_test.go` | WP-012, WP-067 |
| AUT-05 | [B] | offen | keiner; Ausnahme in `docs/acceptance/g1-ausnahmen.csv` (A-02) | WP-450 |
| AUT-06 | [B] | offen | keiner; Ausnahme in `docs/acceptance/g1-ausnahmen.csv` (A-02) | WP-450 |
| AUT-09 | [B] | PASS | `backend/internal/identity/oidc_test.go`, `tests/install-cloud-helpers.test.sh` | WP-043, WP-044 |
| AUT-10 | [B] | PASS | `backend/internal/middleware/middleware_test.go` | WP-050 |
| BLK-01 | [B] | PASS | `backend/internal/ci/bulk_test.go` | WP-181 |
| CI-01 | [B], Teilfelder [P2]/[P3] | PASS | `backend/internal/ci/handler_test.go` | WP-120 |
| CI-02 | [B] | PASS | `backend/internal/ci/indexes_integration_test.go` | WP-121 |
| CI-03 | [B] | PASS | `backend/internal/fieldmeta/fieldmeta_test.go` | WP-117 |
| CI-04 | [B] | PASS | `backend/internal/ci/mergepatch_test.go` | WP-057 |
| CI-05 | [B] | PASS | `backend/internal/topology/scope_integration_test.go`, `backend/internal/ci/handler_test.go`, `backend/internal/ci/delete_integration_test.go` | WP-031, WP-169, WP-170 |
| CI-06 | [B] | PASS | `backend/internal/history/history_integration_test.go` | WP-171 |
| CI-09 | [B] | PASS | `backend/internal/ci/assetfree_integration_test.go` | WP-178 |
| CI-10 | [B] | PASS | `backend/internal/server/authz_test.go`, `backend/internal/override/decide_test.go`, `backend/internal/ci/effective_view_integration_test.go` (+1) | WP-045, WP-058, WP-060, WP-091 |
| CI-11 | [B] | PASS | `backend/internal/ci/typechange_test.go` | WP-172 |
| CI-12 | [B] | PASS | `backend/internal/ci/merge_integration_test.go`, `backend/internal/ci/unmerge_integration_test.go` | WP-173, WP-174 |
| CI-13 | [B] | offen | `backend/internal/discovery/resurrect_test.go`, `backend/internal/tenant/rls/ci_decorator_chain_integration_test.go` | WP-087, WP-369, WP-418 |
| COL-01 | [B] | PASS | `backend/internal/discovery/collector_status_integration_test.go` | WP-143 |
| COL-02 | [B] | PASS | `collector/plugins/redfish/redfish_test.go`, `backend/internal/credential/handler_test.go`, `backend/internal/credential/bundle_test.go` | WP-048, WP-047, WP-147 |
| COL-04 | [B] | PASS | `backend/internal/discovery/enroll_handler_test.go`, `backend/internal/discovery/ca_test.go` | WP-144, WP-145 |
| COL-05 | [B] | PASS | `edgecore/buffer/buffer_test.go`, `backend/internal/discovery/collector_status_integration_test.go` | WP-062, WP-143 |
| COL-06 | [B], Air-Gapped [O] | PASS | `edgecore/update/update_test.go` | WP-102, WP-148 |
| COL-08 | [B] | PASS | `tests/nats-mtls.test.sh` | WP-146 |
| DIS-01 | [B] | PASS | `collector/plugins/sweep/sweep_test.go` | WP-149 |
| DIS-02 | [B], Profilliste (V) | PASS | `collector/profiles/profiles_test.go` | WP-151 |
| DIS-03 | [B] | PASS | `collector/collectorcmd/collectorcmd_test.go` | WP-150 |
| DIS-04 | [B] | PASS | `collector/collectorcmd/collectorcmd_test.go`, `collector/plugins/snmp/snmp_test.go` | WP-150, WP-152 |
| DIS-05 | [B] | PASS | `backend/internal/discovery/devicerecord_test.go` | WP-083 |
| DIS-08 | [Q] | PASS | `backend/internal/discovery/errors_test.go` | WP-155 |
| DIS-09 | [B] | PASS | `backend/internal/discovery/scope_integration_test.go` | WP-153 |
| DIS-10 | [B] | PASS | `collector/collectorcmd/schedule_test.go` | WP-154 |
| DOD-01 | [Q] | PASS | `backend/internal/traceability/traceability_test.go`, `tests/rep01-structure.test.sh` | WP-005, WP-213 |
| ENT-01 | [B] | PASS | `backend/internal/entitlement/entitlement_test.go` | WP-071 |
| ENT-02 | [B] | PASS | `backend/internal/entitlement/entitlement_test.go` | WP-071 |
| ENT-03 | [B] | PASS | `backend/internal/entitlement/enforce_integration_test.go`, `backend/internal/entitlement/unlicensed_integration_test.go` | WP-072, WP-073 |
| ENT-04 | [B] | PASS | `backend/internal/operator/entitlements_test.go` | WP-074 |
| ENT-05 | [B] | PASS | `backend/internal/entitlement/enforce_integration_test.go` | WP-072 |
| ENT-06 | (V), [B–P4], [A] | PASS | `backend/internal/entitlement/addon_test.go` | WP-075 |
| ENT-07 | [B] | PASS | `backend/internal/entitlement/enforce_integration_test.go` | WP-072 |
| ENT-08 | [B] | PASS | `backend/internal/entitlement/unlicensed_integration_test.go` | WP-073 |
| EVT-01 | [B] | PASS | `backend/internal/events/outbox_integration_test.go` | WP-136 |
| EVT-02 | [B] | PASS | `backend/internal/events/outbox_integration_test.go` | WP-136 |
| EXP-01 | [B], DATEV [P2] | PASS | `backend/internal/export/scope_integration_test.go`, `backend/internal/export/export_test.go` | WP-035, WP-189 |
| GATE-01 | [Q] | offen | `tests/rep01-structure.test.sh` | WP-213, WP-451 |
| GATE-02 | [Q] | offen | `tests/rep01-structure.test.sh`, `backend/internal/tenant/rls/ci_decorator_chain_integration_test.go`; Ausnahme in `docs/acceptance/g1-ausnahmen.csv` | WP-213, WP-418, WP-451 |
| GATE-03 | [Q] | offen | `tests/install-smoke-health.test.sh`, `tests/rep01-structure.test.sh`, `docs/security/review-g1.md` (+2); Pentest-Anteil: Ausnahme in `docs/acceptance/g1-ausnahmen.csv` (A-07) | WP-092, WP-209, WP-210, WP-211, WP-451 |
| GATE-04 | [Q], Bezug GA | N/P | Ausnahme in `docs/acceptance/g1-ausnahmen.csv` (Art `np`) | WP-213, WP-450 |
| GATE-05 | [Q] | PASS | `tests/rep01-structure.test.sh` | WP-213 |
| GLO-10 | [B] | PASS | `frontend/src/i18n/catalog.test.ts` | WP-194 |
| GLO-11 | [Q] | PASS | `backend/internal/traceability/traceability_test.go`, `tests/rep01-structure.test.sh` | WP-005, WP-213 |
| GLO-12 | [B] | PASS | `backend/internal/discovery/devicerecord_test.go`, `backend/internal/override/decide_test.go` | WP-083, WP-084 |
| GLO-13 | [B] | PASS | `backend/internal/ci/validation_test.go` | WP-091 |
| GQL-01 | [B] | PASS | `backend/internal/graphqlbff/graphqlbff_test.go` | WP-184 |
| GQL-02 | [B] | PASS | `backend/internal/graphqlbff/resolver_test.go` | WP-185 |
| GQL-04 | [B] | PASS | `backend/internal/graphqlbff/graphqlbff_test.go` | WP-036 |
| IMP-01 | [B] | PASS | `backend/internal/topology/impact_test.go` | WP-176 |
| IMP-02 | [B] | PASS | `backend/internal/topology/impact_test.go` | WP-177 |
| IMP-03 | [B] | PASS | `backend/internal/topology/impact_test.go` | WP-176 |
| IMP-04 | [B] | PASS | `backend/internal/topology/redundancy_test.go` | WP-179 |
| IMP-05 | [B] | PASS | `backend/internal/topology/multi_integration_test.go` | WP-180 |
| IMP-06 | [B] | PASS | `backend/internal/topology/multi_integration_test.go` | WP-180 |
| IMP-07 | [B] | PASS | `backend/internal/tenant/rls/ci_children_integration_test.go`, `backend/internal/topology/scope_integration_test.go` | WP-025, WP-031 |
| IMP-09 | [B] | PASS | `backend/internal/discovery/power_test.go` | WP-161 |
| INS-01 | [Q] | PASS | `tests/install-smoke-health.test.sh`, `docs/acceptance/g1-installation.md` | WP-092, WP-211 |
| INS-02 | [B] | PASS | `tests/install-cloud-helpers.test.sh` | WP-095 |
| INS-03 | [B] | PASS | `tests/install-cloud-helpers.test.sh` | WP-094 |
| INS-04 | [Q] | PASS | `frontend/e2e/onboarding.spec.ts` | WP-200 |
| INS-05 | [B] | PASS | `tests/install-cloud-helpers.test.sh` | WP-096 |
| INS-06 | [B] | PASS | `collector/collectorcmd/collectorcmd_test.go` | WP-156 |
| INS-07 | [B] | PASS | `tests/install-preflight.test.sh` | WP-093 |
| JOB-01 | [B] | PASS | `backend/internal/jobs/service_test.go` | WP-129 |
| JOB-02 | [B], Zusätze [P2], [P3] | PASS | `backend/internal/jobs/scheduler_test.go` | WP-131 |
| JOB-03 | [B] | PASS | `backend/internal/jobs/handler_test.go` | WP-182 |
| JOB-04 | [B] | PASS | `backend/internal/reviews/merge_job_integration_test.go` | WP-183 |
| LCY-01 | [B] | PASS | `backend/internal/lifecycle/status_separation_integration_test.go`, `backend/internal/ci/delete_integration_test.go` | WP-122, WP-170 |
| LCY-05 | [B] | PASS | `backend/internal/lifecycle/offline_integration_test.go` | WP-157 |
| LOC-01 | B/G1 | PASS | `backend/internal/tenantapi/handler_test.go` | WP-107 |
| LOC-02 | B/G1 | PASS | `backend/internal/tenantapi/handler_test.go` | WP-107 |
| LOC-03 | B/G1 | PASS | `backend/internal/tenantapi/handler_test.go` | WP-107 |
| LOC-04 | B/G1 | PASS | `backend/internal/tenantapi/location_uniques_integration_test.go` | WP-108 |
| LOC-05 | B/G1 | PASS | `backend/internal/tenantapi/location_uniques_integration_test.go` | WP-108 |
| LOC-06 | B/G1 | PASS | `backend/internal/tenantapi/location_uniques_integration_test.go` | WP-108 |
| LOC-07 | B/G1 | PASS | `backend/internal/locations/tree_test.go` | WP-053 |
| LOC-10 | B/G1 | PASS | `backend/internal/locations/pg_repository_integration_test.go`, `backend/internal/locations/tree_test.go`, `backend/internal/ci/location_integration_test.go` | WP-026, WP-053, WP-054 |
| LOC-11 | B/G1 | PASS | `backend/internal/locations/move_integration_test.go` | WP-055 |
| MET-01 | B/G1, Teil P2/G2 | PASS | `backend/internal/citype/seed_integration_test.go` | WP-115 |
| MET-02 | B/G1 | PASS | `backend/internal/citype/seed_integration_test.go` | WP-115 |
| MET-03 | B/G1 | PASS | `backend/internal/citype/seed_integration_test.go` | WP-115 |
| MET-04 | B/G1 | PASS | `backend/internal/citype/compose_test.go` | WP-119 |
| MET-10 | B/G1, Teil P2/G2 | PASS | `backend/internal/citype/attrdef_integration_test.go` | WP-116 |
| MET-11 | B/G1, Teil P2/G2 | PASS | `backend/internal/fieldmeta/fieldmeta_test.go` | WP-117 |
| MET-15 | B/G1 | offen | `backend/internal/citype/ddl_job_integration_test.go` | WP-142 |
| MET-16 | B/G1, Teil P2/G2 | PASS | `backend/internal/citype/compose_test.go` | WP-119 |
| MET-54 | B/G1, Teil P2/G2 | PASS | `backend/internal/citype/export_audit_integration_test.go` | WP-134 |
| MON-01 | [B] | PASS | `backend/internal/monitoring/timescale_rls_spike_integration_test.go`, `backend/internal/monitoring/pg_store_integration_test.go` | WP-039, WP-040 |
| MON-02 | [B] | PASS | `backend/internal/monitoring/monitoring_test.go` | WP-190 |
| NET-01 | [B] | PASS | `backend/internal/ipam/vlan_integration_test.go`, `backend/internal/discovery/last_seen_test.go`, `backend/internal/ipam/interface_integration_test.go` | WP-081, WP-090, WP-123 |
| NET-02 | [B] | PASS | `backend/internal/ipam/vlan_integration_test.go`, `backend/internal/ipam/vrf_integration_test.go` | WP-081, WP-082 |
| NET-03 | [B] | PASS | `backend/internal/ipam/vrf_integration_test.go`, `backend/internal/ipam/ip_integration_test.go` | WP-082, WP-124 |
| NET-04 | [B] | offen | keiner; Ausnahme in `docs/acceptance/g1-ausnahmen.csv` (A-02) | WP-450 |
| NET-05 | [B] | PASS | `backend/internal/ipam/handler_test.go` | WP-125 |
| NET-08 | [B] | PASS | `backend/internal/ipam/vlan_integration_test.go` | WP-081 |
| NFR-01 | [B], Teilumfang [P5] | PASS | `backend/internal/discovery/identity_test.go`, `backend/internal/discovery/scale_integration_test.go`, `tests/load/` | WP-086, WP-205, WP-206 |
| NFR-02 | [B] | PASS | `backend/internal/topology/impact_test.go`, `tests/load/`, `backend/internal/topology/querybudget_integration_test.go` (+1) | WP-177, WP-206, WP-335 |
| NFR-03 | [B], Teilumfang [P5] | PASS | `backend/internal/discovery/consumer_test.go`, `collector/collectorcmd/schedule_test.go`, `tests/load/` (+4) | WP-085, WP-154, WP-206, WP-332 … |
| NFR-04 | [B], Teilumfang [P5] | PASS | `edgecore/buffer/buffer_test.go`, `backend/internal/maintenance/status_test.go` | WP-062, WP-063 |
| NFR-05 | [B] | PASS | `tests/backup-config.test.sh`, `tests/restore-pitr.sh` | WP-100, WP-101 |
| NFR-06 | [B] | PASS | `backend/internal/jobs/retention_integration_test.go` | WP-135 |
| NFR-07 | [B] | PASS | `frontend/e2e/a11y.spec.ts` | WP-193 |
| NFR-08 | [B] | offen | keiner; Ausnahme in `docs/acceptance/g1-ausnahmen.csv` (A-07) | WP-450 |
| NFR-09 | [B] | offen | `docs/betrieb/referenzmodell.md` (Datenresidenz); Ausnahme in `docs/acceptance/g1-ausnahmen.csv` (A-11) | WP-105 |
| NFR-10 | [Q] | PASS | `backend/cmd/seed/seed_integration_test.go`, `backend/internal/ci/querybudget_integration_test.go`, `tests/load/` (+1) | WP-203, WP-204, WP-206, WP-334 |
| NTF-01 | [B] | PASS | `backend/internal/notify/channel_test.go` | WP-137 |
| NTF-03 | [B] | PASS | `backend/internal/notify/recipients_integration_test.go` | WP-138 |
| NTF-04 | [B] | PASS | `backend/internal/notify/notifier_test.go` | WP-139 |
| NTF-05 | [B], Zusätze [P2], [P3] | PASS | `backend/internal/maintenance/status_test.go`, `backend/internal/notify/events_integration_test.go` | WP-063, WP-140 |
| OPS-01 | [B] | PASS | `backend/internal/server/health_test.go` | WP-097 |
| OPS-02 | [B], [P3], [A] | PASS | `backend/internal/config/config_test.go` | WP-098 |
| OPS-03 | [B], [P5] | PASS | `.github/workflows/ci.yml`, `backend/internal/jobs/scheduler_test.go` | WP-099, WP-131 |
| OPS-04 | [B], [P5] | PASS | `tests/backup-config.test.sh`, `tests/restore-pitr.sh` | WP-100, WP-101 |
| OPS-05 | [B] | PASS | `backend/internal/tenant/rls/system_worker_integration_test.go`, `backend/internal/server/workers_test.go`, `backend/internal/notify/events_integration_test.go` | WP-022, WP-103, WP-140 |
| OPS-06 | [Q] | PASS | `edgecore/buffer/buffer_test.go`, `tests/outage/` | WP-062, WP-104 |
| OVR-01 | [B] | PASS | `backend/internal/ci/mergepatch_test.go`, `backend/internal/override/decide_test.go`, `backend/internal/ci/effective_view_integration_test.go` | WP-057, WP-058, WP-060 |
| OVR-02 | [B] | PASS | `backend/internal/workflow/executor_test.go` | WP-059 |
| PRI-11 | [B] | PASS | `docs/betrieb/referenzmodell.md` | WP-105 |
| RBA-01 | [B] | PASS | `backend/internal/server/authz_catalog_test.go` | WP-068 |
| RBA-02 | [B] | PASS | `backend/internal/permission/role_matrix_integration_test.go` | WP-046 |
| RBA-03 | [B] | PASS | `backend/internal/permission/scope_integration_test.go` | WP-009 |
| RBA-04 | [B] | PASS | `backend/internal/server/authz_test.go` | WP-045 |
| RBA-08 | [B] | PASS | `backend/internal/user/service_account_integration_test.go` | WP-069 |
| RCK-01 | [B] | PASS | `backend/internal/rack/rules_integration_test.go` | WP-126 |
| RCK-02 | [B] | PASS | `backend/internal/rack/handler_test.go` | WP-175 |
| RCK-03 | [B] | PASS | `backend/internal/rack/mount_location_integration_test.go` | WP-078 |
| REC-01 | [B] | PASS | `backend/internal/discovery/consumer_test.go` | WP-085 |
| REC-02 | [B] | PASS | `backend/internal/discovery/identity_test.go` | WP-086 |
| REC-03 | [B] | PASS | `backend/internal/override/decide_test.go` | WP-058 |
| REC-05 | [B] | PASS | `backend/internal/override/decide_test.go` | WP-084 |
| REC-06 | [B] | PASS | `backend/internal/reviews/conflict_test.go` | WP-089 |
| REC-07 | [B] | PASS | `backend/internal/discovery/last_seen_test.go` | WP-090 |
| REC-08 | [B] | PASS | `backend/internal/reviews/service_test.go` | WP-088 |
| REC-09 | [B] | PASS | `backend/internal/lifecycle/offline_integration_test.go` | WP-157 |
| REC-10 | [B] | PASS | `backend/internal/ci/provenance_integration_test.go` | WP-198 |
| REC-12 | [B] | PASS | `backend/internal/override/decide_test.go`, `backend/internal/reviews/conflict_test.go` | WP-058, WP-089 |
| REL-01 | [B] | PASS | `backend/internal/relationship/projection_integration_test.go`, `backend/internal/relationshiptype/seed_integration_test.go` | WP-079, WP-127 |
| REL-02 | [B] | PASS | `backend/internal/relationship/pg_repository_integration_test.go` | WP-128 |
| REL-03 | [B] | PASS | `backend/internal/relationship/suppression_integration_test.go` | WP-160 |
| REL-04 | [B] | PASS | `backend/internal/relationship/suppression_integration_test.go` | WP-160 |
| REL-05 | [B] | PASS | `backend/internal/relationship/suppression_integration_test.go` | WP-160 |
| REL-08 | [B] | PASS | `backend/internal/relationship/cleanup_integration_test.go` | WP-159 |
| REP-01 | [B] | PASS | `backend/internal/database/migrations_roundtrip_integration_test.go`, `backend/internal/rules/rules_test.go`, `backend/internal/jobs/service_test.go` (+2) | WP-006, WP-118, WP-129, WP-137 … |
| SEC-01 | [B] | PASS | `backend/internal/credential/handler_test.go` | WP-047 |
| SEC-05 | [B] | PASS | `backend/internal/pii/pii_integration_test.go`, `backend/internal/pii/surrogate_integration_test.go` | WP-240, WP-241 |
| SEC-06 | [B] | PASS | `backend/internal/identity/session_test.go`, `backend/internal/platform/crypto/crypto_test.go` | WP-066, WP-141 |
| SEC-07 | [B] | PASS | `backend/internal/operator/operator_test.go` | WP-070 |
| SEC-08 | [B] | PASS | `backend/internal/platform/egress/egress_test.go` | WP-049 |
| SEC-10 | [B] | PASS | `backend/internal/graphqlbff/graphqlbff_test.go` | WP-184 |
| SEC-11 | [Q] | PASS | `docs/security/abhaengigkeitsscans.md`, `docs/security/review-g1.md` | WP-004, WP-209 |
| SEQ-01 | [Q] | PASS | `tests/rep01-structure.test.sh` | WP-213 |
| SIM-01 | [B], Teilumfang [P2] | PASS | `.github/workflows/ci.yml`, `backend/cmd/seed/seed_integration_test.go` | WP-106, WP-203 |
| SIM-02 | [B] | PASS | `collector/plugins/simulate/simulate_test.go` | WP-162 |
| SRC-01 | [B] | PASS | `backend/internal/tenant/rls/ci_children_integration_test.go`, `backend/internal/search/scope_integration_test.go`, `backend/internal/search/search_test.go` | WP-025, WP-032, WP-186 |
| TEC-06 | [B], [Q] Spike | PASS | `backend/internal/monitoring/timescale_rls_spike_integration_test.go`, `backend/internal/locations/pg_repository_integration_test.go`, `backend/internal/monitoring/pg_store_integration_test.go` | WP-039, WP-026, WP-040 |
| TEC-09 | [B] | PASS | `tests/install-smoke-health.test.sh`, `tests/install-preflight.test.sh`, `tests/install-cloud-helpers.test.sh` | WP-092, WP-093, WP-094 |
| TEC-13 | [B] | PASS | `backend/internal/notify/channel_test.go` | WP-137 |
| TEC-14 | [B] | PASS | `backend/internal/jobs/queue_test.go` | WP-130 |
| TEC-15 | [B] | PASS | `edgecore/update/update_test.go` | WP-102 |
| TEN-01 | [B], Reseller [P4] | PASS | `backend/internal/tenantapi/handler_test.go`, `backend/internal/locations/hierarchy_integration_test.go` | WP-107, WP-109 |
| TEN-02 | [B] | PASS | `backend/internal/tenant/rls/global_rows_integration_test.go`, `backend/internal/tenant/rls/ci_children_integration_test.go`, `backend/internal/locations/pg_repository_integration_test.go` | WP-024, WP-025, WP-026 |
| TEN-03 | [B] | PASS | `backend/internal/tenant/rls/catalog_integration_test.go`, `backend/internal/database/role_check_integration_test.go` | WP-007, WP-064 |
| TEN-04 | [B] | PASS | `backend/internal/database/tenant_scope_test.go`, `backend/internal/permission/scope_integration_test.go`, `backend/internal/tenant/rls/site_policies_integration_test.go` | WP-008, WP-009, WP-027 |
| TEN-05 | [B] | PASS | `backend/internal/tenant/rls/catalog_integration_test.go`, `backend/internal/tenant/rls/client_policies_integration_test.go`, `backend/internal/tenant/rls/global_rows_integration_test.go` (+4) | WP-007, WP-023, WP-024, WP-022 … |
| TEN-06 | [B] | PASS | `backend/internal/database/tenant_scope_test.go`, `backend/internal/ci/ci_scope_integration_test.go`, `backend/internal/citype/citype_scope_integration_test.go` (+51) | WP-008, WP-010, WP-011, WP-012 … |
| TEN-09 | [Q] | offen | `backend/internal/tenant/rls/catalog_integration_test.go`, `backend/internal/tenant/rls/matrix_integration_test.go`, `backend/internal/tenant/rls/channel_matrix_integration_test.go` (+6) | WP-007, WP-065, WP-208, WP-340 … |
| TEN-10 | [B] | PASS | `backend/internal/ipam/vrf_integration_test.go` | WP-082 |
| TLC-01 | [B] | PASS | `backend/internal/signup/service_test.go`, `backend/internal/signup/protect_test.go` | WP-111, WP-112 |
| TLC-02 | [B], Reseller [P4] | PASS | `backend/internal/operator/orgs_test.go` | WP-113 |
| TLC-03 | [B] | PASS | `backend/internal/user/invitation_integration_test.go` | WP-114 |
| TLC-04 | [B] | PASS | `backend/internal/identity/deactivation_integration_test.go` | WP-042 |
| TOP-01 | [B] | PASS | `backend/internal/discovery/topology_test.go` | WP-158 |
| TOP-02 | [B] | PASS | `backend/internal/relationship/cleanup_integration_test.go` | WP-159 |
| TST-01 | [B], Teilumfang [P2] | PASS | `backend/internal/discovery/tst01_integration_test.go`, `backend/internal/reviews/tst01_integration_test.go`, `backend/internal/events/tst01_integration_test.go` (+6) | WP-202 |
| TST-02 | [B] | PASS | `.github/workflows/ci.yml`, `backend/internal/tenant/rls/matrix_integration_test.go`, `backend/internal/tenant/rls/channel_matrix_integration_test.go` | WP-003, WP-065, WP-208 |
| TST-03 | [B], Teilumfang [P2] | PASS | `frontend/e2e/onboarding.spec.ts`, `frontend/e2e/ci.spec.ts` | WP-200, WP-201 |
| TST-04 | [Q] | PASS | `.github/workflows/ci.yml`, `docs/security/abhaengigkeitsscans.md` | WP-001, WP-002, WP-003, WP-004 |
| UI-01 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-02 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-03 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-04 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-05 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-06 | [B] | PASS | `frontend/src/pages/TopologyPage.test.tsx` | WP-195 |
| UI-07 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-08 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-09 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-10 | [B] | PASS | `frontend/src/components/CommandPalette.test.tsx`, `frontend/src/components/ScopeSwitcher.test.tsx` | WP-192, WP-191 |
| UI-16 | [B] | PASS | `frontend/src/pages/ReviewInboxPage.test.tsx`, `frontend/src/components/NotificationCenter.test.tsx` | WP-196, WP-197 |
| UI-17 | [B] | PASS | `frontend/src/pages/OnboardingPage.test.tsx` | WP-199 |
| UI-18 | [B] | PASS | `frontend/e2e/a11y.spec.ts`, `frontend/src/i18n/catalog.test.ts` | WP-193, WP-194 |
| VIE-01 | [B] | PASS | `backend/internal/savedview/savedview_integration_test.go` | WP-188 |
| WHK-01 | [B] | PASS | `backend/internal/webhook/whk01_test.go`, `backend/internal/webhook/whk01_integration_test.go` | WP-213 |
