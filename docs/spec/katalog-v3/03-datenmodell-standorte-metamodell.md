Prüfschwerpunkte für diesen Teil
DB-02: Migrationen fortlaufend nummeriert, up und down vollständig, Roundtrip-Test in der CI; docs/schema-baseline.md vorhanden und konsistent mit den Migrationen.
DB-03: attribute_definition ist die einzige Quelle für Validierung und Indizierung; ci_type.attribute_schema wird generiert und ist read-only (kein direkter Schreibpfad über API oder UI); kein EAV.
DB-04: Laufzeit-DDL als Job, CREATE INDEX CONCURRENTLY außerhalb von Transaktionen, Namensschema idx_attr_

Location-Baum (LOC-10): Tabelle location mit kind, parent_id, path (ltree), site_id; erlaubte Eltern-Kind-Kombinationen serverseitig erzwungen; Zyklenschutz; 1:1-Fachtabellen; kind-Werte inkl. warehouse/zone/shelf/bin bereits zulässig.
Eindeutigkeiten LOC-01 bis LOC-06 exakt (site eindeutig pro (org, client_id)); rack height_units 1–60 Default 42.
LOC-11: 409 bei Löschung mit abhängigen CIs/Assets/Mounts/Kindern; Umzug per PATCH parent_id mit location_change, Audit und Pfad-Aktualisierung des Teilbaums.

MET-01 bis MET-03: Felder von ci_type inkl. is_observable (Defaults physisch/logisch) und i18n-Map; Seed-Typen exakt gegen die Listen (fehlende und überzählige Keys auflisten). MET-04: neue Typen ohne Migration/Release.
MET-10/MET-11: Felder von attribute_definition; serverseitige Validierung jedes Phase-1-Datentyps (text … uuid) mit Tests.
MET-15/MET-16: Unique-Prüfung unter Ausschluss soft-gelöschter CIs; Phase-1-Komposition (Basis + Typkette), Materialisierung, schema_version im CI.

MET-45: api/rules.schema.json, Go-Auswertung (verbindlich), TypeScript-Auswertung (Vorschau), gemeinsames Konformitätstestset, das in beiden Sprachen läuft; Phase-1-Umfang required/pattern/range/enum.
MET-54: REST-Verwaltung und Export für Typen und Attributdefinitionen; Audit von Metamodell-Änderungen (AUD-07).

