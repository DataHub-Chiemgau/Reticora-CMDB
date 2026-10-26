# Internes Sicherheitsreview Phase 1 (G1)

WP-209 (SEC-11, GATE-03). Prüfrahmen: OWASP ASVS Level 2 nach SEC-11.
Grundlagen:

- Threat-Model `docs/security/threat-model.md`
- Befundliste des Audits `docs/audit/befunde.csv`
- Implementierungsplan und Traceability `docs/traceability.csv`
- Scan-Jobs nach `docs/security/abhaengigkeitsscans.md`
- Lastnachweis `docs/acceptance/lasttest-g1.md`

Prüfstand: Commit `dbbfded`, also die G1-Nachbesserung (WP-332 bis WP-335)
auf `02ebb76`, dazu die zwei Korrekturen aus dem Gesamtreview des
integrierten Stands (`58863fa`, `dea65b7`) und die Nachträge zu seinen nicht
blockierenden Befunden vom 2026-10-15 (`3f2bfd5`, `458a96d`, `111bdc4`,
`f354c5a`, `0f165cc`; Abschnitt „G1-Nachbesserung“).
Die Erstfassung prüfte `main` nach dem Lasttest (PR #60) zuzüglich WP-208
(Kanalmatrix, Migration 000136). Der Stand ist lokal geprüft; der CI-Lauf auf
dem Release-Kandidaten steht aus. Am 2026-10-15 hat der Auftraggeber E-17,
E-56, E-57 und E-58 entschieden und G1 freigegeben (Abschnitt „Ergebnis“).

Fortschreibung vom 2026-10-25 (WP-452, `t5-review-g1`) auf dem Stand
`425a655`, also mit den gemergten Korrektur-WPs des G1-Korrekturplans
(`docs/plan/g1-korrektur/implementierungsplan.md`):

- SR-11 ist nach dem Bewertungsschema (`docs/audit/README.md`) High und
  behoben.
- Neu sind SR-12 und SR-13 (Abschnitt „Neue Befunde“) und SR-14 bis SR-26,
  die übrigen P0-Befunde des Phase-1-Abnahmeaudits mit Sicherheitswirkung
  (Abschnitt „Befunde des Phase-1-Abnahmeaudits“).
- V4 und SR-03 nennen die geprüften Kombinationen der Kanalmatrix (Abschnitt
  „Kanalmatrix: Kanal × Scope-Art“).
- Die Scanergebnisse bewerten die Advisories der Entwicklungsabhängigkeiten
  (F-297).

Die Testläufe dazu sind lokal. Die Läufe auf dem Release-Kandidaten und die
endgültige Statuspflege übernimmt WP-595 (`t5-rc-evidence`).

## Ergebnis

- **Audit-Befunde:** Von 225 Critical/High-Befunden sind alle G1-Anteile
  geschlossen oder warten nur noch auf ihren Gate-Nachweis (WP-210 bis
  WP-213).
  - 29 High-Befunde betreffen ausschließlich Spätphasen-Module. Sie sind nach
    E-03 zurückgestellt; die Module sind im G1-Release über WP-214
    abgeschaltet.
  - Bei 15 weiteren Befunden ist der G1-Anteil geschlossen, ihr
    Spätphasen-Anteil ist ebenfalls zurückgestellt.
  - NFR-02, NFR-03 und NFR-10 verfehlte der Erstlauf des Lasttests (WP-206).
    Der Wiederholungslauf nach der G1-Nachbesserung belegt sie.
- **Neue Befunde:** Das Review ergab sieben neue Befunde (SR-01 bis SR-07).
  Die G1-Nachbesserung behebt zwei davon und hinterlässt drei Restrisiken
  (SR-08 bis SR-10). Das Gesamtreview fand zusätzlich einen vorbestehenden
  Fehler (SR-11). Die Fortschreibung vom 2026-10-25 nimmt den
  Site-Scope-Bypass im Rack-Kanal (SR-12) und den Review-Items-Leak (SR-13)
  auf. Stand der 13 Befunde (unten):
  - Behoben:
    - zwei Critical (Site-Scope-Bypass im Rack-Kanal; Review-Items ohne
      Kandidaten über Clients)
    - vier High (mandantenübergreifende Client-Verweise; Discovery
      überschreibt einen gleichzeitig gesetzten manuellen Wert;
      Ingest-Durchsatz NFR-03; Reaktivieren in der Produktionsverdrahtung,
      SR-11)
    - ein Medium (Impact im Worst Case, NFR-02)
  - Offen:
    - zwei Medium
    - drei Low (Restrisiken der G1-Nachbesserung)
    - ein Info (Referenzhost des Lasttests)
- **Befunde des Phase-1-Abnahmeaudits:** Die übrigen 13 P0-Befunde mit
  Sicherheitswirkung (F-001, F-004 bis F-015) stehen als SR-14 bis SR-26 mit
  Severity Critical im Review. Ihre Korrektur-WPs sind gemergt, jeder Befund
  ist mit einem regulären Test belegt behoben (lokal grün auf `425a655`).
- **G1-Nachbesserung:** Die sicherheitsrelevanten Änderungen von WP-332 bis
  WP-335 sind mit Tests belegt (Abschnitt „G1-Nachbesserung“ unten). Die
  Schwächen, die die Reviews der WPs und das Gesamtreview des integrierten
  Stands fanden, sind behoben, darunter eine Injektion bei fehlkonfigurierter
  Client-Kodierung, unbegrenzte Advisory-Locks, die die Lock-Tabelle des
  Clusters füllen konnten, und Identitätssperren im Lock-Raum der
  Scheduler-Sperre (behoben in `58863fa`). Die nicht blockierenden Befunde
  des Gesamtreviews sind am 2026-10-15 nachgetragen, darunter fehlende
  Identitätssperren mit einer Spur über mehrere Instanzen; offen blieb nur
  der vorbestehende SR-11, behoben mit WP-418 (`t5-sr11-decorator`).
- **Scans:** govulncheck, npm audit und Trivy sind ohne blockierende Funde, und
  es gibt keine Ausnahmen. Die G1-Nachbesserung ändert keine Abhängigkeiten;
  der Scan-Lauf auf dem Release-Kandidaten steht aus. In den
  Entwicklungsabhängigkeiten meldet npm audit am 2026-10-25 2 Critical
  (tinypool, vitest) und 6 High. Sie sind nicht ausgeliefert, aber als
  Lieferkettenrisiko (T19) offen, bis die Aktualisierung gemergt ist; die
  Frist für Critical endet am 2026-10-25 (Abschnitt „Scanergebnisse“).
- **Pentest:** Der externe Pentest (WP-210, E-28, B3) ist laut Erklärung des
  Auftraggebers vom 2026-10-15 erledigt. Bericht, Befunde und Nachtest
  liegen im Repository nicht vor; Zusammenfassung und Befundliste sind in
  `docs/security/pentest-g1.md` nachzureichen.

**Freigabe des Sicherheitsreviews:** Das Review ist mit der Freigabe des
Auftraggebers vom 2026-10-15 freigegeben. Unter den Befunden SR-01 bis SR-11
war am 2026-10-15 kein High und kein Critical als offen geführt; SR-02 ist
mit WP-332 bis WP-334 behoben. SR-11 stand dabei entgegen dem
Bewertungsschema auf Low (F-119), und SR-12 und SR-13 fehlten (F-040); beides
korrigiert die Fortschreibung vom 2026-10-25. Seitdem ist unter SR-01 bis
SR-26 kein High und kein Critical offen; offen sind die Critical-Advisories
der Entwicklungsabhängigkeiten (Abschnitt „Scanergebnisse“). Das
Ausstiegskriterium „keine offenen High/Critical“ verlangt zusätzlich den
Pentest-Nachweis (WP-210, B3). Die Erklärung des Auftraggebers vom
2026-10-15, B3 sei erledigt, ersetzt ihn nach der Übergangsregel von E-76
(`docs/plan/g1-korrektur/entscheidungen.md`) nicht; nachzureichen sind
Zusammenfassung und Befundliste in `docs/security/pentest-g1.md` (A-07,
WP-453). Enthält die Befundliste ein offenes High oder Critical, ist das
Kriterium nicht erfüllt.

Die Referenzumgebung des Lasttests ist anerkannt: Der Auftraggeber hat E-58
am 2026-10-15 entschieden (Option 1). Den Lasttest belegen der lokale
Nachweislauf auf Commit `dbbfded` und der grüne `load.yml`-Lauf 38665599979
auf Commit `918a832`; nach dem Merge wiederholt der nächtliche Lauf ihn auf
dem Release-Kandidaten. SR-06 bleibt offen, bis vor GA ein dedizierter
Referenzhost bereitsteht. Der Auftraggeber hat G1 am 2026-10-15 freigegeben.
Nach E-76 ersetzt seine Erklärung die Nachweise nicht; das Gate-Ergebnis
lautet „offen“ (`docs/acceptance/g1-gate.md`, WP-451). Nachzureichen sind der
grüne CI-Lauf auf PR #64 mit dem Merge nach `main` und die Nachweise zu B3
(Pentest), B4 (v2-Texte) und B6 (Subprozessorenliste, NFR-09), die der
Auftraggeber für erledigt erklärt hat.
B5 ist mit E-17 geschlossen; gemessen ist mit 10.000 Objekten
(`docs/acceptance/g1-gate.md`).

Die vollständige Liste steht im Anhang.

NFR-02, NFR-03 und NFR-10 verfehlte der Erstlauf des Lasttests (WP-206;
SR-02, SR-05). Der Plan ordnet ihnen und ABN-02 zusätzlich die
G1-Nachbesserung zu (WP-332 bis WP-335); der Anhang führt sie mit diesen WPs.
Der Wiederholungslauf auf Commit `dbbfded` belegt sie, der grüne
`load.yml`-Lauf 38665599979 auf Commit `918a832` bestätigt sie
(`docs/acceptance/lasttest-g1.md`). Die Referenzumgebung ist anerkannt
(E-58, entschieden am 2026-10-15).

## Scanergebnisse (SEC-11, E-31)

Lauf: GitHub Actions, Run 38510025931 (PR #60, Stand vor WP-208), 2026-10-14.

| Job | Gegenstand | Ergebnis |
|---|---|---|
| govulncheck (backend, collector, edgecore) | Go-Abhängigkeiten, erreichbare Schwachstellen | grün, keine erreichbaren Funde |
| npm-audit | Produktionsabhängigkeiten des Frontends | grün, keine High/Critical |
| image-scan (backend, collector, frontend) | Container-Images (Trivy, Critical/High mit Fix) | grün |

`security/scan-exceptions.txt` enthält keine Ausnahmen. Die Berichte liegen als
Artefakte des Laufs vor (30 Tage).

Die G1-Nachbesserung (WP-332 bis WP-335) ändert weder `go.mod`/`go.sum` noch
`package.json`/`package-lock.json` noch die Dockerfiles. Die Scan-Jobs laufen
im CI-Lauf des Release-Kandidaten erneut; dieser Lauf steht aus.

### Entwicklungsabhängigkeiten (F-297, T19)

Der Job `npm-audit` blockiert nur High und Critical der
Produktionsabhängigkeiten; die Funde der Entwicklungsabhängigkeiten stehen nur
im Bericht. Das Review bewertete sie bis zum 2026-10-25 nicht (F-297).

Lauf: lokal am 2026-10-25 auf `425a655`, `frontend/package-lock.json`,
npm 10.9.4, `npm audit --package-lock-only` mit und ohne `--omit=dev`.

- Produktionsabhängigkeiten: 0 Critical, 0 High, 2 Moderate (`react-router`,
  `react-router-dom`); der Job `npm-audit` bleibt grün.
- Alle Abhängigkeiten: 2 Critical, 6 High, 7 Moderate. Jedes Critical und
  High liegt in einer Entwicklungsabhängigkeit. Das Audit zählte am
  2026-10-18 2 Critical und 10 High (F-297).

Bewertung als Lieferkettenrisiko (Threat T19, `docs/security/threat-model.md`):

- Die Pakete werden nicht ausgeliefert. Das Image `reticora-frontend` enthält
  nur das gebaute Bundle (`frontend/Dockerfile`: nginx-Stufe mit `/app/dist`).
- Sie laufen in der Build-Stufe des Images, in CI-Jobs wie `frontend` (Lint,
  Tests, Build, `generate:api:check`) und auf Entwicklerrechnern, jeweils mit
  Code und Daten des Repositorys. Die Skripte in `frontend/package.json`
  starten weder den Browser-Modus noch die Oberfläche von Vitest
  (`frontend/vitest.config.ts`: jsdom); den Vite-Dev-Server startet nur
  `npm run dev` auf dem Entwicklerrechner.
- Wer die Eingaben dieser Werkzeuge steuert, kann in der CI und auf
  Entwicklerrechnern Code ausführen (tinypool) oder sie lahmlegen (High:
  Ressourcenverbrauch, Absturz). Die Gadgets in tinypool brauchen eine
  Prototype Pollution im selben Prozess, also manipulierten Code oder
  manipulierte Daten im Testlauf.
- SEC-11 verlangt die Behebung von Critical binnen 7 und von High binnen
  30 Tagen. Gerechnet wird ab der ersten Meldung, hier spätestens dem
  Phase-1-Abnahmeaudit vom 2026-10-18 (F-297).
- Maßnahme ist die Aktualisierung. Eine Ausnahme in
  `security/scan-exceptions.txt` verlängert die Frist nicht, und der Job
  wertet sie für Entwicklungsabhängigkeiten nicht aus.
- WP-452 ändert keine Abhängigkeiten. Für die Aktualisierung von `vitest`
  sieht der Korrekturplan noch kein WP vor; es ist anzulegen.
- Die Critical-Zeilen bleiben offen, bis die Aktualisierung gemergt ist. Bis
  dahin verbieten sie das Gate-Ergebnis „bestanden“
  (`tests/rep01-structure.test.sh --final`). Wird die Frist verfehlt, ist
  SEC-11 für diese Funde nicht eingehalten.

| Paket | Schweregrad | Status | Advisories (npm audit) | Herkunft | Maßnahme | Frist (SEC-11) |
|---|---|---|---|---|---|---|
| `tinypool` 1.1.1 | Critical | offen | GHSA-5gmw-xhrv-c9v3, GHSA-85c8-ppgw-ccpr: Prototype-Pollution-Gadget in den Worker- bzw. `run()`-Optionen führt zu Codeausführung | transitiv über `vitest` | Aktualisierung von `vitest` (tinypool ab 2.1.2) | 2026-10-25 |
| `vitest` 3.2.7 | Critical | offen | über `tinypool`; dazu GHSA-82fw-gwwq-j7x9 und über `@vitest/mocker` 3.2.7 GHSA-qxg6-89mg-rqv8 (je Moderate) | direkt (`devDependencies`) | Aktualisierung; npm audit nennt `vitest` 5.0.5, einen Major-Sprung von `^3.2.7` | 2026-10-25 |
| `vite` 6.4.3 | High | offen | GHSA-587h-7wmj-h5pm (`launch-editor`); dazu drei Moderate und ein Low | direkt (`devDependencies`) | Aktualisierung ohne Major-Sprung (`npm audit fix`) | 2026-11-17 |
| `js-yaml` 4.3.1 | High | offen | GHSA-2883-xcg3-v3hh | transitiv (`@redocly/openapi-core`, `@eslint/eslintrc`) | wie `vite` | 2026-11-17 |
| `@redocly/openapi-core` 1.34.18 | High | offen | über `js-yaml` | transitiv über `openapi-typescript` (`npm run generate:api`) | wie `vite` | 2026-11-17 |
| `brace-expansion` 1.1.18, 2.1.4, 5.0.9 | High | offen | GHSA-qhr7-859c-m2p7, GHSA-6j4f-fj2g-mc7p; dazu GHSA-q2hr-2g5m-vwhr (Moderate) | transitiv (u. a. Lint und Codegenerierung) | wie `vite` | 2026-11-17 |
| `browserslist` 4.28.4 | High | offen | GHSA-c83g-rgw3-j3cx, GHSA-73wf-gq98-2v4g | transitiv (Build) | wie `vite` | 2026-11-17 |
| `source-map-js` 1.2.1 | High | offen | GHSA-68fv-2mgg-jv7q | transitiv über `postcss` (Build) | wie `vite` | 2026-11-17 |

Für die sechs High-Pakete meldet npm audit einen Fix ohne Major-Sprung. Die
Moderate-Funde (u. a. `tailwindcss` über `postcss-selector-parser`, mit Fix
nur über einen Major-Sprung) folgen der nächsten Aktualisierung; SEC-11
setzt für sie keine Frist.

## ASVS Level 2 – Abgleich je Kapitel

| Kapitel | Umsetzung in G1 | Nachweis |
|---|---|---|
| V1 Architektur | Threat-Model, Mandantentrennung in der DB (RLS FORCE), ein DB-Einstieg `WithTenant` | `docs/security/threat-model.md`, `backend/internal/tenant/rls/` |
| V2 Authentisierung | OIDC mit PKCE über Keycloak, MFA für Operatoren, API-Schlüssel mit Hash und Ablauf | T1, T2, T15 |
| V3 Sessions | RS256-JWT mit 15 min, Refresh-Rotation, Widerruf über Redis | T1 |
| V4 Zugriffskontrolle | Routen-Rechte-Tabelle (fail closed), Scopes je Client/Site/Team; Kanalmatrix mit 29 Kanälen für jede der 6 Scope-Arten seit WP-409, Lücken davor und außerhalb der Matrix im Abschnitt „Kanalmatrix: Kanal × Scope-Art“ | T6, T7, `backend/internal/server/authz_test.go`, `backend/internal/tenant/rls/channel_matrix_integration_test.go` |
| V5 Validierung | OpenAPI-Validierung der Anfragen, typisierte Fehler (RFC 7807) | `backend/internal/middleware/` (Validation) |
| V7 Logging | Audit-Kette, Redaktion personenbezogener Daten | T16, T17 |
| V8 Datenschutz | PII-Vault, Envelope-Encryption der Credentials | T17 |
| V9 Kommunikation | TLS überall, mTLS für Collector | T10 |
| V10 Schadcode/Lieferkette | Scans blockierend, signierte Releases | T19 |
| V12 Dateien | Uploads (SEC-09) gehören zu P2; Dokumente sind im G1-Release abgeschaltet | WP-214 |
| V13 API | Rate-Limits, Idempotenz, GraphQL-Limits, keine Introspection in production | T3, T4, T5 |
| V14 Konfiguration | Fail-closed-Start außerhalb development, Feature-Gates je Modul | `backend/internal/config/`, WP-214 |

### Kanalmatrix: Kanal × Scope-Art

`TestChannelMatrix` (`backend/internal/tenant/rls/channel_matrix_integration_test.go`)
läuft seit WP-409 (`t1-channel-matrix`, `52ac11d`) tabellengetrieben über
`rls.Channels` und `rls.ScopeKinds` (`backend/internal/tenant/rls/matrix_fixtures.go`):

- Scope-Arten: Org, Client, Site, Team, Mehrfach (zwei Clients, eine Site)
  und die andere Organisation, dazu ein zweiter org-weiter Nutzer der
  Organisation.
- Lesend (27 Kanäle): CIs, Suche, Export, Topologie, Impact, Metriken,
  Beziehungen, Sites, Gebäude, Räume, Racks mit Layout, Mounts, Review-Items
  mit und ohne Kandidaten, Webhooks, Benachrichtigungen, Jobs, Export-Jobs und
  je GraphQL-Query-Feld ein Kanal (`cis`, `ci`, `relationships`,
  `currentUser`, `topology`, `rackLayout`, `searchEverything`, `reviewInbox`,
  `jobs`).
- Schreibend (15 Kanäle): CIs, Beziehungen, Sites, Gebäude, Räume, Racks,
  Mounts, Review-Items mit und ohne Kandidaten, Webhooks, Benachrichtigungen,
  Jobs, Export-Jobs, Ingest und Verweise auf Objekte der anderen Organisation
  (`reference`: Kontakte, Subnetze über Client und VRF, Kontaktlinks,
  Discovery-Scopes, Rollenzuweisungen über Benutzer und Rolle).
- Jede der 294 Kombinationen aus Kanal, Nutzer und Lesen bzw. Schreiben prüft
  den exakten Status: 2xx im Scope, 404 außerhalb und für die andere
  Organisation, 403, 409 oder 422 nach Fall, nie 5xx. Schreibzugriffe prüft
  sie zusätzlich per SELECT auf ihre Wirkung. Ein Kanal ohne Matrixzeile und
  ein GraphQL-Query-Feld ohne Kanal lassen den Test scheitern.
- Lauf am 2026-10-25 auf `425a655`: 294 Subtests grün.

Fehlende Kombinationen:

- Bis WP-409 prüfte die Matrix (WP-208) Locations nur über `/sites`,
  Review-Items nur mit Kandidaten, Webhooks, Jobs und Benachrichtigungen nur
  lesend und GraphQL nur über `cis` (F-058). Racks, Mounts und `rackLayout`
  unter Site-Scope fehlten. Deshalb blieb SR-12 unentdeckt, und V4 und SR-03
  zitierten die Matrix bis 2026-10-25 zu Unrecht als Beleg für Racks (F-040).
  Review-Items ohne Kandidaten fehlten ebenso (SR-13). Seit WP-409 läuft
  jeder Kanal der Tabelle für jede Scope-Art.
- Nicht in `rls.Channels` stehen die übrigen REST-Ressourcen mit
  Mandantendaten. Dazu gehören Kontakte, Credentials, IPAM, Collectoren,
  Audit-Log, Historie, gespeicherte Sichten, Benutzer, Rollen und
  API-Schlüssel sowie die Modul-Objekte Dokumente, Wartungsfenster,
  Compliance und Assets; Discovery-Scopes und Rollenzuweisungen nur als
  Verweise. Für sie prüfen die Scope-Tests der Pakete
  (`backend/internal/*/*_scope_integration_test.go`) die Repository-Ebene
  und `TestRLSMatrix` jede Mandantentabelle mit Principals jeder Scope-Art
  (lokal grün auf `425a655`). Eigene Tests haben die Compliance-Auswertung
  (SR-14), Dokumente und Wartungsfenster (SR-15) und die Scope-Kette des
  Site-Grants (SR-25). Einen HTTP-Lauf je Scope-Art hat die Matrix für diese
  Ressourcen nicht.
- Die endgültige Formulierung nach WP-409 auf dem Release-Kandidaten
  übernimmt WP-595 (`t5-rc-evidence`).

## Neue Befunde

| ID | Severity | Status | Befund | Maßnahme |
|---|---|---|---|---|
| SR-12 | Critical | **behoben** (WP-340) | Site-Scope-Bypass im Rack-Kanal (F-002 des Phase-1-Abnahmeaudits): `rackVisible` verband Raum und Gebäude per LEFT JOIN und ließ `building.site_id IS NULL` zu. War das Gebäude für einen site-gescopten Principal unsichtbar, wurde das Rack sichtbar. Racks fremder Sites, auch anderer Clients (etwa für einen Principal mit Client 1 und Site 1), waren über Liste, Detail, Layout, Mounts und GraphQL `rackLayout` lesbar und per DELETE löschbar; der location-Eintrag verwaiste. Severity nach dem Bewertungsschema: Critical (Scope-Isolation; nach TEN-09 ist jede Cross-Client-Exposition Critical). Der Befund fehlte im Review bis 2026-10-25, und V4 und SR-03 zitierten die Kanalmatrix als Beleg für Racks, obwohl sie Racks unter Site-Scope nicht prüfte (F-040). | WP-340 (`t1-rack-site-scope`, `5b29d6f`): `rackVisible` prüft die Location-Zeile des Racks, deren Policy Org-, Client- und Site-Scope erzwingt; `ListMounts` antwortet für fremde Racks 404; `deskVisible` ist gleich korrigiert. Seit WP-393 (`t1-located-site-scope`, Migration 000152) tragen Räume und Racks `site_id` mit RLS. Nachweis: `TestRackChannelSiteScope` (`backend/internal/tenant/rls/rack_channel_scope_integration_test.go`), lokal grün auf `425a655`; Matrixzeilen `rack`, `mount` und `graphql-rack-layout` für jede Scope-Art (WP-409). |
| SR-13 | Critical | **behoben** (WP-342) | Review-Items ohne Kandidaten leaken über Clients (F-003 des Phase-1-Abnahmeaudits): `review_item` trug weder Client noch Site (Policy nur auf die Organisation), und das Go-Prädikat „alle Kandidaten sichtbar“ war für leere `candidate_ci_ids` immer wahr. Ein Client-1-Principal mit `discovery:read` listete, las und verwarf Items zu Geräten, Collectoren und Subnetzen von Client 2 (Gerätename, Management-IP, Seriennummer, Rohdaten, CIDR), über REST und GraphQL `reviewInbox`. Severity nach dem Bewertungsschema: Critical (Scope-Isolation, Integritätsverlust). | WP-342 (`t1-review-scope`, `c8225dd`): Migration 000140 leitet `client_id` und `site_id` des Items aus den Kandidaten bzw. dem Collector ab, die Policies prüfen Client und Site, kandidatenlose Items sind für gescopte Principals fail-closed. Nachweis: `TestReviewItemsWithoutCandidatesStayInClientScope` und `TestReviewItemRowsCarryClientScope` (`backend/internal/tenant/rls/review_item_scope_integration_test.go`), lokal grün auf `425a655`; Matrixzeile `review-without-candidates` für jede Scope-Art (WP-409). |
| SR-01 | High | **behoben** (WP-208) | Mandantenübergreifende Client-Verweise: Fremdschlüssel auf `client` prüfen nur die ID und laufen ohne RLS. Organisation B konnte Standorte, CIs, Kontakte und Subnetze mit einem Client von Organisation A anlegen. | Migration 000136: alle 21 Verweise auf `client` mit Organisation; Kanalmatrix und `TestClientReferencesCarryOrganization` verhindern Regressionen. |
| SR-07 | High | **behoben** (WP-207-Nachtrag) | Discovery während einer manuellen Änderung: Die zentrale Schreibentscheidung lief vor der Schreibtransaktion. Ein Override, das dazwischen committet wurde, schützte den Wert nicht, und `ci.model` trug den Discovery-Wert trotz geschütztem Override (gefunden von `TestDiscoveryConcurrency`). | Das CI-Repository entfernt Felder mit Override aus automatisierten Schreibvorgängen unter der Zeilensperre (`dropOverriddenFields`); deterministischer Test `TestAutomatedWriteKeepsOverride`. |
| SR-02 | High | **behoben** (WP-332 bis WP-334) | Ingest-Durchsatz im Erstlauf (WP-206) 58 statt 200 Records/s je Instanz (NFR-03). Unter Dauerlast staute sich die Queue, die Verzögerung stieg über 30 s (Verfügbarkeit). Datenverlust trat nicht auf, die Queue ist persistent. | G1-Nachbesserung: eine Transaktion je Record (WP-332), Batch-Schreibpfad für neue Geräte (WP-333), parallele Spuren mit Identitätssperre (WP-334; E-56 entschieden: Option 1 mit Identitätssperre). Wiederholungslauf auf Commit `dbbfded` mit 4 Spuren (Standard): Abbau ab Sendeende 233 Records/s, Verzögerung Record → sichtbar höchstens 3,1 s bei 200 Records/s Angebot, Spool-Flush von 10.000 Records in 35,9 s, kein fehlender Record (`docs/acceptance/lasttest-g1.md`). Mit einer Spur (`RETICORA_INGEST_LANES=1`) 182 Records/s, das verfehlt NFR-03. Reserve: SR-08; Referenzumgebung: SR-06. |
| SR-11 | High | **behoben** (WP-418) | Vorbestehend (schon auf `02ebb76`, nicht durch die G1-Nachbesserung), gefunden im Gesamtreview: Der Indexing-Decorator des CI-Repositorys (`ci.NewIndexingRepository`, `backend/internal/ci/indexing_repository.go`) reichte die optionalen Schnittstellen `Restore` und `UniqueConflicts` nicht durch, und `main.go` legte ihn in der Produktion immer um das CI-Repository. Das Auflösen eines Reviews `resurrected_device` mit `merge` antwortete in der Produktion 501 („this repository cannot reactivate CIs“); „reaktivieren“ aus CI-13 war in der Produktionsverdrahtung nicht nutzbar. Die Vorabprüfung eindeutiger Attribute (MET-15) entfiel; den Konflikt fing nur der Unique-Index der Datenbank (DB-04). Das Wiederherstellen per `POST /cis/{id}/restore` (CI-05) war nicht betroffen. Severity nach dem Bewertungsschema: High, weil eine Kernfunktion mit Tag [B] in der Produktion fehlte und das ein Gate sperrt. Die Fassung vom 2026-10-15 führte SR-11 als Low (F-119). | WP-418 (`t5-sr11-decorator`, `423a538`): Der Decorator reicht `Restore` (mit Neuindizierung) und `UniqueConflicts` durch; `bootstrap.NewCIRepository` baut die Kette wie `main.go`. Nachweis: `TestAuditT5CIReactivationThroughProductionDecoratorChain` (`backend/internal/tenant/rls/ci_decorator_chain_integration_test.go`; Kette aus `main.go`: `merge` 200, CI reaktiviert, Audit `ci.restored`, Suchdokument, Principal eines anderen Clients 404, eindeutiges Attribut 409), lokal grün auf `425a655`; `TestIndexingRepositoryForwardsOptionalInterfaces`. CI-13 ist erst nach WP-369 (`t3-resurrect-new`, F-022, `44a0db2`) abgenommen: Bis dahin legte „neues CI“ bei `resurrected_device` ein org-weites CI ohne Identität an. Den Gate-Status von CI-13 und MET-15 setzt WP-595 (`t5-rc-evidence`). |
| SR-05 | Medium | **behoben** (WP-335) | Impact im Worst Case (ganze Organisation, 10.000 CIs) im Erstlauf unter Parallellast p95 2,39 s statt < 2 s; Antwort ca. 5 MB. | Adaptive Kantenlesung, schlanke Knotenprojektion, `hops` und `via` je CI. Die Oberfläche fragt die kompakte Form über den Sparse-Fieldset `fields=` ab (API-02); die Standardantwort ist nur additiv erweitert (E-57 entschieden: Option A). Wiederholungslauf auf Commit `dbbfded`: p95 477 ms (kompakte Form) und 546 ms (Standardform mit Pfaden); kompakte Antwort 2,39 MB (239 Byte je betroffenem CI). Referenzumgebung: SR-06. |
| SR-03 | Medium | offen | Von 194 Fremdschlüsseln zwischen Mandantentabellen enthalten 178 die Organisation nicht (Zählung der Erstfassung). Die Anwendung prüft referenzierte Objekte unter RLS. Die Kanalmatrix belegt das seit WP-409 (`t1-channel-matrix`) für jede Scope-Art: in den Schreibzeilen mit Elternobjekt oder Verweis (Gebäude, Räume, Racks, Mounts, CIs, Kanten, Ingest) und in der Zeile `reference` für Verweise auf Objekte der anderen Organisation (Kontakte, Subnetze über Client und VRF, Kontaktlinks, Discovery-Scopes, Rollenzuweisungen über Benutzer und Rolle). Bis WP-409 prüfte sie Racks unter Site-Scope nicht (F-040, SR-12); fehlende Kombinationen im Abschnitt „Kanalmatrix: Kanal × Scope-Art“. Die Datenbank allein verhindert fremde Verweise nicht (Defense in Depth). | Folge-WP: zusammengesetzte Fremdschlüssel nach dem Muster von 000136, priorisiert nach Schreibpfaden. |
| SR-04 | Medium | offen (MAC- und Attributklasse **behoben**, WP-434, WP-435) | Ausdrucksindizes mit nicht-leakproof Funktionen sind unter RLS für die App-Rolle unbenutzbar. `idx_ci_fts` (`to_tsvector`) führt zu linearem Aufwand bei der Volltextsuche (Verfügbarkeit bei großen Beständen). Für Hostname und FQDN ist das mit 000134 behoben. Dieselbe Ursache hatte der Abgleich über Fingerprint-MACs im Ingest (`jsonb`-Containment, F-114 Identitätsteil): Er las je Batch alle CIs der Organisation (WP-333; vorher je Record mit MACs). Behoben mit Migration 000161 (WP-434): Primäre und Interface-MACs stehen in der Seitentabelle `ci_identity_mac` (B-Baum-Index `(organization_id, mac)`, ENABLE + FORCE RLS mit den Policies von `ci`, gepflegt vom Trigger auf `ci` mit den Rechten des Schreibenden); die Identitätssuche vergleicht nur mit leakproof-Gleichheit. Nachweis: `TestAuditIdentitySearchWithMACsUsesIndexes` (als `reticora_app` kein Seq Scan auf `ci`, Index von `ci_identity_mac`), Wiederentdeckung über die Queue mit 10.500 CIs ohne Organisations-Scan (`TestReconciliationBeyondTenThousandCIs`). Dieselbe Ursache hatte der Attributfilter der CI-Liste (F-114 Attributteil): `attributes ->> key` (`jsonb_object_field_text`, nicht leakproof) machte die Ausdrucksindizes `idx_attr_<id>` für die App-Rolle unbenutzbar (als Owner Index Scan, als `reticora_app` Seq Scan über die Organisation). Behoben mit Migration 000162 (WP-435, E-73 Übergangsregel Option A): Index und Filter nutzen `reticora_attr_text(jsonb, text)`, `->>` als LEAKPROOF-Funktion; der DDL-Job baut bestehende Indizes einmalig neu (der alte erst nach gültigem neuem gelöscht). Sicherheitsreview der Funktion: PL/pgSQL (nie inlinebar, sonst löste der Planer sie zu `->>` auf), IMMUTABLE STRICT PARALLEL SAFE, SECURITY INVOKER, `search_path = pg_catalog`, Eigentümer Superuser (nur er kann sie ändern); sie wirft für keine Eingabe und nennt keinen Wert in einer Meldung (NULL, Nicht-Objekte, Arrays, Skalare, fehlende, leere und 10-MB-Schlüssel, 10-MB-Werte, 1.000 Ebenen tief: `TestAttrTextIsLeakproof`). Offen bleiben wie bei den leakproof-Funktionen von PostgreSQL nur Ressourcengrenzen (Speicher, Abbruch; ein Containerwert mit JSON-Text über 1 GB). LEAKPROOF verlangt Superuser bei der Migration; ohne ihn bricht 000162 mit Verweis auf E-73 Option B ab. Nachweis: `TestAttributeFilterUsesIndexUnderRLS` (Produkt-SQL der Liste als `reticora_app`, org-weit und client-gescopt: Index Scan using `idx_attr_<id>` mit dem Attributwert in der Indexbedingung; Negativfall ohne Index), `TestAttributeIndexRebuild` (Neuaufbau, Unique-Semantik, Abbrüche). | Folge-WP: gespeicherte `tsvector`-Spalte mit Index; Katalogtest gegen neue Ausdrucksindizes auf RLS-Tabellen. |
| SR-08 | Low | offen | Die Reserve bei NFR-03 ist knapp (Verfügbarkeit). Mit Spuren begrenzt der Abschnitt unter den Advisory-Locks von Audit-Kette und Event-Outbox je Organisation den Durchsatz. Darin berechnet der Trigger der Outbox-Nummerierung (Migration 000120, `event_outbox_number()`) je Zeile `max(sequence)` über die Events der Organisation; mit veralteter Statistik aggregiert der Plan über alle Events. Ebenso wählen die Batch-Kandidatenabfragen (WP-333) nach großen Importen bis zur nächsten Analyse Pläne über die ganze Organisation. Frühere Läufe desselben Codes lagen bei 210–233 Records/s. | Folge-WP mit Migration: Zähler je Organisation oder `ORDER BY sequence DESC LIMIT 1`; das verkürzt den Lock-Abschnitt von etwa 200 ms auf etwa 20 ms je Batch. `ANALYZE` nach Massenimport oder robuste Abfrageform. |
| SR-09 | Low | offen | `max_cis` (ENT-03) hat mit Spuren zwei kleine Überschreitungsfenster. `AdoptUnlicensed` (Anlage offener `unlicensed_ci`-Einträge, wenn das Limit steigt) nimmt die `max_cis`-Sperre nicht und kann gleichzeitig mit dem Ingest anlegen (vorbestehend). Drain-Spuren nach dem Verringern der Spurzahl können das Limit je um höchstens 100 überschreiten. | Folge-WP: `AdoptUnlicensed` nimmt die `max_cis`-Sperre der Organisation und zählt in der Transaktion neu wie der Ingest (WP-334). Drain-Spuren sind in `docs/betrieb/ingest-spuren.md` beschrieben. |
| SR-10 | Low | offen | Ein Record mit mehr als 256 Identitätsschlüsseln (Stack oder Chassis mit vielen Port-MACs, Record mit Tausenden MACs) nimmt die Identitätssperre seiner Organisation exklusiv. Für die Dauer seiner Transaktion warten alle sperrenden Ingest-Transaktionen dieser Organisation. Andere Organisationen warten nur, soweit ihre Records im selben Spur-Batch dahinter stehen. Der DeviceRecord-Vertrag begrenzt die Zahl der MACs und Interfaces nicht. Die Lock-Tabelle des Clusters bleibt geschützt (Abschnitt „G1-Nachbesserung“). | Folge-WP: Obergrenze für MACs und Interfaces je Record (Änderung des DeviceRecord-Vertrags, API-01) oder konfigurierbare Sperrgrenze. |
| SR-06 | Info | offen | Der Lasttest lief nicht auf einem dedizierten Referenzhost. Auch der Wiederholungslauf auf Commit `dbbfded` lief auf einem Host mit 4 vCPU und 15 GB RAM, den sich PostgreSQL, NATS, Redis, Server und k6 teilten (Größenklasse des Mindestsizings nach INS-07). | E-58 (entschieden am 2026-10-15, Option 1): Der Auftraggeber erkennt den Lastnachweis auf 4-vCPU-Hosts als G1-Nachweis für GATE-03 an, mit Auflagen, darunter mindestens ein grüner `load.yml`-Lauf auf dem Commit des Release-Kandidaten und ein dedizierter Referenzhost vor GA. Die Auflage des `load.yml`-Laufs ist für Commit `918a832` erfüllt: Seit `458a96d` läuft jedes Szenario in `load.yml` als eigener Job mit frischem Seed und `ANALYZE`, und Run 38665599979 auf dem Standard-Runner `ubuntu-latest` (4 CPUs, 15,61 GB RAM) hält alle Schwellen ein, zweiter Nachweis neben dem lokalen Lauf (`docs/acceptance/lasttest-g1.md`). SR-06 bleibt offen, bis vor GA ein dedizierter Referenzhost bereitsteht; G1 sperrt es nicht. Nächtlicher Lauf `load.yml`, nach dem Merge auf dem Release-Kandidaten; dedizierter Referenzhost vor GA. |

## Befunde des Phase-1-Abnahmeaudits

Das Phase-1-Abnahmeaudit (`audit/phase1-final/PHASE1_FINDINGS.md`,
2026-10-18) führt 16 P0-Befunde. F-002 und F-003 sind SR-12 und SR-13
(oben); F-016 (CI-Dialog mit fest kodierten Typschlüsseln) hat keine
Sicherheitswirkung. Die übrigen 13 stehen hier:

- Severity nach dem Bewertungsschema (`docs/audit/README.md`): Critical, weil
  jeder Befund die Mandanten- oder Scope-Isolation bricht, Zugangsdaten
  preisgibt oder Rechte ausweitet (Auth-Bypass).
- Korrektur-WP nach der Tabelle „Schwere Befunde (P0/P1) und ihre Korrektur“
  in `docs/plan/g1-korrektur/implementierungsplan.md`; alle sind gemergt.
- Status nach dem Stand `425a655`: behoben, wenn der Nachweis ein regulärer
  Test ohne Build-Tag `auditdefect` ist und grün läuft. Das gilt für alle 13.
- Lauf am 2026-10-25 auf `425a655` (PostgreSQL mit TimescaleDB 2.31.0 und
  Migration 000160, NATS 2.10.29, Redis, `go test -count=1 -p 1`): die 20
  genannten Backend-Tests (8 Pakete) und 4 Collector-Tests (4 Pakete) sind
  grün, keiner ist übersprungen.
- Die Real-Stack-Nachweise von SR-16 und SR-25 laufen nur im CI-Job
  `install-smoke` und sind hier nicht ausgeführt. Die Läufe auf dem
  Release-Kandidaten übernimmt WP-595 (`t5-rc-evidence`).

| ID | Severity | Status | Befund | Korrektur-WP | Nachweis |
|---|---|---|---|---|---|
| SR-14 | Critical | **behoben** | F-001: Die Compliance-Auswertung durch einen client-gescopten Principal (Standardrolle engineer) lieferte und überschrieb die Ergebnisse der CIs anderer Clients; der Evaluator ersetzte den Scope des Aufrufers durch einen org-weiten. | WP-339 (`t1-compliance-scope`, `e721281`): Auswertung nur für org-weite Principals, sonst 403. | `TestComplianceEvaluationStaysInCallerScope` (`backend/internal/tenant/rls/compliance_channel_integration_test.go`) |
| SR-15 | Critical | **behoben** | F-004: Ein teilgescopter Principal änderte und löschte geteilte Dokumente und Wartungsfenster; die FK-Kaskade entfernte die Links anderer Clients. | WP-341 (`t1-doc-window-writes`, `b7aff9a`): Schreiben nur bei vollständig sichtbaren Links (Migration 000139). | `TestDocumentWritesOutsideScope`, `TestMaintenanceWindowWritesOutsideScope` (`backend/internal/tenant/rls/module_object_writes_integration_test.go`) |
| SR-16 | Critical | **behoben** | F-005: Keine MFA-Pflicht für org_admins aus Signup, Einladung, Operator-Pfad, Rollenzuweisung, IdP-Gruppe, Default-Rolle und Realm-Gruppe reticora-admin; die Konten waren nur durch ein Passwort geschützt. | WP-367 (`t2-mfa-login-path`, `6562ad5`); Teile WP-357 (`t2-mfa-accounts`, `07fb145`) und WP-358 (`t2-mfa-role-sync`, `00b2e4e`). | `TestAuditT2FirstOrgAdminAccountRequiresMFA`, `TestAuditT2RealmAdminGroupRequiresMFA` (`backend/internal/signup/mfa_test.go`); `TestRoleAssignmentOrgAdminRequiresMFA`, `TestInvitationAcceptanceOrgAdminRequiresMFA`, `TestFirstLoginWithOrgAdminInvitationRequiresMFA` (`backend/internal/user/role_assignment_mfa_test.go`); `TestLoginBindsOrgAdminOfGroupAndDefaultRoleToMFA` (`backend/internal/identity/handler_test.go`); Real-Stack `frontend/e2e-real/mfa-admin.spec.ts` (nur CI) |
| SR-17 | Critical | **behoben** | F-006: Jeder Halter von `apikey:manage` rotierte oder widerrief jeden API-Key der Organisation und erhielt beim Rotieren eines org-weiten Admin-Keys den Klartext eines Nachfolgers mit dessen Rechten. | WP-354 (`t2-apikey-authz`, `ae575c1`): nur Owner oder ein Aufrufer, der die Rechte des Keys im Scope des Inhabers hält. | `TestAuditT2APIKeyRotateRevokeOnlyByEntitledCaller` (`backend/internal/identity/apikey_authz_integration_test.go`) |
| SR-18 | Critical | **behoben** | F-007: Die Bindung eines API-Keys an einen Service-Account prüfte den Scope nicht; ein client-gebundener Ersteller erhielt über einen org-weiten Service-Account einen org-weiten Key. | WP-354 (`t2-apikey-authz`, `ae575c1`): Bindung nur im Scope des Erstellers, sonst 403. | `TestAuditT2KeyBindingNeverBroadensCreatorScope` (`backend/internal/identity/apikey_authz_integration_test.go`) |
| SR-19 | Critical | **behoben** | F-008: Rollen eines Service-Accounts ließen sich ohne Rechte- und Scope-Prüfung zuweisen, die Route verlangte nur `user:manage`; ein Halter von `user:manage` erzeugte Admin-Service-Accounts. | WP-355 (`t2-sa-role-ceiling`, `3e58a7f`): `role:manage` und Grant-Obergrenze. | `TestAuditT2ServiceAccountRolesWithinCallerRights` (`backend/internal/identity/apikey_authz_integration_test.go`), `TestAuditT2ServiceAccountRoleAssignmentNeedsRoleManage` (`backend/internal/server/t2_auth_chain_test.go`), `TestServiceAccountRoleCeiling` (`backend/internal/user/service_account_integration_test.go`) |
| SR-20 | Critical | **behoben** | F-009: Der Webhook-Filter stellte `ci.merged` und `relationship.removed` über CIs anderer Clients an client-gebundene Service-Accounts zu; ein Payload ohne Scope galt als org-weit. | WP-356 (`t2-webhook-bound-scope`, `4cae303`): Scope serverseitig aufgelöst, fail-closed bei unbekanntem Scope. | `TestAuditT2BoundSubscriptionGetsOnlyReadableClientEvents` (`backend/internal/webhook/bound_filter_integration_test.go`) |
| SR-21 | Critical | **behoben** | F-010: Das WMI-Plugin sendete Zugangsdaten per HTTP Basic im Klartext (Port 5985) und im HTTPS-Modus ohne Zertifikatsprüfung; über einen Protokoll-Alias erhielt es das SSH-Konto. | WP-378 (`t4-cred-scope`, `bf8d16a`); Teil WP-377 (`t4-wmi-tls`, `70626ed`): WinRM nur über geprüftes TLS, Credentials je Art. | `TestAuditWMICredentialTransport` (`collector/plugins/wmi/wmi_test.go`), `TestAuditT2CredentialsArePerProtocol` (`collector/plugins/plugin_test.go`) |
| SR-22 | Critical | **behoben** | F-011: Das NAS-Plugin sendete Konto und Passwort der Synology in der URL über HTTP (Port 5000) und an ungeprüftes HTTPS (5001). | WP-379 (`t4-nas-tls`, `3b798fa`): Anmeldung per POST über geprüftes TLS. | `TestAuditNASCredentialTransport` (`collector/plugins/nas/nas_test.go`) |
| SR-23 | Critical | **behoben** | F-012: Der HTTP-Erneuerungsendpunkt war nicht an den Collector gebunden; ein Principal mit `discovery:ingest` stellte sich ein Collector-Zertifikat für den eigenen Schlüssel aus und konnte die Credential-Bundles der Scopes öffnen. | WP-375 (`t4-pki-renew-server`, `e80b75f`): Erneuerung nur über die mTLS-Identität des Collectors, HTTP-Erneuerung entfernt. | `TestAuditCertificateRenewalBoundToCollector` (`backend/internal/discovery/renew_edge_integration_test.go`) |
| SR-24 | Critical | **behoben** | F-013: Der Collector ignorierte `credential_ids` je Scope; das erste Credential je Protokoll galt für alle Scopes, auch über Client-Grenzen. | WP-378 (`t4-cred-scope`, `bf8d16a`): Credentials je Scope und je Art. | `TestAuditPerScopeCredentials` (`collector/collectorcmd/discovery_credentials_test.go`) |
| SR-25 | Critical | **behoben** | F-014: Ein Site-Grant setzte den Client-Scope auf „alle“; Site-Nutzer lasen Kontakte, Credential-Metadaten, Assets und unplatzierte CIs fremder Clients. | WP-392 (`t6-asset-site-scope`, `6c92576`); Teil WP-390 (`t6-site-grant-scope`, `85e117d`): Site-Grant auf den Client des Standorts begrenzt, Site-Scope für Assets. | `TestSiteGrantConfinedToClient`, `TestSiteGrantConfinedToClientAndSite` (`backend/internal/locations/sitescope_integration_test.go`); Real-Stack `frontend/e2e-real/tenant-isolation.spec.ts` T18 (nur CI) |
| SR-26 | Critical | **behoben** | F-015: Keine Grant-Obergrenze; ein client-gescopter Inhaber von `user:manage` erzeugte org-weite org_admin-Einladungen und -Rollenzuweisungen. | WP-394 (`t6-invite-ceiling`, `ae60e06`): Zielrechte und Zielscope nur innerhalb der eigenen. | `TestAuditDefectInvitationGrantCeiling`, `TestAuditDefectRoleAssignmentGrantCeiling` (`backend/internal/user/invitation_integration_test.go`) |

## G1-Nachbesserung (WP-332 bis WP-335)

Die G1-Nachbesserung behebt SR-02 und SR-05 (`docs/plan/implementierungsplan.md`,
Abschnitt „G1-Nachbesserung (NFR-02, NFR-03)“). Sie ändert den
Datenbank-Einstieg aller Mandantentransaktionen (`WithTenant`), den
Ingest-Pfad und die Impact-API. Geprüft ist Commit `dbbfded` mit diesen
Commits auf `02ebb76`, dazu die Korrekturen aus dem Gesamtreview:

- WP-332: `d2daeed`, Nachtrag `82110bf`
- WP-333: `8320a12`
- WP-334: `25f0c79`, Nachbesserung `55aa9f3`; Korrekturen aus dem
  Gesamtreview `58863fa` und `dea65b7`; Nachträge zu seinen nicht
  blockierenden Befunden `3f2bfd5`, `458a96d`, `111bdc4`, `f354c5a` und
  `0f165cc`
- WP-335: `a8f5912`, Nachtrag `dbbfded`

Jedes WP hatte drei unabhängige Reviews: Transaktionen und Nebenläufigkeit,
Sicherheit und Mandantentrennung, Akzeptanzkriterien mit echten Testläufen.
Blockierende Befunde gab es nur bei WP-334; sie sind in `55aa9f3` behoben.
Die übrigen Befunde sind in den Nachträgen behoben oder als offene Punkte
festgehalten.

Ein Gesamtreview des integrierten Stands `dbbfded` fand drei weitere
blockierende Befunde zu WP-334. Sie sind behoben:

- **Lock-Raum (Sicherheit):** Die Identitäts- und `max_cis`-Sperren waren
  Ein-Argument-Sperren mit FNV-1a-Schlüsseln aus Werten des Mandanten, im
  selben Raum wie die Sperre des Scheduler-Leiters. FNV-1a lässt sich
  umkehren; eine gewählte Seriennummer hätte eine Spur für alle Mandanten
  ohne Zeitgrenze blockieren können. Behoben in `58863fa` (Zeile
  „Identitätssperre“ unten).
- **`/readyz` (Betrieb):** Das Ingest-Detail wartete bis zu 2 s auf
  JetStream. Stockte JetStream, lief die Kubernetes-Probe (Standard 1 s) ab,
  und alle Pods verließen den Service, obwohl jeder Check bestand. Behoben
  in `dea65b7` (Zeile „Rückstand je Spur“ unten).
- **Rollback (Betrieb):** Der beschriebene Rückfall auf ein Release vor
  WP-334 ließ den Rückstand der Spur 0 ungelesen verfallen (nachgestellt mit
  NATS 2.10.29). Korrigiert ist die Betriebsdoku
  (`docs/betrieb/ingest-spuren.md`, „Rollback auf ein Release vor WP-334“);
  der Code ist unverändert.

Die nicht blockierenden Befunde des Gesamtreviews sind am 2026-10-15
nachgetragen:

- **Sperren mit einer Spur (Sicherheit, Betrieb):** Identitätssperre,
  `max_cis`-Sperre und Sofort-Wiederholung hingen an mehr als einer Spur. Mit
  einer Spur konsumiert eine Instanz aber weiter Spur 0, den Altconsumer und
  alle Drain-Spuren, jeweils mit eigenem `MaxAckPending`, und andere Instanzen
  tun dasselbe. Beim Rückfall auf eine Spur und bei rollenden Änderungen der
  Spurzahl konnten zwei Instanzen ein Gerät doppelt anlegen (PRI-10) und
  `max_cis` überschreiten (ENT-03); die Betriebsdoku behauptete das
  Gegenteil. Nachgestellt gegen den alten Stand: zwei Instanzen mit einer
  Spur legten je Runde 2 CIs an, vier Instanzen bei `max_cis` 300 legten 550
  an. Behoben in `3f2bfd5`: Jede Transaktion eines Records aus der Queue
  nimmt die Sperren, unabhängig von der Spurzahl (Zeilen „Identitätssperre“
  und „`max_cis`“ unten).
- **Speicherbetrieb (Integration):** Mit `--no-db` und NATS liefen die
  Spuren einer Organisation parallel, obwohl das Speicher-Repository nicht
  sperren kann; ein Gerät konnte zwei CIs bekommen. PostgreSQL war nicht
  betroffen. Behoben in `3f2bfd5`: Parallel laufen die Spuren nur mit einem
  Repository, das sperren kann; sonst verarbeitet ein Prozess jede
  Organisation seriell.
- **`/readyz` ohne Anmeldung (Sicherheit):** Jede Anfrage listete die
  JetStream-Consumer, also ein NATS-API-Aufruf mit bis zu 2 s Wartezeit auf
  dem Server je Anfrage. Behoben in `111bdc4`: Das Detail ist die letzte
  30-s-Stichprobe des Ingest-Workers. Seit `0f165cc` melden es nur Instanzen
  mit Ingest-Worker; auf API-Instanzen schrieb sonst jede Probe eine
  WARN-Zeile (Zeile „Rückstand je Spur“ unten).
- **Lastnachweis (Betrieb):** `load.yml` führte die Szenarien nacheinander
  gegen einen Seed aus, ohne neuen Seed und ohne `ANALYZE`, und `ingest.js`
  ließ eine einzelne abgelehnte Anfrage durch. Behoben in `458a96d`: ein Job
  je Szenario mit frischem Seed und `ANALYZE`, Schwellen
  `http_req_failed{name:ingest}` und `http_req_failed{name:spool}` = 0;
  `f354c5a` nennt im Spool-Log das Alter der Stichprobe. Der Standard-Runner
  von GitHub trägt den Lauf: Run 38665599979 auf Commit `918a832` ist grün
  (`docs/acceptance/lasttest-g1.md`).
- **Betriebsdoku:** Der Altconsumer `reticora-ingest` bleibt dauerhaft; die
  beschriebene Entfernung wirkte nicht, weil der Server ihn wieder anlegt.
  Die Lock-Tabelle ist nach einer Formel zu bemessen, die das HPA-Maximum
  von 10 Replikaten abdeckt (`docs/betrieb/ingest-spuren.md`).
- **Vorbestehend:** Der Indexing-Decorator reichte `Restore` und
  `UniqueConflicts` nicht durch (SR-11, behoben außerhalb der
  G1-Nachbesserung mit WP-418).

### Änderungen und Nachweise

| Änderung | Bewertung | Nachweis |
|---|---|---|
| **Transaktionsbeginn in einem Roundtrip** (WP-332): `WithTenant` sendet `BEGIN` und die fünf `set_config` der Mandanten-GUCs als eine Anweisung. Die Werte stehen als Literale im Text, nicht mehr als Bind-Parameter. | Organisations- und Scope-IDs sind validierte UUIDs. Die Benutzerkennung (Subject des Principals) steht als `E'…'`-Literal; Backslash und Hochkomma werden verdoppelt (nie `\'`), das gilt unter jeder Einstellung von `standard_conforming_strings` und `backslash_quote`. `Validate` lehnt NUL-Bytes und ungültiges UTF-8 ab. Die Verdopplung ist nur in UTF-8 sicher: Jeder Pool setzt `client_encoding=UTF8` als Startparameter (vor DSN und Rollen-Defaults) und verweigert Verbindungen mit anderer Kodierung. Diese Lücke fand das Review; sie bestand nur in den Zwischenständen ab `d2daeed`, setzte eine fehlkonfigurierte DSN voraus (z. B. `client_encoding=SJIS` mit `backslash_quote=on`) und ist mit `82110bf` geschlossen. Rest: Die GUC-Werte erscheinen im Anweisungstext (`pg_stat_activity`, Serverlog bei Anweisungsprotokollierung). Es sind Kennungen, keine Geheimnisse. | `TestBeginTenantSQLQuotesValues` (feindliche Werte `'`, `\`, `;`, `--`, `\'`, Zeilenumbruch, eingeschleuste `set_config`), `TestTenantScopeValidate` (NUL, ungültiges UTF-8), `TestWithTenantQuotesHostileUserID` (feindliche Benutzerkennungen kommen unverändert in `app.user_id` an, `app.org_id` bleibt, RLS isoliert weiter; auch mit `standard_conforming_strings=off` und mit einer DSN, die `client_encoding=SJIS` verlangt). Gegenprobe mit dem `pool.go` vor `82110bf`: Die SJIS-Variante setzte `app.org_id` auf eine fremde Organisation. |
| **Beitritt zur Kontext-Transaktion** (WP-332): Die Repository-Aufrufe eines Records laufen in einer Transaktion; `WithTenant` und `WithRequestTenant` treten einer Transaktion im Kontext bei. | Beitritt nur, wenn `WithTenant` die Transaktion geöffnet hat, sie offen ist und der Scope identisch ist (Organisation, Benutzer, Client-, Site- und Team-Scope). Sonst scheitert der Aufruf, ohne seine Funktion auszuführen (`ErrTxNotJoinable`, `ErrTxDone`, `ErrTxScopeMismatch`; fail closed). `WithSystem`, `WithOperator` und `WithOrganizationSetup` treten nie bei. Ein Kontext mit eigenem Scope behält ihn; ein engerer Scope wird nie auf den der Transaktion erweitert (Review-Befund, behoben in `82110bf`). Ein verschluckter Anweisungsfehler rollt die ganze Transaktion zurück (`ErrTxAborted`). | `TestContextTxFailsClosed`, `TestTenantScopeSameAs`, `TestContextTxJoin`; der Architekturtest `tenantguard_test.go` (WP-041) ist unverändert grün. |
| **Externe Aufrufe und Benachrichtigungen nach dem Commit** (WP-332, WP-333): `database.AfterCommit`. | Review-Benachrichtigungen, das Log `review.opened`, der Remote-Index (OpenSearch), Einwilligungsprüfung und Embedding der KI sowie Zähler und Log von `unlicensed_ci` laufen erst nach dem Commit, auch für die Batch-Transaktion; ein Rollback verwirft sie. Ein zurückgerollter Record kündigt also nichts an und gibt keine Daten an externe Dienste. Keine Transaktion wartet unter den Sperren der Organisation auf einen externen Dienst. Die KI-Einwilligung gilt zum Zeitpunkt des Commits. Ohne Kontext-Transaktion (REST-Pfad) bleibt alles wie bisher. Logs zu zurückgerollten Reviews und die Einwilligungsprüfung in der Transaktion fand das Review (behoben in `82110bf`). | `TestDiscoveryConcurrency` (Rollback-Runden für Record und Batch: kein Review angekündigt, im Einzelpfad genau eins nach dem Commit), `TestIngestKeepsPrefixOfFailedBatch` (kein `review.opened` für den zurückgerollten Record), `TestHybridIndexesRemoteAfterCommit`, `TestCIChunkIndexerEmbedsAfterCommit` (keine Einwilligungsprüfung in der Transaktion, verweigerte Einwilligung), `TestContextTxJoin`. |
| **Audit-Kette im Batch und am Transaktionsende** (WP-333, WP-334) | Die neuen Geräte eines Batches schreiben ihre Audit-Einträge mit einer Anweisung unter dem Ketten-Lock der Organisation, verkettet in fester Reihenfolge mit lückenloser `seq` (AUD-01). Audit- und Outbox-Einträge des Ingests stehen am Ende der Transaktion (`database.BeforeCommit`); die Kette bleibt in Commit-Reihenfolge. Ein per Batch angelegtes CI hat dieselben Events, dieselbe Historie, Provenienz und dieselben Suchdokumente wie bei der Einzelanlage. | `TestIngestStatementBudget` (`audit.Verify` nach jedem Batch; Audit und Outbox zuletzt in jeder Record- und Batch-Transaktion), `TestCreateMany/equal_to_the_single_creation` (einschließlich `seq` im Audit-Log), `TestIngestBatchMatchesRecordPath`, `TestBeforeCommit`. |
| **Identitätssperre, begrenzt je Transaktion** (WP-334, E-56 entschieden) | Jede Record- und Batch-Transaktion eines Records aus der Queue nimmt als erste Anweisung transaktionsgebundene Advisory-Locks, seit `3f2bfd5` unabhängig von der Spurzahl (vorher nur mit mehr als einer Spur): die Identitätssperre der Organisation geteilt, dann die Identitätsschlüssel exklusiv und aufsteigend (deadlockfrei). Schlüssel sind Seriennummer, Hardware-UUID, jede MAC und die Management-IP je VRF. Das verhindert doppelte CIs über Spuren (PRI-10) und dass eine ältere Beobachtung eine neuere überschreibt (REC-05). Eigener Lock-Raum (seit `58863fa`): Die Sperren nutzen die Zwei-Argument-Form mit eigenen Klassen (Organisation `0x52496f72`, Schlüssel `0x52496b79`, `max_cis` `0x52436e74`), verschieden von Audit-Kette, Outbox und Publisher; ihr Lock-Tag kann keiner Ein-Argument-Sperre gleichen (Scheduler-Leiter, Jobs, Location-Baum, Operator-Audit). Der Schlüssel in der Klasse sind die ersten 4 Byte des SHA-256 von Organisation und Wert; eine gezielte Kollision kostet etwa 2^32 Hashes und serialisiert nur zwei Ingest-Transaktionen. Jede Sperranweisung wartet höchstens 20 s (`lock_timeout`, in derselben Anweisung zurückgesetzt; andere Wartezeiten der Transaktion bleiben unverändert); danach wird der Record mit Pause wiederholt (55P03), statt eine Spur aller Mandanten anzuhalten. Befund des Gesamtreviews: Vorher waren es Ein-Argument-Sperren mit umkehrbaren FNV-1a-Schlüsseln im Raum der Scheduler-Sperre, ohne Wartegrenze. Review-Befund, behoben in `55aa9f3`: Ohne Obergrenze hielt eine Transaktion bis zu 20.003 Advisory-Locks. Jeder belegt einen Eintrag der Lock-Tabelle des ganzen PostgreSQL-Clusters; ist sie voll, scheitern Transaktionen aller Mandanten und Dienste mit „out of shared memory“ (53200). Jetzt sperrt eine Transaktion höchstens 256 Schlüssel. Batch-Fenster enden vor der Grenze; ein Record darüber nimmt nur die Sperre der Organisation exklusiv. Eine Instanz hält höchstens 6 × 257 = 1.542 Identitätssperren; 53200 und 55P03 werden bis zu dreimal nach 100, 200 und 300 ms wiederholt. Die Lock-Tabelle ist nach `max_locks_per_transaction` × (`max_connections` + `max_prepared_transactions`) ≥ Ingest-Instanzen × 1.542 + Reserve zu bemessen; beim HPA-Maximum von 10 Replikaten und `max_connections` = 100 sind das `max_locks_per_transaction` = 256, alternativ eigene Ingest-Instanzen (`RETICORA_WORKER_INGEST_ENABLED`, `docs/betrieb/ingest-spuren.md`). Ohne Sperre bleiben nur der Ingest ohne NATS (in der Anfrage, wie vor WP-334) und der Speicherbetrieb (`--no-db`), der jede Organisation seriell verarbeitet. Befund des Gesamtreviews, behoben in `3f2bfd5`: Mit einer Spur nahm eine Instanz keine Sperre, obwohl andere Instanzen, Altconsumer und Drain-Spuren dieselbe Organisation gleichzeitig verarbeiten. Rest: SR-10. | `TestIdentityLocksBounded`, `TestQueuedRecordsTakeLocks` (Sperre und Wiederholung für Records aus der Queue auch mit einer Spur, nicht über HTTP ohne Queue), `TestNoDBLanesKeepOneDevice` (Speicherbetrieb mit 1 und 4 Spuren: je 1 CI), `TestIngestLanesIdentityLock` (ohne Sperre 2 CIs, mit Sperre 1; `one_lane_from_the_queue`: zwei Instanzen mit einer Spur, über die Queue je Runde 1 CI, über HTTP ohne Queue 2; `bounded_per_transaction`: höchstens 256 Locks je Transaktion gemessen, der Record mit 20.000 MACs hält genau eine exklusive Sperre, kein 53200), `TestDiscoveryConcurrency` (Doppel-Ingest über Spuren, drei Fälle, je genau 1 CI). Gegenprobe ohne Grenze: 20.003 Locks in einer Transaktion. `TestIngestLockSpace` (Klassen disjunkt, nur Zwei-Argument-Form, `lock_timeout` gesetzt und zurückgesetzt), `TestIngestLanesIdentityLock/own_lock_space` (die Scheduler-Sperre und Ein-Argument-Sperren mit denselben Bits blockieren nicht) und `/lock_timeout` (gegen einen hängenden Halter scheitert der Record nach Batch- und vier Record-Versuchen mit 55P03, bei 300 ms Grenze nach etwa 2,1 s statt unbegrenzt; danach entsteht genau 1 CI). Gegenproben: Mit dem Code vor `58863fa` scheitert `own_lock_space`, mit `lock_timeout` „0“ scheitert `lock_timeout`; mit der Bedingung vor `3f2bfd5` (nur mehr als eine Spur) scheitern `TestQueuedRecordsTakeLocks`, `one_lane_from_the_queue` (2 CIs in 5 von 5 Runden) und `TestNoDBLanesKeepOneDevice`. |
| **`max_cis` exakt mit Spuren** (WP-334, ENT-03) | Für Records aus der Queue (seit `3f2bfd5` auch mit einer Spur) und mit gespeichertem Limit nimmt jede anlegende Transaktion die `max_cis`-Sperre der Organisation (eigene Klasse, Wartegrenze 20 s wie die Identitätssperre) und zählt in der Transaktion neu: exklusiv nahe am Limit, sonst geteilt. Reicht der Spielraum unter der geteilten Sperre nicht, beginnt die Transaktion exklusiv neu. Ohne Limit gibt es keine Sperre. Rest: SR-09. | `TestIngestLanesMaxCIs` (Limit 150 bzw. 500, 4 Spuren gleichzeitig: genau 150 bzw. 500 CIs, der Rest als `unlicensed_ci`; vier Instanzen mit einer Spur aus der Queue, Limit 300: genau 300 CIs). Gegenproben: ohne Neuzählung 400 bzw. 800 CIs, mit der Bedingung vor `3f2bfd5` 550 statt 300. |
| **NATS-Subjects und Consumer** (WP-334) | Der Server veröffentlicht Records unter `ingest.<org>.<spur>.<collector>`; je Spur liest ein Consumer `reticora-ingest-lane-<n>`. Der Altconsumer `reticora-ingest` wird auf die alten Subjects `ingest.*.*` eingeschränkt und bleibt dauerhaft bestehen (fehlt er, legt der Server ihn wieder an); wartende Nachrichten bleiben erhalten. Die Collector-Berechtigungen sind unverändert (`natsedge.go` unverändert): Ein Collector publiziert und abonniert nur unter `collector.<org>.<collector>.>` und in seinem Inbox-Präfix, `ingest.>` erreicht er nicht. Organisation und Scope einer Nachricht setzt weiter der Server aus der Authentisierung; die Spur berechnet der Consumer aus dem Record, nicht aus dem Subject. Rolling Upgrade: Eine Instanz eines Releases vor WP-334 setzt den Altconsumer beim Start ungefiltert zurück. Bis neue Instanzen den Filter wiederherstellen (höchstens 30 s), verarbeitet sie Spur-Nachrichten, die in dieser Zeit veröffentlicht werden, ohne Identitäts- und `max_cis`-Sperre; ältere Spur-Nachrichten sieht sie nicht. Rollback: Ein Release vor WP-334 liest Nachrichten, die schon in Spur-Subjects liegen, nicht; bei einem Rolling Rollback verfielen sie nach 48 h (Befund des Gesamtreviews, nachgestellt mit NATS 2.10.29). Verlustfrei ist nur der Ablauf mit gestoppter Annahme, leerer Queue und gelöschten Spur-Consumern (`docs/betrieb/ingest-spuren.md`, „Rollback auf ein Release vor WP-334“). | `TestIngestLanes` (Upgrade vom ungefilterten Altconsumer mit wartenden Nachrichten, Subjects für HTTP und Edge, Reihenfolge je Gerät, Drain, Wiederherstellen des Filters), `natsedge_integration_test.go` (T10, unverändert). |
| **Rückstand je Spur in `/readyz`** (WP-334, OPS-05) | `/readyz` ist ohne Anmeldung erreichbar und zeigt unter `details.ingest` die Spurzahl und den Rückstand je Spur, summiert über alle Organisationen, ohne Mandanten-IDs. Die Summe lieferte `/metrics` auf demselben Port schon vorher ohne Anmeldung (`reticora_worker_backlog`), je Spur steht sie dort jetzt als `reticora_ingest_lane_backlog`. Ein Fehler erscheint nur als `{"error":"unavailable"}`, ohne Fehlertext in der Antwort. Die Details ändern den Status nie. OpenAPI `ReadinessResponse` ist additiv erweitert. Seit `dea65b7` (Befund des Gesamtreviews) laufen die Details getrennt von den Checks mit einer eigenen Grenze von 300 ms; ein langsames oder hängendes Detail meldet `unavailable` und hält die Antwort nicht auf. Vorher wartete `/readyz` bis zu 2 s auf JetStream, die Kubernetes-Probe (Standard 1 s) lief ab, und alle Pods verließen den Service. Die Readiness-Probe wartet jetzt 3 s (`deploy/k8s/base/deployment.yaml`). Seit `111bdc4` (Befund des Gesamtreviews) ist das Detail die letzte Stichprobe, die der Ingest-Worker alle 30 s ohnehin für die Metriken nimmt, mit ihrem Zeitpunkt (`sampled_at`, OpenAPI additiv). Eine Anfrage ruft NATS nicht mehr auf; vorher löste jede unauthentifizierte Anfrage eine Auflistung der JetStream-Consumer aus. Seit `0f165cc` melden nur Instanzen mit Ingest-Worker das Detail; vor der ersten Stichprobe steht dort `unavailable`, ohne WARN-Zeile. | `TestReadyzDetails` (Details nicht in `checks`, Fehlertext nicht in der Antwort, Status unverändert), `TestReadyzSlowDetail` (ein Detail, das auf seinen Kontext wartet oder ihn ignoriert: Antwort unter 500 ms, Status `ready`, Detail `unavailable`), `TestReadinessDetailFromSample` (eine Stichprobe und 50 Details: eine Auflistung der Consumer), `TestIngestLanes` (echter NATS: 20 Details lösen keine JetStream-Anfrage aus), `TestReadyzDetailNotSampledIsQuiet`. |
| **Impact und Topologie** (WP-335, IMP-07) | Kanten werden ohne Join auf `ci` gelesen; die RLS der Kanten bleibt. Die erreichten CIs prüft eine Abfrage unter demselben Scope. Eine Kante zählt nur, wenn beide Enden sichtbar und nicht gelöscht sind (fail closed, auch wenn ein CI nach dem Lesen der Kanten gelöscht oder aus dem Scope verschoben wurde). Erreicht eine Ebene mehr als 1.000 CIs, liest die Traversierung alle Kanten und die sichtbaren CIs der Organisation und prüft genauso. `ListNodes` projiziert unter RLS mit Override-Overlay; ab 2.000 IDs liest es die sichtbaren CIs der Organisation und filtert in Go. `fields=` (API-02) lehnt unbekannte Felder mit 422 ab. Die teuerste Analyse (T20) wird billiger: Die Zahl der Anweisungen hängt nur noch von der Tiefe ab, nicht von der Größe des Graphen, und die kompakte Antwort ist 2,39 MB statt 5,0 MB groß. | `TestTraversalStaysInsideScope` (Ebenen- und Organisationslesung; ohne Sichtbarkeitsfilter der Organisationslesung rot), `TestListNodesProjection` (gelöschte CIs und CIs außerhalb des Scopes fehlen, auch bei Organisationslesung), `TestImpactFields`, `TestTopologyQueryBudget`, `TestImpactWorstCase`. |

### Prüfungen auf Commit `dbbfded`

- `go build`, `gofmt`, `go vet ./...` und golangci-lint v2.14.0
  (Konfiguration des Repositorys): ohne Befund. `go test ./...`: grün.
- Datenbank-Tests auf einer frischen Datenbank mit NATS und Redis: 89 Pakete,
  1.885 Tests, zwei Fehlschläge in `internal/permission` (Sortierung des
  Katalogs). Ursache ist die Kollation der lokalen Datenbank (en_US, glibc);
  das CI-Image (Alpine/musl) sortiert wie C, und mit C.UTF-8 ist das Paket
  grün. `internal/permission` und die Migrationen ändert die Nachbesserung
  nicht. `TestMigrationsRoundtrip` läuft nur in der CI, nicht auf dem
  geteilten lokalen Cluster.
- Frontend: format:check, lint, typecheck, vitest (47 Dateien, 249 Tests),
  build und generate:api:check sind grün.
- Lastnachweis: `tests/load/ingest.js` zählt fehlende Records als Fehler
  (Schwelle `ingest_records_missing` = 0). Vorher bestand k6 auch mit
  fehlenden Records; das fand das Review von WP-334 (behoben in `55aa9f3`).
- Korrekturen aus dem Gesamtreview (`58863fa`, `dea65b7`): `gofmt`,
  `go vet ./...` und golangci-lint ohne Befund, `go test ./...` grün. Die
  Datenbank-Tests der Pakete `discovery`, `database`, `ci`, `topology`,
  `relationship`, `server`, `tenant`, `tenant/rls`, `override` und `jobs`
  sind grün; `kustomize build` von base, staging und prod gelingt. Eine
  Kontrollmessung des Szenarios `spool` mit 4 Spuren auf diesem Stand ergab
  243 Records/s ab Sendeende (Wiederholungslauf: 233) ohne fehlenden Record;
  die Werte des Wiederholungslaufs bleiben gültig
  (`docs/acceptance/lasttest-g1.md`).
- Nachträge vom 2026-10-15 (Stand `111bdc4`): `gofmt`, `go vet ./...` und
  golangci-lint ohne Befund, `go test ./...` grün. Die Datenbank-Tests der
  Pakete `discovery`, `database`, `ci`, `topology`, `relationship`, `server`,
  `tenant` (mit `rls`), `override`, `middleware` und `api` sind grün,
  ebenso `tests/rep01-structure.test.sh`, `generate:api:check` und der
  Typecheck des Frontends. Die neuen Tests scheitern gegen den Code vor der
  Korrektur (Gegenproben). Kontrollmessung `spool`: 4 Spuren 232–255
  Records/s, 1 Spur mit Sperren 177 Records/s, kein fehlender Record.
- Lastlauf auf GitHub: `load.yml` auf Commit `918a832` (Run 38665599979)
  ist grün, alle k6-Schwellen eingehalten; damit ist die Auflage von E-58
  für diesen Commit erfüllt. Nach dem Merge wiederholt der nächtliche Lauf
  ihn auf dem Release-Kandidaten.
- Ausstehend: CI-Lauf auf dem Release-Kandidaten (mit den Scan-Jobs und
  `TestMigrationsRoundtrip`).

## Externer Pentest

WP-210: Nach E-28 beauftragt der Auftraggeber den Pentest (Aufgabe A-07).
Bericht und Nachtest werden in `docs/security/pentest-g1.md` zusammengefasst.
Am 2026-10-15 hat der Auftraggeber den Pentest (B3) für erledigt erklärt.
Nach der Übergangsregel von E-76 ersetzt die Erklärung den Nachweis nicht;
GATE-03 und NFR-08 stehen auf „offen“ (`docs/acceptance/g1-gate.md`,
WP-451). Bericht, Befunde und Nachtest
liegen im Repository nicht vor; Zusammenfassung und Befundliste sind in
`docs/security/pentest-g1.md` nachzureichen.

Die G1-Nachbesserung ändert den Datenbank-Einstieg der Mandantentransaktionen,
den Ingest-Pfad (Spuren, Subjects) und die Impact-API. Alle drei liegen im
vorgeschlagenen Umfang. Der Pentest sollte deshalb den Stand nach WP-332 bis
WP-335 prüfen; beginnt er früher, braucht er einen Nachtest für Ingest und
Impact.

## Anhang: Critical/High-Befunde des Audits

| ID | Tag | Severity | Status | WPs |
|---|---|---|---|---|
| AGT-03 | [P4] | Critical | geschlossen | WP-051 |
| AGT-06 | [P4] | Critical | geschlossen | WP-038 |
| AI-01 | [A] | Critical | geschlossen | WP-037 |
| API-04 | [B] | Critical | geschlossen | WP-050, WP-072, WP-166 |
| API-07 | [B] | Critical | geschlossen | WP-058, WP-061 |
| AUT-01 | [B] | Critical | geschlossen | WP-042, WP-043 |
| AUT-02 | [B] | Critical | geschlossen | WP-042, WP-066 |
| AUT-09 | [B] | Critical | geschlossen | WP-043, WP-044 |
| CI-04 | [B] | Critical | geschlossen | WP-057 |
| CI-10 | [B] | Critical | geschlossen | WP-045, WP-058, WP-060, WP-091 |
| COL-02 | [B] | Critical | geschlossen | WP-047, WP-048, WP-147 |
| COL-05 | [B] | Critical | geschlossen | WP-062, WP-143 |
| EXP-01 | [B]; DATEV [P2] | Critical | geschlossen | WP-035, WP-189 |
| GQL-04 | [B] | Critical | geschlossen | WP-036 |
| IMP-07 | [B] | Critical | geschlossen | WP-025, WP-031 |
| MGT-01 | [P2] | Critical | geschlossen | WP-028, WP-030 |
| MGT-03 | [P4] | Critical | geschlossen | WP-019, WP-023, WP-028 |
| MGT-05 | [P4] | Critical | geschlossen | WP-029 |
| MGT-06 | [P4] | Critical | geschlossen | WP-029 |
| MGT-07 | [P4] | Critical | geschlossen | WP-029 |
| MGT-11 | [P5] | Critical | geschlossen | WP-017, WP-023 |
| MON-01 | [B] | Critical | geschlossen | WP-039, WP-040 |
| NFR-04 | [B]; Teilumfang [P5] | Critical | geschlossen | WP-062, WP-063 |
| OPS-06 | [Q] | Critical | geschlossen | WP-062, WP-104 |
| OVR-01 | [B] | Critical | geschlossen | WP-057, WP-058, WP-060 |
| OVR-02 | [B] | Critical | geschlossen | WP-059 |
| RBA-02 | [B] | Critical | geschlossen | WP-046 |
| RBA-03 | [B] | Critical | geschlossen | WP-009 |
| RBA-04 | [B] | Critical | geschlossen | WP-045 |
| SEC-01 | nicht angegeben | Critical | geschlossen | WP-047 |
| SEC-08 | nicht angegeben | Critical | geschlossen | WP-049 |
| SRC-01 | [B] | Critical | geschlossen | WP-025, WP-032, WP-186 |
| SRC-04 | [P2] | Critical | geschlossen | WP-034 |
| TEC-06 | [B]; [Q] Spike | Critical | geschlossen | WP-026, WP-039, WP-040 |
| TEC-12 | [P5] | Critical | geschlossen | WP-027, WP-033 |
| TEN-02 | [B] | Critical | geschlossen | WP-024, WP-025, WP-026 |
| TEN-04 | [B] | Critical | geschlossen | WP-008, WP-009, WP-027 |
| TEN-05 | [B] | Critical | geschlossen | WP-007, WP-022, WP-023, WP-024, WP-025, WP-027, WP-029 |
| TEN-06 | [B] | Critical | geschlossen | WP-008, WP-010, WP-011, WP-012, WP-013, WP-014, WP-015, WP-016, WP-017, WP-018, WP-019, WP-020, WP-021, WP-022, WP-030, WP-035, WP-041 |
| TKT-01 | [P2]; Pro [P4]; Monitoring/Findings/Automationen [P3] | Critical | geschlossen | WP-020, WP-028, WP-029 |
| TLC-04 | [B] | Critical | geschlossen | WP-042 |
| AGT-05 | [P4] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-059, WP-220 |
| AST-01 | [P2] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-017, WP-023, WP-222 |
| AST-05 | [P2] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-052, WP-079, WP-222 |
| MGT-02 | [P2] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-028, WP-224 |
| MGT-04 | [P3] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-020, WP-028, WP-063, WP-228 |
| SRC-03 | [P2]; Textsyntax [O] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-034, WP-187, WP-236 |
| WFL-01 | [P3] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-021, WP-023, WP-231 |
| WFL-02 | [P3] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-059, WP-231 |
| ABN-02 | [Q] | High | geschlossen | WP-204, WP-206, WP-335 |
| ABN-03 | [Q]; jeweilige Phase | High | geschlossen | WP-207 |
| AI-02 | [A] | High | geschlossen | WP-075, WP-076 |
| API-05 | [B] | High | geschlossen | WP-067, WP-069, WP-070, WP-074, WP-088, WP-107, WP-111, WP-114, WP-116, WP-125, WP-139, WP-153, WP-168, WP-169 |
| API-06 | [P2]/[P3]/[P4]/[A] | High | geschlossen | WP-075, WP-214 |
| API-08 | [B] | High | geschlossen | WP-181 |
| API-09 | [B] | High | geschlossen | WP-182 |
| AUD-03 | nicht angegeben | High | geschlossen | WP-090, WP-171 |
| AUD-04 | nicht angegeben | High | geschlossen | WP-132, WP-241 |
| AUD-05 | nicht angegeben | High | geschlossen | WP-133 |
| AUD-07 | nicht angegeben | High | geschlossen | WP-134 |
| AUD-09 | nicht angegeben | High | geschlossen | WP-133 |
| AUT-04 | [B] | High | geschlossen | WP-012, WP-067 |
| AUT-10 | [B] | High | geschlossen | WP-050 |
| BLK-01 | [B] | High | geschlossen | WP-181 |
| CI-01 | [B], Teilfelder [P2]/[P3] | High | geschlossen | WP-120 |
| CI-03 | [B] | High | geschlossen | WP-117 |
| CI-05 | [B] | High | geschlossen | WP-031, WP-169, WP-170 |
| CI-11 | [B] | High | geschlossen | WP-172 |
| CI-12 | [B] | High | geschlossen | WP-173, WP-174 |
| CI-13 | [B] | High | geschlossen | WP-087, WP-369, WP-418 |
| COL-01 | [B] | High | geschlossen | WP-143 |
| COL-04 | [B] | High | geschlossen | WP-144, WP-145 |
| COL-06 | [B], Air-Gapped [O] | High | geschlossen | WP-102, WP-148 |
| COL-08 | [B] | High | geschlossen | WP-146 |
| DIS-01 | [B] | High | geschlossen | WP-149 |
| DIS-02 | [B], Profilliste (V) | High | geschlossen | WP-151 |
| DIS-03 | [B] | High | geschlossen | WP-150 |
| DIS-04 | [B] | High | geschlossen | WP-150, WP-152 |
| DIS-05 | [B] | High | geschlossen | WP-083 |
| DIS-09 | [B] | High | geschlossen | WP-153 |
| DIS-10 | [B] | High | geschlossen | WP-154 |
| ENT-01 | [B] | High | geschlossen | WP-071 |
| ENT-02 | [B] | High | geschlossen | WP-071 |
| ENT-03 | [B] | High | geschlossen | WP-072, WP-073 |
| ENT-04 | [B] | High | geschlossen | WP-074 |
| ENT-05 | [B] | High | geschlossen | WP-072 |
| ENT-06 | (V), [B–P4], [A] | High | geschlossen | WP-075 |
| ENT-07 | [B] | High | geschlossen | WP-072 |
| ENT-08 | [B] | High | geschlossen | WP-073 |
| EVT-01 | nicht angegeben | High | geschlossen | WP-136 |
| EVT-02 | nicht angegeben | High | geschlossen | WP-136 |
| GLO-12 | [B] | High | geschlossen | WP-083, WP-084 |
| GLO-13 | [B] | High | geschlossen | WP-091 |
| GQL-01 | [B] | High | geschlossen | WP-184 |
| GQL-02 | [B] | High | geschlossen | WP-185 |
| IMP-01 | [B] | High | geschlossen | WP-176 |
| IMP-02 | [B] | High | geschlossen | WP-177 |
| IMP-03 | [B] | High | geschlossen | WP-176 |
| IMP-04 | [B] | High | geschlossen | WP-179 |
| IMP-05 | [B] | High | geschlossen | WP-180 |
| IMP-06 | [B] | High | geschlossen | WP-180 |
| IMP-09 | [B] | High | geschlossen | WP-161 |
| INS-02 | [B] | High | geschlossen | WP-095 |
| INS-03 | [B] | High | geschlossen | WP-094 |
| INS-04 | [Q] | High | geschlossen | WP-200 |
| INS-05 | [B] | High | geschlossen | WP-096 |
| INS-06 | [B] | High | geschlossen | WP-156 |
| JOB-01 | [B] | High | geschlossen | WP-129 |
| JOB-02 | [B]; Zusätze [P2], [P3] | High | geschlossen | WP-131 |
| JOB-03 | [B] | High | geschlossen | WP-182 |
| JOB-04 | [B] | High | geschlossen | WP-183 |
| LCY-05 | [B] | High | geschlossen | WP-157 |
| MON-02 | [B] | High | geschlossen | WP-190 |
| NET-01 | [B] | High | geschlossen | WP-081, WP-090, WP-123 |
| NET-02 | [B] | High | geschlossen | WP-081, WP-082 |
| NET-03 | [B] | High | geschlossen | WP-082, WP-124 |
| NET-05 | [B] | High | geschlossen | WP-125 |
| NET-08 | [B] | High | geschlossen | WP-081 |
| NFR-01 | [B]; Teilumfang [P5] | High | geschlossen | WP-086, WP-205, WP-206 |
| NFR-02 | [B] | High | geschlossen | WP-177, WP-206, WP-335 |
| NFR-03 | [B]; Teilumfang [P5] | High | geschlossen | WP-085, WP-154, WP-206, WP-332, WP-333, WP-334 |
| NFR-05 | [B] | High | geschlossen | WP-100, WP-101 |
| NFR-06 | [B] | High | geschlossen | WP-135 |
| NFR-07 | [B] | High | geschlossen | WP-193 |
| NFR-09 | [B] | High | geschlossen | WP-105 |
| NFR-10 | [Q] | High | geschlossen | WP-203, WP-204, WP-206, WP-334 |
| NTF-01 | [B] | High | geschlossen | WP-137 |
| NTF-03 | [B] | High | geschlossen | WP-138 |
| NTF-04 | [B] | High | geschlossen | WP-139 |
| NTF-05 | [B]; Zusätze [P2], [P3] | High | geschlossen | WP-063, WP-140 |
| OPS-01 | [B] | High | geschlossen | WP-097 |
| OPS-02 | [B]; [P3]; [A] | High | geschlossen | WP-098 |
| OPS-03 | [B]; [P5] | High | geschlossen | WP-099, WP-131 |
| OPS-04 | [B]; [P5] | High | geschlossen | WP-100, WP-101 |
| OPS-05 | [B] | High | geschlossen | WP-022, WP-103, WP-140 |
| PRI-07 | Grundsatz (ohne Tag) | High | geschlossen | WP-146 |
| RBA-01 | [B] | High | geschlossen | WP-068 |
| RBA-05 | [P2–P4], [A] | High | geschlossen | WP-214 |
| RBA-06 | [P2] | High | geschlossen | WP-045 |
| RBA-08 | [B] | High | geschlossen | WP-069 |
| RCK-01 | [B] | High | geschlossen | WP-126 |
| RCK-02 | [B] | High | geschlossen | WP-175 |
| RCK-03 | [B] | High | geschlossen | WP-078 |
| REC-01 | [B] | High | geschlossen | WP-085 |
| REC-02 | [B] | High | geschlossen | WP-086 |
| REC-03 | [B] | High | geschlossen | WP-058 |
| REC-05 | [B] | High | geschlossen | WP-084 |
| REC-06 | [B] | High | geschlossen | WP-089 |
| REC-07 | [B] | High | geschlossen | WP-090 |
| REC-08 | [B] | High | geschlossen | WP-088 |
| REC-09 | [B] | High | geschlossen | WP-157 |
| REC-12 | [B] | High | geschlossen | WP-058, WP-089 |
| REL-01 | [B] | High | geschlossen | WP-079, WP-127 |
| REL-02 | [B] | High | geschlossen | WP-128 |
| REL-03 | [B] | High | geschlossen | WP-160 |
| REL-04 | [B] | High | geschlossen | WP-160 |
| SEC-06 | nicht angegeben | High | geschlossen | WP-066, WP-141 |
| SEC-07 | nicht angegeben | High | geschlossen | WP-070 |
| SEC-10 | nicht angegeben | High | geschlossen | WP-184 |
| SIM-01 | [B]; Teilumfang [P2] | High | geschlossen | WP-106, WP-203 |
| SIM-02 | [B] | High | geschlossen | WP-162 |
| TEC-09 | [B] | High | geschlossen | WP-092, WP-093, WP-094 |
| TEC-13 | [B] | High | geschlossen | WP-137 |
| TEC-14 | [B] | High | geschlossen | WP-130 |
| TEC-15 | [B] | High | geschlossen | WP-102 |
| TEN-03 | [B] | High | geschlossen | WP-007, WP-064 |
| TEN-09 | [Q] | High | geschlossen | WP-007, WP-065, WP-208, WP-340, WP-342, WP-409 |
| TEN-10 | [B] | High | geschlossen | WP-082 |
| TLC-01 | [B] | High | geschlossen | WP-111, WP-112 |
| TLC-02 | [B]; Reseller [P4] | High | geschlossen | WP-113 |
| TLC-03 | [B] | High | geschlossen | WP-114 |
| TOP-01 | [B] | High | geschlossen | WP-158 |
| TOP-02 | [B] | High | geschlossen | WP-159 |
| TST-01 | [B]; Teilumfang [P2] | High | geschlossen | WP-202 |
| TST-02 | [B] | High | geschlossen | WP-003, WP-065, WP-208 |
| TST-03 | [B]; Teilumfang [P2] | High | geschlossen | WP-200, WP-201 |
| TST-04 | [Q] | High | geschlossen | WP-001, WP-002, WP-003, WP-004 |
| UI-06 | [B] | High | geschlossen | WP-195 |
| UI-16 | [B] | High | geschlossen | WP-196, WP-197 |
| UI-17 | [B] | High | geschlossen | WP-199 |
| UI-18 | [B] | High | geschlossen | WP-193, WP-194 |
| ABN-01 | [Q] | High | geschlossen bis auf Gate-Nachweis | WP-200, WP-212 |
| AGT-01 | [P4] | High | verschoben (E-03) | WP-230 |
| AGT-02 | [P4] | High | verschoben (E-03) | WP-230 |
| AGT-04 | [P4] | High | verschoben (E-03) | WP-230 |
| AST-02 | [P2] | High | verschoben (E-03) | WP-223 |
| AST-04 | [P2] | High | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-077, WP-222 |
| AST-06 | [P2] | High | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-022, WP-223 |
| AST-07 | [P2] | High | verschoben (E-03) | WP-224 |
| AUT-08 | [P4] | High | verschoben (E-03) | WP-233 |
| COL-07 | [A], Relay [P4] | High | verschoben (E-03) | WP-219 |
| DOD-01 | [Q] | High | geschlossen bis auf Gate-Nachweis | WP-005, WP-213 |
| GATE-02 | [Q] | High | G1-Gate | WP-213 |
| GATE-03 | [Q] | High | geschlossen bis auf Gate-Nachweis | WP-092, WP-209, WP-210, WP-211 |
| GLO-11 | [Q] | High | geschlossen bis auf Gate-Nachweis | WP-005, WP-213 |
| IGA-01 | [A] | High | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-075, WP-232 |
| IGA-02 | [A] | High | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-075, WP-232 |
| IGA-03 | [A] | High | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-075, WP-232 |
| IGA-04 | [A] | High | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-075, WP-232 |
| IMP-08 | [P2] | High | verschoben (E-03) | WP-218 |
| INS-01 | [Q] | High | geschlossen bis auf Gate-Nachweis | WP-092, WP-211 |
| INV-01 | [P2] | High | verschoben (E-03) | WP-227 |
| INV-03 | [P2]; RFID/Direct-Access [P4] | High | verschoben (E-03) | WP-227 |
| LCY-02 | [P2] | High | verschoben (E-03) | WP-215 |
| LCY-03 | [P2] | High | verschoben (E-03) | WP-215 |
| MON-03 | [P3] | High | verschoben (E-03) | WP-234 |
| MON-04 | [P3] | High | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-022, WP-234 |
| NET-09 | [P2] | High | verschoben (E-03) | WP-216 |
| OVR-03 | [P2] | High | verschoben (E-03) | WP-221 |
| REC-04 | [P3] | High | verschoben (E-03) | WP-220 |
| REC-11 | [P4] | High | verschoben (E-03) | WP-220 |
| REL-09 | [P2] | High | verschoben (E-03) | WP-217 |
| REP-01 | [B] | High | geschlossen bis auf Gate-Nachweis | WP-006, WP-118, WP-129, WP-137, WP-213 |
| SEC-11 | nicht angegeben | High | geschlossen bis auf Gate-Nachweis | WP-004, WP-209 |
| STK-01 | [P2] | High | verschoben (E-03) | WP-225 |
| STK-02 | [P2] | High | verschoben (E-03) | WP-225 |
| STK-03 | [P2] | High | verschoben (E-03) | WP-225 |
| STK-04 | [P2] | High | verschoben (E-03) | WP-224 |
| STK-05 | [P2] | High | verschoben (E-03) | WP-226 |
| TKT-02 | [P2] | High | verschoben (E-03) | WP-229 |
| UI-11 | [P2] | High | verschoben (E-03) | WP-236 |
| UI-13 | [P4] | High | verschoben (E-03) | WP-237 |
| UI-14 | [P2–P4, A] | High | verschoben (E-03) | WP-237 |
| UI-15 | [P2] | High | verschoben (E-03) | WP-235 |
| WFL-04 | [P3]; delegierte Inventur [P4]; IGA [A] | High | verschoben (E-03) | WP-231 |
