# Entscheidungen – Korrekturplan G1

Stand: 2026-10-19. Fortsetzung der Nummerierung aus [`../entscheidungen.md`](../entscheidungen.md) (E-01 bis E-36, E-56 bis E-58) und [`../phase2/entscheidungen.md`](../phase2/entscheidungen.md) (E-37 bis E-55). Jede Entscheidung nennt Status, Frage, Optionen, Empfehlung, Übergangsregel sowie betroffene IDs und WPs. Alle Entscheidungen sind offen; ihre Empfehlung gilt als Übergangsregel, damit die WPs starten können. Widerspricht der Auftraggeber, wird das betroffene WP vor Beginn angepasst. Ausnahme war WP-388 (`t5-install-default`), das erst nach der Entscheidung von E-69 für Option 1 beginnen durfte; E-69 ist am 2026-10-21 mit Option 1 entschieden, WP-388 ist umgesetzt. E-72 ist am 2026-10-26 mit Option 1 entschieden, E-76 am 2026-10-26 mit Option 2. Unabhängig von Entscheidungen warten WP-457 (`t5-abn01-ab`), WP-453 (`t5-pentest-evidence`), WP-454 (`t5-subprocessors`), WP-455 (`t5-v2-ids`) auf die externen Lieferungen A-02, A-07 und A-11.

**Nachtrag vom 2026-10-28:** Der Auftraggeber hat alle bis dahin offenen Entscheidungen (31: E-59, E-60, E-61, E-65, E-66, E-67, E-68, E-70, E-73, E-74, E-75, E-77, E-78, E-79, E-80, E-81, E-82, E-83, E-84, E-85, E-86, E-87, E-88, E-89, E-90, E-91, E-92, E-93, E-94, E-95, E-100) nach der jeweiligen Empfehlung entschieden („Setze alle Entscheidungen wie empfohlen um“). Die Übergangsregeln, nach denen die betroffenen WPs bereits umgesetzt sind, gelten damit als Festlegung; Folge-WPs für andere Optionen entfallen.

## Übersicht

