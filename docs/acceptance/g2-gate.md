# Gate G2 – Checkliste

WP-331 (`g2-gate`). Die Checkliste prüft die Gate-Regeln GATE-01, GATE-02
und GATE-05 sowie SEQ-01, DOD-01 und GLO-11
(`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md`, Abschnitt
„Gates“) für Phase 2. Umfang von GATE-02 sind alle IDs aus
`docs/plan/phase2/abdeckung.csv` und alle [Q]-IDs außer GATE-03 (nur G1) und
GATE-04 (GA). Die Nachweise sind Tests und die Traceability
(`docs/traceability.csv`); diese Checkliste ist nie Nachweis einer
Traceability-Zeile (F-121, WP-450).

**Die Gate-Entscheidung trifft der Auftraggeber.** Diese Checkliste hat sie
vorbereitet: Sie belegt, was bestanden ist, und führt die Abweichungen und
Ausnahmen auf, die der Auftraggeber bewerten musste (Abschnitt „Ergebnis“).
Seine Entscheidung vom 2026-11-09 steht im Abschnitt „Freigabe des
Auftraggebers“; die IDs, die nicht PASS sind, stehen als Ausnahmen in
`docs/acceptance/g2-ausnahmen.csv` (Format wie
`docs/acceptance/g1-ausnahmen.csv`).

Prüfungen:

- `backend/internal/traceability` (`TestTraceabilityFile`) läuft im CI-Job
  `traceability` seit WP-331 mit `TRACEABILITY_REQUIRE_COMPLETE=B,Q,P2`
  (E-48): Jede ID aus `docs/plan/phase2/abdeckung.csv` braucht eine Zeile
  ihres `abschluss_wp`, jedes nicht optionale WP des Phase-2-Plans alle
  Zeilen seines Plans (`CheckWorkPackages`). Ausgenommen sind nur die IDs
  mit `abschluss_wp` ‚–‘ (E-37) und das optionale WP-293 (TKT-03, [O]).
  [B]/[Q] blockieren wie seit G1.
- `tests/rep01-structure.test.sh` prüft G1 unverändert und zusätzlich die
  G2-Stufe (Regeln `g2_*`):
  - (a) die Abschnitte GATE-01, GATE-02 (mit „N/P und WARN“), GATE-05,
    SEQ-01, DOD-01 und GLO-11 dieser Checkliste und das Ergebnis;
  - (b) GATE-01: keine offene Critical-Zeile eines SR- oder PT-Befunds in
    `docs/security/review-phase2.md` (Severity normalisiert wie in G1:
    Fettdruck, Groß-/Kleinschreibung, Kritisch/Hoch; geschlossen mit
    „behoben“ oder „geschlossen“) und keine offene Zeile in der
    Befundtabelle von GATE-01 (zulässig „geschlossen“, „behoben“ und für
    P3/P4-Reste „abgeschaltet (E-03)“), jede Critical-ID der Quellen (4)
    bis (7) mit Zeile; `--final` verlangt zusätzlich, dass kein SR-Befund
    mit Critical oder High in `review-phase2.md` offen ist;
  - (c) GATE-02: jede Zeile der Tabelle „N/P und WARN“ mit Status und
    Begründung, jede ID des Anhangs, die nicht PASS ist, mit Begründung im
    Abschnitt GATE-02, und keine Zeile in `docs/traceability.csv`, die diese
    Checkliste als Nachweis nennt. Die Regeln der G1-Stufe zu Erklärung und
    Lieferungen (`exceptions`, `final_exceptions`, `delivery_*`; E-76)
    gelten hier nicht: Nach E-103 ist „PASS (Erklärung des Auftraggebers vom
    2026-10-15)“ zulässig;
  - (d) GATE-05: keine Zeile `…;[O];G2;…` in `docs/traceability.csv`, kein
    `O` in der Stufe von `TRACEABILITY_REQUIRE_COMPLETE`, WP-293 trägt im
    Phase-2-Plan „G2-Umfang: optional“, und der Abschnitt GATE-05 nennt
    TKT-03 und SRC-03;
  - (e) SEQ-01 nennt die Schritte der Phase 2;
  - (f) GLO-11: der Job `traceability` setzt `TRACEABILITY_REQUIRE_COMPLETE`
    mit der Stufe `P2` und hat kein `continue-on-error`;
  - das Ergebnis „bestanden“ verlangt jede GATE-Zeile des Anhangs PASS;
    `--final` verlangt außerdem keinen Platzhalter eines RC-Laufs und
    keinen Vermerk, dass ein Lauf aussteht;
  - (g) Ausnahmen: Jede ID aus `docs/acceptance/g2-ausnahmen.csv` (Spalten
    `id;art;grund;entscheidung;lieferung`, Art `erklaerung`, `luecke` oder
    `np` wie in G1) hat eine Zeile im Anhang und ist nicht PASS. Das
    Ergebnis „bedingt bestanden“ (Freigabe des Auftraggebers, wie E-76
    Option 2 in G1) verlangt (1) den Abschnitt „Freigabe des
    Auftraggebers“ mit der wörtlichen Entscheidung und jeder ID der Art
    `erklaerung`, (2) jede ID, die nicht PASS ist, als Ausnahme der Art
    `erklaerung` oder `np` und keine Ausnahme der Art `luecke`, (3) in der
    Spalte `lieferung` jeder Zeile der Art `erklaerung` eine Frist
    („Frist …“) und (4) jede GATE-Zeile des Anhangs PASS oder selbst eine
    Ausnahme der Art `erklaerung`. Ein offenes Critical schließt (b) in
    jedem Modus aus.
- Selbsttests auf den Fixtures `tests/fixtures/rep01/g2/` (Ergebnis
  „bestanden“) und `tests/fixtures/rep01/g2-conditional/` (Ergebnis
  „bedingt bestanden“) (`rep01_g2_*`) belegen jede Regel der G2-Stufe in
  beiden Modi.

