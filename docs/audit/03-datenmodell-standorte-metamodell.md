# Audit Teil 3 – Datenbank-Konventionen, Standorte & Lagerorte, Metamodell

## Stand

UNVOLLSTÄNDIG – fortsetzen ab ID LOC-01.

Prüfdatum: 2026-10-01. Geprüfter Produktstand: `198f142a453f3f9445062f69569cb56c990a4f97`.
Nur Dokumente unter `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/` und `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/` werden geändert; Produktivcode, Migrationen und Tests bleiben unverändert.

## Quellen und Grenzen

- Vorbereitung: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/README.md`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/00-bestandsaufnahme.md`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/00-schema-ist.md`. Vorhandene Bestandsaufnahme ist Orientierung, kein Ersatz für aktuelle Quellprüfung.
- Grundlagen: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/katalog-v3/00-grundlagen.md`, insbesondere CH8–CH30 und PRI-10.
- Übermittelter Fachwortlaut und Prüfschwerpunkte: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/katalog-v3/03-datenmodell-standorte-metamodell.md`. Die verstreut gelieferten Fachblöcke wurden in Abschnittsreihenfolge zusammengeführt, Tabellen in Markdown umgesetzt; kein fehlender Satz wurde ergänzt. Die wiederholt identische DB-04-Zeile steht einmal, weiterhin unvervollständigt bei `idx_attr_`.
- Trotz abschließender Vollständigkeitsmeldung fehlt Fachwortlaut zu **DB-01, MET-20, MET-30–34, MET-40–44**. Diese zwölf IDs sind N/P, nicht FAIL oder OFFEN. Zu DB-02–04 und MET-45 sind nur Prüfschwerpunkte übermittelt; Bewertungen gelten für diese belegten Sollteile. Die Tabelle kanonischer Speicherorte wird unter DB-05 zusammen mit CH12/PRI-10 geprüft; ein zusätzlicher DB-05-Anforderungssatz fehlt.
- Die 46 vorgegebenen IDs werden jeweils einmal in der Ergebnistabelle geführt. Fehlende v2-Grundlagen, AUD-07 und vollständige Epic-/Gate-Abnahmeunterlagen werden nicht erfunden.
- PASS erfordert vollständige Wortlautkonformität und geeignete Testbelege, nicht nur Mocks oder vorhandenen Code. Datenbankaussagen ohne ausgeführten PostgreSQL-Test sind statische Befunde.
- Status: PASS | PARTIAL | ABWEICHEND | FAIL | OFFEN | N/P. Severity: Critical | High | Medium | Low. Aufwand: S (≤ 0,5 Tag), M (0,5–2 Tage), L (> 2 Tage, aufzuteilen). Aufwand ist eine grobe Behebungsgröße, keine Zusage.
- [B] → G1, [P2] → G2, [P4] → G4. Spätere Phasen ohne Implementierung: OFFEN, solange ihre Fälligkeit nicht belegt ist; vorhandene widersprechende Implementierungen: ABWEICHEND. Unbekannter Tag/Gate: „nicht geliefert“.

## Ergebnistabelle

| ID | Phase/Gate | Status | Befund und Evidenz | Testbeleg / Prüflücke | Severity | Aufwand |
|---|---|---|---|---|---|---|
| DB-01 | nicht geliefert | N/P | Kein Anforderungstext im übermittelten Fachkatalog; keine Konventionen aus dem Ist-Code als Soll rekonstruiert. Quellenlücke siehe oben. | Erst mit Wortlaut, Tag und Gate prüfbar. | – | – |
| DB-02 | nicht geliefert | ABWEICHEND | 57 nichtleere Up-/Down-Paare, lückenlos 000001–000057. CI definiert Up/Down-all/Up in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/.github/workflows/ci.yml:76–89`. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/schema-baseline.md` fehlt. Down ist nicht vollständig: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000021_spec_alignment.down.sql:9–24` nimmt die NULL-Zulassung für globale Typen und Site-Unique nicht zurück; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000055_cmdb_extensions.down.sql:45–49,79–89` restauriert falsches Beziehungsvokabular und lässt den Asset-CI-Unique-Index bestehen. | Dateipaar-/Nummernprüfung durchgeführt. Workflowdefinition ist kein grüner Lauf und kein Schema-Vergleich nach jedem einzelnen Down. Kein lokaler PostgreSQL-Roundtrip. | High | L |
| DB-03 | nicht geliefert | ABWEICHEND | Keine `attribute_definition`; stattdessen typgebundene `ci_type_attribute` und separate Instanzdefinitionen in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000055_cmdb_extensions.up.sql:40–91`. `attribute_schema` existiert nur als nullable JSONB-Spalte (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000021_spec_alignment.up.sql:78`); keine Generierung/Materialisierung. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/citype/pg_repository.go:16–39,137–172` liest/schreibt sie nicht. Kein direkter API-Schreibpfad gefunden, aber fehlende Nutzung ist keine read-only-Generierung. Werte liegen in `ci.attributes` JSONB, nicht in einer klassischen EAV-Wertetabelle; Provenienz bleibt gesondert. | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/validation.go:60–107` validiert aufgelöste Felder; kein Generator-/Indexierungs-/Read-only-Integrationstest. Suche nach `attribute_definition`, `attribute_schema`, `schema_version` in Backend/API. | High | L |
| DB-04 | nicht geliefert | ABWEICHEND | Kein Attribut-DDL-Job, `CREATE INDEX CONCURRENTLY` oder `idx_attr_` in Backend/API gefunden. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000056_rls_enforcement.up.sql:25–46` vergibt NOSUPERUSER/NOBYPASSRLS, USAGE und DML, aber weder Schema-CREATE noch Tabellen-Ownership für Index-DDL. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/database/pool.go:52–77,112–130` schaltet zur App-Rolle und verweigert effektiv privilegierten Betrieb; dieser Teil entspricht CH19. Keine Annahme über extern zusätzlich vergebene Rechte. | Kein DDL-/Nichttransaktions-/Indexnamens-Test. Namenssuffix nach `idx_attr_` nicht übermittelt und daher N/P; keine erfundene Formatvorgabe. | High | L |
| DB-05 | nicht geliefert; CH12/PRI-10 | ABWEICHEND | Kanonische Speicherorte mehrfach nicht umgesetzt: CI-Read/Update bleibt unabhängig vom Asset (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/service.go:131–167`), Seriennummer schreibbar (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/model.go:71`). Asset hat API-Freitext `location`, nicht `location_id` (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/asset/model.go:25,68`); Mount schreibt nur `rack_mount` (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/pg_repository.go:231–257`). Location-FKs zeigen auf paralleles `location_node`, nicht den gemeinsamen Baum. Weitere Details je Tabellenzeile unten. | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/asset/handler_composition_test.go:41–71` prüft mittels Stub lediglich Parent-/Child-Assets, nicht Asset-Hoheit über CI. Kein Nachweis für Spiegelung, `serial_mismatch` oder kanonische Strukturprojektion. | High | L |

## DB-05 – Abgleich der kanonischen Speicherorte

| Übermittelte Information | Statischer Iststand und Evidenz |
|---|---|
| Seriennummer | Asset und CI besitzen getrennte schreibbare Serial-Felder. CI-Get liefert Repositorydaten ohne Asset-Auflösung (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/service.go:131–167`); `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/asset/handler.go:159–169` reichert umgekehrt das Asset mit CI-Daten an. Kein `serial_mismatch` in Backend/API gefunden. |
| Standort physisches Objekt mit Asset | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000055_cmdb_extensions.up.sql:317–322` ergänzt zwei unabhängige FKs; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/pg_repository.go:231–257` setzt beim Mount weder Asset- noch CI-Location. |
| Standort CI ohne Asset / logisches CI | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/model.go:7–36,62–84` führt schreibbare Site-/Room-IDs, aber keine Location-ID; keine Ableitung der Scope-Spalten aus dem neuen Baum. |
| Physischer Lifecycle | Unabhängige freie Textspalten in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000055_cmdb_extensions.up.sql:269–274`; CI-Service ohne Asset-Hoheitsprüfung, keine read-only-Anzeige im CI-Modell. |
| Software-Lifecycle | Dieselben Textspalten ohne Einschränkung auf Typen mit Lifecycle-Modell; logischer Seed `software` fehlt. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000021_spec_alignment.up.sql:89–109`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000055_cmdb_extensions.up.sql:269–280`. |
| Operativer Status | Kanonisches `ci.status` vorhanden (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/model.go:15`); allein kein Nachweis der vollständigen Status-/Lifecycle-/Health-Trennung. |
| Health | Kein `ci.health` in Migrationen oder CI-Modell gefunden; Kommentar über „technical health“ in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000055_cmdb_extensions.up.sql:269–274` ersetzt keine Berechnung/Persistenz. |
| Inventarnummer, Barcode, RFID, Kaufdaten, Garantie, Owner, Kostenstelle | Asset besitzt `asset_tag`, Kauf-/Garantie-/Barcode-/RFID-Felder, aber kein explizites Owner-/Kostenstellenfeld in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/asset/model.go:7–30`; CI-Service hat keine read-only-Auflösung dieser Asset-Daten. Freie JSON-Felder sind kein kanonisch erzwungener Vertrag. |
| Rack-Platzierung | `rack_mount` vorhanden. Strukturtyp `mounted_in` zugleich als speicherbare Beziehung zugelassen; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/relationship/relationship.go:82,329–398`. |
| Enthaltensein (Chassis/Blade) | Kein `ci.parent_ci_id` in Migrationen/CI-Modell; separate `composition` in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000055_cmdb_extensions.up.sql:453–475`. `contains` wird als normale Beziehung gespeichert (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/relationship/relationship.go:92,329–398`). |
| Standort-Enthaltensein | Paralleles `location_node.parent_id`, keine kanonische `location.parent_id`; `located_in` regulär speicherbar (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/relationship/relationship.go:96,329–398`). |
| VLAN | Keine Tabelle `vlan`; `subnet.vlan_id` ist INTEGER ohne VLAN-FK, Interface ohne VLAN-Referenz (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000015_network_ipam.up.sql:4–36`). Kein gesäter CI-Typ `vlan`. |
| Subnetz | Tabelle vorhanden, jedoch Unique `(organization_id,cidr)` statt CH10 mit VRF (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000015_network_ipam.up.sql:22–36`). Kein gesäter Typ `network`. Projektionsprüfung siehe Gegenprüfung. |
| Vertrag | Keine Tabelle `contract` in Migrationen gefunden; kein entsprechender Seed-Typ. Ein fehlender verbotener Typ ersetzt nicht den fehlenden kanonischen Vertragsspeicher. |
| Installierte Software | Keine Tabelle `installed_software`; Softwareliste im Telemetrie-Payload (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/agent/model.go:9–28`) allein ist kein Inventartabellen-Nachweis. Logischer Produkt-Seed fehlt. |
| Kontakt, Owner | Tabellen `contact`, `app_user`, `team` vorhanden; Attribute tragen nur ein textuelles `reference_target`, keine typisierten Fremdbezüge (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000055_cmdb_extensions.up.sql:47,79`). Tenant-/Scope-/Vault-Garantien folgen daraus nicht; siehe MET-12 und CH16. |

## Tests und Validierung

Noch nicht ausgeführt.

## Gegenprüfung CH8–CH30 und PRI-10

Noch in Bearbeitung.