| Information | Kanonischer Speicherort | Abgeleitet / beobachtet |
|---|---|---|
| Seriennummer | asset.serial_number (wenn Asset verknüpft) | ci.serial_number = beobachteter Wert (Discovery/Agent) für Identity-Resolution; bei Verknüpfung Anzeige aus Asset, Abweichung → Review serial_mismatch |
| Standort physisches Objekt mit Asset | asset.location_id (Location-Baum) | ci.location_id spiegelt das Asset; rack_mount ist die feine Platzierung und setzt asset.location_id auf das Rack |
| Standort CI ohne Asset / logisches CI | ci.location_id | site_id, room_id denormalisiert für Indizes und RLS |
| Physischer Lifecycle | asset.lifecycle_state | CI zeigt read-only |
| Software-Lifecycle | ci.lifecycle_state (nur Typen mit Lifecycle-Modell) | – |
| Operativer Status | ci.status | – |
| Health | ci.health (berechnet aus Monitoring/Findings) | – |
| Inventarnummer, Barcode, RFID, Kaufdaten, Garantie, Owner, Kostenstelle | asset | CI read-only |
| Rack-Platzierung | rack_mount | Beziehung mounted_in projiziert |
| Enthaltensein (Chassis/Blade) | ci.parent_ci_id | Beziehung contains projiziert |
| Standort-Enthaltensein | location.parent_id | Beziehung located_in projiziert |
| VLAN | Tabelle vlan (NET-08) | kein CI-Typ vlan; Interface und Subnetz referenzieren vlan_id |
| Subnetz | Tabelle subnet | kein CI-Typ network; Topologie projiziert Subnetze als Knoten (IMP-11) |
| Vertrag | Tabelle contract (MGT-12) | kein CI-Typ contract; Referenzattribut „Vertrag" zeigt auf contract |
| Installierte Software | Tabelle installed_software (AGT-02) | CI-Typ software nur für Produkt-/Katalogeinträge (z. B. „SAP ERP"), Beziehung runs_on nur für diese |
| Kontakt, Owner | contact, app_user, team | Referenzattribute; PII über Vault (SEC-05) |

10. Standorte & Lagerorte

LOC-01 [B] organization(name, slug unique, plan, settings, timezone IANA, currency ISO 4217, locale).

LOC-02 [B] client(org, name unique pro Org, external_ref, settings, timezone optional).

LOC-03 [B] site(org, client, name unique pro (org, client_id), address, geo_lat, geo_lon, timezone optional, notes) (V: Eindeutigkeit pro Client wegen MSP-Kollisionen).

LOC-04 [B] building(site, name unique pro Site, floorplan_object_key).

LOC-05 [B] room(building, name unique pro Building, floor).

LOC-06 [B] rack(room, name unique pro Room, height_units 1–60 Default 42, width_mm 600, depth_mm 1000, notes).

LOC-07 [B] CRUD für alle Ebenen; Standortbaum mit Inline-Anlage.

LOC-08 [P2] Lagerhierarchie Warehouse → Zone → Shelf → Bin; Warehouse hängt an einer Site; integriert in den Location-Baum (LOC-10).

LOC-09 [P4] GPS-Erfassung, GIS-Karte mit Clustern, Raumpläne als Layer, WLAN-Heatmaps.

LOC-10 [B] Gemeinsamer Location-Baum (CH28): location(id, org, client, kind site|building|room|rack|warehouse|zone|shelf|bin, parent_id, name, path ltree, site_id denormalisiert). Die Fachtabellen site, building, room, rack, warehouse, zone, shelf, bin halten ihre spezifischen Spalten und referenzieren 1:1 die location-Zeile. Erlaubte Eltern: building→site, room→building, rack→room, warehouse→site, zone→warehouse, shelf→zone, bin→shelf. Referenzattribute (MET-12), asset.location_id und ci.location_id zeigen auf location.

LOC-11 [B] Löschregeln: Locations mit zugeordneten CIs, Assets, Mounts oder Kindern können nicht gelöscht werden (409 conflict); Umzug per PATCH parent_id mit Historie in location_change und Audit.

11. Metamodell: Typen, Attribute, Templates, Regeln
11.1 Objekttypen

MET-01 [B] ci_type: org (NULL = System), key unique pro Org, display_name (i18n-Map de/en), icon, category, description, is_system, is_active, is_observable (Default true für physische, false für logische Typen; steuert Offline-Detector LCY-05), parent_type_id [P2], lifecycle_model_id [P2], attribute_schema (generiert, read-only, JSON Schema 2020-12), required_fields (generiert), schema_version.

MET-02 [B] Seed-Typen physisch: switch, router, firewall, access_point, server, hypervisor, vm, client, pdu, ups, nas, storage_array, printer, ip_phone, camera, generic_device, patch_panel.

MET-03 [B] Seed-Typen logisch: business_service, application, database, cluster, cloud_resource, software (Produkt). network, vlan und contract entfallen als CI-Typen (DB-05).

MET-04 [B] Neue Typen ohne Migration und Code-Release nutzbar.

MET-05 [P2] Typen klonen und deaktivieren.

MET-06 [P2] Vererbung über parent_type_id; Attribute, Regeln, Layouts und Lifecycle-Modell werden vererbt; Zyklen verboten (Check beim Schreiben); Filter auf Obertyp schließt Untertypen ein; Vererbungstiefe maximal 6.

11.2 Attributdefinitionen

MET-10 [B] Zentrale attribute_definition pro Org (org NULL = System): key, display_name (i18n-Map), data_type, description, required, default, multi_value, searchable, unique (Scope org|client), inheritable, validation (JSON-AST, MET-45), group, order, unit. [P2] zusätzlich: Sichtbarkeits- und Editierrechte je Rolle (RBA-07), computed (MET-44).

MET-11 Datentypen, serverseitig validiert: [B] text, multiline, integer, decimal, boolean, date, datetime, enum, multiselect, ip, mac, url, email, uuid; [P2] file (Dokumentmodul), json, reference (CI, Asset, Nutzer, Team, Location, Vertrag).

MET-12 [P2] Referenzattribute sind Fremdbezüge mit Integritäts- und Tenant-Prüfung (gleiche Org; Client-Scope kompatibel) und Anzeige des Zielnamens; Standort ist eine Referenz auf location.

MET-13 [P2] Globale Attribute für alle anwendbaren Typen.

MET-14 [P2] Instanzattribute im Namensraum attributes._instance (CI-10): persistiert, per API verfügbar, berechtigungsgeprüft, auditiert, von Discovery unberührt.

MET-15 [B] Indizierung durchsuchbarer/eindeutiger Attribute gemäß DB-04; Eindeutigkeit serverseitig und per Unique-Index durchgesetzt (unter Ausschluss soft-gelöschter CIs).

MET-16 [B] Kompositionsregeln: effektives Schema = System-Basis → Typkette (Root → Blatt) → Attribute Sets (in definierter Reihenfolge) → Templates (in definierter Reihenfolge) → Instanzattribute. Bei gleichem Schlüssel: required = OR; erlaubte Werte = Schnittmenge (leer → Publish-Fehler); min/max = engster Bereich; pattern = alle gelten (allOf); default = letzte Ebene gewinnt; abweichender Datentyp = Publish-Fehler. Das Ergebnis wird materialisiert, mit schema_version versioniert und im CI referenziert. Phase 1: Basis + Typkette ohne Vererbung; Sets/Templates [P2].

11.6 Formulare &amp; Layouts

MET-50 [P2] Form Builder mit Drag &amp; Drop, getrennt vom Datenmodell; Layouts pro Typ, Template und Rolle.

MET-51 [P2] Progressive Disclosure bei der Anlage (Typ → Hersteller/Modell → Felder); Kachelauswahl.

MET-52 [P2] Preview-Modus als Rolle X mit Template Y.

MET-53 [P2] Draft → Test → Publish für Typen, Templates, Sets, Regeln, Layouts; Drafts wirken nicht produktiv; direktes Publizieren möglich (MET-04); Publikationen auditiert und als Event metamodel.published. Hochgestuft auf MUSS (UI-11, ABN-01).

MET-54 [B] Metamodell per REST verwaltbar und exportierbar (Phase 1: Typen, Attributdefinitionen; Phase 2: alles).