Stand: 2026-11-09. Produktstand des Release-Kandidaten ist `69e6668d`
(`main` nach PR #152; der letzte Produktcode kam mit PR #150, `6d019370`;
PR #151 und #152 ändern nur Lastskripte, Workflows der Lastläufe und
Dokumentation). Release-Kandidat G2 ist `e17954ca`, `main` nach dem Merge
von WP-327 und WP-331 (PR #153); beide ändern nur Dokumentation, Tests und
die CI-Konfiguration. Die Läufe auf `e17954ca` stehen im Abschnitt „Läufe
auf dem Release-Kandidaten“.

Nachtrag 2026-11-09 (Gate-Entscheidung): Der Auftraggeber hat G2 am
2026-11-09 freigegeben (Abschnitt „Freigabe des Auftraggebers“). Das
Ergebnis lautet „bedingt bestanden“: NFR-02, RBA-07, SEC-11 und GATE-02 sind
nicht PASS und stehen als vom Auftraggeber akzeptierte Ausnahmen mit Frist
in `docs/acceptance/g2-ausnahmen.csv`; „bestanden“ verlangte jede GATE-Zeile
PASS. TST-04 und OPS-06 sind mit den Läufen auf `e17954ca` belegt.
`docs/acceptance/g1-gate.md` ändert dieser Nachtrag nicht (D-8).

## Ergebnis

**Ergebnis: bedingt bestanden.** G2 ist durch den Auftraggeber am
2026-11-09 freigegeben (Abschnitt „Freigabe des Auftraggebers“). Bedingt,
weil NFR-02, RBA-07, SEC-11 und damit GATE-02 nicht PASS sind; sie stehen
als vom Auftraggeber akzeptierte Ausnahmen der Art `erklaerung` mit Frist in
`docs/acceptance/g2-ausnahmen.csv`, die N/P-IDs dort mit der Art `np`.
Release-Kandidat ist `e17954ca`.

Bestanden (belegt):

- GATE-01: kein offenes Critical in allen Quellen (Abschnitt GATE-01),
  Scans grün auf `69e6668d` (CI-Lauf 42675826333) und auf dem
  Release-Kandidaten `e17954ca` (CI-Lauf 42695897709).
- GATE-05: [O]-Punkte ohne Gate-Wirkung.
- SEQ-01: Merge-Folge der Phase 2 ohne Verletzung einer WP-Voraussetzung
  (Abschnitt SEQ-01).
- DOD-01 und GLO-11: Traceability mit `B,Q,P2` ohne fehlende Zeile, im
  CI-Lauf 42695897709 auf `e17954ca` grün, jedes WP des Phase-2-Plans mit
  Commit.
- TST-04: CI-Lauf 42695897709 auf `e17954ca`, alle 30 Jobs grün.
- OPS-06: Ausfallsuite, Lauf 42695925438 auf `e17954ca` grün.
- Install-Smoke (INS-01, INS-04) auf `e17954ca`: Lauf 42695897334 grün.
- Release-Konfiguration G2 (WP-830) und die Nachweise von E2E, Last,
  Sicherheitsreview und Abnahme auf Commits mit WP-830.
- 96 von 113 IDs des Anhangs PASS, 13 N/P mit Begründung, 4 offen
  (NFR-02, RBA-07, SEC-11, GATE-02), alle als Ausnahmen freigegeben.

Nicht auf dem Release-Kandidaten gelaufen ist `gate-final.yml` (nur auf
Tags `v*` und von Hand); sein letzter Lauf 41514357730 ist auf `201f6fbe`
rot. Der lokale Lauf von `tests/rep01-structure.test.sh --final` auf dem
Stand dieser Eintragung ist in allen G2-Regeln grün und bleibt rot nur in
Regeln der G1-Stufe, weil `docs/acceptance/g1-gate.md` bewusst auf „offen“
steht (Abschnitt „Läufe auf dem Release-Kandidaten“).

### Freigabe des Auftraggebers

Entscheidung des Auftraggebers vom 2026-11-09 (wörtlich): „Gate G2 ist
freigegeben, trag die Entscheidung ein“.

Die Freigabe gilt für G2 mit dem Release-Kandidaten `e17954ca`. Sie nimmt
die offenen Punkte als Ausnahmen bzw. Restrisiken an; ein Nachweis wird
dadurch nicht ersetzt, die IDs bleiben „offen“ (wie E-76 in G1):

- NFR-02 (D-1, D-2, D-3), RBA-07 (D-4), SEC-11 (D-4, D-6) und GATE-02 (folgt
  aus ihnen) als Ausnahmen der Art `erklaerung` in
  `docs/acceptance/g2-ausnahmen.csv`;
- die N/P-IDs MET-20, MET-30 bis MET-34, MET-40 bis MET-44, NFR-10 und
  TKT-03 als Ausnahmen der Art `np`;
- D-1 bis D-9 als akzeptierte Abweichungen und Restrisiken nach der Tabelle
  unten.

TST-04 und OPS-06, beim Stand vor der Freigabe offen, sind mit den Läufen
42695897709 und 42695925438 auf `e17954ca` belegt und PASS.

Die Fristen sind die der Quellen (Termin „vor GA“ im Sicherheitsreview
Phase 2, Referenzhost vor GA nach E-58 und SR-06, Vorläufigkeit der
Lastschwellen nach E-146); der Auftraggeber hat mit der Freigabe keine
eigenen Fristen genannt. Die Folge-WPs sind Vorschläge; angelegt ist noch
keines.

| # | Behandlung nach der Freigabe | Frist | Folge-WP-Vorschlag |
|---|---|---|---|
| D-1 | akzeptiert: NFR-02 gilt für G2 gegen den vorläufigen Budgetsatz G2 (E-146); Ausnahme NFR-02 | vor GA | V-1 `g2-search-rework` bis V-8 `g2-view-presets` (`docs/acceptance/lasttest-g2.md`) |
| D-2 | akzeptiert: Lastarchive nur als GitHub-Artefakte | innerhalb der Aufbewahrungsfrist der Artefakte | Übernahme nach `tests/load/README.md` |
| D-3 | akzeptiert: kein lokaler Lastlauf, kein Referenzhost | vor GA (E-58, SR-06) | Retest auf dem Referenzhost |
| D-4 | akzeptiert: SR-58, SR-59, SR-60 (Medium) offen; Ausnahmen RBA-07 und SEC-11 | vor GA (Review) | ein Folge-WP für SR-58 bis SR-60 (die im Review genannten Nummern WP-854 und WP-855 sind anders vergeben) |
| D-5 | akzeptiert: Low SR-50, SR-61 bis SR-63 offen | vor GA (Review), SR-50 ohne Termin | Folge-WP je Befund nach Review |
| D-6 | akzeptiert: Review-Stand `f74831fc`, keine Fortschreibung vor G2; Teil der Ausnahme SEC-11 | vor GA | Fortschreibung des Sicherheitsreviews Phase 2 für PR #150 |
| D-7 | akzeptiert als bekannte Abweichung: `listAssetCIs` bleibt `x-status: planned` | vor GA | Gerüst `GET /api/v1/assets/{id}/cis` entfernen (Nachtrag) |
| D-8 | akzeptiert: Phase 2 vor dem GO von WP-600; WP-600 gilt nach der Entscheidung vom 2026-11-09 als erledigt, `docs/acceptance/g1-gate.md` bleibt unverändert auf „offen“ | – | Eintrag der G1-Gate-Entscheidung in `g1-gate.md` (nicht Teil dieser Freigabe) |
| D-9 | Nachweis nach E-103, keine Ausnahme | – | – |

### Abweichungen und Ausnahmen (D-1 bis D-9)

Diese Abweichungen und Ausnahmen sind nicht durch einen Test zu schließen.
Der Auftraggeber hat sie mit der Freigabe vom 2026-11-09 bewertet (Tabelle
oben); die Spalte „Stand“ gibt den Stand vor der Freigabe wieder:

| # | Abweichung oder Ausnahme | Betrifft | Stand | Quelle |
|---|---|---|---|---|
| D-1 | **Lastschwellen G2 vorläufig (E-146).** Der Lastnachweis hält jedes Budget nur im vorläufigen Budgetsatz G2. Gegen NFR-02 im Wortlaut (Satz G1) verfehlen im Median 14 Latenz- und Dauerbudgets ihre Schwelle: `list_name_filter`, `global_search`, `bulk_500_duration`, `bulk_fields_500_duration` (Job `api`), `list` und `list_sorted` bei 28.000 CIs, `asset_list`, `asset_list_restricted`, `ticket_list`, `search_fields`, `ci_attr_filter`, `ast_asset_filter`, `view_presets`, `relation_search` (Job `phase2`). | NFR-02 (offen), ABN-02 (PASS gegen Satz G2), NFR-10 | Folge-WP-Vorschläge V-1 `g2-search-rework` (mit Neubewertung von E-133), V-2 `g2-name-trgm`, V-3 `g2-graph-cache`, V-4 `g2-list-count` (Erweiterung von E-104), V-5 `g2-override-projection`, V-6 `g2-bulk-batch`, V-7 `g2-ingest-throughput`, V-8 `g2-view-presets`; noch kein WP angelegt | `docs/acceptance/lasttest-g2.md`, Abschnitte „Bewertung für G2“ und „Offene Punkte“; E-146 |
| D-2 | **Lastarchive nicht im Repository.** Die Archive `load-archive-<Lauf-ID>` der Läufe 42675878596, 42675885412 und 42675891357 liegen nur als GitHub-Artefakte (Aufbewahrungsfrist); CPU/RAM je Lauf (E-58) sind nicht belegt. Die Bewertung von `load-evidence.yml` (Lauf 42683419968) hat die vollständigen Archive mit Prüfsumme gelesen. | NFR-02, NFR-03, ABN-02 | Übernahme nach `tests/load/README.md` innerhalb der Aufbewahrungsfrist | `docs/acceptance/lasttest-g2.md`, „Quelle der Werte“ |
| D-3 | **Lokaler Lastlauf (4 vCPU) und Query-Budget-Tests mit `RETICORA_PERF_BUDGET=1` nicht durchgeführt;** dedizierter Referenzhost (SR-06) bleibt bis GA offen. | ABN-02, NFR-10 | Retest auf dem Referenzhost vor GA (E-58) | `docs/acceptance/lasttest-g2.md`, „Weitere Punkte“ |
| D-4 | **Offene Medium-Befunde des Sicherheitsreviews:** SR-58 (berechnete CI-Attribute legen Asset-Felder an den Feldrechten vorbei offen), SR-59 (Formularzustände als Orakel für unlesbare Felder), SR-60 (Fremdimages des Betriebs ohne Digest und Scan, Keycloak 24). Termin laut Review „vor GA“. Die im Review vorgeschlagenen Nummern WP-854 und WP-855 sind inzwischen anders vergeben (WP-854 `g2-default-client`, WP-855 `g2-client-admin`); ein Folge-WP für SR-58 bis SR-60 ist nicht angelegt. | RBA-07 (offen), SEC-11 (offen) | offen, kein Folge-WP | `docs/security/review-phase2.md`, Befunde und Folge-WPs |
| D-5 | **Offene Low-Befunde:** SR-50 (404 statt 422 bei fremdem Verweis, API-03, kein Folge-WP für G2), SR-61 (Ticket-Anhänge ohne Feature `documents`, vor GA), SR-62 (Rücksprung nach dem Login, react-router-Advisories Moderate, vor GA), SR-63 (Kanalmatrix ohne eigene Zeilen für Entsorgung, SLA, Kalender, Ticket-Anhänge, Etiketten-Batches; vor GA). Info SR-64 bis SR-66 akzeptiert bzw. dokumentiert. | GQL-04 (Abgleich), keine Gate-ID offen | offen | `docs/security/review-phase2.md` |
| D-6 | **Prüfstand des Sicherheitsreviews `f74831fc`.** Danach kam Produktcode mit PR #150 (WP-854 bis WP-859, Lastregression Runde 4: Beziehungssuche, Bulk je CI in einer Transaktion). Das Review ist für diesen Code nicht fortgeschrieben; die Tests der Kanalmatrix und Feldrechte liefen im CI-Lauf 42675826333 auf `69e6668d` grün. | SEC-11, GATE-01 | Bewertung, ob eine Fortschreibung vor G2 nötig ist | `docs/security/review-phase2.md`, „Prüfstand“, „Offene Punkte für WP-331“ |
| D-7 | **Eine Operation mit `x-status: planned`:** `GET /api/v1/assets/{id}/cis` (`listAssetCIs`) steht noch als Gerüst in `api/openapi.yaml`; die CIs eines Assets liefert `GET /api/v1/cis?filter[asset_id]=` (E-38, WP-272). Der Plan verlangt zum G2-Release keine Operation mit `x-status: planned`; `api/openapi.yaml` gehört nicht zur Dateiliste von WP-331. | API-06 (PASS mit WP-830) | Gerüst entfernen (Nachtrag) oder als bekannte Abweichung hinnehmen | `docs/plan/phase2/implementierungsplan.md`, „Pfadvertrag“ |
| D-8 | **Phase 2 begann vor dem GO von WP-600 (`k-gate`).** PR #112 (2026-11-02) ist der erste Merge der Phase 2; `docs/acceptance/g1-gate.md` steht weiter auf „Ergebnis: offen“. Nach der Entscheidung des Auftraggebers vom 2026-11-09 gilt WP-600 als erledigt; einen Commit „WP-600:“ und die vier G1-Zeilen des Korrekturplans (GATE-01, GATE-02, GATE-05, SEQ-01 mit WP-600) gibt es nicht. | SEQ-01 | durch Entscheidung vom 2026-11-09 abgedeckt; Eintrag in `g1-gate.md` nicht Teil von WP-331 und nicht Teil der G2-Freigabe | Abschnitt SEQ-01 |
| D-9 | **Abnahme durch Erklärung (E-103).** AUT-05, AUT-06, NET-04, NFR-08, NFR-09, ABN-01 (a)/(b) und der Pentest sind durch die Erklärung des Auftraggebers vom 2026-10-15 nachgewiesen; Pentest-Befundliste, v2-Texte und Subprozessorenliste liegen nicht vor. | GATE-01, GATE-02 | nach E-103 Nachweis für G2 | Abschnitt GATE-02, „Abnahme durch Erklärung“ |

### Läufe auf dem Release-Kandidaten

Ein roter Lauf stünde hier mit Ursache und Folge-WP. Release-Kandidat ist
`e17954ca` (`main` nach PR #153 mit WP-327 und WP-331). Läufe auf
`69e6668d` gelten für den Produktstand des Release-Kandidaten.

| Workflow | Gegenstand | Commit | Lauf | Ergebnis |
|---|---|---|---|---|
| `ci.yml` (push auf `main`) | alle 30 Jobs, u. a. `backend` (Kanalmatrix TEN-09, DIS-08), `frontend`, `e2e` in sechs Browserprojekten (WP-324), `traceability`, `gate`, `govulncheck` (backend, collector, edgecore), `npm-audit`, `image-scan` (backend, collector, frontend, postgres), `image-scan-thirdparty` (clamd), `migrations`, `auditdefect` | `69e6668d` | 42675826333 | grün |
| `ci.yml` (push auf `main`) auf dem Release-Kandidaten | TST-04, alle 30 Jobs wie oben, `traceability` mit `B,Q,P2` | `e17954ca` | 42695897709 | grün (30 von 30 Jobs) |
| `install-smoke.yml` (push auf `main`) | Install-Smoke mit Onboarding-Suite (INS-01, INS-04), clamd im Profil `documents`, Real-Stack-Suite in sechs Browserprojekten, Ratchet, Reinstall, TLS | `e3c1aa7d` (Produktstand wie `69e6668d`) | 42661935039 | grün |
| `install-smoke.yml` (push auf `main`) | wie oben | `69e6668d` | 42675825844 | grün |
| `install-smoke.yml` (push auf `main`) auf dem Release-Kandidaten | wie oben; `release-mode` übersprungen (nur auf Tags) | `e17954ca` | 42695897334 | grün |
| `outage.yml` (von Hand) auf dem Release-Kandidaten | Ausfallsuite mit clamd- und SMTP-Szenario (OPS-06) | `e17954ca` | 42695925438 (vorher grün: 42550700633 auf `6ba79f54`) | grün |
| `gate-final.yml` | `tests/rep01-structure.test.sh --final`, `TestTraceabilityFile` | nicht auf `e17954ca` gelaufen (Auslöser nur Tag `v*` oder von Hand); letzter Lauf auf `201f6fbe` | 41514357730 | rot (G1); lokal auf dem Stand dieser Eintragung: alle G2-Regeln grün, rot nur in Regeln der G1-Stufe (`final_gate_rows`, `final_rc_runs` für `g1-gate.md`, `review-g1.md`, `g1-installation.md`, `docs/README.md`), weil `g1-gate.md` bewusst auf „offen“ steht (D-8); `TestTraceabilityFile` mit `B,Q,P2` lokal grün |
| `load.yml` (drei Läufe, `scenarios=all`) | Lastnachweis NFR-02, NFR-03, ABN-02 nach E-58 | `69e6668d` | 42675878596, 42675885412, 42675891357 | grün (Satz G2) |
| `load-evidence.yml` | Bewertung nach E-58 (Median), `gate` = `G2` | `69e6668d` | 42683419968 | grün, „alle Budgets gehalten“ (Satz G2) |

## Release-Konfiguration G2

- **Release-Standard:** WP-830 (`g2-release`, PR #146, `6ba79f54`). Ohne
  `RETICORA_MODULES` sind die P2-Module aktiv, P3, P4 und [A] bleiben
  abgeschaltet (E-03). Nachweis: `TestG2DefaultEnablesP2ModulesOnly`
  (`backend/internal/server/modules_test.go`) und der Paritätstest mit dem
  Release G2 (`backend/internal/server/parity_test.go`), beide im Job
  `backend` des CI-Laufs 42675826333 grün.
- **Freischaltung je Organisation:** P2-Funktionen sind nur über die
  Entitlements nach E-44 erreichbar (WP-244, WP-705, WP-706;
  `TestPhase2RouteGates` in
  `backend/internal/entitlement/enforce_integration_test.go`).
- **Nachweise auf Commits mit WP-830:**

| Nachweis | WP | Commit | Lauf bzw. Beleg |
|---|---|---|---|
| E2E Phase 2 (Mock-Backend) | WP-324 | `69e6668d` | CI-Lauf 42675826333, Jobs `e2e` (chromium, firefox, webkit, edge, mobile-chrome, mobile-safari) |
| Performance (`load.yml`, Release-Standard G2) | WP-327 | `69e6668d` | 42675878596, 42675885412, 42675891357; Bewertung 42683419968 |
| Sicherheitsreview | WP-329 | `f74831fc` (Kopf von PR #149, enthält PR #146) | `docs/security/review-phase2.md` |
| Abnahme ABN-01 (c) | WP-330 | PR #146 (`6ba79f54`), Commit nach WP-830 | `docs/acceptance/abn-01.md`, `frontend/e2e/metamodel-scenario.spec.ts` |
| Install-Smoke mit Onboarding-Suite (Release-Standard ohne `RETICORA_MODULES`) | – | `e3c1aa7d`; Release-Kandidat `e17954ca` | 42661935039; 42695897334 |

## GATE-01 – Kein offenes Critical

| Quelle | Critical offen | Nachweis |
|---|---:|---|
| (1) Sicherheitsreview Phase 2 (`docs/security/review-phase2.md`, SR-50 bis SR-66) | 0 | SR-51 (WP-828) und SR-56 (WP-852, Migration 000281) behoben; offen sind nur Medium (SR-58 bis SR-60), Low und Info (D-4, D-5) |
| (2) Abhängigkeits- und Image-Scans | 0 | CI-Lauf 42675826333 auf `69e6668d`: `govulncheck` (backend, collector, edgecore), `npm-audit`, `image-scan` (backend, collector, frontend, postgres), `image-scan-thirdparty` (clamd) grün; `security/scan-exceptions.txt` ohne Einträge (`review-phase2.md`, „Prüfungen“) |
| (3) Pentest G1 (`docs/security/pentest-g1.md`) | 0 laut Erklärung | PASS (Erklärung des Auftraggebers vom 2026-10-15): nach E-103 Nachweis auch für G2; eine Befundliste wird nicht nachgereicht |
| (4) Audit-Critical mit P2-Rest aus `docs/security/review-g1.md` (AST-01, AST-05, MGT-02, SRC-03) | 0 | Befundtabelle unten, je mit `abschluss_wp` aus `docs/plan/phase2/abdeckung.csv` (E-46) |
| (5) übrige Critical-IDs der Phase-2-Abdeckung (EXP-01, GQL-04, MET-14, MGT-01, SRC-01, SRC-04, TKT-01) | 0 | Befundtabelle unten |
| (6) Teil 03 (`docs/audit/03-datenmodell-standorte-metamodell.md`; konsolidiert mit WP-832 in `docs/audit/befunde.csv`) | 0 | MET-14, LOC-10, LOC-11, X-SCOPE, X-MANUAL geschlossen (Anhang „Teil 03“ von `docs/security/review-g1.md`) |
| (7) Critical-Reste P3/P4 (AGT-05, MGT-04, WFL-01, WFL-02) | 0 | abgeschaltet (E-03): Module im Release-Standard G2 aus, `TestG2DefaultEnablesP2ModulesOnly` (WP-830) |

| ID | Severity | Status | Nachweis |
|---|---|---|---|
| AST-01 | Critical | geschlossen | WP-740 (`abschluss_wp`); `backend/internal/asset/concurrency_integration_test.go`, `backend/internal/asset/asset_integration_test.go` |
| AST-05 | Critical | geschlossen | WP-743; `backend/internal/composition/quarantine_integration_test.go`, `backend/internal/ci/composition_integration_test.go` |
| MGT-02 | Critical | geschlossen | WP-278; `backend/internal/disposal/disposal_tx_integration_test.go` |
| SRC-03 | Critical | geschlossen | WP-315; `frontend/src/components/rules/RuleBuilder.test.tsx`; Textsyntax [O] ohne Gate-Wirkung (GATE-05) |
| EXP-01 | Critical | geschlossen | WP-791; `frontend/src/pages/ExportPage.test.tsx` |
| GQL-04 | Critical | geschlossen | WP-727; `backend/internal/graphqlbff/fieldrights_test.go` |
| MET-14 | Critical | geschlossen | WP-714; `backend/internal/ci/instance_lifecycle_integration_test.go`, `backend/internal/ci/instance_integration_test.go` (Teil 03, WP-832) |
| MGT-01 | Critical | geschlossen | WP-734; `backend/internal/document/scan_integration_test.go` |
| SRC-01 | Critical | geschlossen | WP-266; `backend/internal/search/fieldrights_integration_test.go` |
| SRC-04 | Critical | geschlossen | WP-789; `backend/internal/savedview/ast_related_integration_test.go` |
| TKT-01 | Critical | geschlossen | WP-767; `backend/internal/discovery/review_ticket_integration_test.go` |
| LOC-10 | Critical | geschlossen | WP-026, WP-053, WP-054, WP-473 (Teil 03, WP-832); `backend/internal/locations/pg_repository_integration_test.go`, `backend/internal/locations/tree_test.go` |
| LOC-11 | Critical | geschlossen | WP-055, WP-411, WP-473, WP-474, WP-701 (Teil 03, WP-832); `backend/internal/locations/move_integration_test.go` |
| X-SCOPE | Critical | geschlossen | WP-023, WP-026, WP-027, WP-029, WP-065, WP-340, WP-391, WP-393 (Teil 03, WP-832); `backend/internal/tenant/rls/client_policies_integration_test.go` |
| X-MANUAL | Critical | geschlossen | WP-056, WP-057, WP-058, WP-089, WP-714 (Teil 03, WP-832); `backend/internal/override/decide_test.go`, `backend/internal/ci/mergepatch_test.go` |
| AGT-05 | Critical | abgeschaltet (E-03) | P4, Modul im Release-Standard G2 aus; `TestG2DefaultEnablesP2ModulesOnly` (WP-830) |
| MGT-04 | Critical | abgeschaltet (E-03) | P3, Modul im Release-Standard G2 aus; `TestG2DefaultEnablesP2ModulesOnly` (WP-830) |
| WFL-01 | Critical | abgeschaltet (E-03) | P3, Modul im Release-Standard G2 aus; `TestG2DefaultEnablesP2ModulesOnly` (WP-830) |
| WFL-02 | Critical | abgeschaltet (E-03) | P3, Modul im Release-Standard G2 aus; `TestG2DefaultEnablesP2ModulesOnly` (WP-830) |

**Status:** PASS. Kein Critical offen. `tests/rep01-structure.test.sh`
verbietet eine offene Critical-Zeile eines SR- oder PT-Befunds in
`docs/security/review-phase2.md` und eine offene Zeile dieser Tabelle;
`--final` verlangt zusätzlich, dass kein SR-Befund mit Critical oder High
offen ist (heute keiner).

## GATE-02 – Alle Anforderungen der Phase sind PASS

Umfang: jede ID aus `docs/plan/phase2/abdeckung.csv` (112) und die [Q]-ID
NFR-10, die dort fehlt; GATE-03 (nur G1) und GATE-04 (GA) gehören nicht
dazu. Für gemischte Tags zählt der P2-Anteil. Der Status je ID steht im
[Anhang](#anhang-status-je-id). PASS heißt: Die Traceability-Zeile des
`abschluss_wp` liegt vor, ihr Test ist im CI-Lauf 42675826333 auf
`69e6668d` und im CI-Lauf 42695897709 auf dem Release-Kandidaten
`e17954ca` grün, und kein offener Befund berührt die ID.

Traceability (lokal, `TRACEABILITY_REQUIRE_COMPLETE=B,Q,P2`): „Phase 2: 0 of
112 IDs of docs/plan/phase2/abdeckung.csv without a row of their
abschluss_wp“, „Phase 2: 0 planned rows missing“; G1 unverändert
(214 von 219 [B]/[Q]-IDs mit Zeile, 9 Ausnahmen nach
`docs/acceptance/g1-ausnahmen.csv`).

**Status:** offen. Folgt aus der Tabelle „FAIL und offen“; nach der
Freigabe des Auftraggebers vom 2026-11-09 Ausnahme der Art `erklaerung`
(`docs/acceptance/g2-ausnahmen.csv`).

### N/P und WARN

WARN ist nur für SOLL-Punkte zulässig (E-27); Phase 2 hat keine
SOLL-Punkte mit WARN.

| ID | Status | Begründung |
|---|---|---|
| MET-20 | N/P | Anforderungstext nicht geliefert; nach E-37 gilt die Umsetzung nach den Querverweisen als Regel, `abschluss_wp` ‚–‘, von GATE-02 ausgenommen (E-48). |
| MET-30 | N/P | Anforderungstext nicht geliefert (Templates); Regel aus E-37, von GATE-02 ausgenommen (E-48). |
| MET-31 | N/P | Anforderungstext nicht geliefert (Templates); Regel aus E-37, von GATE-02 ausgenommen (E-48). |
| MET-32 | N/P | Anforderungstext nicht geliefert (Templates); Regel aus E-37, von GATE-02 ausgenommen (E-48). |
| MET-33 | N/P | Anforderungstext nicht geliefert (Templates); Regel aus E-37, von GATE-02 ausgenommen (E-48). |
| MET-34 | N/P | Anforderungstext nicht geliefert (Templates); Regel aus E-37, von GATE-02 ausgenommen (E-48). |
| MET-40 | N/P | Anforderungstext nicht geliefert (Regeln, berechnete Attribute); Regel aus E-37, von GATE-02 ausgenommen (E-48). |
| MET-41 | N/P | Anforderungstext nicht geliefert (Regeln, berechnete Attribute); Regel aus E-37, von GATE-02 ausgenommen (E-48). |
| MET-42 | N/P | Anforderungstext nicht geliefert (Regeln, berechnete Attribute); Regel aus E-37, von GATE-02 ausgenommen (E-48). |
| MET-43 | N/P | Anforderungstext nicht geliefert (Regeln, berechnete Attribute); Regel aus E-37, von GATE-02 ausgenommen (E-48). |
| MET-44 | N/P | Anforderungstext nicht geliefert (Regeln, berechnete Attribute); Regel aus E-37, von GATE-02 ausgenommen (E-48). |
| NFR-10 | N/P | Lasttest „vor G1“: G1-Nachweis in `docs/acceptance/lasttest-g1.md`; der G2-Lastnachweis steht unter NFR-02 und ABN-02 (`docs/acceptance/lasttest-g2.md`). |
| TKT-03 | N/P | [O], ohne Gate-Wirkung (GATE-05, E-47): WP-293 ist optional und gemergt (PR #127), Zeile mit Phase P2, keine G2-Zeile. |

### FAIL und offen

| ID | Status | Begründung |
|---|---|---|
| NFR-02 | offen | Nachgewiesen nur gegen den vorläufigen Budgetsatz G2 (E-146); gegen NFR-02 im Wortlaut 14 Budgets im Median verfehlt (D-1). Vom Auftraggeber am 2026-11-09 als Ausnahme freigegeben, Frist vor GA. |
| RBA-07 | offen | Feldrechte in allen Kanälen belegt (`TestFieldRightsChannelMatrix`, WP-730), aber SR-58 (berechnete Attribute umgehen die Feldrechte des Assets) und SR-59 (Formularzustände als Orakel) sind offen, Medium, ohne Folge-WP (D-4). Vom Auftraggeber am 2026-11-09 als Ausnahme freigegeben, Frist vor GA. |
| SEC-11 | offen | Sicherheitsreview Phase 2 nach ASVS L2 liegt vor (WP-329), Scans grün; SR-60 (Fremdimages ohne Digest und Scan) nennt SEC-11 und ist offen, Medium (D-4); Review-Stand `f74831fc` (D-6). Vom Auftraggeber am 2026-11-09 als Ausnahme freigegeben, Frist vor GA. |
| GATE-02 | offen | Folgt aus NFR-02, RBA-07 und SEC-11. Vom Auftraggeber am 2026-11-09 als Ausnahme freigegeben. |

Seit dem Stand vor der Freigabe PASS: OPS-06 (Ausfallsuite, Lauf
42695925438 auf `e17954ca` grün) und TST-04 (CI-Lauf 42695897709 auf
`e17954ca`, alle 30 Jobs grün).

### Abnahme durch Erklärung

Nach dem Beschluss vom 2026-10-28 (E-103) gilt die Erklärung des
Auftraggebers vom 2026-10-15 als Nachweis für G1 und G2; E-02 und E-35 sind
entschieden. Diese IDs sind [B] der Phase 1 und gehören nicht zur
Phase-2-Abdeckung; für G2 stehen sie hier.

| ID | Status | Begründung |
|---|---|---|
| AUT-05 | PASS (Erklärung des Auftraggebers vom 2026-10-15) | B4, E-02, E-103; der v2-Text wird nicht nachgereicht. |
| AUT-06 | PASS (Erklärung des Auftraggebers vom 2026-10-15) | B4, E-02, E-103; wie AUT-05. |
| NET-04 | PASS (Erklärung des Auftraggebers vom 2026-10-15) | B4, E-02, E-103; Kabelmodell unverändert. |
| NFR-08 | PASS (Erklärung des Auftraggebers vom 2026-10-15) | B3, E-28, E-103; `docs/security/pentest-g1.md` ohne Befundliste. Ein externer Pentest für G2 ist nicht gefordert (`review-phase2.md`). |
| NFR-09 | PASS (Erklärung des Auftraggebers vom 2026-10-15) | B6, E-35, E-103; `docs/legal/subprozessoren.md` verweist auf die Erklärung. |
| ABN-01 | PASS (Erklärung des Auftraggebers vom 2026-10-15) | Nur (a)/(b): B4, E-02, E-103. (c) ist mit WP-330 belegt (`frontend/e2e/metamodel-scenario.spec.ts`), (d) und (e) mit eingefrorenen Tests (`docs/acceptance/abn-01.md`). |

### Audit-High mit P2-Anteil

Die 21 nach E-03 verschobenen Audit-High-Befunde mit Status und
schließendem WP nach E-46; Stand aus dem Anhang „Critical/High-Befunde“ von
`docs/security/review-phase2.md` (WP-329). Keiner ist offen.

| ID | Status | WPs | Nachweis |
|---|---|---|---|
| AST-02 | geschlossen | WP-275, WP-277 | `backend/internal/assignment/transfer_integration_test.go` |
| AST-04 | geschlossen | G1: WP-077; Phase 2: WP-272, WP-273 | `backend/internal/asset/link_integration_test.go`, `backend/internal/ci/composition_integration_test.go` |
| AST-06 | geschlossen | G1: WP-022; Phase 2: WP-275, WP-277 | `backend/internal/reservation/concurrency_integration_test.go` |
| AST-07 | geschlossen | WP-274, WP-278 (dazu WP-275) | `backend/internal/movement/service_integration_test.go`, `backend/internal/assignment/transfer_integration_test.go` |
| IMP-08 | geschlossen | WP-297 | `backend/internal/topology/blast_test.go` |
| INV-01 | geschlossen | WP-287, WP-289 | `backend/internal/stocktake/stocktake_scope_integration_test.go` |
| INV-03 | geschlossen (P2-Anteil) | WP-287, WP-289 | `backend/internal/locations/barcode_integration_test.go` |
| LCY-02 | geschlossen | WP-263, WP-279 | `backend/internal/lifecycle/model_integration_test.go` |
| LCY-03 | geschlossen | WP-263, WP-279 | `backend/internal/lifecycle/guards_integration_test.go` |
| NET-09 | geschlossen | WP-299 | `backend/internal/ipam/reservation_integration_test.go` |
| OVR-03 | geschlossen | WP-306 | `backend/internal/ci/bulk_override_integration_test.go` |
| REL-09 | geschlossen | WP-296 | `backend/internal/relationship/history_integration_test.go` |
| STK-01 | geschlossen | WP-283, WP-284, WP-285 | `backend/internal/consumable/stock_integration_test.go`, `backend/internal/consumable/minlevel_integration_test.go` |
| STK-02 | geschlossen | WP-283, WP-284, WP-285 | `backend/internal/consumable/stock_integration_test.go` |
| STK-03 | geschlossen | WP-283, WP-284, WP-285 | `backend/internal/consumable/stock_integration_test.go`, `backend/internal/reservation/consumable_reservation_integration_test.go` |
| STK-04 | geschlossen | WP-274, WP-278 | `backend/internal/movement/service_integration_test.go` |
| STK-05 | geschlossen | WP-286 | `backend/internal/order/order_integration_test.go` |
| TKT-02 | geschlossen | WP-292 | `backend/internal/sla/sla_integration_test.go` |
| UI-11 | geschlossen | WP-313, WP-314, WP-315, WP-316 | `frontend/src/pages/CITypeAdminPage.test.tsx`, `frontend/src/pages/TemplatesPage.test.tsx`, `frontend/src/pages/FormBuilderPage.test.tsx` |
| UI-14 | geschlossen (P2-Anteil); P3, P4 und [A] späteren Phasen zugeordnet | WP-319, WP-320, WP-321, WP-322 | `frontend/src/pages/AssetListPage.test.tsx`, `frontend/src/pages/ConsumableDetailPage.test.tsx`, `frontend/src/pages/DocumentDetailPage.test.tsx`, `frontend/src/pages/ImportPage.test.tsx` |
| UI-15 | geschlossen | WP-318 | `frontend/src/components/pickers/pickers.test.tsx` |

### Abgleich offener Befunde

Ein offener SR-Befund unter High, der eine ID des Anhangs berührt, ist hier
abgeglichen; RBA-07 und SEC-11 stehen wegen SR-58 bis SR-60 auf „offen“.

| Befund | ID | Begründung |
|---|---|---|
| SR-61 | GQL-04 | Der Befund betrifft nur den REST-Pfad der Ticket-Anhänge; laut Befundtext prüft der GraphQL-Weg Modul und Feature `documents` (GQL-04 erfüllt). Low, Termin vor GA. |
| SR-50 | ABN-03 | Fremder und unbekannter Verweis antworten gleich (404), kein Existenzorakel und keine Exposition; Abweichung vom API-Vertrag (API-03), die `TestForeignReferencesP2` als Status festhält. |

## GATE-05 – Kein Gate wird durch [O]-Punkte beeinflusst

- TKT-03 ([O], Ticket-Provider-Schnittstelle): WP-293 trägt im Phase-2-Plan
  „G2-Umfang: optional“, ist gemergt (PR #127) und hat keine G2-Zeile;
  `CheckWorkPackages` nimmt es aus (E-47).
- SRC-03, Textsyntax ([O]): Der [O]-Anteil geht in keinen Blocker ein; der
  P2-Anteil (Klick-Builder) ist mit WP-315 geschlossen.
- `docs/traceability.csv` hat keine Zeile `…;[O];G2;…`, und die Stufe
  `B,Q,P2` in `TRACEABILITY_REQUIRE_COMPLETE` enthält kein `O`.
  `tests/rep01-structure.test.sh` prüft beides.

**Status:** PASS.

## SEQ-01 – Reihenfolge der Phase 2

SEQ-01 legt für Phase 2 fest: Metamodell → Verträge, Asset, Lager,
Stocktake, Dokumente → Ticketing Essential/Standard → Import, Bulk, Views →
G2; README §17 ergänzt die Feldrechte nach dem Metamodell. Der Plan ordnet
das als M2.1 Metamodell → M2.2 Feldrechte → M2.3 Dokumente → M2.4
Verträge/Assets → M2.5 Lager/Inventur (Stocktake) → M2.6 Ticketing → M2.8
Import/Bulk/Views → M2.9 Oberflächen → M2.10 Gate G2, mit M2.0b parallel zu
M2.1 und M2.7 parallel zu M2.3 bis M2.6. Innerhalb dieser Folge darf ein WP
starten, sobald seine Voraussetzungen gemergt sind (Plan, Reihenfolge,
Punkt 5).

Merge-Folge auf `main` (erster und letzter PR mit einem WP des
Meilensteins; Commits „WP-nnn:“ je Merge ausgewertet):

| Meilenstein | WPs | PRs | gemergt |
|---|---:|---|---|
| M2.0 Werkzeugkette, Verträge und Freigaben | 9 | #112, #113 | 2026-11-02 – 2026-11-03 |
| M2.0b G1-Folgearbeiten | 11 | #112, #113; WP-831, WP-832 in #149 | 2026-11-02 – 2026-11-09 |
| M2.1 Metamodell | 34 | #113 – #123 | 2026-11-03 – 2026-11-04 |
| M2.2 Feldrechte | 10 | #120, #123, #124 | 2026-11-04 |
| M2.3 Dokumente | 9 | #119 – #123 | 2026-11-03 – 2026-11-04 |
| M2.4 Verträge, Assets, Zuweisung, Lifecycle | 36 | #120 – #134 | 2026-11-04 – 2026-11-05 |
| M2.5 Lager, Bestellungen, Inventur (Stocktake), Barcode | 21 | #120, #127 – #135 | 2026-11-04 – 2026-11-05 |
| M2.6 Ticketing Essential und Standard | 10 | #119 – #127 | 2026-11-03 – 2026-11-04 |
| M2.7 Beziehungen, Graph und Netz | 10 | #114 – #132 | 2026-11-03 – 2026-11-05 |
| M2.8 Import, Bulk, Views, Suche, Export, BFF, Historie | 32 | #113 – #143 | 2026-11-03 – 2026-11-08 |
| M2.9 Oberflächen Phase 2 | 33 | #117 – #146 | 2026-11-03 – 2026-11-09 |
| M2.10 Seed, Qualität, Abnahme und Gate G2 | 27 | #123, #143 – #150; WP-327 und WP-331 nach #152 | 2026-11-04 – 2026-11-09 |
| M2.11 Lint-Altlast | 17 | #121, #122 | 2026-11-04 |

Prüfung der Voraussetzungen: Für alle 259 WPs mit Voraussetzungen ist jede
Voraussetzung, die ein Phase-2-WP ist, im selben oder einem früheren PR
gemergt (1.043 Paare, 0 Verstöße); innerhalb eines PRs liegt der Commit der
Voraussetzung vor dem des WPs, mit einer Ausnahme (WP-855 vor WP-854, beide
in PR #150). Jedes WP des Phase-2-Plans außer diesem hat einen Commit
„WP-nnn:“.

Abweichungen mit Begründung:

- **Überlappende Meilensteine:** Einzelne WPs späterer Meilensteine sind vor
  dem Abschluss früherer gemergt, z. B. WP-778 (M2.8, Idempotenz-Middleware)
  in #113, WP-290 und WP-292 (M2.6) in #119, WP-325 (M2.10, Seed-Rahmen) in
  #123. Zulässig nach Punkt 5 der Reihenfolge, weil ihre Voraussetzungen
  vorher gemergt waren (Prüfung oben).
- **M2.0b parallel zu M2.1, M2.7 parallel zu M2.3 bis M2.6:** wie im Plan
  vorgesehen; WP-831 und WP-832 sind erst vor WP-329 und WP-331 nötig und
  kamen mit #149.
- **GO von WP-600:** Phase 2 begann am 2026-11-02 vor dem GO von WP-600;
  nach der Entscheidung des Auftraggebers vom 2026-11-09 gilt WP-600 als
  erledigt (D-8).
- **Reihenfolge der Nachweise:** WP-830 (Release-Standard G2) vor WP-327,
  WP-329 und WP-330; G2 (dieses WP) zuletzt.

**Status:** PASS.

## DOD-01 und GLO-11

- **DOD-01:** Die PR-Vorlage `.github/pull_request_template.md` enthält die
  Definition of Done. Statt einer Stichprobe ist jedes WP geprüft: Jedes WP
  des Phase-2-Plans hat einen Commit „WP-nnn:“, und `CheckWorkPackages`
  meldet mit der Stufe `P2` 0 fehlende Planzeilen über alle nicht optionalen
  WPs. Die CI-Jobs `traceability`, `gate`, `backend`, `frontend`, `lint`
  und `e2e` sind auf `69e6668d` grün (CI-Lauf 42675826333) und auf dem
  Release-Kandidaten `e17954ca` (CI-Lauf 42695897709).
- **GLO-11:** Der CI-Job `traceability` setzt seit WP-331
  `TRACEABILITY_REQUIRE_COMPLETE=B,Q,P2` ohne `continue-on-error`. Lauf
  lokal auf diesem WP: 0 fehlende Zeilen über alle 112 IDs der
  Phase-2-Abdeckung und alle nicht optionalen Phase-2-WPs; [B]/[Q] weiter
  vollständig. Phase-1-Zeilen zählen nicht als P2-Nachweis (WP-243).

**Status:** PASS.

## Anhang: Status je ID

Erzeugt aus `docs/plan/phase2/abdeckung.csv` (Spalte `abschluss_wp`) und
`docs/traceability.csv` (Zeilen des `abschluss_wp`, höchstens drei
Nachweise je ID). PASS: wie im Abschnitt GATE-02; „offen“ und N/P mit
Begründung im Abschnitt GATE-02.

| ID | Tag | Status | Nachweis | WP |
|---|---|---|---|---|
| ABN-01 | [Q] | PASS | `frontend/e2e/metamodel-scenario.spec.ts` | WP-330 |
| ABN-02 | [Q] | PASS | `tests/load/` | WP-327 |
| ABN-03 | [Q], jeweilige Phase | PASS | `backend/internal/tenant/rls/foreign_refs_p2_integration_test.go` | WP-827 |
| API-06 | [P2]/[P3]/[P4]/[A] | PASS | `backend/internal/server/modules_test.go` | WP-830 |
| API-09 | [B] | PASS | `backend/internal/importer/job_test.go` | WP-301 |
| AST-01 | [P2] | PASS | `backend/internal/asset/concurrency_integration_test.go` | WP-740 |
| AST-02 | [P2] | PASS | `backend/internal/assignment/signature_legacy_integration_test.go` | WP-749 |
| AST-03 | [P2] | PASS | `backend/internal/discovery/asset_link_integration_test.go` | WP-741 |
| AST-04 | [P2] | PASS | `frontend/src/pages/AssetDetailPage.test.tsx` | WP-809 |
| AST-05 | [P2] | PASS | `backend/internal/composition/quarantine_integration_test.go` | WP-743 |
| AST-06 | [P2] | PASS | `backend/internal/reservation/concurrency_integration_test.go` | WP-277 |
| AST-07 | [P2] | PASS | `backend/internal/assignment/projection_integration_test.go` | WP-843 |
| AUD-07 | [B] | PASS | `backend/internal/relationship/history_integration_test.go` | WP-296 |
| AUD-08 | [P2] | PASS | `backend/internal/history/asset_replay_integration_test.go` | WP-795 |
| BLK-02 | [P2] | PASS | `backend/internal/asset/bulk_revert_integration_test.go` | WP-784 |
| CI-01 | [B], Teilfelder [P2]/[P3] | PASS | `backend/internal/ci/owner_integration_test.go` | WP-280 |
| CI-07 | [P2] | PASS | `frontend/src/pages/CIDetailPage.test.tsx` | WP-805 |
| CI-08 | [P2] | PASS | `backend/internal/ci/bulk_ops_integration_test.go` | WP-304 |
| DB-05 | nicht geliefert, CH12/PRI-10 | PASS | `backend/internal/contract/reference_integration_test.go` | WP-737 |
| DIS-08 | [Q] | PASS | `backend/internal/discovery/errors_test.go` | WP-331 |
| DOD-01 | [Q] | PASS | `tests/rep01-structure.test.sh` | WP-331 |
| ENT-02 | [B] | PASS | `backend/internal/export/datev_gate_test.go` | WP-790 |
| ENT-06 | (V), [B–P4], [A] | PASS | `backend/internal/entitlement/entitlement_test.go` | WP-244 |
| EVT-03 | [P2–P3] | PASS | `backend/internal/relationship/history_integration_test.go` | WP-296 |
| EXP-01 | [B], DATEV [P2] | PASS | `frontend/src/pages/ExportPage.test.tsx` | WP-791 |
| GATE-01 | [Q] | PASS | `tests/rep01-structure.test.sh` | WP-331 |
| GATE-02 | [Q] | offen | `tests/rep01-structure.test.sh` | WP-331 |
| GATE-05 | [Q] | PASS | `tests/rep01-structure.test.sh` | WP-331 |
| GLO-11 | [Q] | PASS | `tests/rep01-structure.test.sh` | WP-331 |
| GQL-03 | [P2] | PASS | `backend/internal/graphqlbff/ci_aggregates_test.go` | WP-792 |
| GQL-04 | [B] | PASS | `backend/internal/graphqlbff/fieldrights_test.go` | WP-727 |
| IMP-08 | [P2] | PASS | `backend/internal/topology/blast_test.go` | WP-297 |
| IMP-10 | [P2] | PASS | `backend/internal/topology/custom_types_test.go` | WP-294 |
| IMP-11 | [P2] | PASS | `backend/internal/topology/projection_integration_test.go` | WP-298 |
| IMP-IO-01 | [P2] | PASS | `backend/internal/importer/api_integration_test.go` | WP-303 |
| INS-01 | [Q] | PASS | `tests/install-smoke-health.test.sh` | WP-331 |
| INS-04 | [Q] | PASS | `frontend/e2e/onboarding.spec.ts` | WP-331 |
| INV-01 | [P2] | PASS | `backend/internal/stocktake/complete_integration_test.go` | WP-761 |
| INV-02 | [P2] | PASS | `backend/internal/stocktake/area_integration_test.go` | WP-288 |
| INV-03 | [P2], RFID/Direct-Access [P4] | PASS | `frontend/src/components/flows/scan.test.tsx` | WP-807 |
| JOB-02 | [B], Zusätze [P2], [P3] | PASS | `backend/internal/ci/computed_refresh_integration_test.go` | WP-718 |
| JOB-03 | [B] | PASS | `backend/internal/jobs/revert_integration_test.go` | WP-305 |
| JOB-04 | [B] | PASS | `backend/internal/importer/job_test.go` | WP-301 |
| LCY-02 | [P2] | PASS | `backend/internal/lifecycle/transition_integration_test.go` | WP-724 |
| LCY-03 | [P2] | PASS | `backend/internal/lifecycle/guards_integration_test.go` | WP-279 |
| LOC-08 | P2/G2 | PASS | `backend/internal/locations/loc08_integration_test.go`, `frontend/src/pages/LocationTreePage.test.tsx` | WP-282 |
| MET-01 | B/G1, Teil P2/G2 | PASS | `backend/internal/lifecycle/model_integration_test.go` | WP-263 |
| MET-05 | [P2] | PASS | `backend/internal/citype/deactivate_integration_test.go` | WP-249 |
| MET-06 | [P2] | PASS | `backend/internal/citype/inherit_test.go` | WP-326 |
| MET-10 | B/G1, Teil P2/G2 | PASS | `backend/internal/permission/fields_integration_test.go` | WP-264 |
| MET-11 | B/G1, Teil P2/G2 | PASS | `backend/internal/contract/reference_integration_test.go` | WP-737 |
| MET-12 | P2/G2 | PASS | `backend/internal/contract/reference_integration_test.go` | WP-737 |
| MET-13 | [P2] | PASS | `backend/internal/citype/global_attr_integration_test.go` | WP-255 |
| MET-14 | P2/G2 | PASS | `backend/internal/ci/instance_lifecycle_integration_test.go` | WP-714 |
| MET-16 | B/G1, Teil P2/G2 | PASS | `backend/internal/citype/compose_integration_test.go` | WP-252 |
| MET-20 | nicht geliefert | N/P | ohne Abschluss-WP (E-37) | – |
| MET-30 | nicht geliefert | N/P | ohne Abschluss-WP (E-37) | – |
| MET-31 | nicht geliefert | N/P | ohne Abschluss-WP (E-37) | – |
| MET-32 | nicht geliefert | N/P | ohne Abschluss-WP (E-37) | – |
| MET-33 | nicht geliefert | N/P | ohne Abschluss-WP (E-37) | – |
| MET-34 | nicht geliefert | N/P | ohne Abschluss-WP (E-37) | – |
| MET-40 | nicht geliefert | N/P | ohne Abschluss-WP (E-37) | – |
| MET-41 | nicht geliefert | N/P | ohne Abschluss-WP (E-37) | – |
| MET-42 | nicht geliefert | N/P | ohne Abschluss-WP (E-37) | – |
| MET-43 | nicht geliefert | N/P | ohne Abschluss-WP (E-37) | – |
| MET-44 | nicht geliefert | N/P | ohne Abschluss-WP (E-37) | – |
| MET-45 | Phase 1 im Fokus, voller Tag fehlt | PASS | `backend/internal/citype/inherit_test.go` | WP-326 |
| MET-50 | [P2] | PASS | `frontend/src/pages/FormBuilderPage.test.tsx` | WP-316 |
| MET-51 | P2/G2 | PASS | `frontend/src/components/create/GuidedCreate.test.tsx` | WP-317 |
| MET-52 | [P2] | PASS | `frontend/src/components/metamodel/LayoutPreview.test.tsx` | WP-803 |
| MET-53 | [P2] | PASS | `frontend/src/pages/MetamodelDraftsPage.test.tsx` | WP-799 |
| MET-54 | B/G1, Teil P2/G2 | PASS | `backend/internal/citype/export_grants_test.go` | WP-726 |
| MGT-01 | [P2] | PASS | `backend/internal/document/scan_integration_test.go` | WP-734 |
| MGT-02 | [P2] | PASS | `backend/internal/disposal/disposal_tx_integration_test.go` | WP-278 |
| MGT-12 | [P2] | PASS | `backend/internal/jobs/expiry_check_integration_test.go` | WP-270 |
| NET-06 | [P2] | PASS | `backend/internal/discovery/duplicate_ip_resolve_integration_test.go` | WP-773 |
| NET-09 | [P2] | PASS | `backend/internal/discovery/ip_reservation_conflict_integration_test.go` | WP-772 |
| NFR-02 | [B] | offen | `tests/load/` | WP-327 |
| NFR-10 | [Q] | N/P | vor G1 (G1-Nachweis, `docs/acceptance/lasttest-g1.md`) | – |
| NTF-05 | [B], Zusätze [P2], [P3] | PASS | `backend/internal/notify/stock_notify_integration_test.go` | WP-845 |
| OPS-06 | [Q] | PASS | `tests/outage/run.sh`; Lauf 42695925438 auf `e17954ca` | WP-331 |
| OVR-03 | [P2] | PASS | `backend/internal/ci/bulk_override_integration_test.go` | WP-306 |
| RBA-05 | [P2–P4], [A] | PASS | `backend/internal/server/modules_test.go` | WP-830 |
| RBA-06 | [P2] | PASS | `backend/internal/permission/role_matrix_integration_test.go` | WP-245 |
| RBA-07 | [P2] | offen | `backend/internal/tenant/rls/fieldrights_matrix_integration_test.go` | WP-730 |
| REL-06 | [P2] | PASS | `backend/internal/relationshiptype/seed_integration_test.go` | WP-294 |
| REL-07 | [P2] | PASS | `backend/internal/relationship/metadata_integration_test.go` | WP-295 |
| REL-09 | [P2] | PASS | `backend/internal/relationship/history_system_integration_test.go` | WP-771 |
| SEC-09 | [P2] | PASS | `backend/internal/document/clamd_integration_test.go` | WP-735 |
| SEC-11 | [Q] | offen | `docs/security/review-phase2.md` | WP-329 |
| SEQ-01 | [Q] | PASS | `tests/rep01-structure.test.sh` | WP-331 |
| SIM-01 | [B], Teilumfang [P2] | PASS | `backend/cmd/seed/seed_integration_test.go` | WP-821 |
| SRC-01 | [B] | PASS | `backend/internal/search/fieldrights_integration_test.go` | WP-266 |
| SRC-02 | [P2] | PASS | `backend/internal/search/users_tags_integration_test.go` | WP-788 |
| SRC-03 | [P2], Textsyntax [O] | PASS | `frontend/src/components/rules/RuleBuilder.test.tsx` | WP-315 |
| SRC-04 | [P2] | PASS | `backend/internal/savedview/ast_related_integration_test.go` | WP-789 |
| STK-01 | [P2] | PASS | `backend/internal/notify/stock_notify_integration_test.go` | WP-845 |
| STK-02 | [P2] | PASS | `backend/internal/consumable/stock_integration_test.go` | WP-283 |
| STK-03 | [P2] | PASS | `backend/internal/reservation/consumable_availability_integration_test.go` | WP-759 |
| STK-04 | [P2] | PASS | `backend/internal/movement/service_integration_test.go` | WP-274 |
| STK-05 | [P2] | PASS | `backend/internal/order/order_receive_integration_test.go` | WP-760 |
| TEN-09 | [Q] | PASS | `backend/internal/tenant/rls/channel_matrix_integration_test.go` | WP-331 |
| TKT-01 | [P2], Pro [P4], Monitoring/Findings/Automationen [P3] | PASS | `backend/internal/discovery/review_ticket_integration_test.go` | WP-767 |
| TKT-02 | [P2] | PASS | `backend/internal/sla/calendar_api_integration_test.go` | WP-768 |
| TKT-03 | [O] | N/P | `backend/internal/ticket/provider_test.go` | WP-293 |
| TST-01 | [B], Teilumfang [P2] | PASS | `backend/internal/citype/inherit_test.go` | WP-326 |
| TST-03 | [B], Teilumfang [P2] | PASS | `frontend/e2e/form-builder.spec.ts` | WP-324 |
| TST-04 | [Q] | PASS | `.github/workflows/ci.yml`; CI-Lauf 42695897709 auf `e17954ca` | WP-331 |
| UI-11 | [P2] | PASS | `frontend/src/components/rules/ComputedEditor.test.tsx` | WP-802 |
| UI-12 | [P2] | PASS | `frontend/src/pages/AssetListPage.test.tsx` | WP-319 |
| UI-14 | [P2–P4, A] | PASS | `frontend/e2e/a11y.spec.ts` | WP-324 |
| UI-15 | [P2] | PASS | `frontend/src/components/flows/scan.test.tsx` | WP-807 |
| VIE-02 | [P2] | PASS | `frontend/src/pages/SavedViewsPage.test.tsx` | WP-817 |
