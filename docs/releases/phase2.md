# Release-Notes Phase 2 (G2)

WP-331 (`g2-gate`). Stand 2026-11-09, Produktstand `69e6668d` (`main` nach
PR #152). Die Gate-Entscheidung G2 trifft der Auftraggeber; Stand und
offene Punkte führt `docs/acceptance/g2-gate.md`. Die GitHub-Release-Notes
erzeugt `.github/workflows/release.yml` weiter mit `--generate-notes`;
dieses Dokument ist die fachliche Zusammenfassung.

## Gate-Entscheidung G2

Entscheidung des Auftraggebers vom 2026-11-09: „Gate G2 ist freigegeben,
trag die Entscheidung ein“. Ergebnis in `docs/acceptance/g2-gate.md`:
**bedingt bestanden**, Release-Kandidat `e17954ca` (`main` nach PR #153).
Auf dem Release-Kandidaten grün: CI (Lauf 42695897709, alle 30 Jobs,
TST-04), Ausfallsuite (`outage.yml`, Lauf 42695925438, OPS-06) und
Install-Smoke (Lauf 42695897334). Nicht PASS und vom Auftraggeber als
Ausnahmen mit Frist vor GA angenommen sind NFR-02 (vorläufige Lastschwellen
G2), RBA-07 (SR-58, SR-59) und SEC-11 (SR-60, Review-Stand) und daraus
GATE-02 (`docs/acceptance/g2-ausnahmen.csv`); die Abweichungen D-1 bis D-9
der Checkliste gelten als akzeptierte Restrisiken mit Frist bzw.
Folge-WP-Vorschlag (siehe „Bekannte Einschränkungen“). Gate G1
(`docs/acceptance/g1-gate.md`) bleibt davon unberührt auf „offen“.

## Überblick

Phase 2 schaltet die Module der Phase P2 im Release-Standard frei:
Metamodell, Feldrechte, Dokumente, Lieferanten und Verträge, Assets mit
Zuweisung, Unterschrift, Reservierung und Entsorgung, Lager mit
Mengenartikeln, Bestellungen, Inventur und Barcode, Ticketing Essential und
Standard, eigene Beziehungstypen mit Kantenhistorie, Import mit
Mapping-Profilen, Bulk mit Undo, geteilte Views, DATEV-Export und die
zugehörigen Oberflächen. Grundlage ist der Plan
`docs/plan/phase2/implementierungsplan.md` mit 242 WPs (241 im G2-Umfang,
WP-293 optional), gemergt mit PR #112 bis #152.

## Neue Module und Funktionen je Meilenstein

| Meilenstein | Inhalt | Anforderungen |
|---|---|---|
| M2.0 Werkzeugkette, Verträge und Freigaben | Traceability für drei Pläne mit Stufe `P2` (WP-243, WP-704), Entitlement-Schlüssel der Phase 2 (WP-244), Rollenmatrix der P2-Rechte (WP-245), Ereigniskatalog, Pfadgerüst (WP-247), Org-Gates je Route und Modulphasen mit `x-feature` (WP-705, WP-706) | ENT-06, RBA-05, API-06 |
| M2.0b G1-Folgearbeiten | Outbox-Nummerierung, Zulassungssperre, Volltext und Identitätsklasse, Fremdschlüssel mit Organisation (000194), gedeckelte Zählung (E-104), Mehrfach-Impact, Override-Autor, G1-Nachweise nach E-103, Konsolidierung von Audit-Teil 03 | NFR-02, SR-03, SR-04, SR-08, SR-09 |
| M2.1 Metamodell | Typvererbung, Attribut-Sets, Templates, Regelkatalog, berechnete Attribute mit Refresh, Layouts und Formularauflösung, Lifecycle-Modelle mit Übergangsregeln, Entwürfe mit Veröffentlichung und Migration, Export/Import des Metamodells, Instanzattribute | MET-01 bis MET-16, MET-45, MET-54, LCY-02, LCY-03 |
| M2.2 Feldrechte | Feldrechte je Rolle und Objektart in REST, GraphQL, Suche, Export, Webhooks und Historie; Beziehungsattribute mit Rechten | RBA-06, RBA-07, MET-10, SRC-01, GQL-04 |
| M2.3 Dokumente | Dokumente mit polymorphen Links, Versionen, Soft-Delete, Speicherlimit, Malware-Scan mit clamd (fail-closed), signierte Downloads als Anhang | MGT-01, SEC-09 |
| M2.4 Verträge, Assets, Zuweisung, Lifecycle | Lieferanten und Verträge mit Ablaufwarnung, Asset-Kern mit Versionen und Tags, Asset↔CI-Verknüpfung, Komposition über Eltern-CI, kanonische Bewegungshistorie, Zuweisung mit Unterschrift, Reservierung, Entsorgung nach DIN 66399, CI-Owner und Tags | AST-01 bis AST-07, MGT-02, MGT-12, DB-05, CI-01, CI-07, CI-08 |
| M2.5 Lager, Bestellungen, Inventur, Barcode | Lagerhierarchie Warehouse → Zone → Shelf → Bin, Mengenartikel mit Bestand je Bin und Mindestbestand, interne Bestellungen mit Vier-Augen-Genehmigung, Inventur mit Abschluss, Barcode-Registry und Etiketten | LOC-08, STK-01 bis STK-05, INV-01 bis INV-03 |
| M2.6 Ticketing | Ticket-Kern mit Zustandsautomat, Nummer je Organisation, Objektbezug und Anhängen, Tickets aus Reviews, SLA mit Business-Kalender je Org und Client; optional Provider-Schnittstelle (WP-293, [O]) | TKT-01, TKT-02, TKT-03 |
| M2.7 Beziehungen, Graph und Netz | eigene Beziehungstypen und -attribute, Kantenhistorie, Service-Impact mit Blast Radius und projizierten Netzknoten, IP-Reservierung und Duplikaterkennung | REL-06, REL-07, REL-09, IMP-08, IMP-10, IMP-11, NET-06, NET-09 |
| M2.8 Import, Bulk, Views, Suche, Export, BFF, Historie | Import mit Mapping-Profilen und Report, Bulk-Operationen mit Undo, geteilte Views und Systemsichten, Suchfelder und Filter-AST, DATEV-Export, GraphQL-Aggregate, Snapshot und Zustand zum Zeitpunkt T | IMP-IO-01, BLK-02, OVR-03, VIE-02, SRC-02 bis SRC-04, EXP-01, GQL-03, AUD-08 |
| M2.9 Oberflächen Phase 2 | Admin-Oberflächen des Metamodells, Klick-Builder, Form Builder mit Vorschau, geführte Anlage, Picker, Oberflächen für Assets, Verträge, Lager, Bestellungen, Inventur, Dokumente, Tickets, Import, Bulk, Views und Beziehungsgraph; Playwright- und axe-Läufe | UI-11, UI-12, UI-14, UI-15, MET-50 bis MET-53, TST-03 |
| M2.10 Seed, Qualität, Abnahme und Gate G2 | Seed Phase 2 (auch Skalierung), TST-01-Pflichtfälle, Lastnachweis G2, Konkurrenzmatrix, Isolationstest und Kanalmatrix über die P2-Kanäle, Sicherheitsreview Phase 2, Abnahmeszenario (c), Release-Standard G2; Abnahmebefunde WP-852 bis WP-859 (Dokument-Scope ohne Links, clamd im Standard, Standardkunde, Kundenverwaltung, Zugangsdaten-UI, Auffindbarkeit, CI-Typen je Kunde, Beziehungssuche per IP) | SIM-01, TST-01, ABN-01 bis ABN-03, NFR-02, TEN-09, SEC-11 |
| M2.11 Lint-Altlast | golangci-lint ohne `new-from-rev`, 0 Befunde | – |

## Entitlement-Schlüssel (E-44) und Datenmigration (WP-244)

Neue Feature-Schlüssel nach ENT-06 in der Fassung von E-44:

| Plan | enthaltene P2-Schlüssel |
|---|---|
| Essential | `assets_basic`, `documents`, `import`, `ticketing_essential` |
| Standard | zusätzlich `ticketing_standard`, `signature`, `stock`, `stocktake_standard`, `barcode`, `gps_standard`, `workplace`, `export_datev` |
| Pro | zusätzlich `internal_orders` |

`gps_standard` und `workplace` haben in Phase 2 kein Modul (LOC-09, MGT-06
sind P4). Speicherlimit: `max_storage_mb` mit Staffel ohne Plandefault
(E-34, E-44). `valid_until` stoppt nur `discovery`.

Datenmigration 000190 (WP-244): Die bisherigen Modulschlüssel weichen den
Schlüsseln von ENT-06: `inventory` → `assets_basic`, `ticketing` →
`ticketing_essential`, `stocktake` → `stocktake_standard`, mit Plan,
`enabled`, `limits`, `valid_until` und Quelle der bisherigen Zeile.
Organisationen ab Plan Standard erhalten zusätzlich `stock` und
`ticketing_standard` als Kopie; jede Organisation mit Entitlement-Zeilen
erhält die fehlenden P2-Schlüssel ihres Plans. Die bisherigen Zeilen stehen
in `migration_quarantine`; die Down-Migration stellt sie wieder her.

## Release-Standard `RETICORA_MODULES` (WP-830)

- Leer (Standard) ist jetzt das Release G2, gleichbedeutend mit `P2`: Jedes
  Modul der Phase P2 (`inventory`, `documents`, `metamodel`, `import`,
  `stocktake`, `ticketing`, `disposal`) ist an; P3, P4 und die Add-ons [A]
  bleiben abgeschaltet (E-03). Nachweis `TestG2DefaultEnablesP2ModulesOnly`.
- Je Organisation sind die P2-Funktionen nur über ihre Entitlements
  erreichbar (Org-Gate je Route, WP-705, WP-706).
- Das Modul `documents` braucht clamd (`RETICORA_CLAMD_ADDR`) und NATS;
  Compose startet clamd mit dem Profil `documents`, Kubernetes bindet die
  Komponente `clamav` seit WP-853 in `deploy/k8s/base` ein. Ohne clamd
  bleiben neue Dokumentversionen `pending_scan` (fail-closed).
- Der Release G1 (alle Spätmodule aus) ist nur noch ein Testprofil.

## Migrationen

Neu in Phase 2 sind die Migrationen 000190 bis 000283 (000139 bis 000189
stammen aus dem Korrekturplan und gehören zum G1-Stand). Jede hat eine
Down-Migration; Datenmigrationen sichern ersetzte Zeilen in
`migration_quarantine`.

| Bereich | Migrationen |
|---|---|
| Entitlements, Rechte, Outbox, Verweise | 000190 `entitlement_p2_keys`, 000192 `permissions_p2`, 000193 `event_outbox_counter`, 000194 `object_reference_organization`, 000275 `p2_reference_integrity` |
| Suche und Listen | 000191 `drop_idx_ci_fts`, 000195 `ci_field_value_override_index`, 000196 `ci_list_name_index`, 000228 `search_document_fields`, 000233 `search_fields`, 000271 `search_users_tags`, 000274 `ci_warranty_days_left_index`, 000276 bis 000279 (Listen-Indizes), 000280 `search_field_scope` |
| Metamodell | 000197 `ci_type_parent`, 000198 `composition_codes`, 000199 `attribute_sets`, 000200 `attribute_set_compose`, 000201 `lifecycle_models`, 000203 `attribute_applies_to`, 000204 `instance_attribute_overrides`, 000205 `templates`, 000206 `ci_template`, 000207 `metamodel_rule`, 000210 `ci_change_template_migration`, 000211 `attribute_computed`, 000212 `form_layout`, 000215 `metamodel_draft`, 000224 `metamodel_publish_regeneration`, 000225 `computed_seed`, 000263 `lifecycle_guards_lcy03` |
| Feldrechte | 000226 `field_grants`, 000231 `asset_field_names` |
| Beziehungen, Netz, Historie | 000202 `relationship_types_p2`, 000208 `ip_reservation`, 000209 `ci_snapshot`, 000213 `ip_address_host`, 000232 `relationship_metadata_schema`, 000261 `relationship_change`, 000270 `asset_snapshot` |
| Dokumente | 000217 `document_link_targets`, 000220 `document_versions_usage`, 000221 `document_soft_delete`, 000222 `document_scan_status`, 000281 `document_orphaned_scope` |
| Tags, Verträge, Lieferanten | 000218 `tags`, 000223 `supplier`, 000227 `contract`, 000238 `contract_links`, 000249 `asset_tags` |
| Assets, Zuweisung, Entsorgung | 000229 `asset_ast01`, 000235 `asset_ci_link`, 000236 `asset_version`, 000237 `ci_owner`, 000241 `asset_serial_norm`, 000244 `ci_child_mode`, 000245 `disposal_norm_fields`, 000248 `composition_to_parent_ci`, 000250 `asset_history`, 000251 `asset_holder_columns`, 000253 `drop_composition`, 000254 `assignment_holder_backfill`, 000257 `assignment_projection`, 000262 `signature_movement`, 000266 `assignment_drop`, 000268 `signature_legacy_payload`, 000273 `ci_parent_cycle_lock` |
| Lager, Bewegungen, Bestellungen, Inventur, Barcode | 000240 `internal_order_states`, 000242 `stock_movement_canonical`, 000246 `drop_asset_movement`, 000247 `internal_order_target_bin`, 000252 `consumable_stock_level`, 000255 `barcode_registry`, 000256 `quantity_item_merge`, 000258 `reservation_client_scope`, 000259 `consumable_on_hand_total`, 000260 `stocktake_snapshot`, 000264 `reservation_consumable_bin`, 000265 `stock_scan_classification`, 000267 `stocktake_immutable`, 000269 `quantity_item_drop` |
| Ticketing | 000214 `ticket_states_number`, 000216 `business_calendar`, 000230 `ticket_links`, 000234 `ticket_comment_system_author`, 000239 `ticket_external_ref`, 000272 `ticket_asset_site` |
| Import und Bulk | 000219 `import_profiles`, 000243 `bulk_change_set` |
| Kunden (Abnahmebefunde) | 000282 `default_client`, 000283 `client_ci_type_hidden` |

Hinweis zum Betrieb: 000246, 000253, 000266 und 000269 entfernen abgelöste
Tabellen (Asset-Bewegungen, Komposition, Zuweisung, Inventar-Items) nach
der Überführung in die kanonischen Tabellen; ein Backup vor dem Update ist
Pflicht wie bei jedem Release.

## Abgekündigte Pfade (E-38, E-25)

Nur G1-Pfade erhalten einen Alias mit `Deprecation`- und `Sunset`-Header
(E-25); P2-Bestandspfade entfallen ohne Alias. Neu abgekündigt in Phase 2:

| Pfad | Nachfolger | Sunset |
|---|---|---|
| `GET /api/v1/lifecycle-definitions`, `POST`, `GET`/`DELETE …/{id}` | `/api/v1/lifecycle-models` | 2027-11-04 |
| `GET /api/v1/assets/{id}/movements` | `GET /api/v1/stock-movements?asset_id=` | 2027-11-03 |
| `GET`/`POST /api/v1/consumables/{id}/movements` | `GET`/`POST /api/v1/stock-movements` mit `consumable_id` | 2027-11-05 bzw. 2027-11-09 |
| `GET`/`POST /api/v1/inventory/items`, `GET`/`PATCH`/`DELETE …/{id}` | `/api/v1/consumables` | 2027-11-09 bzw. 2027-11-06 |
| `GET /api/v1/stocktakes/{id}/difference` | `GET /api/v1/stocktakes/{id}/differences` | 2027-11-06 |
| `GET /api/v1/cis/{id}/parent` | `GET /api/v1/cis/{id}` (`parent_ci_id`, `child_mode`) | 2027-11-05 |
| `GET /api/v1/cis/{id}/state`, `GET /api/v1/assets/{id}/state` | `…/as-of` | 2027-11-06 |

Die Aliase der Phase 1 (u. a. `/api/v1/audit`, `/api/v1/export/*`,
`/api/v1/racks/{id}/mounts`, `/api/v1/ip-addresses`) bleiben mit Sunset
2027-10-13 bis 2027-10-15 bestehen. `inventory/items/{id}/convert` und der
Tab „Verfügbarkeit“ entfallen ohne Nachfolger (A-16).

Verschärfungen an G1-Routen nach E-125: Prüfungen der Beziehungstypen als
Korrektur mit Release-Hinweis, `attributes.tags` mit Übersetzung bis zum
Sunset, Override-Weg sofort 422.

## Bekannte Einschränkungen

- **Skalierung (E-17, E-104):** Die Budgets gelten bei 10.000 Objekten.
  `GET /api/v1/cis` und `GET /api/v1/search` zählen ab 10.000 Treffern
  gedeckelt (`total_capped`); die übrigen Listen zählen exakt und werden
  über etwa 25.000 Objekten langsamer. Bei 50.000 Objekten bleibt das
  System funktionsfähig, ohne Budgetzusage.
- **Lastschwellen (E-146):** Der Lastnachweis G2 hält den vorläufigen
  Budgetsatz G2. Gegen NFR-02 verfehlen 14 Budgets im Median ihre Schwelle
  (u. a. globale Suche, Namensfilter, Bulk mit 500 CIs, Asset- und
  Ticketliste, Systemsichten, Beziehungssuche); Folge-WP-Vorschläge V-1 bis
  V-8 in `docs/acceptance/lasttest-g2.md`. Dedizierter Referenzhost vor GA
  (SR-06).
- **Sicherheit:** offene Befunde Medium SR-58 (berechnete Attribute über
  Asset-Felder an den Feldrechten vorbei), SR-59 (Formularzustände als
  Orakel für unlesbare Felder), SR-60 (Fremdimages ohne Digest und Scan,
  Keycloak 24), Termin vor GA; Low SR-50, SR-61 bis SR-63
  (`docs/security/review-phase2.md`). Das Audit-Log bleibt ohne
  Feldrechte-Projektion (`audit:read`, E-43).
- **Feldrechte und Lifecycle-Modelle** werden in Phase 2 nur über REST
  gepflegt, ohne Admin-Oberfläche (E-107).
- **Metamodell-Texte** MET-20, MET-30 bis MET-34 und MET-40 bis MET-44 sind
  nicht geliefert; umgesetzt nach den Querverweisen (E-37).
- **`GET /api/v1/assets/{id}/cis`** ist im Vertrag noch als geplant
  markiert; die CIs eines Assets liefert `GET /api/v1/cis?filter[asset_id]=`.
- **Ticket-Provider-Schnittstelle** (TKT-03, [O]) ist enthalten, aber ohne
  Gate-Wirkung (GATE-05).