| Nr. | Thema | Status | Betroffene WPs |
|---|---|---|---|
| [E-59](#e-59) | Einordnung des Korrekturplans vor Phase 2 und Freigabe des Phase-2-Starts | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-596, WP-597, WP-598, WP-599, WP-600 |
| [E-60](#e-60) | Zurückgestellte P3-Befunde und Risikoakzeptanz für G1 | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-600 |
| [E-61](#e-61) | Anlage von Dokumenten durch client- oder site-gescopte Principals | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-341 |
| [E-62](#e-62) | Beziehungen zwischen eigenem CI und org-weitem CI | entschieden: Option 2 (Auftraggeber, 2026-10-20) | WP-345 |
| [E-63](#e-63) | Client-/Site-Scope für Metriken (WP-025 AK1 gegenüber ADR 0001 Punkt 4) | entschieden: Option 1 (Auftraggeber, 2026-10-20) | WP-350 |
| [E-64](#e-64) | Widerruf von Service-Account-Keys bei Deaktivierung des anlegenden Benutzers | entschieden: Option 2 (Auftraggeber, 2026-10-20) | WP-359 |
| [E-65](#e-65) | MFA-Pflicht im Login-Pfad ohne Keycloak-Admin-Client (AUT-09) | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-367 |
| [E-66](#e-66) | API-07/API-08: Bulk-Statusänderung bei erzwungenem If-Match | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-368 |
| [E-67](#e-67) | Credentials eines Discovery-Scopes ohne credential_ids | entschieden: Option A (Auftraggeber, 2026-10-28) | WP-376, WP-378, WP-384, WP-496 |
| [E-68](#e-68) | Credential-Art für das NAS-Plugin (Synology DSM) | entschieden: Option A (Auftraggeber, 2026-10-28) | WP-379 |
| [E-69](#e-69) | TEC-15: Installationsquelle für die Signaturprüfung vor der Installation | entschieden: Option 1 (Auftraggeber, 2026-10-21) | WP-387, WP-388, WP-595 |
| [E-70](#e-70) | Kombination von Client- und Site-Zuweisungen eines Nutzers (RBA-03) | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-391 |
| [E-71](#e-71) | LOC-11: Löschen einer Site mit Netz- und Discovery-Objekten | entschieden: Option 1 (Auftraggeber, 2026-10-25) | WP-411 |
| [E-72](#e-72) | Änderung ausgelieferter Migrationen 000056 (down) und 000066 (up) | entschieden: Option 1 (Auftraggeber, 2026-10-26) | WP-427, WP-560, WP-574 |
| [E-73](#e-73) | Indizierte Attributfilter unter Row-Level-Security | entschieden: Option A (Auftraggeber, 2026-10-28); Ergänzung entschieden (Auftraggeber, 2026-10-26) | WP-435, WP-607 |
| [E-74](#e-74) | OPS-04 in Kubernetes: Datenbankseite der WAL-Archivierung | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-444 |
| [E-75](#e-75) | OPS-05: Alarmierung des Betriebs über NTF-Kanäle | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-448 |
| [E-76](#e-76) | GATE-02: Abnahme durch Erklärung des Auftraggebers statt Nachweis | entschieden: Option 2 (Auftraggeber, 2026-10-26) | WP-450, WP-451, WP-453, WP-454, WP-455, WP-457, WP-595, WP-600 |
| [E-77](#e-77) | NFR-06 „Logs 30 Tage“ in den Auslieferungen | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-458 |
| [E-78](#e-78) | CI-06: Berechtigung der einheitlichen CI-Historie | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-475 |
| [E-79](#e-79) | NFR-04: Verlust von Metrik-Samples und Traps bei Upload-Fehlern | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-483 |
| [E-80](#e-80) | CI-12: Unmerge nach späteren Änderungen am Survivor | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-485 |
| [E-81](#e-81) | DB-05: Verknüpfung eines CI mit einem Asset ohne Standort | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-486 |
| [E-82](#e-82) | AUD-03: Datenmodell der ci_change-Zeilen | entschieden: Option 2 (Auftraggeber, 2026-10-28) | WP-487, WP-488, WP-489 |
| [E-83](#e-83) | IMP-02: Traversierung per Ebenen-BFS statt rekursivem CTE | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-490 |
| [E-84](#e-84) | DIS-10: Poll-Intervalle je Scope und Zeitzone von Cron-Zeitplänen | entschieden: Option A (Auftraggeber, 2026-10-28) | WP-495, WP-496 |
| [E-85](#e-85) | IMP-09: ausgeschaltete Outlets in der Redundanzkennung | entschieden: Option A (Auftraggeber, 2026-10-28) | WP-498 |
| [E-86](#e-86) | Bearbeitungs- und Löschrechte org-weit geteilter Sichten (VIE-01) | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-518, WP-555 |
| [E-87](#e-87) | CI-Detail unter gewähltem Client-/Site-Scope (WP-191) | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-524 |
| [E-88](#e-88) | Nachweis der Browser-Vorversionen (UI-18, NFR-07) | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-531 |
| [E-89](#e-89) | Review-Merge bei ambiguous_identity: synchroner Effekt mit gespiegeltem Job (JOB-04, Folge zu E-25) | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-554 |
| [E-90](#e-90) | REC-07: Entfernen manuell gepflegter IPs und Interfaces | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-580 |
| [E-91](#e-91) | NTF-03: Ausprägung von Abonnements und Org-Branding in Phase 1 | entschieden: Option 2 (Auftraggeber, 2026-10-28) | WP-588 |
| [E-92](#e-92) | Instanzattributdefinitionen und DB-03 („attribute_definition einzige Quelle“) | entschieden: Option 2 (Auftraggeber, 2026-10-28) | WP-589 |
| [E-93](#e-93) | Materialisierung org-globaler Attributdefinitionen (MET-16) | entschieden: Option 2 (Auftraggeber, 2026-10-28) | WP-590 |
| [E-94](#e-94) | Korrektur der Migration 000110 (Duplikat-Suffixe der Interfaces) | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-592 |
| [E-95](#e-95) | Durchsetzung von allowed_source_types, allowed_target_types und cardinality (REL-01) | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-592 |
| [E-96](#e-96) | TimescaleDB-Version des Datenbank-Images und Funde seines Basisimages | entschieden: Option 2 (Auftraggeber, 2026-10-21) | WP-389 |
| [E-97](#e-97) | Obergrenze der Dauer von Alarmregeln (MON-01) | entschieden: 9600 h (Auftraggeber, 2026-10-25) | WP-406 |
| [E-98](#e-98) | Webhook-Abos und der Scope ihres Erzeugers (TEN-09) | entschieden: Option B (Auftraggeber, 2026-10-25) | WP-601 |
| [E-99](#e-99) | Auswahl der Oberflächensprache (GLO-10, UI-18) | entschieden (Auftraggeber 2026-10-27): Option 1 | WP-523 |
| [E-100](#e-100) | Upgrade von Datenbanken mit 000056 vor E-26 (fehlende `migration_quarantine`, Migration 000089) | entschieden: Option 1 (Auftraggeber, 2026-10-28) | WP-649 |
| [E-101](#e-101) | WP-332 AK 3: Rahmen und Ledger eines Ingest-Batches in einer oder in zwei Transaktionen (NFR-03) | offen; Übergangsregel: Empfehlung (Option 1) | WP-657 |

## E-59

**Thema:** Einordnung des Korrekturplans vor Phase 2 und Freigabe des Phase-2-Starts

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** Darf Phase 2 (WP-243 ff.) beginnen, bevor die Wiederholungsprüfung und die erneute Gate-Entscheidung G1 (WP-600 (`k-gate`)) vorliegen?

**Optionen:**

- 1: Phase 2 beginnt erst nach WP-600 (`k-gate`) (GO). Der Phase-2-Plan setzt den vollständigen Phase-1-Zielzustand voraus; GATE-02 verlangt alle [B]-Anforderungen PASS.
- 2: Phase 2 beginnt nach K0–K3 (alle P0/P1 behoben und ihre WPs wiederholt geprüft) parallel zu K4–K6; P2-Mängel werden in Kauf genommen, Konfliktgefahr in gemeinsam geänderten Dateien.
- 3: Phase 2 beginnt sofort parallel zum Korrekturplan.

**Empfehlung:** Option 1. Die Phase-2-Pakete bauen auf Scope-, Rechte-, Metamodell- und Discovery-Pfaden auf, die dieser Plan ändert; ein früher Start verdoppelt Korrekturen und widerspricht GATE-02.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** Bis zur Entscheidung startet kein Phase-2-WP. WP-243 (`p2-trace`) liest bei seiner Umsetzung alle drei Pläne.

**Betroffene IDs:** GATE-01, GATE-02, SEQ-01

**Betroffene WPs:** WP-596, WP-597, WP-598, WP-599, WP-600

## E-60

**Thema:** Zurückgestellte P3-Befunde und Risikoakzeptanz für G1

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** Dürfen P3-Befunde, die dieser Plan begründet zurückstellt, bei der erneuten Gate-Entscheidung G1 offen bleiben?

**Optionen:**

- 1: Ja, wenn sie kein Akzeptanzkriterium eines Phase-1-WPs verletzen; sie stehen als bekannte Einschränkungen in `docs/acceptance/g1-gate.md`, ihr Defekttest bleibt in der Erwartungsliste.
- 2: Nein, alle P3-Befunde werden vor G1 behoben.

**Empfehlung:** Option 1. GATE-01 verlangt kein offenes Critical; P3-Befunde sind geringfügig. Zurückgestellt werden nur Befunde mit Begründung (Abschnitt „Zurückgestellte Befunde“).

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** Die Zurückstellung gilt vorläufig; widerspricht der Auftraggeber, entsteht je Befund ein K6-WP.

**Betroffene IDs:** GATE-01

**Betroffene WPs:** WP-600

## E-61

**Thema:** Anlage von Dokumenten durch client- oder site-gescopte Principals

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** E-09 verbietet gescopten Principals das Anlegen org-weiter Zeilen; ein Dokument ohne Link ist org-weit, und `POST /api/v1/documents` kennt heute keine Links (Links folgen über `POST /documents/{id}/links`). Wie legen gescopte Principals Dokumente an?

**Optionen:**

- 1. `POST /api/v1/documents` nimmt optionale `links` (entity_type, entity_id) an und legt Dokument und Links atomar an; für gescopte Principals ist mindestens ein sichtbarer Link Pflicht.
- 2. Nur org-weite Principals legen Dokumente an; gescopte Principals erhalten 403.
- 3. Dokumente erhalten ein eigenes Owner-Feld `client_id`; Links müssen im Owner-Client liegen.

**Empfehlung:** Option 1: hält MGT-01 für Client-Techniker nutzbar, bleibt E-09-konform und braucht keine Modelländerung.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** Option 2 (403 für gescopte Principals ohne Link) gilt bis zur Entscheidung und wird von WP-341 (`t1-doc-window-writes`) umgesetzt; Option 1 folgt nach Freigabe als eigenes Folge-WP (K4: `links` im POST /api/v1/documents mit Spec, schema.d.ts und Modell).

**Betroffene IDs:** MGT-01, TEN-05

**Betroffene WPs:** WP-341

## E-62

**Thema:** Beziehungen zwischen eigenem CI und org-weitem CI

**Status:** entschieden: Option 2 (Auftraggeber, 2026-10-20)

**Frage:** E-09 verbietet gescopten Principals das Schreiben org-weiter Zeilen. Darf ein client-gescopter Principal eine Kante (bzw. ein Interface) von einem eigenen CI zu einem sichtbaren org-weiten CI (client_id NULL) anlegen? Heute verlangt WITH CHECK beide Endpunkt-Clients im Scope.

**Optionen:**

- 1. Nein (heutiges Verhalten); Kanten zu org-weiten CIs legen nur org-weite Principals an.
- 2. Ja, wenn mindestens ein Endpunkt nicht NULL und im Scope ist und der andere NULL oder im Scope (analog Site); die Kante ist dann nicht org-weit.

**Empfehlung:** Option 2: MSP-Techniker modellieren Abhängigkeiten zu geteilter Infrastruktur; die Kante trägt den Client des eigenen Endpunkts und ist nur dort sichtbar.

**Übergangsregel (bis 2026-10-20):** Option 1 gilt bis zur Freigabe; WP-345 (`t1-edge-scope`) pinnt die Abweisung (42501 bzw. generisches 403/404). Option 2 wird nur umgesetzt, wenn die Freigabe vor dem Merge von WP-345 (`t1-edge-scope`) vorliegt, sonst als Folge-WP.

**Umsetzung:** Folge-Commit zu WP-345 (`t1-edge-scope`), Migration 000142_edge_orgwide_endpoint: WITH CHECK und die restriktiven UPDATE-/DELETE-Policies von ci_relationship und relationship_suppression verlangen je Dimension (Client, Site) `(source NULL oder im Scope) AND (target NULL oder im Scope) AND (source oder target im Scope)`; USING bleibt, die Kante trägt den Client/Site des eigenen Endpunkts und ist nur dort sichtbar. Kanten zwischen zwei org-weiten Endpunkten bleiben org-weiten Principals vorbehalten (E-09). network_interface hat nur ein CI: Ein Interface an einem org-weiten CI ist eine org-weite Zeile und bleibt abgewiesen; Kanten zwischen Interfaces des eigenen und des org-weiten CI sind erlaubt. Test: `TestCIChildEdgeToOrgWideCI`, `TestCIChildEdgeToOrgWideCISite` in `backend/internal/tenant/rls/ci_children_integration_test.go`.

**Betroffene IDs:** TEN-05, IMP-07

**Betroffene WPs:** WP-345

## E-63

**Thema:** Client-/Site-Scope für Metriken (WP-025 AK1 gegenüber ADR 0001 Punkt 4)

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-20)

**Frage:** WP-025 AK1 verlangt abgeleitete client_id/site_id an metric_sample; ADR 0001 Punkt 4 sieht Client-/Site-Prädikate als Sub-Select in den Security-Barrier-Views vor. metric_sample ist eine komprimierte Hypertable ohne RLS. Welche Variante gilt?

**Optionen:**

- 1. Sub-Select-Prädikat über ci mit den Scope-GUCs in beiden Views; keine Denormalisierung; WP-025 AK1 gilt für metric_sample damit als erfüllt.
- 2. Denormalisierte client_id/site_id in metric_sample (Trigger beim Ingest, Backfill über komprimierte Chunks, Propagation bei CI-Verschiebung).
- 3. Dokumentierte Ausnahme ohne DB-seitigen Client-/Site-Scope; Schutz nur in den Store-Abfragen.

**Empfehlung:** Option 1: folgt der ADR, ist bei CI-Verschiebungen immer aktuell und vermeidet Schreibkosten auf komprimierten Chunks; Messwerte in der ADR.

**Übergangsregel (bis 2026-10-20):** WP-350 (`t1-metric-views`) setzt Option 1 um; bei abweichender Entscheidung folgt ein eigenes WP.

**Umsetzung:** WP-350 (Migration 000143): beide Metrik-Views prüfen Client und Site über das CI der Probe; Messwerte in ADR 0001 Punkt 4. Abfragen ohne CI-Filter sind messbar langsamer; Option 2 bleibt der Ausweg, falls sie zeitkritisch werden.

**Betroffene IDs:** TEN-02, TEN-05, TEC-06, MON-01

**Betroffene WPs:** WP-350

## E-64

**Thema:** Widerruf von Service-Account-Keys bei Deaktivierung des anlegenden Benutzers

**Status:** entschieden: Option 2 (Auftraggeber, 2026-10-20)

**Frage:** Sollen bei der Deaktivierung eines Benutzers auch die API-Keys widerrufen werden, die er für Service-Accounts angelegt hat? TLC-04 verlangt, dass die API-Keys des Nutzers sofort ungültig werden; AUT-04/RBA-08 ordnen einen Key entweder einem Nutzer oder einem Service-Account zu.

**Optionen:**

- 1. Alle vom Benutzer angelegten Keys widerrufen, auch Service-Account-gebundene (Ist-Verhalten; Integrationen brechen beim Personalwechsel).
- 2. Service-Account-gebundene Keys bleiben gültig (sie gehören dem Service-Account); der Audit-Eintrag der Deaktivierung listet sie, org_admins erhalten eine Benachrichtigung zur Rotation.
- 3. Service-Account-gebundene Keys bleiben ohne weitere Maßnahme gültig.

**Empfehlung:** Option 2: entspricht AUT-04/RBA-08 (der Key gehört dem Service-Account), hält Integrationen am Laufen und macht das Restrisiko sichtbar, dass die ausgeschiedene Person das Secret bei der Erstellung gesehen hat.

**Übergangsregel (bis 2026-10-20):** Bis zur Entscheidung gilt Option 1 (fail-closed, Ist-Verhalten); WP-359 (`t2-deactivation-tenant`) schreibt sie mit Test und OpenAPI-Beschreibung fest. Wird Option 2 entschieden, ändern sich nur der Filter `service_account_id IS NULL` in `RevokeUserAPIKeys`, der Audit-Eintrag und die Testerwartung (im selben WP, wenn vor dem Merge entschieden, sonst Folgeänderung ≤ 0,5 PT).

**Umsetzung:** Folge-Commit zu WP-359 (`t2-deactivation-tenant`). `identity.RevokeUserAPIKeys` widerruft nur noch die eigenen Keys des Benutzers (`AND service_account_id IS NULL`); `identity.KeptServiceAccountKeys` liest in derselben Transaktion die aktiven, nicht abgelaufenen Service-Account-Keys, die er angelegt hat. `deactivateInTx` (Deaktivierung per PATCH, Anonymisierung und alle anderen Aufrufer von `UpdateUser`/`AnonymizeUser`) schreibt sie als `service_account_keys_kept` (id, key_prefix, name, service_account_id, service_account_name; nie Secret oder Hash) in den Audit-Eintrag `user.deactivated` bzw. `user.anonymized` und legt, wenn es welche gibt, im selben Transaktionsschritt das Outbox-Ereignis `apikey.creator_deactivated` an. Die NTF-05-Subscription (`notify.Subscriptions`) stellt es als kritische Benachrichtigung der Kategorie `apikey` den aktiven Inhabern der Rolle `org_admin` der Organisation mit `apikey:manage` über die Kanäle der Organisation zu (Notification-Center, E-Mail), Dedup über die Event-ID. Ein Fehler beim Lesen, im Audit oder in der Outbox rollt die Deaktivierung zurück (fail-closed wie WP-359). OpenAPI-Beschreibung von `PATCH /users/{id}` und `POST /users/{id}/anonymize` nachgeführt. Test: `TestDeactivationRevokesServiceAccountKeysOfCreator` in `backend/internal/identity/deactivation_integration_test.go` (Service-Account-Key bleibt gültig, `api_keys_revoked` = 1, Audit listet den Key, je org_admin genau eine Benachrichtigung, keine in der fremden Organisation).

**Betroffene IDs:** TLC-04, AUT-04, RBA-08

**Betroffene WPs:** WP-359

## E-65

**Thema:** MFA-Pflicht im Login-Pfad ohne Keycloak-Admin-Client (AUT-09)

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** Der Login-Pfad (WP-367 (`t2-mfa-login-path`)) weist einem org_admin aus IdP-Gruppe oder Default-Rolle `mfa_required` über die Keycloak-Admin-API zu. Ohne `RETICORA_KEYCLOAK_ADMIN_CLIENT_ID` ist der Enforcer außerhalb von `development` fail-closed (WP-358 (`t2-mfa-role-sync`)). Das Standard-Compose läuft mit `RETICORA_ENVIRONMENT: production` (`deploy/docker-compose/docker-compose.yml:304`) und ohne Admin-Client (gesetzt nur in `frontend/e2e/fixtures/docker-compose.e2e.yml:21`); der Installer nimmt den Admin in die Gruppe `reticora-admin` auf (`install-cloud.sh:1543-1546`). Ein Aufruf für diese Gruppe beendete jeden Login des Installer-Admins mit 503 und bräche Onboarding- und install-smoke-Suite. Wie verhält sich der Login-Pfad, wenn kein Admin-Client konfiguriert ist?

**Optionen:**

- 1. Der Login-Pfad ruft `RequireMFA` nicht für die Gruppe `reticora-admin` auf, die `mfa_required` über den Realm trägt (WP-357 (`t2-mfa-accounts`)), sondern nur für die IdP-Gruppe `org_admin` und die Default-Rolle; `RETICORA_DEFAULT_PROVISION_ROLE=org_admin` ohne Admin-Client ist außerhalb von `development` ein Startfehler.
- 2. Installer und Compose konfigurieren den Keycloak-Admin-Client; der Login-Pfad ruft `RequireMFA` für alle Wege zu org_admin auf.
- 3. Fail-open im Login-Pfad: Ein Fehler des Enforcers wird geloggt und als Metrik gezählt, der Login läuft weiter.

**Empfehlung:** Option 1: Die Gruppe `reticora-admin` ist nach WP-357 (`t2-mfa-accounts`) bereits über den Realm abgesichert (der Installer-Admin trägt zusätzlich die Realm-Rolle org_admin mit dem Komposit `mfa_required`, `install-cloud.sh:1541`); ein Aufruf brächte dort keinen Schutzgewinn, sperrte aber den Admin jeder Standardinstallation aus. Die übrigen Wege bleiben fail-closed. Option 3 lässt im Fehlerfall einen org_admin ohne MFA zu (AUT-09 [B]); Option 2 erweitert Installer und Compose um ein weiteres Admin-Credential.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-367 (`t2-mfa-login-path`) setzt Option 1 um. Bei Option 2 oder 3 wird WP-367 (`t2-mfa-login-path`) vor Beginn angepasst (Option 2: Installer- und Compose-Änderung als eigenes WP vor WP-367 (`t2-mfa-login-path`), Ausnahme für `reticora-admin` und Startfehler entfallen).

**Betroffene IDs:** AUT-09

**Betroffene WPs:** WP-367

## E-66

**Thema:** API-07/API-08: Bulk-Statusänderung bei erzwungenem If-Match

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** Nach der Korrektur ist die Bulk-Statusänderung ein manueller Schreibvorgang (Override, Versionserhöhung). Bei aktivem Org-Setting require_if_match verlangt checkPrecondition ein If-Match je CI; die Bulk-API (API-08) hat kein Versionsfeld je ID. Was gilt?

**Optionen:**

- 1. Bulk ist ein bewusster Überschreibvorgang (intern If-Match *); kein 412, Version und Override werden geschrieben.
- 2. Optionales Versionsfeld je ID (items: [{id, version}]); bei erzwungenem If-Match ohne Version 412 je ID im Ergebnisbericht.
- 3. Bulk bei erzwungenem If-Match vollständig mit 412 abweisen.

**Empfehlung:** Option 1 für Phase 1 (API-07: If-Match optional und per Org-Setting erzwingbar für Einzeländerungen; API-08 ohne Versionsliste); Option 2 als spätere Erweiterung.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-368 (`t3-bulk-manual`) setzt Option 1 um.

**Betroffene IDs:** API-07, API-08, OVR-03

**Betroffene WPs:** WP-368

## E-67

**Thema:** Credentials eines Discovery-Scopes ohne credential_ids

**Status:** entschieden: Option A (Auftraggeber, 2026-10-28)

**Frage:** Welche Credentials erhält der Collector für einen aktiven Scope mit leerem credential_ids (Scope aus dem UI-Formular ohne Auswahl, angenommener scope_suggested-Vorschlag), und was erhält ein Collector ohne aktive Scopes? DIS-09 nennt credential_ids als Scope-Feld, COL-02 verlangt „nur Scope-relevante Credentials“; den leeren Fall legen beide nicht fest.

**Optionen:**

- A: Scope ohne credential_ids nutzt die Credentials seines Clients und die org-weiten (client_id NULL); ohne aktive Scopes leeres Bundle.
- B: Scope ohne credential_ids erhält keine Credentials (nur unauthentifizierte Protokolle); Formular und Bestätigungsablauf verlangen eine Auswahl.
- C: Org-weite Liste von Standard-Credentials in den Discovery-Einstellungen; Scope ohne credential_ids nutzt diese Liste.

**Empfehlung:** Option A: entspricht dem bisherigen Verhalten ohne Scopes, hält das Zero-Config-Onboarding nach INS-06 (Vorschlag bestätigen, Infrastruktur erscheint) funktionsfähig und begrenzt die Geheimnisse auf den Client des Scopes; der Defekttest TestAuditBundleCredentialSelection kodiert sie.

**Entscheidung:** Option A nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** Bis zur Entscheidung gilt Option A; WP-376 (`t4-bundle-select`) und WP-378 (`t4-cred-scope`) setzen sie um. Bei Option B oder C ändert ein Folge-WP nur die Auswahlregel in VerifyBundleTarget/CollectorScopes und den Testfall „Scope ohne credential_ids“.

**Betroffene IDs:** DIS-09, COL-02, INS-06

**Betroffene WPs:** WP-376, WP-378, WP-384, WP-496

## E-68

**Thema:** Credential-Art für das NAS-Plugin (Synology DSM)

**Status:** entschieden: Option A (Auftraggeber, 2026-10-28)

**Frage:** Mit welcher Credential-Art meldet sich das NAS-Plugin an, nachdem der Alias nas→ssh entfällt (F-028)? COL-02 verweist auf „Typen wie v2“; die v2-Texte fehlen im Repository (E-02 offen), die Arten in credential.go kennen kein NAS.

**Optionen:**

- A: Neue Art nas (Benutzer/Passwort), Migration der CHECK-Constraint von credential.kind, OpenAPI-Enum.
- B: NAS nutzt ausschließlich am Scope explizit gebundene Credentials der Art ssh_password (keine Default-Zuordnung).
- C: NAS ohne Anmeldung (nur unauthentifizierte Daten) bis zur Lieferung der v2-Texte.

**Empfehlung:** Option A: trennt NAS-Konten von SSH-Root-Konten (Least Privilege, COL-02), passt zur Namespace-Regel des Collectors und lässt sich mit den v2-Texten nachträglich abgleichen.

**Entscheidung:** Option A nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** Bis zur Entscheidung gilt Option A; WP-379 (`t4-nas-tls`) setzt sie um. Zwischen dem Merge von WP-378 (`t4-cred-scope`) und WP-379 (`t4-nas-tls`) erhält das NAS-Plugin keine Credentials (fail-closed).

**Betroffene IDs:** COL-02, DIS-04

**Betroffene WPs:** WP-379

## E-69

**Thema:** TEC-15: Installationsquelle für die Signaturprüfung vor der Installation

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-21)

**Frage:** Welche Artefakte installiert der Installer standardmäßig, damit 'Signaturprüfung vor Installation' greift: die signierten Release-Images und -Binaries oder einen lokalen Quellbuild, der nicht signiert sein kann?

**Optionen:**

- Standard sind die signierten Release-Artefakte (Images per 'cosign verify', Collector-Binary per 'cosign verify-blob', Quelle nur von einem Release-Tag mit Manifestprüfung, fail-closed); der Quellbuild bleibt per ausdrücklichem Schalter --build-from-source mit Warnung 'nicht signaturgeprüft'.
- Standard bleibt der Quellbuild; geprüft werden ein signierter Quell-Tarball des Releases (cosign verify-blob) und die Go-Toolchain (sha256); die lokal gebauten Images sind nicht signiert.
- Signaturprüfung nur im Registry-Modus (RETICORA_IMAGE_REGISTRY); der Quellbuild bleibt ungeprüft.

**Empfehlung:** Option 1.

**Übergangsregel (bis 2026-10-21):** WP-387 (`t5-install-signature`) setzt die fail-closed-Prüfung im Release-Modus von install-cloud.sh, für den Collector-Release-Download in install-vm.sh und für den Quellbezug von einem Release-Tag in install.sh und install-vm.sh um (in allen Optionen nötig) und lässt den Standard unverändert; der Quellbuild meldet 'nicht signaturgeprüft'. Nach Entscheidung für Option 1 stellt WP-388 (`t5-install-default`) den Standard um; bei Option 2 oder 3 entfällt WP-388 (`t5-install-default`) mit Vermerk.

**Umsetzung:** WP-388 (`t5-install-default`): install.sh, install-cloud.sh und install-vm.sh installieren standardmäßig die signierten Release-Artefakte des genannten (`--ref`/`--release v<version>`) bzw. neuesten Releases — Quelle nur von einem Release-Tag mit Manifestprüfung, Images per 'cosign verify' mit Digest-Pin, Collector-Binary per 'cosign verify-blob' bzw. im Docker-Modus das signierte Collector-Image; install-cloud.sh prüft zusätzlich, dass sein Checkout (Compose-Datei, Realm, Migrationen) die signierte Quelle dieses Releases ist. Der Quellbuild nur noch per `--build-from-source` (`RETICORA_BUILD_FROM_SOURCE=1`) mit der Warnung 'not signature-verified'; ein Ref ohne Release-Tag bricht ohne den Schalter mit Hinweis ab. Die CI-Läufe des unveröffentlichten Stands (install-smoke.yml, outage.yml) bauen mit dem Schalter, der Job release-mode prüft den Standardpfad auf Tags v*.

**Betroffene IDs:** TEC-15, COL-06, INS-01

**Betroffene WPs:** WP-387, WP-388, WP-595

## E-70

**Thema:** Kombination von Client- und Site-Zuweisungen eines Nutzers (RBA-03)

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** RBA-03 nennt „Rechte = Vereinigung aller Rollen“ und „Scopes = Vereinigung der Scope-Mengen je Ebene“. Mit per AND verknüpften RLS-Dimensionen schneidet eine zusätzliche Site-Rolle den Client-Zugriff (Client-Rolle C1 + Site-Rolle S1 → nur C1 ∩ (S1 oder NULL)). Welche Sichtbarkeit gilt? Zu berücksichtigen ist F-014 (T6 WP-390 (`t6-site-grant-scope`)): Ein Site-Grant setzt künftig den Client des Standorts als Client-Dimension, sodass gemischte Grants die Client-Menge erweitern.

**Optionen:**

- 1. Vereinigung je Dimension, Dimensionen per AND (Wortlaut RBA-03, heutiges Verhalten, fail-closed); dokumentiert in API und Rollen-Doku.
- 2. Vereinigung der (Client × Site)-Rechtecke je Grant; erfordert Scope-Tupel statt der GUC-Mengen aus TEN-04.
- 3. Gemischte Zuweisungen, deren Site nicht in einem zugewiesenen Client liegt, werden mit 422 abgewiesen.

**Empfehlung:** Option 1 mit Dokumentation; Option 2 nur als Phase-2-Thema, weil sie TEN-04 ändert.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** Option 1 gilt; WP-391 (`t1-team-scope`) fixiert sie per Integrationstest und dokumentiert sie.

**Betroffene IDs:** RBA-03, TEN-04, TEN-05

**Betroffene WPs:** WP-391

## E-71

**Thema:** LOC-11: Löschen einer Site mit Netz- und Discovery-Objekten

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-25)

**Frage:** Darf eine Site gelöscht werden, an der VLANs, Subnetze, Collectoren, Discovery-Scopes, Agent-Enrollment-Tokens oder Merge-Tombstones hängen? LOC-11 nennt nur CIs, Assets, Mounts und Kinder; heute kaskadieren bzw. lösen die Fremdschlüssel diese Objekte still (F-024).

**Optionen:**

- 1. 409 mit den Abhängigkeitsarten vlan, subnet, collector, discovery_scope, agent_enrollment_token, ci_merge (FK RESTRICT); die Objekte müssen vorher entfernt oder umgehängt werden. Abgelaufene bzw. widerrufene Tokens und ci_merge-Tombstones nach tombstone_until blockieren nicht und werden in der Lösch-Transaktion entfernt; Folge: eine Site mit einem Merge ist bis zu 12 Monate (tombstone_until, 000131) nicht löschbar, auch ohne CIs.
- 2. Löschen erlaubt; die Objekte werden org-weit (site_id NULL), VLANs und Tokens werden nicht mehr gelöscht, das Lösen wird je Objekt auditiert.
- 3. Explizites Lösen per Parameter detach=true, sonst 409.

**Empfehlung:** Option 1: entspricht dem Schutzgedanken von LOC-11 und OVR-01 (kein stiller Verlust); die bis zu 12 Monate lange Sperre durch Tombstones ist hinnehmbar, weil sie mit der 301-Weiterleitung des Victims nach CI-12 (12 Monate) endet; Option 3 kann später als Erweiterung folgen.

**Umsetzung:** WP-411 (`t3-loc-delete-409`), Migration 000157, gemergt mit PR #90 (Real-Stack L05 grün). Die Abhängigkeitsliste steht in `locations/dependencies.go`. Option 3 kann später als Erweiterung folgen. Sie sollte die Objekte dann auf eine andere Site umhängen statt `site_id` zu leeren, sonst würde ihre Sichtbarkeit still ausgeweitet.

Folgen der Entscheidung:
- Eine Site mit einem Merge-Tombstone ist bis `tombstone_until` (höchstens 12 Monate) nicht löschbar.
- Ein verbrauchtes, noch nicht abgelaufenes Enrollment-Token blockiert bis zu seinem Ablauf (Standard 24 h, höchstens 30 Tage). Das Schema kennt keinen Widerrufsstatus.

**Betroffene IDs:** LOC-11

**Betroffene WPs:** WP-411

## E-72

**Thema:** Änderung ausgelieferter Migrationen 000056 (down) und 000066 (up)

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-26)

**Frage:** Der Projektplan ändert bestehende Migrationen nicht. 000056.down droppt die clusterweite Rolle reticora_app und scheitert in geteilten Clustern (F-193); 000066.up leert nicht konforme Storage-Keys ohne Bericht je Dokument (F-187). Beides lässt sich nur in den Migrationen selbst beheben.

**Optionen:**

- 1. Beide Migrationen anpassen (000056 nur im down-Pfad: DROP ROLE nur ohne fremde Abhängigkeiten; 000066 up: Bericht je Dokument vor dem Leeren), mit Release-Hinweis; wirkt nur für Rollbacks bzw. noch nicht migrierte Installationen.
- 2. Migrationen unverändert; Betriebsdoku (manuelles REVOKE vor Rollback, Hinweis auf irreversibel geleerte Keys) und Roundtrip-Test in eigener Datenbank.

**Empfehlung:** Option 1 nach dem Präzedenzfall E-26; bereits migrierte Installationen erhalten den Release-Hinweis.

**Umsetzung:** Der Auftraggeber hat am 2026-10-26 Option 1 nach der Empfehlung entschieden. Damit dürfen die ausgelieferten Migrationen in genau diesen Punkten geändert werden; jede Änderung bekommt einen Release-Hinweis in `docs/betrieb/upgrade-hinweise.md`.
- 000056 down: WP-427 (`t1-mig56-down`, K3) entzieht die Rechte in der eigenen Datenbank und droppt `reticora_app` nur ohne fremde Abhängigkeiten; `TestMigrationsRoundtrip` läuft in einer eigenen Datenbank (F-193).
- 000066 up: WP-560 (`t1-doc-blob-cleanup`, K6) schreibt vor dem Leeren je Dokument einen Bericht in `migration_quarantine` (F-187).
- 000056 down auf Altinstallationen: WP-574 (`t3-migration-guards`, K6) prüft `to_regclass('migration_quarantine')` (F-224).
Bereits migrierte bzw. zurückgerollte Installationen betrifft die Änderung nicht; für sie gilt der Release-Hinweis.

**Betroffene IDs:** TEN-03, MGT-01

**Betroffene WPs:** WP-427, WP-560, WP-574

## E-73

**Thema:** Indizierte Attributfilter unter Row-Level-Security

**Status:** entschieden: Option A (Auftraggeber, 2026-10-28)

**Frage:** Die Ausdrucksindizes idx_attr_<id> greifen für die App-Rolle nicht, weil jsonb_object_field_text (->>) nicht leakproof ist. Wie wird der Attributfilter indexfähig: über eine als LEAKPROOF markierte, nicht inlinebare Wrapper-Funktion (verlangt Superuser bei der Migration), über eine trigger-gepflegte Seitentabelle, über gespeicherte generierte Spalten je Attribut oder gar nicht (dokumentierte Ausnahme)?

**Optionen:**

- A: LEAKPROOF-Wrapper reticora_attr_text(jsonb, text), nicht inlinebar (plpgsql oder SQL mit SET search_path), für Index und Filter; Migration mit Superuser (Installer-Rolle), Sicherheitsreview der Funktion.
- B: Seitentabelle ci_attribute_value für durchsuchbare Attribute (Trigger auf ci, Backfill über den DDL-Job), Filter per Semijoin mit leakproof-Gleichheit.
- C: Ausnahme akzeptieren: Attributfilter bei 10.000 Objekten ohne Indexnutzung (p95 im Lastlauf belegt), SR-04 und lasttest-g1.md korrigieren, Folge-WP vor GA.
- D: Der DDL-Job legt je durchsuchbarem Attribut eine gespeicherte generierte Spalte (attributes->>'<key>') mit B-Tree-Index an (Muster hostname_lc, 000134); Filter mit leakproof-Textgleichheit; kein Superuser, Tabellen-Rewrite je neuem Attribut.

**Empfehlung:** Option A, solange Migrationen mit der Installer-Rolle (Superuser) laufen (Compose, VM, K8s mit eigenem PostgreSQL): kleinster Eingriff ohne Mehrkosten im Ingest-Pfad (NFR-03). Option D scheidet trotz fehlender Superuser-Pflicht aus: Attributdefinitionen gelten je Organisation (citype.IndexStatement), jede durchsuchbare Definition legte eine Spalte der gemeinsamen Tabelle ci an (PostgreSQL-Grenze 1.600 Spalten einschließlich gelöschter, bei 500 Organisationen nach NFR-01 absehbar erreicht), und jeder Neuaufbau schreibt ci mit ACCESS-EXCLUSIVE-Sperre um und blockiert den Ingest. Option B nur, wenn für G1 ein Betrieb ohne Superuser (Managed PostgreSQL) zugesagt wird.

**Entscheidung:** Option A nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** Bis zur Entscheidung gilt Option A; WP-435 (`t4-attr-filter-index`) setzt sie um, die Migration bricht ohne Superuser mit Hinweis auf Option B ab.

**Ergänzung vom 2026-10-26 (Index je Schlüssel):** Der Auftraggeber hat am 2026-10-26 mit der Freigabe der K3-Nachträge entschieden, die Indexe nach Option A je Attribut-Schlüssel statt je Definition und Organisation anzulegen.
- **Anlass:** Die Lastläufe von WP-439 auf den NFR-01-Volumina: Mit 500 Organisationen, die denselben Schlüssel durchsuchbar machen, trägt `ci` 528 statt 28 Indexe. Planung und Schreibpfad jeder Abfrage auf `ci` werden dadurch etwa zehn- bzw. dreimal teurer, generische Pläne nutzen die Indexe nie (Ursache U1, `docs/acceptance/lasttest-g1.md`, Punkt 11).
- **Festlegung:** Für eine nicht eindeutige durchsuchbare Definition ohne `ci_type_id` gibt es einen Index je Schlüssel, `(organization_id, reticora_attr_text(attributes, '<key>'))` mit `deleted_at IS NULL` und ohne Organisationsprädikat. Er fällt erst, wenn keine Definition den Schlüssel mehr nutzt. Eindeutige Definitionen und Definitionen mit `ci_type_id` behalten ihren Index je Definition. Die Mandantentrennung bleibt bei RLS (`ci_isolation`, FORCE) und dem expliziten Organisationsfilter; `reticora_attr_text` bleibt LEAKPROOF.
- **Grenze:** Die Zahl der Indexe wächst weiter mit der Zahl verschiedener Schlüssel. Wächst sie über den Bedarf hinaus, ist Option B (Seitentabelle) der Pfad vor GA.
- **Umsetzung:** WP-607 (`k3n-attr-index-key`), Migration 000164.

**Betroffene IDs:** ABN-02, NFR-02, NFR-01

**Betroffene WPs:** WP-435, WP-607

## E-74

**Thema:** OPS-04 in Kubernetes: Datenbankseite der WAL-Archivierung

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** Liefert das Produkt für Kubernetes die PostgreSQL-Instanz mit WAL-Archivierung selbst aus, oder setzt es eine vom Betreiber bereitgestellte Datenbank (extern oder Managed) mit PITR voraus?

**Optionen:**

- Kustomize-Komponente postgres: StatefulSet mit dem signierten Image reticora-postgres, archive_mode und archive_command über pgBackRest, pgBackRest-TLS-Server für den Repository-Host; im prod-Overlay eingebunden.
- Betreiber-Datenbank als verbindliche Vorbedingung; das Produkt prüft per CronJob ('pgbackrest check', Fortschritt von pg_stat_archiver) und alarmiert bei fehlender Archivierung.
- Managed PostgreSQL mit PITR des Hosters (E-29 Option 3); Nachweis über Terraform-Variablen und Betriebsdokumentation.

**Empfehlung:** Option 1: entspricht E-29 ('Compose und Kubernetes'), nutzt das bereits gebaute und signierte Image und ist per kind-Smoke prüfbar.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-444 (`t5-k8s-wal`) setzt Option 1 als optionale Komponente um, die das prod-Overlay einbindet; eine spätere Entscheidung für Option 2 oder 3 ersetzt nur die Komponente, die Render-Assertion und den kind-Fall.

**Betroffene IDs:** OPS-04, NFR-05, OPS-03

**Betroffene WPs:** WP-444

## E-75

**Thema:** OPS-05: Alarmierung des Betriebs über NTF-Kanäle

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** Über welchen Endpunkt und welche Organisation erreichen Betriebsalarme aus Alertmanager die NTF-Kanäle des Betreibers, nachdem WP-137 geliefert ist?

**Optionen:**

- Webhook-Brücke im Operator-Pfad (POST /api/v1/admin/alerts/alertmanager, eigenes Token RETICORA_OPS_ALERT_TOKEN); Zustellung über die NTF-Kanäle einer konfigurierten Betreiber-Organisation (RETICORA_OPS_ALERT_ORG_ID).
- Alertmanager stellt direkt über eigene Receiver (E-Mail, Webhook) zu, ohne NTF; OPS-05 wird im Gate als Abweichung geführt.
- Zurückstellung der NTF-Anbindung nach Phase 2; OPS-05 steht im Gate nicht auf PASS.

**Empfehlung:** Option 1.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-448 (`t5-ntf-bridge`) setzt Option 1 um; ohne konfigurierte Betreiber-Organisation antwortet die Brücke 404 und Alertmanager nutzt den E-Mail-Receiver aus WP-446 (`t5-alerting-config`).

**Betroffene IDs:** OPS-05

**Betroffene WPs:** WP-448

## E-76

**Thema:** GATE-02: Abnahme durch Erklärung des Auftraggebers statt Nachweis

**Status:** entschieden: Option 2 (Auftraggeber, 2026-10-26)

**Frage:** Dürfen Anforderungen, deren Nachweis durch die Erklärung des Auftraggebers vom 2026-10-15 ersetzt ist (B3 Pentest, B4 v2-Texte, B6 Subprozessorenliste), in der Gate-Checkliste als PASS stehen, und wie behandelt die zu G1 vollständig blockierende Traceability (E-30) IDs ohne Testnachweis?

**Optionen:**

- Eigener Status außerhalb von PASS ('offen – Erklärung des Auftraggebers, Nachweis ausstehend'); die IDs stehen mit Lieferung und Frist in docs/acceptance/g1-ausnahmen.csv; die Traceability zählt sie nicht als nachgewiesen und blockiert nur nicht gelistete Lücken; das Gate-Ergebnis bleibt 'offen', bis die Lieferungen vorliegen.
- Wie Option 1, aber Gate-Ergebnis 'bedingt bestanden' mit Frist je Lieferung als ausdrücklich freigegebene Abweichung nach GATE-02; die Freigabe des Auftraggebers wird in docs/acceptance/g1-gate.md dokumentiert.
- Beibehaltung: Die Erklärung gilt als PASS (widerspricht GATE-02 'Nachweis per Test/Traceability', WP-213 AK3 und GLO-11).

**Empfehlung:** Option 1; Option 2 nur, wenn der Auftraggeber G1 vor Eingang der Lieferungen ausdrücklich freigeben will.

**Übergangsregel:** Bis zur Entscheidung führt WP-450 (`t5-trace-evidence`) alle betroffenen IDs (AUT-05, AUT-06, NET-04, NFR-08, NFR-09, ABN-01, GATE-03 Pentest-Anteil, GATE-02) in docs/acceptance/g1-ausnahmen.csv mit art=erklaerung, und WP-451 (`t5-gate-consistency`) führt sie in docs/acceptance/g1-gate.md als 'offen' mit Begründung; das ist mit Option 1 und 2 vereinbar. Keine ID steht ohne Nachweis auf PASS, das Gate-Ergebnis lautet 'offen'. Jedes Lieferungs-WP entfernt seine Zeilen; die GATE-02-Zeile entfernt WP-595 (`t5-rc-evidence`), wenn keine andere Ausnahme der Art erklaerung oder luecke besteht.

**Umsetzung:** Der Auftraggeber hat am 2026-10-26 erklärt: „GATE-02 darf durch meine hiermit erteilte Erklärung abgenommen werden“, und auf Rückfrage Option 2 gewählt; die Frist der Lieferungen A-02, A-07 und A-11 ist „vor GA“ (keine abweichende Frist genannt). Die Erklärung gilt für alle oben genannten IDs.
- Die IDs bleiben, was Option 1 vorsieht: nicht PASS, Status „offen“ mit der Begründung „Erklärung des Auftraggebers, Nachweis ausstehend“, je eine Zeile in `docs/acceptance/g1-ausnahmen.csv` (Art `erklaerung`) mit Lieferung und Frist. Die Traceability zählt sie nicht als nachgewiesen. Eine Erklärung ersetzt keinen Nachweis (F-120); sie gibt die Abweichung frei.
- Neu ist das Gate-Ergebnis „bedingt bestanden“: Es ist zulässig, wenn außer den Zeilen der Art `erklaerung` mit Frist jede Bedingung von „bestanden“ erfüllt ist (kein offenes Critical, kein offenes High/Critical im Pentest, keine Ausnahme der Art `luecke`, jede GATE-Zeile PASS oder selbst eine solche Ausnahme). Die Freigabe des Auftraggebers steht in `docs/acceptance/g1-gate.md` (Abschnitt „Freigabe nach E-76“). Bis dahin lautet das Ergebnis „offen“, weil andere Bedingungen fehlen (GATE-01, Release-Kandidat).
- WP-595 (`t5-rc-evidence`, K7) erweitert `tests/rep01-structure.test.sh` (Normalmodus und `--final`) um dieses Ergebnis und seine Bedingungen; WP-600 (`k-gate`, K7) darf G1 mit „bedingt bestanden“ entscheiden.
- WP-453, WP-454, WP-455 und WP-457 bleiben offen, sind keine Voraussetzung von G1 (wie bisher) und entfernen mit der Lieferung ihre Zeilen; die Frist „vor GA“ steht in `g1-ausnahmen.csv`.

**Betroffene IDs:** GATE-02, GATE-03, NFR-08, NFR-09, ABN-01, AUT-05, AUT-06, NET-04, GLO-11

**Betroffene WPs:** WP-450, WP-451, WP-453, WP-454, WP-455, WP-457, WP-595, WP-600

## E-77

**Thema:** NFR-06 „Logs 30 Tage“ in den Auslieferungen

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** Mit welchem Mechanismus gewährleisten Compose- und K8s-Auslieferung die zeitbasierte 30-Tage-Grenze für Container-Logs? Docker-Größenrotation (json-file max-size/max-file) begrenzt nur das Volumen, nicht das Alter; ein Log-Store ist bisher nicht Teil der Auslieferung.

**Optionen:**

- 1) Compose-Dienste loggen über journald; der Installer legt auf Hosts mit systemd-journald den Drop-in MaxRetentionSec=30day an, ohne systemd-journald aktiviert er eine Override-Datei mit json-file-Rotation als dokumentierten Fallback; K8s: Cluster-Log-Retention als dokumentierte Betreiberanforderung.
- 2) Mitgelieferter Log-Store (Loki + Promtail) mit retention_period 720h in Compose und als K8s-Overlay.
- 3) Nur Größenrotation (json-file max-size/max-file) für alle Dienste; die 30 Tage werden als Betreiberpflicht dokumentiert.

**Empfehlung:** Option 1: zeitbasiert ohne zusätzlichen Dienst. Der Drop-in wirkt allerdings hostweit auf das gesamte Journal (auch Nicht-Reticora-Dienste); der Installer weist beim Anlegen darauf hin, docs/ops/workers.md beschreibt die Wirkung. Option 3 erfüllt die Zeitgrenze nicht, Option 2 erhöht Betriebsaufwand und Ressourcenbedarf des Mindest-Sizings (INS-07).

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-458 (`t6-log-retention`) startet mit der für alle Optionen nötigen Logging-Begrenzung jedes Compose-Dienstes und der Dokumentation und setzt Option 1 um; wird Option 2 gewählt, folgt der Log-Store als eigenes WP.

**Betroffene IDs:** NFR-06

**Betroffene WPs:** WP-458

## E-78

**Thema:** CI-06: Berechtigung der einheitlichen CI-Historie

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** Die vereinte Historie (GET /history/{entityType}/{id}) verlangt audit:read, das nach RBA-02 (Migration 000073) nur org_admin hat; CI-06 verlangt Historie und Audit als Teil des CI-Lesens. Welche Berechtigung gilt für welchen Anteil der Historie?

**Optionen:**

- 1. Änderungsanteil (ci_change, entity_change, location_change, Overrides) mit der Leseberechtigung des Objekts (ci:read, location:read, asset:read …) unter RLS; Audit-Einträge nur zusätzlich mit audit:read.
- 2. Gesamte Historie weiter nur mit audit:read; die UI blendet den Abschnitt für andere Rollen aus.
- 3. audit:read an engineer und viewer vergeben (Änderung der RBA-02-Matrix).

**Empfehlung:** Option 1: erfüllt CI-06 für alle Leserollen, erweitert keine Rolle (E-13) und lässt AUD-05 (audit:read für das Audit-Log) unberührt.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-475 (`t3-history-access`) setzt Option 1 um; bis zur Bestätigung gilt sie vorläufig wie E-13.

**Betroffene IDs:** CI-06, AUD-05, RBA-02

**Betroffene WPs:** WP-475

## E-79

**Thema:** NFR-04: Verlust von Metrik-Samples und Traps bei Upload-Fehlern

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** NFR-04 verlangt, dass der Collector Ausfälle bis 24 h ohne Verlust puffert. Heute verwirft der Collector Metrik-Samples und Traps bei Upload-Fehlern ungezählt (F-234: Umfang für Samples unklar). Gilt „ohne Verlust“ auch für Metrik-Samples und Traps?

**Optionen:**

- 1. Samples/Traps werden gezählt (dropped_samples) und serverseitig als Metrik sichtbar, nicht gespoolt.
- 2. Samples/Traps werden wie Records gespoolt (24 h).

**Empfehlung:** Option 1: COL-05 bindet den Spool an die Reconciliation nach observed_at (GLO-12), also an Discovery-Ergebnisse; der Verlust von Samples und Traps wird sichtbar statt still.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-483 (`t3-spool-poison`) setzt Option 1 um; Option 2 als eigenes WP.

**Betroffene IDs:** NFR-04, COL-05

**Betroffene WPs:** WP-483

## E-80

**Thema:** CI-12: Unmerge nach späteren Änderungen am Survivor

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** Unmerge stellt Survivor und Victim aus dem Snapshot wieder her und überschreibt dabei Änderungen, die nach dem Merge am Survivor vorgenommen wurden (F-097). Welcher Vertrag gilt?

**Optionen:**

- 1. 409 mit den seit dem Merge geänderten Feldern; force=true stellt den Snapshot bewusst wieder her (auditiert).
- 2. Feldweise Wiederherstellung: seit dem Merge geänderte Survivor-Felder bleiben, der Rest kommt aus dem Snapshot; die Antwort nennt die behaltenen Felder.
- 3. Überschreiben mit Warnung in der Antwort.

**Empfehlung:** Option 1: kein stiller Verlust, eindeutiger Vertrag, geringe Komplexität; entspricht der Empfehlung des Befunds.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-485 (`t3-unmerge-conflict`) setzt Option 1 um.

**Betroffene IDs:** CI-12

**Betroffene WPs:** WP-485

## E-81

**Thema:** DB-05: Verknüpfung eines CI mit einem Asset ohne Standort

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** Der Spiegel-Trigger (000088) übernimmt beim Verknüpfen den Asset-Standort; hat das Asset keinen, verliert das CI still Standort, Site und Raum und damit seine Scope-Sichtbarkeit (F-239). Was gilt?

**Optionen:**

- 1. Das Asset übernimmt in derselben Transaktion den Standort des CI (Audit am Asset), danach spiegelt der Trigger.
- 2. Die Verknüpfung wird mit 422 auf asset_id abgewiesen, solange das Asset keinen Standort hat.
- 3. Verknüpfung erlaubt, das CI wird standortlos (Ist-Zustand), mit Warnung.

**Empfehlung:** Option 1: kein Datenverlust, das Asset bleibt kanonisch (DB-05), keine zusätzliche Bedienhürde.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-486 (`t3-asset-link-location`) setzt Option 1 um; ein Wechsel auf Option 2 betrifft nur `ci/pg_repository.go`, `ci/service.go` und den zugehörigen Test.

**Betroffene IDs:** DB-05, AST-04

**Betroffene WPs:** WP-486

## E-82

**Thema:** AUD-03: Datenmodell der ci_change-Zeilen

**Status:** entschieden: Option 2 (Auftraggeber, 2026-10-28)

**Frage:** AUD-03 beschreibt eine ci_change je Änderung mit change_kind, field_changes [{field, old, new}], Akteur, Collector, Quelle, request_id, correlation_id und observed_at sowie genau eine ci_change je Ingest-Record. Heute ist ci_change feldgranular (eine Zeile je Feld); darauf beruhen die feldgranulare 409-Prüfung (API-07, checkPrecondition), die Historie und der Merge. Wie wird AUD-03 umgesetzt?

**Optionen:**

- 1. Umbau auf eine Zeile je Änderung mit field_changes (jsonb); feldgranulare Konfliktprüfung über jsonb-Abfragen.
- 2. Feldzeilen bleiben Speicherform; die neue Spalte change_set_id gruppiert sie zu einer Änderung, die AUD-03-Spalten werden ergänzt; API und Historie liefern je change_set eine Änderung mit field_changes.
- 3. AUD-03 formal auf das Vokabular (change_kind) begrenzen; Korrelation und Gruppierung entfallen in Phase 1.

**Empfehlung:** Option 2: erfüllt AUD-03 in Lesesicht und Ingest (eine Änderung je Record), ohne API-07 und den Merge umzubauen.

**Entscheidung:** Option 2 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-487 (`t3-aud03-schema`), WP-488 (`t3-aud03-history`) und WP-489 (`t3-aud03-ingest`) setzen Option 2 um; die Migration ist additiv, eine spätere Wahl von Option 1 baut darauf auf.

**Betroffene IDs:** AUD-03, CI-11, API-07

**Betroffene WPs:** WP-487, WP-488, WP-489

## E-83

**Thema:** IMP-02: Traversierung per Ebenen-BFS statt rekursivem CTE

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** IMP-02 nennt einen rekursiven CTE; implementiert ist eine ebenenweise Breitensuche mit einer SQL-Abfrage je Ebene (Performance-Korrektur aus WP-206, docs/acceptance/lasttest-g1.md:526). Das Verhalten (Tiefe 20, Pfade mit Typfolge, Kürzestpfad, Kategorien) ist erfüllt. Ist die Abweichung zulässig?

**Optionen:**

- 1. Ebenen-BFS gilt als IMP-02-konform; IMP-02 wird als Verhaltensanforderung gelesen, die Abweichung wird dokumentiert.
- 2. Rückbau auf rekursiven CTE mit erneutem Lastnachweis (NFR-02: Impact Tiefe 20 < 2 s).

**Empfehlung:** Option 1: NFR-02 ist mit der BFS belegt; ein Rückbau gefährdet den Lastnachweis ohne fachlichen Gewinn.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-490 (`t3-impact-heuristic`) lässt die BFS unverändert und validiert nur den depth-Parameter.

**Betroffene IDs:** IMP-02, NFR-02

**Betroffene WPs:** WP-490

## E-84

**Thema:** DIS-10: Poll-Intervalle je Scope und Zeitzone von Cron-Zeitplänen

**Status:** entschieden: Option A (Auftraggeber, 2026-10-28)

**Frage:** DIS-10 sagt „pro Scope und Org konfigurierbar“, das Scope-Modell nach DIS-09 hat aber nur ein Feld schedule. Sollen Poll-Intervalle (SNMP/Redfish) zusätzlich je Scope einstellbar sein, und in welcher Zeitzone wird ein Cron-Zeitplan ausgewertet (heute UTC)?

**Optionen:**

- A: Sweep-Zeitplan je Scope (schedule), Poll-Intervalle je Org und Protokoll (Status quo); Cron in der Zeitzone der Organisation (organization.timezone).
- B: Zusätzlich Poll-Intervalle je Scope (neue Scope-Felder, Migration, API, UI); Cron in der Zeitzone der Organisation.
- C: Wie A, Cron bleibt UTC und wird in UI und API als UTC gekennzeichnet.

**Empfehlung:** Option A: deckt sich mit dem Datenmodell aus DIS-09 (ein schedule je Scope) und den Mindestwerten aus DIS-10; Cron in Ortszeit vermeidet die Verschiebung der Scan-Fenster um 1–2 h für deutsche Organisationen.

**Entscheidung:** Option A nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** Bis zur Entscheidung gilt Option A; WP-495 (`t4-scope-schedule`) wertet Cron in der Zeitzone der Org aus, WP-496 (`t4-scope-form-ui`) zeigt den Hinweis. Option B erfordert ein Folge-WP mit Migration.

**Betroffene IDs:** DIS-10, DIS-09

**Betroffene WPs:** WP-495, WP-496

## E-85

**Thema:** IMP-09: ausgeschaltete Outlets in der Redundanzkennung

**Status:** entschieden: Option A (Auftraggeber, 2026-10-28)

**Frage:** Zählt ein Outlet mit outlet_state=off als Einspeisung für attributes.redundant der powered_by-Kante? IMP-09 nennt redundante Einspeisungen, schweigt aber zum Outlet-Zustand.

**Optionen:**

- A: Die Kante entsteht (outlet_state=off als Attribut), zählt aber nicht für redundant.
- B: Für ausgeschaltete Outlets entsteht keine powered_by-Kante.
- C: Status quo: jeder Outlet-Eintrag zählt als Einspeisung.

**Empfehlung:** Option A: die Verkabelung bleibt für Impact und Planung sichtbar, die Redundanzaussage entspricht der tatsächlichen Versorgung.

**Entscheidung:** Option A nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** Bis zur Entscheidung gilt Option A; WP-498 (`t4-power-redundancy`) setzt sie um.

**Betroffene IDs:** IMP-09

**Betroffene WPs:** WP-498

## E-86

**Thema:** Bearbeitungs- und Löschrechte org-weit geteilter Sichten (VIE-01)

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** Wer darf eine org-weit geteilte gespeicherte Sicht umbenennen, entsperren oder löschen? Heute jeder Inhaber von `saved_view:write` (in `api/openapi.yaml` dokumentiert), rollengeteilte Sichten nur der Owner.

**Optionen:**

- Nur der Owner; für alle anderen ist jede geteilte Sicht read-only (PATCH/DELETE → 404 wie bei rollengeteilten Sichten).
- Owner und org-weite Principals der Rolle org_admin; für alle anderen read-only.
- Ist-Vertrag beibehalten: jeder Inhaber von `saved_view:write` ändert und löscht org-geteilte Sichten.

**Empfehlung:** Option 1: eine Regel für alle geteilten Sichten, keine stille Änderung fremder Sichten; die Übernahme verwaister Sichten gehört zu VIE-02 [P2].

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-518 (`t7-saved-views-api`) setzt Option 1 um; bei Option 2 oder 3 werden nur das Prädikat in `savedview/pg_repository.go`, die OpenAPI-Beschreibung und `TestAuditT7SavedViewEditDeleteRights` angepasst.

**Umsetzung (2026-10-26):** WP-518 setzt Option 1 um: PATCH und DELETE nur durch den Owner (Prädikat `owner_id` in `savedview/pg_repository.go`, Owner-Prüfung vor der Validierung in `savedview/handler.go`), sonst 404 bei unveränderter Zeile; `SavedViewsPage` bietet „Löschen“ nur für eigene Sichten an. Eingetragen in [`../entscheidungen.md`](../entscheidungen.md) (Nachtrag vom 2026-10-26).

**Betroffene IDs:** VIE-01

**Betroffene WPs:** WP-518, WP-555

## E-87

**Thema:** CI-Detail unter gewähltem Client-/Site-Scope (WP-191)

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** Wie berücksichtigt die CI-Detailseite einen im Umschalter gewählten Client/Standort, wenn das CI außerhalb liegt? Der Umschalter ist ein Bedienfilter, die Berechtigung entscheidet die RLS.

**Optionen:**

- Hinweisbanner „außerhalb des gewählten Bereichs“ mit „Bereich wechseln“/„Alle Kunden“; eingebettete Beziehungsliste zeigt standardmäßig nur CIs im Scope und nennt die ausgeblendeten (umschaltbar).
- Sperre: außerhalb des Scopes zeigt die Detailseite nur den Hinweis mit Bereichswechsel, der Inhalt erst danach.
- Detailseiten ignorieren den Scope; das WP-191-Kriterium wird auf Listen beschränkt (Plananpassung).

**Empfehlung:** Option 1: Deep-Links und Suche bleiben nutzbar, der Widerspruch zum gewählten Bereich ist sichtbar, und es entsteht keine Scheinsicherheit, weil die Rechte serverseitig bleiben.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-524 (`t7-ui-scope-detail`) setzt Option 1 um; entscheidet der Auftraggeber anders, werden nur `ScopeBanner`, `CIRelationshipsCard` und ihre Tests angepasst.

**Betroffene IDs:** UI-01, UI-02, UI-03, UI-04, UI-05, UI-07, UI-08, UI-09, UI-10

**Betroffene WPs:** WP-524

## E-88

**Thema:** Nachweis der Browser-Vorversionen (UI-18, NFR-07)

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** UI-18 verlangt die aktuelle und die Vorversion von Chrome, Edge, Firefox und Safari; Playwright bündelt je Version genau einen Build je Engine. Wie wird die Vorversion nachgewiesen?

**Optionen:**

- Zweiter CI-Job (nächtlich und vor Release) mit gepinnter Vorgängerversion von Playwright (Chromium, Firefox, WebKit n-1) und Chrome n-1 aus Chrome for Testing; Edge und Safari n-1 über ihre Engines angenähert (dokumentierte Einschränkung).
- Externer Browser-Grid-Dienst mit echten Vorversionen inkl. Safari/iOS (Kosten, neuer Subprozessor nach NFR-09).
- Vorversionen nur per manueller Release-Checkliste; automatisiert nur die aktuellen Versionen.

**Empfehlung:** Option 1: ohne externen Dienst, reproduzierbar in CI, deckt alle Engines ab; Edge/Safari n-1 bleiben bis GA eine bekannte Einschränkung.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-531 (`t7-browser-matrix`) setzt Option 1 um; bei Option 2 wird nur der Vorversions-Job ersetzt, bei Option 3 durch eine Checkliste in `docs/README.md`.

**Betroffene IDs:** UI-18, NFR-07

**Betroffene WPs:** WP-531

## E-89

**Thema:** Review-Merge bei ambiguous_identity: synchroner Effekt mit gespiegeltem Job (JOB-04, Folge zu E-25)

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** Genügt für die Review-Auflösung „merge“ bei ambiguous_identity ein im Request ausgeführter Effekt mit gespiegeltem Jobdatensatz (`review.merge`), oder muss der Merge eingereiht werden (202 mit Job-Location)? Merge bei probable_duplicate läuft nach WP-479 (`t3-review-merge`) über den Job `ci.merge`.

**Optionen:**

- Synchron mit gespiegeltem Job: Status, Versuche und Fehler im Jobcenter; Antwort 200 nach Abschluss.
- Asynchron: `POST /review-items/{id}/resolve` mit merge antwortet 202 mit `Location: /api/v1/jobs/{id}`, ein Worker löst das Item auf, die UI pollt.

**Empfehlung:** Option 1: ein Review-Merge betrifft ein Item und zwei CIs und bleibt im Request-Budget (NFR-02); JOB-04 fordert Jobs für große Operationen; der gespiegelte Job macht den Vorgang nachvollziehbar.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** Option 1 gilt für merge bei ambiguous_identity (review.merge); merge bei probable_duplicate läuft über den ci.merge-Job aus WP-479 (`t3-review-merge`), der review.merge-Spiegel übernimmt dessen Status. WP-554 (`t7-review-merge-job`) dokumentiert das als Nachtrag zu E-25 und korrigiert den Retry-Status. Bei Option 2 folgt ein eigenes WP (Queue-Art review.merge, 202-Vertrag, UI-Polling).

**Betroffene IDs:** JOB-04, UI-16

**Betroffene WPs:** WP-554

## E-90

**Thema:** REC-07: Entfernen manuell gepflegter IPs und Interfaces

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** REC-07 entfernt Interfaces und IPs nach drei Fehlläufen; heute zählt das auch manuell gepflegte Adressen (type reserved/static) und manuell angelegte Interfaces an entdeckten CIs, die damit gelöscht werden (F-249). REC-03 und OVR-02 schützen manuelle Daten. Was gilt?

**Optionen:**

- 1. Nur von Discovery stammende Objekte zählen Fehlläufe (ip_address.type = discovered; Interfaces mit Herkunft discovery); manuelle Objekte bleiben.
- 2. Wortlaut REC-07: alle Interfaces und IPs eines entdeckten CI zählen.

**Empfehlung:** Option 1: vereinbar mit REC-03 und OVR-02 (Automation löscht keine manuellen Daten).

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-580 (`t3-discovery-p3`) setzt Option 1 um; Interfaces erhalten dafür eine Herkunftsspalte origin: Bestand origin = 'manual', der Discovery-Abgleich setzt 'discovered' für jedes gemeldete Interface; nur origin = 'discovered' zählt Fehlläufe.

**Betroffene IDs:** REC-07, REC-03, OVR-02

**Betroffene WPs:** WP-580

## E-91

**Thema:** NTF-03: Ausprägung von Abonnements und Org-Branding in Phase 1

**Status:** entschieden: Option 2 (Auftraggeber, 2026-10-28)

**Frage:** NTF-03 und das WP-138-Kriterium nennen Abonnements, Vorlagen je Ereignistyp, i18n und Org-Branding ohne nähere Festlegung. Welche Ausprägung gilt für Phase 1?

**Optionen:**

- 1) Persistierte Abonnements je Nutzer und Ereignistyp (Tabelle, REST /notifications/subscriptions); Branding mit Logo und Farben in HTML-Mails.
- 2) Abonnement = Kategorie-Präferenz des Nutzers (Erweiterung der NTF-04-Präferenzen); Branding = Organisationsname bzw. Absendername in Betreff, Absender und Fußzeile; Vorlagen je Ereignistyp in der Empfängersprache.
- 3) Abonnements und Branding formal auf Phase 2 verschieben (Planänderung; NTF-03 bleibt teilweise).

**Empfehlung:** Option 2: erfüllt NTF-03 mit dem vorhandenen Präferenzmodell ohne neue Tabelle; i18n wird in jedem Fall umgesetzt. Client-Kontakte werden ohne konfiguriertes Abonnement nicht adressiert.

**Entscheidung:** Option 2 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-588 (`t6-ntf-templates`) setzt Empfänger-i18n und Option 2 sofort um; bei Option 1 folgt ein WP mit Migration und REST-Ressource (einschließlich Abonnements für Client-Kontakte).

**Betroffene IDs:** NTF-03

**Betroffene WPs:** WP-588

## E-92

**Thema:** Instanzattributdefinitionen und DB-03 („attribute_definition einzige Quelle“)

**Status:** entschieden: Option 2 (Auftraggeber, 2026-10-28)

**Frage:** Müssen die Definitionen der Instanzattribute (ci_instance_field_definition) in attribute_definition übernommen werden (WP-116-Kriterium „Übernahme aus … Instanzdefinitionen“, DB-03), obwohl Instanzattribute MET-14 [P2] sind und der Phase-2-Plan (WP-256) die Tabelle ausdrücklich beibehält?

**Optionen:**

- 1) Übernahme in attribute_definition (Spalte ci_id, Sichtbarkeit über den Scope des CI), alte Tabelle als Sicht für die Kompatibilität.
- 2) ci_instance_field_definition bleibt Quelle der Instanzschicht (letzte Ebene nach MET-16, MET-14 [P2]); DB-03 gilt für System-, Org- und Typdefinitionen; das WP-116-Kriterium wird entsprechend präzisiert.
- 3) Instanzschicht im G1-Release nach E-03 abschalten, bis WP-256 sie neu aufbaut.

**Empfehlung:** Option 2: deckt sich mit dem Phase-2-Plan (WP-256) und MET-16; Option 1 erzwingt eine RLS-sensible Datenmigration für eine Phase-2-Funktion.

**Entscheidung:** Option 2 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-589 (`t6-attrdef-ast`) liefert Resolver- und Schematest nach Option 2 und den AST-Teil unabhängig davon; bei Option 1 entsteht ein Folge-WP zusammen mit WP-256.

**Betroffene IDs:** DB-03, MET-10, MET-14, MET-16

**Betroffene WPs:** WP-589

## E-93

**Thema:** Materialisierung org-globaler Attributdefinitionen (MET-16)

**Status:** entschieden: Option 2 (Auftraggeber, 2026-10-28)

**Frage:** Werden org-globale Definitionen (organization_id gesetzt, ci_type_id NULL) in das materialisierte und versionierte Schema aufgenommen, obwohl System-Typen org-übergreifend materialisiert sind und MET-16 für Phase 1 „Basis + Typkette“ nennt?

**Optionen:**

- 1) Org-Schemarevision: Materialisierung je (Org, Typ) einschließlich org-globaler Schicht mit eigener Version, die das CI referenziert.
- 2) Org-globale Definitionen bleiben eine validierte, nicht materialisierte Laufzeitschicht; schema_version referenziert Basis + Typkette; als Entscheidung dokumentiert.
- 3) Org-globale Definitionen in Phase 1 nicht zulassen.

**Empfehlung:** Option 2: entspricht dem Phase-1-Umfang von MET-16 und dem heutigen Verhalten; Option 1 verdoppelt die Schemaversionen je Org ohne Phase-1-Anforderung.

**Entscheidung:** Option 2 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-590 (`t6-schema-version`) setzt den schema_version-Fix und den Laufzeittest nach Option 2 um; Option 1 folgt als eigenes WP mit Migration.

**Betroffene IDs:** MET-16, MET-10

**Betroffene WPs:** WP-590

## E-94

**Thema:** Korrektur der Migration 000110 (Duplikat-Suffixe der Interfaces)

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** Darf die bestehende Migration 000110 angepasst werden, obwohl die Konvention Änderungen bestehender Migrationen ausschließt (Präzedenz E-26)? Es gibt noch kein ausgeliefertes Release (keine Release-Tags); betroffen sind nur Datenbanken mit Stand vor 000110 und bereits vorhandenen Suffixnamen.

**Optionen:**

- 1) 000110 anpassen: kollisionsfreie Suffixe; Hinweis, dass bereits migrierte Datenbanken nicht betroffen sind.
- 2) 000110 unverändert; eine Upgrade-Vorprüfung erkennt kollidierende Namen vor der Migration und bricht mit Anleitung ab.
- 3) Nur Dokumentation der manuellen Bereinigung.

**Empfehlung:** Option 1 analog E-26: ohne ausgeliefertes Release wirkt die Änderung auf alle künftigen Upgrades und kostet keine Daten.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-592 (`t6-p3-sammel`) beginnt mit dem Migrationsdatentest (Probe net01-dup.sql) und setzt Option 1 um; bei Option 2 ersetzt die Vorprüfung die Änderung, der Test bleibt.

**Betroffene IDs:** NET-01

**Betroffene WPs:** WP-592

## E-95

**Thema:** Durchsetzung von allowed_source_types, allowed_target_types und cardinality (REL-01)

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** REL-01 nennt die Felder ohne Semantik; sollen sie bei der Typanlage und bei POST /relationships durchgesetzt werden oder nur informativ sein?

**Optionen:**

- 1) Durchsetzen: unbekannte Typ-Keys bei Typanlage 400, unzulässige Quell-/Zieltypen bei Kantenanlage 422; cardinality informativ bis REL-06 [P2].
- 2) Alle drei Felder informativ; die OpenAPI kennzeichnet sie, keine Prüfung.
- 3) Durchsetzen einschließlich cardinality (one/many je Quelle bzw. Ziel).

**Empfehlung:** Option 1: Admins verlassen sich auf die gespeicherten Einschränkungen; cardinality braucht eine Semantik, die erst mit REL-06 kommt.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-592 (`t6-p3-sammel`) setzt Option 1 um; bei Option 2 beschränkt sich der Teil auf die OpenAPI-Beschreibung und einen Test, dass keine Prüfung erfolgt.

**Betroffene IDs:** REL-01

**Betroffene WPs:** WP-592

## E-96

**Thema:** TimescaleDB-Version des Datenbank-Images und Funde seines Basisimages

**Status:** entschieden: Option 2 (Auftraggeber, 2026-10-21)

**Frage:** WP-389 pinnt `TIMESCALE_IMAGE` laut F-291-Kriterium auf `2.17.2-pg16` „wie in CI“. Der erste Scan von `reticora-postgres` mit der Schwelle des Jobs `image-scan` (Trivy 0.75.0, Critical/High mit Fix) findet im Basisimage 2.17.2-pg16 443 Funde; der Job wäre rot. Ein Pin unter dem bisher gleitenden `latest-pg16` riskiert zudem ein Downgrade für Datenbanken, deren Erweiterung ein neueres Image angelegt hat. 2.31.0-pg16 (am 2026-10-21 die neueste 2.x-pg16, Ziel von `latest-pg16`) hat 80 Funde: 79 in den Go-Programmen des Basisimages (`gosu`, `timescaledb-tune`, `timescaledb-parallel-copy`: Go-Standardbibliothek, golang.org/x/crypto, x/text, pgx) und 1 im Alpine-Paket libexpat. Welche Version gilt, und wie werden die Funde behoben?

**Optionen:**

- 1) 2.17.2 wie im Plan, die Funde als Scan-Ausnahmen; das bricht die SEC-11-SLA (Critical 7, High 30 Tage), weil sich die Funde nicht innerhalb der Frist beheben lassen.
- 2) 2.31.0 per Version und Digest überall (DB-Image, CI- und Deploy-Images); die Go-Hilfsprogramme des Basisimages werden entfernt oder mit der Go-Toolchain des Repositorys neu gebaut, der verbleibende Paketfund per Upgrade behoben.
- 3) Der gleitende Tag `latest-pg16` bleibt.

**Empfehlung:** Option 2: der Scan wird ohne Ausnahme grün, bestehende Datenbanken erleben kein Downgrade, und CI testet die Version, die ausgeliefert wird. Option 3 lässt F-291 offen (nicht reproduzierbar, ungeprüfte Updates).

**Umsetzung:** WP-389 (`t5-image-scan`): `TIMESCALE_IMAGE` = `timescale/timescaledb:2.31.0-pg16@sha256:322ae364eca3321cf2a48f1ef31213b87843413b3cf8feeebf54e999a26bb731` (Digest des Multi-Arch-Index gegen Docker Hub geprüft); dieselbe Referenz in `ci.yml` (migrations, auditdefect), `load.yml`, `restore-test.yml`, im Backup-CronJob von `deploy/k8s`, in `docker-compose.yml`, `docs/README.md` und ADR 0001; `tests/ci-config.test.sh` (`timescale_image_refs`) prüft jede Referenz. Die Stufe `go-tools` des Dockerfiles baut `timescaledb-tune` (v0.19.0) und `gosu` (Commit des Tags 1.19), die Versionen des Basisimages, mit `golang:1.26.10-alpine` (per Digest) neu; `timescaledb-parallel-copy` wird entfernt (weder Entrypoint, Init-Skripte noch Reticora nutzen es); `apk upgrade libexpat`. Der Spike-Test von ADR 0001 akzeptiert die in TimescaleDB 2.18 umbenannten Fehlertexte (columnstore statt compression). Umstieg bestehender Datenbanken (`ALTER EXTENSION timescaledb UPDATE` bei deaktiviertem Event-Trigger `reticora_restrict_app_ddl`) in `docs/security/abhaengigkeitsscans.md`.

**Betroffene IDs:** SEC-11, TST-04, MON-01

**Betroffene WPs:** WP-389

## E-97

**Thema:** Obergrenze der Dauer von Alarmregeln (MON-01)

**Status:** entschieden: 9600 h (Auftraggeber, 2026-10-25)

**Frage:** WP-406 (`t1-err-ident-loc`) prüft die Dauer einer Alarmregel vor der Umwandlung in eine Ganzzahl; der Plan nennt keinen Höchstwert. Welche Obergrenze gilt?

**Optionen:**

1. 9600 h (400 Tage), die Aufbewahrungsdauer der Metriken nach MON-01: eine längere Dauer kann nie erfüllt werden.
2. Keine fachliche Obergrenze, nur die technische Grenze des Datentyps.

**Empfehlung:** Option 1.

**Umsetzung:** WP-406 (`t1-err-ident-loc`): Dauer außerhalb 0 bis 9600 h ergibt 422 mit Feldfehler; numerische Werte werden vor der Umwandlung geprüft. Vom Auftraggeber am 2026-10-25 bestätigt.

**Betroffene IDs:** MON-01

**Betroffene WPs:** WP-406

## E-98

**Thema:** Webhook-Abos und der Scope ihres Erzeugers (TEN-09)

**Status:** entschieden: Option B (Auftraggeber, 2026-10-25)

**Frage:** Webhook-Abos gehören der ganzen Organisation. Jeder client- oder site-beschränkte Principal mit `webhook:manage` legt Abos an und löscht sie; ein Abo ohne Service-Account erhält die Events aller Clients (Fund der Kanalmatrix in WP-409 (`t1-channel-matrix`)). Wie wird das begrenzt?

**Optionen:**

- A) Webhook-Verwaltung nur für Principals ohne Client- und Site-Einschränkung; beschränkte Principals erhalten 403.
- B) Abos tragen den Scope ihres Erzeugers: Verwaltung nur im eigenen Scope, Zustellung nur von Events, deren Objekte im Scope liegen; bestehende Abos bleiben org-weit.

**Empfehlung:** Option B: beschränkte Teams können Webhooks für ihre Clients weiter nutzen, und ein gebundener Service-Account erweitert den Scope des Erzeugers nicht.

**Umsetzung:** WP-601 (`k1n-webhook-scope`).

**Betroffene IDs:** TEN-09, TEN-06, WHK-01

**Betroffene WPs:** WP-601

## E-99

**Thema:** Auswahl der Oberflächensprache (GLO-10, UI-18)

**Status:** entschieden (Auftraggeber 2026-10-27): Option 1

**Frage:** WP-523 (`t7-i18n-labels`) soll einen Sprachwechsel gegen reale API-Daten belegen. Die Oberfläche startete fest in de-DE (`frontend/src/i18n/index.ts`, `lng: 'de-DE'`) und bot keinen Weg, die Sprache zu wechseln; der Plan nannte für WP-523 keinen Entscheidungsbedarf. Wie wählt der Nutzer die Oberflächensprache?

**Optionen:**

1. Sprachumschalter im Benutzermenü (Kopfzeile); die Wahl wird je Browser gespeichert (localStorage). Ohne gespeicherte Wahl entscheidet die Browsersprache (de* → de-DE, en* → en-US), jede andere ergibt de-DE.
2. Nur die Browsersprache, ohne Umschalter.
3. Nur ein Umschalter, ohne Auswertung der Browsersprache (Start immer in de-DE).
4. Sprache des Nutzerkontos mit Rückfall auf die Org-Locale (`app_user.locale`, Org-Locale nach LOC-01, wie für Benachrichtigungen in WP-588 (`t6-ntf-templates`)), serverseitig gespeichert.

**Entscheidung:** Option 1.

**Umsetzung:** WP-523 (`t7-i18n-labels`): `frontend/src/i18n/index.ts` ermittelt die Sprache beim Start (gespeicherte Wahl unter `reticora-language`, sonst die primäre Browsersprache `navigator.language`, sonst de-DE) und speichert die Wahl des Umschalters; `document.documentElement.lang` folgt der Sprache. `frontend/src/components/LanguageSwitch.tsx` ist ein benanntes natives Auswahlfeld („Sprache“/„Language“, Optionen „Deutsch“/„English“) in der Kopfzeile neben dem Benutzernamen, per Tastatur bedienbar (Pfeiltasten wie die übrigen Auswahlfelder nach WP-519), auch auf der Anmeldeseite. Ohne verfügbaren Speicher gilt die Wahl nur für die geöffnete Seite. Keine neue npm-Abhängigkeit, keine Server- oder Schemaänderung. Tests: Vitest `src/i18n/language.test.ts`, `src/components/LanguageSwitch.test.tsx`; Real-Stack `frontend/e2e-real/i18n.spec.ts` I2 (Umschalter, Speicherung über Reload) und I3 (englischer Browser ohne gespeicherte Wahl). Eine kontobezogene Sprache (Option 4) bleibt möglich; sie ersetzte dann die Browserspeicherung als erste Quelle.

**Betroffene IDs:** GLO-10, UI-18

**Betroffene WPs:** WP-523

## E-100

**Thema:** Upgrade von Datenbanken mit 000056 vor E-26 (fehlende `migration_quarantine`, Migration 000089)

**Status:** entschieden: Option 1 (Auftraggeber, 2026-10-28)

**Frage:** E-26 hat die Migration 000056 am 2026-10-01 so geändert, dass sie die Tabelle `migration_quarantine` anlegt. Datenbanken, die 000056 zwischen 2026-09-02 und 2026-10-01 in der früheren Fassung angewendet haben, besitzen die Tabelle nicht. Ihr Upgrade bricht bei 000089 ab, der ersten Migration, die in die Tabelle schreibt (000090, 000091 und 000166 schreiben ebenfalls hinein), mit `relation "migration_quarantine" does not exist`; `schema_migrations` steht danach auf 89 dirty (F-378). Darf die bestehende Migration 000089 angepasst werden, obwohl die Konvention Änderungen bestehender Migrationen ausschließt (Präzedenz E-26, E-94)? Es gibt noch kein ausgeliefertes Release (keine Release-Tags).

**Optionen:**

- 1) 000089 legt `migration_quarantine` an, wenn sie fehlt, mit genau der DDL von 000056 (Spalten, Index, RLS ENABLE und FORCE, Policy, Rechte); idempotente Prüfung über `to_regclass('public.migration_quarantine')`, auf jeder anderen Datenbank ohne Wirkung.
- 2) 000089 unverändert; eine Vorprüfung von Installer und Kubernetes-Job vor `migrate up` legt die Tabelle an.
- 3) Nur Dokumentation eines manuellen Schritts (Tabelle vor dem Upgrade von Hand anlegen).

**Empfehlung:** Option 1 analog E-26 und E-94: ohne ausgeliefertes Release wirkt die Änderung auf alle künftigen Upgrades, gilt für jeden Weg zu `migrate up` (Installer, Kubernetes, golang-migrate von Hand) und lässt das Schema frischer Installationen unverändert. Option 2 deckt manuelle Upgrades nicht ab, Option 3 hängt an der Sorgfalt des Betreibers.

**Entscheidung:** Option 1 nach der Empfehlung (Auftraggeber, 2026-10-28: „Setze alle Entscheidungen wie empfohlen um“). Die bisherige Übergangsregel ist damit die Festlegung; ein Folge-WP für eine andere Option entfällt.

**Umsetzung (bisher Übergangsregel):** WP-649 (`k6n-quarantine-upgrade`) setzt Option 1 um, mit Release-Hinweis in `docs/betrieb/upgrade-hinweise.md`. Bei Option 2 oder 3 ersetzt die Vorprüfung bzw. die Anleitung die Änderung an 000089; der Test `TestUpgradeFromPreQuarantine56` bleibt.

**Betroffene IDs:** DB-02

**Betroffene WPs:** WP-649


## E-101

**Thema:** WP-332 AK 3: Rahmen und Ledger eines Ingest-Batches in einer oder in zwei Transaktionen (NFR-03)

**Status:** offen; Übergangsregel: Empfehlung (Option 1)

**Frage:** WP-332 (`nfr03-tx`) verlangt in AK 3: „Rahmen und Ledger eines Batches laufen in einer Transaktion“. Gebaut und gemessen sind zwei Transaktionen je Batch: Der Rahmen (Ledger lesen, Typ-Lookup, VRF) läuft kurz vor den Records, der Ledger (ein `unnest`-INSERT) nach den Records; zusammen 8 Statements in 2 Transaktionen, unabhängig von der Batchgröße (`TestIngestStatementBudget`, `docs/acceptance/lasttest-g1.md`). Die Wiederholungsprüfung (K7) hat die Abweichung als F-386 (P3) erfasst. Der Ledger darf nur Records nennen, deren eigene Transaktion committet hat, und der Rahmen muss ihn vor den Records lesen; eine gemeinsame Transaktion bliebe deshalb über den ganzen Batch auf einer zweiten Pool-Verbindung offen. Ein Prototyp (WP-657) hängt damit bei einem Pool mit einer Verbindung bis zum Timeout (500 nach 10 s statt 202 nach 107 ms), zwei Organisationen auf einem Pool mit zwei Verbindungen in 5 von 5 Runden; die bestehenden Suiten fangen das nicht. Funktional wirkt die Abweichung nicht: Die Budgets halten, und eine Neuzustellung nach verlorenem Ledger-Schreibvorgang wendet idempotent an (gemessen: 0 neue CIs, 0 neue `ci_change`). Soll der Plantext an den gebauten Entwurf angeglichen werden?

**Optionen:**

- 1) Plantext von WP-332 AK 3 an den Entwurf angleichen: „Der Rahmen eines Batches läuft in einer kurzen Transaktion vor den Records, der Ledger nach den Records in einer zweiten mit einem `unnest`-INSERT; zusammen höchstens 8 Statements in 2 Transaktionen je Batch, unabhängig von der Batchgröße.“ Keine Codeänderung.
- 2) Zusammenlegung in eine Transaktion mit eigener Verbindungsbegrenzung, sodass ein Batch nie zwei Pool-Verbindungen belegt; Aufwand rund 2 PT, Gewinn 2 Statements je Batch.
- 3) Savepoints statt eigener Record-Transaktionen; widerspricht WP-332 AK 2 und AK 4 sowie der deadlockfreien Sperrreihenfolge von WP-334.

**Empfehlung:** Option 1. Die Trennung folgt aus der Ledger-Semantik (REC-01) und hält den Verbindungsbedarf des Ingest bei einer Verbindung je Batch (WP-334, `processingSlots`); NFR-03 ist mit zwei Transaktionen erfüllt. Option 2 kostet rund 2 PT für 2 Statements je Batch, Option 3 bricht die Sperrreihenfolge.

**Übergangsregel:** bis zur Entscheidung gilt Option 1: Code und `TestIngestStatementBudget` bleiben unverändert; WP-332 bleibt in der Wiederholungsprüfung wegen F-386 TEILWEISE BESTANDEN, bis die Entscheidung den Plantext angleicht (WP-657).

**Betroffene IDs:** NFR-03

**Betroffene WPs:** WP-332, WP-657
