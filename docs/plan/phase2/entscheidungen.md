# Entscheidungen – Phase 2 (Verwaltung, G2)

Stand: 2026-10-13. Fortsetzung der Nummerierung aus [`../entscheidungen.md`](../entscheidungen.md) (E-01 bis E-36). Jede Entscheidung nennt Status, Festlegung bzw. Übergangsregel, Frage, Optionen, Empfehlung, Auswirkung, Quelle sowie betroffene IDs und WPs. „Vorläufig entschieden“ bedeutet: Die WPs starten nach der Empfehlung; widerspricht der Auftraggeber, wird das betroffene WP vor Beginn angepasst.

## Übersicht

| Nr. | Thema | Status | Betroffene WPs |
|---|---|---|---|
| [E-37](#e-37) | Fehlende Metamodell-Texte MET-20, MET-30 bis MET-34, MET-40 bis MET-44 (Sets, Templates, Regeln, berechnete Attribute) | offen (Übergangsregel gilt) | WP-250, WP-251, WP-257, WP-258, WP-261 |
| [E-38](#e-38) | API-06: REST-Ressourcennamen der Phase 2 und Umstellung abweichender Bestandspfade | vorläufig entschieden (Übergangsregel: Tabelle unten) | WP-247, WP-250, WP-251, WP-257, WP-259, WP-260, WP-263, WP-267, WP-269, WP-281, WP-283, WP-301, WP-312 |
| [E-39](#e-39) | TKT-01/ENT-06: Abgrenzung Ticketing Essential und Standard | offen (Empfehlung als Übergangsregel) | WP-290, WP-292, WP-321 |
| [E-40](#e-40) | Mengenartikel: `consumable` und `quantity_item` zu einem Modell zusammenführen | vorläufig entschieden (Option 1) | WP-283, WP-284 |
| [E-41](#e-41) | Lieferantenstammdaten (`supplier_id` in AST-01 und MGT-12) | vorläufig entschieden (Option 1) | WP-269, WP-271, WP-283, WP-286 |
| [E-42](#e-42) | SEC-09: Betrieb des Malware-Scans (ClamAV) und Verhalten bei Ausfall | vorläufig entschieden (Option 1) | WP-268 |
| [E-43](#e-43) | RBA-07: Granularität der Feldrechte in Phase 2 (Ebenen Objekttyp, Objekt, Attribut, Relation, Aktion, Mandant, Standort, Team) | vorläufig entschieden (Option 1) | WP-264, WP-265, WP-266 |
| [E-44](#e-44) | ENT-06 (V): Feature-Keys der Phase 2 und Paketzuordnung | offen (Empfehlung als Übergangsregel; ergänzt E-34) | WP-244, WP-267, WP-276, WP-286, WP-288, WP-289, WP-290, WP-310 |
| [E-45](#e-45) | Epic-Zuordnung und Traceability-Epics der Phase 2 | vorläufig entschieden (Option 1) | WP-245 |
| [E-46](#e-46) | Verschobene Phase-1-WPs (WP-215 bis WP-239) durch Phase-2-WPs ersetzen | vorläufig entschieden (Option 1) | WP-243 |
| [E-47](#e-47) | TKT-03 [O] und CH17: Ticket-Provider-Schnittstelle in Phase 2 | offen | WP-293 |
| [E-48](#e-48) | Traceability-Vollständigkeit für [P2] (Erweiterung von E-30) | vorläufig entschieden (Option 1) | WP-243, WP-331 |
| [E-49](#e-49) | IMP-IO-01: Import-Dateiformate, Bibliotheken und Grenzen | vorläufig entschieden (Option 1) | WP-301, WP-302 |
| [E-50](#e-50) | EXP-01: DATEV-Exportformat für die Anlagenbuchhaltung | offen (Übergangsregel gilt) | WP-310 |
| [E-51](#e-51) | AST-02: Art der digitalen Unterschrift bei Übergaben | vorläufig entschieden (Option 1) | WP-276, WP-318 |
| [E-52](#e-52) | INV-03: Barcode-Symbologie, Etikettenvorlagen und Bibliotheken | vorläufig entschieden (Option 1) | WP-289, WP-318, WP-320 |
| [E-53](#e-53) | NET-06/NET-09: Review-Typ `duplicate_ip` als Erweiterung des Review-Modells (REC-08) | vorläufig entschieden (Option 1) | WP-299, WP-300 |
| [E-54](#e-54) | MET-50/CH18: Technische Basis für Form Builder und Klick-Builder | vorläufig entschieden (Option 1) | WP-313, WP-315, WP-316 |
| [E-55](#e-55) | MGT-02: Norm und Nachweiskriterien für die Entsorgungsdokumentation | offen (Übergangsregel gilt) | WP-278, WP-321 |

### Weiter gültige Entscheidungen aus Phase 1

| Nr. | Thema | Status | Bedeutung für Phase 2 |
|---|---|---|---|
| [E-02](../entscheidungen.md#e-02) | Fehlende v2-Originaltexte | offen | Betrifft in Phase 2 nur den [A]-Anteil von UI-14 (Self-Service, Access-Reviews) und ABN-01 (a)/(b); keine Blockade für P2-WPs. |
| [E-03](../entscheidungen.md#e-03) | Spätphasen-Module im G1-Release deaktiviert | entschieden | WP-244 stellt die Modul-Gates auf Entitlements um; P2-Module werden im G2-Release freigeschaltet, P3+/[A] bleiben aus. |
| [E-04](../entscheidungen.md#e-04) | Teil 03 als Quelle; Indexnamen `idx_attr_<id>` | entschieden | Metamodell-Befunde werden weiter aus Teil 03 zitiert; N/P-Texte siehe E-37. |
| [E-11](../entscheidungen.md#e-11) | Team-Scope als Schnittmenge | vorläufig | Gilt für Teamzuweisungen, Ticket-Teams und Feldrechte-Scopes. |
| [E-17](../entscheidungen.md#e-17) | Skalierungsziel CH15 | offen | Lastbudgets der Performance-Sanity (WP-327) nutzen 10.000 Objekte. |
| [E-25](../entscheidungen.md#e-25) | API-05-Pfade wörtlich mit Deprecation/Sunset | entschieden | Vorbild für E-38. |
| [E-30](../entscheidungen.md#e-30) | Traceability stufenweise blockierend | entschieden | Erweitert durch E-48. |
| [E-34](../entscheidungen.md#e-34) | ENT-06 (V)/SIM-01 Planmatrix | vorläufig | Ergänzt durch E-44. |
| [E-36](../entscheidungen.md#e-36) | DB-05: contract in Phase 2 | entschieden | WP-269 liefert die Vertragszeile. |

## E-37

**Thema:** Fehlende Metamodell-Texte MET-20, MET-30 bis MET-34, MET-40 bis MET-44 (Sets, Templates, Regeln, berechnete Attribute)

**Status:** offen (Übergangsregel gilt)

**Festlegung:** Keine Festlegung möglich: Die Texte kann nur der Auftraggeber liefern (Aufgabe A-03 aus E-04, bisher nicht erledigt). Übergangsregel: Die WPs WP-250, WP-251, WP-257 und WP-258 setzen nur die Bestandteile um, die aus den vorliegenden Querverweisen folgen (MET-10 „validation (JSON-AST)“ und „computed (MET-44)“, MET-16 „Attribute Sets (in definierter Reihenfolge)“ und „Templates (in definierter Reihenfolge)“, MET-45 Regelsprache, MET-53 „Draft → Test → Publish für Typen, Templates, Sets, Regeln, Layouts“, UI-11 „Sets, Templates, Regeln (Klick-Builder)“, API-06 „Sets, Templates/Versionen, Regeln“, VIE-02 „Garantie läuft bald ab (MET-44)“, JOB-02 „Computed-Attribute-Refresh“, ABN-01 (c) „Attribute Set zuordnen; zwei Templates kombinieren; Regel im Klick-Builder definieren; Template-Version migrieren, Overrides bleiben erhalten“). Die N/P-IDs erhalten erst nach Lieferung Traceability-Zeilen; bis dahin tragen die WPs die verweisenden IDs (MET-16, MET-10, MET-53, UI-11).

**Aufgabe:** Wortlaut, Tag/Gate und Testziel für MET-20, MET-30 bis MET-34 und MET-40 bis MET-44 nachliefern. Zuständig: Auftraggeber/Product Owner. Termin: vor WP-250 (`met-sets`), spätestens vor WP-261 (`met-publish`).

**Frage:** Elf Metamodell-IDs sind nur in der Vollständigkeitsliste enthalten; der Wortlaut fehlt (Audit Teil 03). Sie betreffen vermutlich Attribute Sets, Templates mit Versionen, den Regelkatalog und berechnete Attribute – Kernbestandteile der Phase 2.

**Optionen:**

1. Texte nachliefern; die WPs prüfen ihre Akzeptanzkriterien vor Beginn gegen den Text.
2. Umsetzung ausschließlich nach den Querverweisen (Übergangsregel); die IDs bleiben N/P und zählen nicht für GATE-02.
3. Phase 2 ohne Sets/Templates/Regeln/computed ausliefern (verletzt MET-16, MET-53, UI-11, ABN-01 c).

**Empfehlung:** Option 1 mit Option 2 als Übergangsregel. Option 3 scheidet aus, weil MET-16, MET-53 und ABN-01 (c) die Bestandteile verbindlich verlangen.

**Blockiert / Auswirkung:** Akzeptanzkriterien von WP-250, WP-251, WP-257, WP-258, WP-261, WP-313 bis WP-316; GATE-02 für die N/P-IDs.

**Quelle:** docs/audit/03-datenmodell-standorte-metamodell.md:61–71; docs/plan/entscheidungen.md#e-04

**Betroffene IDs:** API-06, AUD-07, EVT-03, JOB-02, MET-10, MET-16, MET-20, MET-30, MET-31, MET-32, MET-33, MET-34, MET-40, MET-41, MET-42, MET-43, MET-44, MET-53

**Betroffene WPs:** WP-250 (`met-sets`), WP-251 (`met-templates`), WP-257 (`met-rules`), WP-258 (`met-computed`), WP-261 (`met-publish`)

## E-38

**Thema:** API-06: REST-Ressourcennamen der Phase 2 und Umstellung abweichender Bestandspfade

**Status:** vorläufig entschieden (Übergangsregel: Tabelle unten)

**Festlegung:** Die Ressourcen der Phase 2 erhalten folgende Pfade unter `/api/v1` (englische Plural-Substantive wie API-05): `attribute-sets`, `templates` und `templates/{id}/versions`, `rules`, `layouts`, `metamodel/drafts` mit `…/test` und `…/publish`, `tags`, `lifecycle-models` (ersetzt `lifecycle-definitions`), `assets` mit `…/cis`, `…/assign`, `…/return`, `…/transfer`, `…/reserve`, `…/label`, `assignments` (Projektion, nur lesend), `reservations`, `stock-movements` (einzige Bewegungshistorie, lesend; schreibend nur `receipt`, `transfer`, `correction`, `retrieval` für Mengenartikel), `suppliers`, `contracts`, `consumables` mit `…/stock` und `…/availability` (ersetzt `inventory/items`), `orders` mit `…/receive`, `stocktakes` mit `…/items`, `…/scans`, `…/differences`, `…/complete`, `labels/lookup`, `documents` mit `…/versions` und `…/links`, `disposal-records`, `tickets` mit `…/comments`, `…/links`, `business-calendars`, `slas`, `import-profiles`, `imports` mit `…/report`, `field-permissions`, `subnets/{id}/reservations` (wörtlich NET-09), `cis/{id}/as-of` und `assets/{id}/as-of` (AUD-08), `jobs/{id}/revert` (wörtlich JOB-03), `cis/{id}/form` und `ci-types/{id}/form` (aufgelöstes Formular), `cis/{id}/instance-attributes`. Abweichende Bestandspfade (`inventory/items*`, `lifecycle-definitions*`, `ci-types/templates`, `assets/{id}/movements`, `consumables/{id}/movements`) bleiben für mindestens 12 Monate als Alias mit `Deprecation`- und `Sunset`-Header (E-25). Deaktivierte oder noch nicht freigegebene Pfade tragen `x-phase: P2` und `x-feature`.

**Aufgabe:** Tabelle bestätigen oder Abweichungen benennen. Zuständig: Product Owner. Termin: vor WP-247 (`p2-paths`).

**Frage:** API-06 nennt die Ressourcen der Phase 2 nur als deutsche Sammelbegriffe („Sets, Templates/Versionen, Regeln, Layouts, Publish, Tags, Lifecycles, Assets, Verträge, Lager, Stocktake, Reservierungen, Bewegungen, Tickets, Dokumente, Importe“). Der Bestand nutzt teilweise andere Pfade.

**Optionen:**

1. Tabelle wie oben (konsistent mit API-05 und E-25).
2. Bestandspfade beibehalten und nur neue Ressourcen ergänzen (inkonsistente Benennung, verletzt E-25-Logik für API-06).
3. Andere Benennung durch den Auftraggeber.

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** WP-247 und alle WPs mit neuen Routen; Frontend-Clientgenerierung.

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:12–14; docs/plan/entscheidungen.md#e-25

**Betroffene IDs:** API-06, API-09, AUD-07, AUD-08, CI-08, DB-05, GQL-03, IMP-IO-01, JOB-04, LCY-02, MET-01, MET-10, MET-16, MET-50, MET-52, MET-53, MGT-01, MGT-12, STK-01, STK-02, STK-03

**Betroffene WPs:** WP-247 (`p2-paths`), WP-250 (`met-sets`), WP-251 (`met-templates`), WP-257 (`met-rules`), WP-259 (`met-layouts`), WP-260 (`met-drafts`), WP-263 (`met-lifecycle-models`), WP-267 (`doc-model`), WP-269 (`ast-supplier-contract`), WP-281 (`ci-tags`), WP-283 (`stk-consumable-model`), WP-301 (`io-import-model`), WP-312 (`aud-snapshot`)

## E-39

**Thema:** TKT-01/ENT-06: Abgrenzung Ticketing Essential und Standard

**Status:** offen (Empfehlung als Übergangsregel)

**Festlegung:** Keine Festlegung möglich; der Katalog nennt nur die Paketstufen. Übergangsregel: Essential = Tickets mit Status, Priorität, Assignee (Nutzer), CI-/Asset-/Standortbezug, Kommentaren, Tags, Anhängen und Tickets aus Reviews; Standard = zusätzlich Team-Queues, SLA mit Business-Kalender und Feiertagen (TKT-02), Eskalationsbenachrichtigung bei SLA-Verletzung. Pro [P4] bleibt außerhalb.

**Aufgabe:** Funktionsmatrix Essential/Standard bestätigen. Zuständig: Product Owner. Termin: vor WP-290 (`tkt-core`).

**Frage:** TKT-01 verlangt „Ticketing Essential und Standard“ und ENT-06 führt `ticketing_essential` (alle Pakete) und `ticketing_standard` (ab Standard) als getrennte Schlüssel, ohne die Funktionsgrenze zu benennen.

**Optionen:**

1. Übergangsregel wie oben.
2. Standard umfasst nur SLA (TKT-02), alles andere Essential.
3. Andere Abgrenzung durch den Auftraggeber.

**Empfehlung:** Option 1; sie ordnet TKT-02 eindeutig dem Standard-Schlüssel zu und hält Essential ohne Kalenderpflege nutzbar.

**Blockiert / Auswirkung:** WP-244, WP-290, WP-292, WP-321.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:66–68; docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:97–106

**Betroffene IDs:** TKT-01, TKT-02, UI-14

**Betroffene WPs:** WP-290 (`tkt-core`), WP-292 (`tkt-sla-calendar`), WP-321 (`ui-documents-tickets`)

## E-40

**Thema:** Mengenartikel: `consumable` und `quantity_item` zu einem Modell zusammenführen

**Status:** vorläufig entschieden (Option 1)

**Festlegung:** Option 1: Ein Modell `consumable` (STK-01) mit Bestand pro Bin in `stock_level`; `quantity_item` und `inventory/items*` werden per Datenmigration übernommen und als deprecated Alias geführt (E-25). Reservierungen von Mengenartikeln referenzieren `consumable_id`.

**Frage:** Der Bestand führt zwei Tabellen und Routen für Mengenartikel (`consumable` aus Migration 000045, `quantity_item` aus 000055) mit teils widersprüchlicher Semantik (Audit STK-03/STK-05). Der Katalog kennt nur `consumable`.

**Optionen:**

1. Zusammenführen auf `consumable` (Katalogwortlaut).
2. Beide behalten und synchron halten.
3. `quantity_item` behalten und umbenennen.

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** WP-283, WP-284, WP-286, WP-289, WP-320.

**Quelle:** docs/audit/09-module-phase2plus.md:38–40; docs/audit/00-schema-ist.md:114–138

**Betroffene IDs:** API-06, STK-01, STK-02, STK-03

**Betroffene WPs:** WP-283 (`stk-consumable-model`), WP-284 (`stk-availability`)

## E-41

**Thema:** Lieferantenstammdaten (`supplier_id` in AST-01 und MGT-12)

**Status:** vorläufig entschieden (Option 1)

**Festlegung:** Option 1: Eigene Mandantentabelle `supplier(org, client nullable, name unique pro Org, external_ref, contact_surrogate, notes)` mit REST `suppliers`; Assets und Verträge referenzieren sie per Fremdschlüssel; der bisherige Freitext `asset.supplier` wird per Migration in Lieferantenzeilen überführt.

**Frage:** AST-01 und MGT-12 verlangen `supplier_id`, der Katalog definiert aber keine Lieferantentabelle. Der Bestand speichert Lieferanten als Freitext.

**Optionen:**

1. Eigene Tabelle `supplier` (minimal).
2. Lieferant als `contact` mit Rolle vendor (AUD-02).
3. Freitext beibehalten (verletzt AST-01).

**Empfehlung:** Option 1; Kontakte bleiben Personen (PII-Vault), Lieferanten sind Organisationen.

**Blockiert / Auswirkung:** WP-269, WP-271, WP-283, WP-286, WP-319.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:4,62

**Betroffene IDs:** API-06, AST-01, CI-01, DB-05, MGT-12, STK-01, STK-02, STK-03, STK-05

**Betroffene WPs:** WP-269 (`ast-supplier-contract`), WP-271 (`ast-core`), WP-283 (`stk-consumable-model`), WP-286 (`stk-orders`)

## E-42

**Thema:** SEC-09: Betrieb des Malware-Scans (ClamAV) und Verhalten bei Ausfall

**Status:** vorläufig entschieden (Option 1)

**Festlegung:** Option 1: `clamd` als eigener Dienst (Compose-Service bzw. Kubernetes-Deployment, Signaturaktualisierung über freshclam; im Air-Gapped-Profil Offline-Signaturbundle). Uploads sind bis zum Scanergebnis `pending_scan` und nicht abrufbar; fällt der Scanner aus, bleibt der Status erhalten (fail-closed), eine Betriebsmetrik und ein Alarm (WP-103) melden den Rückstau. Befunde führen zu Quarantäne, Benachrichtigung und Audit.

**Frage:** SEC-09 verlangt einen Malware-Scan mit ClamAV und Quarantäne, sagt aber nichts zu Betrieb und Ausfallverhalten.

**Optionen:**

1. clamd als Dienst, fail-closed.
2. clamd als Dienst, fail-open mit nachträglichem Scan.
3. Externer Scan-Dienst (verletzt EU-Residenz, wenn nicht EU).

**Empfehlung:** Option 1; fail-open würde ungeprüfte Dateien an Nutzer ausliefern.

**Blockiert / Auswirkung:** WP-268, Deploy-Manifeste, Sizing-Doku.

**Quelle:** docs/spec/katalog-v3/06-audit-sicherheit-events.md:40; docs/spec/katalog-v3/01-installation-stack.md (INS-08 Air-Gapped)

**Betroffene IDs:** MGT-01, SEC-09

**Betroffene WPs:** WP-268 (`doc-upload-security`)

## E-43

**Thema:** RBA-07: Granularität der Feldrechte in Phase 2 (Ebenen Objekttyp, Objekt, Attribut, Relation, Aktion, Mandant, Standort, Team)

**Status:** vorläufig entschieden (Option 1)

**Festlegung:** Option 1: Grants `field_grant(role, object_kind, ci_type (inkl. Untertypen), attribute | relationship_type | action, level none|read|write)`; die Ebenen Mandant, Standort und Team kommen aus dem Scope der Rollenzuweisung (RBA-03, CH11) und werden nicht erneut modelliert; die Ebene Objekt wird über `object_grant(role | team, object_id, level)` als Ausnahme je Einzelobjekt abgebildet. Auflösung: spezifischster Grant gewinnt, bei gleicher Spezifität gewinnt `none` (deny), Standard ohne Grant ist `write` für Rollen mit Schreibrecht und `read` für Leserollen; als `sensitive` markierte Attribute sind ohne ausdrücklichen Grant `none`.

**Frage:** RBA-07 zählt acht Ebenen auf, ohne Auflösungsregeln. Eine vollständige Matrix über alle Ebenen wäre in Phase 2 nicht in 0,5–2-PT-Paketen umsetzbar und für Nutzer kaum verständlich.

**Optionen:**

1. Wie oben (Rolle × Typ × Attribut/Relation/Aktion, Scope aus der Rollenzuweisung, Objektausnahmen).
2. Nur Rolle × Attribut.
3. Vollständige achtdimensionale Matrix.

**Empfehlung:** Option 1; sie deckt alle genannten Ebenen mit zwei Tabellen ab und bleibt in RLS erzwingbar.

**Blockiert / Auswirkung:** WP-264 bis WP-266, WP-310, WP-311.

**Quelle:** docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:74–82

**Betroffene IDs:** API-06, GQL-04, MET-10, RBA-07, SRC-01, TST-01

**Betroffene WPs:** WP-264 (`fr-model`), WP-265 (`fr-rest-gql`), WP-266 (`fr-search-export-webhooks`)

## E-44

**Thema:** ENT-06 (V): Feature-Keys der Phase 2 und Paketzuordnung

**Status:** offen (Empfehlung als Übergangsregel; ergänzt E-34)

**Festlegung:** Übergangsregel: Schlüssel und Pakete wie in der ENT-06-Tabelle: `assets_basic`, `documents`, `import`, `ticketing_essential` in allen Paketen; `ticketing_standard`, `signature`, `stock`, `stocktake_standard`, `barcode`, `export_datev` ab Standard; `internal_orders` ab Pro. `gps_standard` und `workplace` werden als Schlüssel angelegt, haben in Phase 2 aber kein Modul (MGT-06 Arbeitsplatzverwaltung ist [P4]; GPS gehört zu Mobil [P4]). Die Bestandsschlüssel `inventory`, `ticketing`, `stocktake` werden per Datenmigration auf die neuen Schlüssel abgebildet. Limits: `max_storage_mb` (documents) 1.000 / 10.000 / 50.000 / unbegrenzt (V).

**Aufgabe:** Paketmatrix der Phase 2 und das Speicherlimit bestätigen. Zuständig: Product Owner/Vertrieb. Termin: vor WP-244 (`p2-entitlements`).

**Frage:** ENT-06 ist mit (V) „zu bestätigen“ markiert; `gps_standard` und `workplace` sind P2 getaggt, obwohl die zugehörigen Module in P4 liegen; ein Speicherlimit pro Org (MGT-01) ist nicht beziffert.

**Optionen:**

1. Übergangsregel wie oben.
2. `gps_standard`/`workplace` erst in Phase 4 anlegen.
3. Andere Matrix durch den Auftraggeber.

**Empfehlung:** Option 1; Schlüssel ohne Modul schaden nicht und halten die Matrix katalogtreu.

**Blockiert / Auswirkung:** WP-244 und alle Modul-WPs mit `RequireFeature`.

**Quelle:** docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:97–106; docs/plan/entscheidungen.md#e-34

**Betroffene IDs:** API-06, AST-02, ENT-02, ENT-06, EXP-01, INV-02, INV-03, MGT-01, RBA-05, STK-05, TKT-01, TST-01

**Betroffene WPs:** WP-244 (`p2-entitlements`), WP-267 (`doc-model`), WP-276 (`ast-signature`), WP-286 (`stk-orders`), WP-288 (`inv-standard`), WP-289 (`inv-barcode`), WP-290 (`tkt-core`), WP-310 (`exp-datev`)

## E-45

**Thema:** Epic-Zuordnung und Traceability-Epics der Phase 2

**Status:** vorläufig entschieden (Option 1)

**Festlegung:** Option 1 nach Katalogabschnitt 46: Datenmodell, Metamodell, Rechte, Module (Abschnitte 9–18, 23–27) und Graph tragen Epic **B**; API, Import/Export, GraphQL und Frontend (Abschnitte 34–38) tragen **C**; Seed, Tests, Abnahme und Gate tragen **G2** (analog LT/G1 in Phase 1). Die Spalte `epic` in `docs/traceability.csv` folgt dem Feld „Epic (Traceability)“ des WP-Blocks.

**Frage:** Der Katalog ordnet Phase-2-Module „B (Phase 2)“ zu, nennt aber keine Epics für Oberflächen und Qualitätssicherung der Phase 2.

**Optionen:**

1. B/C/G2 wie oben.
2. Eigener Epic-Buchstabe je Modul.
3. Alle Phase-2-WPs unter B.

**Empfehlung:** Option 1 (konsistent mit E-06 und dem Phase-1-Plan).

**Blockiert / Auswirkung:** Alle Traceability-Zeilen der Phase 2.

**Quelle:** docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:105–117; docs/plan/entscheidungen.md#e-06

**Betroffene IDs:** RBA-05, RBA-06

**Betroffene WPs:** WP-245 (`p2-permissions`)

## E-46

**Thema:** Verschobene Phase-1-WPs (WP-215 bis WP-239) durch Phase-2-WPs ersetzen

**Status:** vorläufig entschieden (Option 1)

**Festlegung:** Option 1: Die nach E-03 verschobenen WPs mit [P2]-IDs werden durch neue WPs dieses Plans ersetzt (Tabelle „Verschobene Arbeitspakete“); ihre Blöcke im Phase-1-Plan bleiben unverändert, werden aber nicht mehr als PR-Nummer verwendet. WPs mit ausschließlich P3/P4/[A]-IDs bleiben verschoben.

**Frage:** Die verschobenen WPs haben Voraussetzungen und Dateilisten aus der Zeit vor Phase 1 und decken ihre IDs nur teilweise (meist nur den Befund, nicht die ganze Anforderung).

**Optionen:**

1. Ersetzen durch neue WPs (dieser Plan).
2. Verschobene WPs unverändert ausführen und Lücken mit Zusatz-WPs schließen.
3. Verschobene Blöcke im Phase-1-Plan umschreiben (ändert ein eingefrorenes Dokument).

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** PR-Nummern, Traceability-Prüfung (WP-243), Tabelle der verschobenen WPs.

**Quelle:** docs/plan/implementierungsplan.md#m-s--high-befunde-späterer-phasen-nach-e-03

**Betroffene IDs:** DOD-01, GLO-11

**Betroffene WPs:** WP-243 (`p2-trace`)

## E-47

**Thema:** TKT-03 [O] und CH17: Ticket-Provider-Schnittstelle in Phase 2

**Status:** offen

**Festlegung:** Keine Festlegung: [O]-Punkte beeinflussen kein Gate (GATE-05); CH17 (S) sieht die Schnittstelle aber ausdrücklich vor. Der Plan enthält WP-293 (`tkt-provider`, 1,0 PT) als optionales WP ohne Gate-Wirkung; ob es in Phase 2 ausgeführt wird, entscheidet der Auftraggeber.

**Aufgabe:** Entscheiden, ob WP-293 in Phase 2 ausgeführt wird. Zuständig: Product Owner. Termin: vor M2.6.

**Frage:** Soll die minimale Provider-Schnittstelle (Interface create/update/comment/link/status_sync, interner Referenz-Provider, generische `external_ref`) jetzt angelegt werden, obwohl TKT-03 [O] ist?

**Optionen:**

1. Ja, als optionales WP in Phase 2 (geringer Aufwand, erfüllt CH17).
2. Nein, Backlog.

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** WP-293.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:70; docs/spec/katalog-v3/00-grundlagen.md (CH17)

**Betroffene IDs:** TKT-03

**Betroffene WPs:** WP-293 (`tkt-provider`)

## E-48

**Thema:** Traceability-Vollständigkeit für [P2] (Erweiterung von E-30)

**Status:** vorläufig entschieden (Option 1)

**Festlegung:** Option 1: Die Prüfung liest beide Pläne und beide Abdeckungsdateien; `TRACEABILITY_REQUIRE_COMPLETE` nimmt eine Tag-Liste (z. B. `B,Q` ab G1, `B,Q,P2` ab G2); fehlende IDs anderer Tags werden weiterhin nur berichtet. IDs mit Status N/P in `abdeckung.csv` (Spalte `status`) sind von der Vollständigkeit ausgenommen, bis ihr Text vorliegt (E-37).

**Frage:** E-30 regelt nur Phase 1. Ohne Erweiterung würde die Prüfung entweder alle [P3]/[P4]/[A]-IDs blockieren oder [P2] nie verlangen.

**Optionen:**

1. Tag-Liste wie oben.
2. Blockierend für alle [B]/[Pn]/[A] ab G2 (nicht erfüllbar).
3. Nie blockierend für [P2].

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** WP-243, WP-331.

**Quelle:** docs/plan/entscheidungen.md#e-30; backend/internal/traceability/traceability.go

**Betroffene IDs:** DOD-01, GATE-01, GATE-02, GATE-05, GLO-11, SEQ-01

**Betroffene WPs:** WP-243 (`p2-trace`), WP-331 (`g2-gate`)

## E-49

**Thema:** IMP-IO-01: Import-Dateiformate, Bibliotheken und Grenzen

**Status:** vorläufig entschieden (Option 1)

**Festlegung:** Option 1: CSV (UTF-8 mit/ohne BOM, Trennzeichen automatisch: Semikolon, Komma, Tab) und Excel `.xlsx` (Bibliothek `github.com/xuri/excelize/v2`, nur das erste bzw. ein benanntes Blatt); JSON-Zeilen über die API. Grenzen: 50 MB Datei (SEC-09), 50.000 Zeilen je Import, 5.000 Zeilen je API-Aufruf; größere Dateien werden abgelehnt (422). `.xls` und `.ods` werden nicht unterstützt.

**Frage:** IMP-IO-01 nennt „CSV, Excel, API“ ohne Formate, Bibliotheken und Grenzen.

**Optionen:**

1. Wie oben.
2. Nur CSV und API.
3. Zusätzlich .xls/.ods.

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** WP-301 bis WP-303, Abhängigkeitsscan (neue Bibliothek).

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:52

**Betroffene IDs:** API-06, API-09, IMP-IO-01, JOB-04

**Betroffene WPs:** WP-301 (`io-import-model`), WP-302 (`io-import-run`)

## E-50

**Thema:** EXP-01: DATEV-Exportformat für die Anlagenbuchhaltung

**Status:** offen (Übergangsregel gilt)

**Festlegung:** Keine Festlegung möglich: Das konkrete DATEV-Zielformat (Anlagenbuchführung-Importschnittstelle ASCII mit Semikolon, Windows-1252, Kopfzeile nach DATEV-Vorgabe) und die Pflichtfelder muss die Buchhaltung des Auftraggebers bestätigen. Übergangsregel: WP-310 implementiert ein Default-Mapping der Asset-Felder (inventory_no, name, manufacturer/model, purchase_date, purchase_price, currency, supplier, cost_center, warranty_until, location, lifecycle_state) auf eine konfigurierbare Spaltenliste mit Kopfzeile, Semikolon und Windows-1252; das Zielformat ist als Mapping-Datei austauschbar.

**Aufgabe:** DATEV-Zielformat (Schnittstellenbeschreibung, Pflichtspalten, Beispiel) liefern. Zuständig: Auftraggeber/Buchhaltung. Termin: vor WP-310 (`exp-datev`).

**Frage:** EXP-01 verlangt „DATEV-Mapping mit Default für die Anlagenbuchhaltung (Assets)“, ohne das Format zu nennen.

**Optionen:**

1. Format nachliefern; Default-Mapping wie Übergangsregel.
2. Generischer Semikolon-Export mit DATEV-ähnlicher Kopfzeile ohne Nachweis.

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** WP-310, Unit-Tests DATEV (TST-01).

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:50

**Betroffene IDs:** ENT-02, EXP-01, TST-01

**Betroffene WPs:** WP-310 (`exp-datev`)

## E-51

**Thema:** AST-02: Art der digitalen Unterschrift bei Übergaben

**Status:** vorläufig entschieden (Option 1)

**Festlegung:** Option 1: Einfache elektronische Signatur: Unterschriftsbild (PNG, Canvas) als Dokument mit Link auf die Bewegung, SHA-256 des Bildes und des Bewegungsdatensatzes in `stock_movement.signature_hash`, Unterzeichner als Surrogat, Zeitstempel; Bewegungen sind append-only. Qualifizierte Signaturen (eIDAS) sind nicht Bestandteil.

**Frage:** AST-02 verlangt „digitale Unterschrift ab Paket Standard“, ohne Verfahren.

**Optionen:**

1. Einfache elektronische Signatur wie oben.
2. Fortgeschrittene/qualifizierte Signatur über externen Vertrauensdienst.

**Empfehlung:** Option 1; Option 2 würde einen Subprozessor und eigenes Entitlement verlangen.

**Blockiert / Auswirkung:** WP-276, WP-267 (Link-Art stock_movement), WP-318.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:6

**Betroffene IDs:** AST-02, UI-15

**Betroffene WPs:** WP-276 (`ast-signature`), WP-318 (`ui-pickers-flows`)

## E-52

**Thema:** INV-03: Barcode-Symbologie, Etikettenvorlagen und Bibliotheken

**Status:** vorläufig entschieden (Option 1)

**Festlegung:** Option 1: Code128 und QR über `github.com/boombuler/barcode` (SVG/PNG), PDF-Etiketten über `github.com/go-pdf/fpdf` mit drei festen Vorlagen (Asset-Etikett 50×25 mm, Bin-Etikett 100×50 mm, A4-Bogen 24 Etiketten); Barcode-Inhalt ist die Inventarnummer bzw. ein `reticora:`-URN mit Objektart und ID; Kamera-Scan im Browser über `BarcodeDetector` mit `@zxing/browser` als Fallback.

**Frage:** INV-03 nennt Code128/QR und PDF-Vorlagen, aber keine Bibliotheken oder Vorlagenformate.

**Optionen:**

1. Wie oben.
2. Nur SVG ohne PDF (verletzt INV-03).
3. Externer Etikettendienst.

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** WP-289, WP-318, WP-320; Abhängigkeitsscan.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:36

**Betroffene IDs:** INV-03, UI-14, UI-15

**Betroffene WPs:** WP-289 (`inv-barcode`), WP-318 (`ui-pickers-flows`), WP-320 (`ui-stock-orders-stocktake`)

## E-53

**Thema:** NET-06/NET-09: Review-Typ `duplicate_ip` als Erweiterung des Review-Modells (REC-08)

**Status:** vorläufig entschieden (Option 1)

**Festlegung:** Option 1: `duplicate_ip` wird als elfter Review-Typ ergänzt (Aktionen: `keep_both`, `mark_stale`, `dismiss`); REC-08 nennt den Typ nicht, NET-06 und NET-09 verlangen ihn.

**Frage:** WP-088 hat die zehn Typen aus REC-08 fest verdrahtet; NET-06/NET-09 verlangen einen weiteren Typ.

**Optionen:**

1. Elfter Typ.
2. `conflicting_values` wiederverwenden (verliert die IP-Semantik).

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** WP-299, WP-300, Review-Inbox (Phase 1 UI zeigt neue Typen generisch).

**Quelle:** docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:66,72; docs/spec/katalog-v3/05-collector-discovery-reconciliation.md (REC-08)

**Betroffene IDs:** NET-06, NET-09

**Betroffene WPs:** WP-299 (`net-ip-reservation`), WP-300 (`net-duplicate-ip`)

## E-54

**Thema:** MET-50/CH18: Technische Basis für Form Builder und Klick-Builder

**Status:** vorläufig entschieden (Option 1)

**Festlegung:** Option 1: Drag & Drop mit `@dnd-kit/core` (Tastaturalternative nach WCAG), Regel-/Filter-Editor als eigene Komponente `RuleBuilder`, die ausschließlich den AST nach `api/rules.schema.json` erzeugt und die TypeScript-Auswertung aus WP-118 zur Vorschau nutzt; keine Text-DSL.

**Frage:** Der Katalog verlangt Klick-Builder und Drag & Drop, nennt aber keine Bibliotheken.

**Optionen:**

1. dnd-kit + eigene RuleBuilder-Komponente.
2. Fertige Form-Builder-Bibliothek (meist nicht WCAG-konform und nicht AST-kompatibel).

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** WP-315, WP-316; Abhängigkeitsscan.

**Quelle:** docs/spec/katalog-v3/03-datenmodell-standorte-metamodell.md:93; docs/spec/katalog-v3/00-grundlagen.md (CH18)

**Betroffene IDs:** MET-50, MET-52, SRC-03, UI-11

**Betroffene WPs:** WP-313 (`ui-meta-types`), WP-315 (`ui-rule-builder`), WP-316 (`ui-form-builder`)

## E-55

**Thema:** MGT-02: Norm und Nachweiskriterien für die Entsorgungsdokumentation

**Status:** offen (Übergangsregel gilt)

**Festlegung:** Keine Festlegung möglich: MGT-02 nennt „BSI/ISO“ ohne konkrete Norm. Übergangsregel: WP-278 dokumentiert Entsorgungsmethode nach DIN 66399 (Schutzklasse, Sicherheitsstufe) bzw. BSI-Empfehlung für Datenträger, Datenträger-Kennung, Dienstleister (Lieferant), Nachweisdokument, Durchführenden und Zeugen (Surrogate), Zeitpunkt; der Datensatz ist append-only und wird in der Audit-Hashkette referenziert.

**Aufgabe:** Norm und Pflichtfelder bestätigen. Zuständig: Auftraggeber/Datenschutz. Termin: vor WP-278 (`ast-disposal`).

**Frage:** Welche Norm und welche Pflichtangaben gelten für „revisionssicher nach BSI/ISO“?

**Optionen:**

1. DIN 66399 + BSI-Datenträgerempfehlung (Übergangsregel).
2. ISO/IEC 27001 Annex A 7.14 (Entsorgung von Betriebsmitteln) mit frei definierten Feldern.
3. Andere Vorgabe des Auftraggebers.

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** WP-278, WP-321.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:42; docs/audit/09-module-phase2plus.md:45

**Betroffene IDs:** MGT-02, UI-14

**Betroffene WPs:** WP-278 (`ast-disposal`), WP-321 (`ui-documents-tickets`)

## Offene Aufgaben

Diese Aufgaben folgen aus den Empfehlungen. Die betroffenen WPs starten erst, wenn die Aufgabe erledigt ist, oder sie nutzen die Übergangsregel der Entscheidung.

| Nr. | Entscheidung | Aufgabe | Zuständig | Termin |
|---|---|---|---|---|
| A-12 | [E-37](#e-37) | Wortlaut, Tag/Gate und Testziel für MET-20, MET-30 bis MET-34, MET-40 bis MET-44 nachliefern (verlängert A-03) | Auftraggeber/Product Owner | vor WP-250, spätestens vor WP-261 |
| A-13 | [E-38](#e-38) | REST-Ressourcentabelle der Phase 2 bestätigen | Product Owner | vor WP-247 |
| A-14 | [E-39](#e-39) | Funktionsmatrix Ticketing Essential/Standard bestätigen | Product Owner | vor WP-290 |
| A-15 | [E-44](#e-44) | Paketmatrix Phase 2 und Speicherlimit bestätigen (ergänzt E-34) | Product Owner/Vertrieb | vor WP-244 |
| A-16 | [E-47](#e-47) | Ausführung von WP-293 (Ticket-Provider-Schnittstelle, [O]) freigeben oder zurückstellen | Product Owner | vor M2.6 |
| A-17 | [E-50](#e-50) | DATEV-Zielformat für die Anlagenbuchhaltung liefern | Auftraggeber/Buchhaltung | vor WP-310 |
| A-18 | [E-55](#e-55) | Norm und Pflichtfelder der Entsorgungsdokumentation bestätigen | Auftraggeber/Datenschutz | vor WP-278 |
| A-19 | [E-02](../entscheidungen.md#e-02) | v2-Texte für UI-14 ([A]-Anteil) und ABN-01 (a)/(b) nachliefern (unverändert aus Phase 1, A-02) | Auftraggeber/Product Owner | vor dem Add-on-Gate (GA) bzw. vor der Abnahme der Szenarien (a)/(b) |
