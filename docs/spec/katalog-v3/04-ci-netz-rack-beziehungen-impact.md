KATALOGAUSZUG (Abschnitte 12, 13, 14, 15, 16, 17)
12. CI-Kern

CI-01 [B] ci-Felder:

- Zuordnung: org, client, location_id (+ denormalisiert site_id, room_id), type, name, asset_id (nullable; genutzt ab [P2]), parent_ci_id (Komposition, AST-05)
- status: active, inactive, maintenance, decommissioned, unknown (Default active)
- Hardware (beobachtet): manufacturer, model, serial_number, hardware_uuid
- Netz: management_ip, primary_mac, hostname, fqdn
- System: os_name, os_version, firmware_version, sys_object_id
- attributes (jsonb, Namensräume nach CI-10), schema_version
- discovery_source: snmp, ssh, redfish, ipmi, wmi, api, agent, sweep, manual, import
- first_seen_at, last_seen_at, is_manual, deleted_at, merged_into_id
- version (int, nur für manuelle Schreibpfade, API-07), health [P3], owner_user_id/owner_team_id [P2], lifecycle_state (nur Software-Typen, [P2])

CI-02 [B] Indizes: (org,type), (org,status), (org,location_id), (org,asset_id), (org,parent_ci_id); partiell auf serial, hardware_uuid, management_ip; GIN jsonb_path_ops auf attributes; GIN-FTS (simple) auf name, hostname, manufacturer, model, serial.

CI-03 [B] Manuelle Anlage mit Name, Typ, Attributen; validiert gegen Metamodell und Regeln.

CI-04 [B] PATCH ist Feld-granular (JSON Merge Patch); jedes manuell geänderte, discovery-relevante Feld wird zum Override (CI-10).

CI-05 [B] DELETE setzt deleted_at und Status decommissioned. Beziehungen bleiben, Traversal ignoriert gelöschte Knoten. Restore per POST /cis/{id}/restore innerhalb der Retention (Default 90 Tage); danach Hard-Delete per Job (JOB-02) inklusive Interfaces/IPs; Asset-Link wird beim Soft-Delete gelöst und im Restore wiederhergestellt.

CI-06 [B] Lesen, Suchen, Filtern, Kontakte, Standort, Zeitstempel, Historie, Audit.

CI-07 [P2] Tags, Owner, Health getrennt von Lifecycle und Status.

CI-08 [P2] Tags als lose Zusatzklassifikation; pro Org verwaltbar, filterbar, per Bulk änderbar.

CI-09 [B] Logische CIs ohne Asset nehmen an Beziehungen, Suche, Topologie, Historie und Impact teil.

CI-10 [B] Namensräume in attributes: fachliche Schlüssel ohne Präfix; _overrides {field: {value, by, at, reason}}; _observed {field: {value, source, rank, at}} (zurückgestellter beobachteter Wert bei aktivem Override); _provenance {field: {source, rank, at}}; _instance {key: value} (MET-14). Typisierte Spalten werden über ihren Spaltennamen in denselben Namensräumen geführt. API liefert eine aufgelöste Sicht (effective, observed, override) pro Feld (REC-10).

CI-11 [B] Reklassifikation: Discovery darf den Typ generic_device auf einen spezifischen Typ heben (nie umgekehrt, nie zwischen spezifischen Typen). Jede andere Typänderung ist manuell, erzeugt ci_change type_changed, prüft Pflichtfelder des Zieltyps als Befunde statt Blockade und behält Beziehungen, Mounts, Asset-Link und Historie.

CI-12 [B] Zusammenführung: POST /cis/{survivor}/merge {victim_id}: Beziehungen, Interfaces, IPs, Kontakte, Historie, Dokumente, Tags und Asset-Link werden auf den Survivor umgehängt; Attribute nach Provenienz-Rang, Overrides beider bleiben erhalten (Konflikt → Review override_conflict); Victim erhält deleted_at und merged_into_id; GET auf das Victim liefert 301 auf den Survivor (12 Monate); auditiert; POST /cis/{id}/unmerge innerhalb 30 Tagen über Snapshot.

CI-13 [B] Wiedersichtung gelöschter Geräte: Identity-Resolution schließt soft-gelöschte CIs ein; Treffer erzeugt Review resurrected_device (reaktivieren oder neues CI). Kein automatisches Duplikat (PRI-03).

13. Lifecycle & Health

LCY-01 [B] ci.status bleibt der operative Status.

LCY-02 [P2] Konfigurierbare Lifecycle-Modelle als Zustandsgraph mit Guards (MET-45): physisch am Asset (CH12) mit States ordered, received, in_stock, reserved, preparing, deployed, repair, retired, disposed; Software am CI mit evaluation, approved, production, deprecated, eol. Modelle pro Objekttyp, vererbbar (MET-06).

LCY-03 [P2] Übergangsregeln: in_stock erfordert location.kind im Lagerzweig; deployed erfordert location.kind im Betriebszweig und/oder Assignee; disposed erfordert disposal_record (MGT-02).

LCY-04 [P3] Lifecycle-Zustände sperren Felder (Regel-Engine) und lösen Automationen aus (Abschnitt 30).

LCY-05 [B] Offline-Detector (REC-09) gilt nur für CIs mit is_manual = false, discovery_source ≠ manual/import und Typ is_observable = true.

LCY-06 [P3] Health: ok, warning, critical, unknown; berechnet aus Alerting (MON-04) und Findings (SEC-02/03); nicht manuell setzbar.

14. Netzwerk, IPAM & Verkabelung

NET-01 [B] network_interface(ci, name unique pro CI, if_index, mac, speed_mbps, mtu, admin_status, oper_status, if_type, description, vlan_id → vlan optional, is_management, miss_count); Index (org, mac).

NET-02 [B] subnet(org, client, site, vrf_id NOT NULL Default global, cidr, vlan_id, description, gateway); unique (org, vrf_id, cidr) (CH10).

NET-03 [B] ip_address(subnet, interface, address, type discovered|reserved|static|dhcp, last_seen_at); unique (org, vrf_id, address, interface).

NET-04 [B] cable(...) unverändert.

NET-05 [B] CRUD für VRFs, VLANs, Subnetze, Kabel; IPs pro Subnetz; Interfaces pro CI.

NET-06 [P2] Erkennung doppelter IPs innerhalb einer VRF → Review duplicate_ip.

NET-07 [P4] Verkabelungs-Workflow.

NET-08 [B] vlan(org, site nullable, vlan_id, name, description); unique (org, COALESCE(site_id), vlan_id).

NET-09 [P2] IP-Reservierung: POST /subnets/{id}/reservations erzeugt ip_address vom Typ reserved; Konflikt mit discovered → Review duplicate_ip.

15. Rack

RCK-01 [B] rack_mount(rack_id, ci_id UNIQUE, position_u ≥ 1, height_u Default 1, face front|rear|both). Exclusion-Constraint berücksichtigt Face: face_range = front [1,2), rear [2,3), both [1,3); EXCLUDE USING gist (rack_id WITH =, int4range(position_u, position_u + height_u) WITH &&, face_range WITH &&). Server prüft position_u + height_u − 1 ≤ rack.height_units (409). Kind-CIs einer Komposition (AST-05) werden nicht separat gemountet.

RCK-02 [B] GET /racks/{id}/layout; PUT /racks/{id}/mounts/{ci_id} liefert 409 bei Kollision oder Höhenüberschreitung.

RCK-03 [B] Unmount und Umpositionieren mit Historie in ci_change und Audit; Mount setzt asset.location_id bzw. ci.location_id auf das Rack (DB-05).

16. Beziehungen

REL-01 [B] relationship_type(key, display_name, reverse_name, directed, category network|power|hosting|logical|physical, impact_direction source_depends_on_target|target_depends_on_source|none|undirected, redundant bool, allowed_source_types[], allowed_target_types[], cardinality, is_system). Seeds mit Impact-Semantik: connected_to (undirected, network), powered_by (source_depends_on_target, redundant), hosted_on (source_depends_on_target), runs_on (source_depends_on_target), member_of_cluster (source_depends_on_target, redundant), depends_on (source_depends_on_target), uplink_to (source_depends_on_target). mounted_in, contains, located_in werden nicht gespeichert, sondern projiziert (CH27).

REL-02 [B] ci_relationship(source, target, type, source_interface?, target_interface?, provenance, confidence 0–1, metadata, last_confirmed_at, verified_by?, verified_at?); UNIQUE (org, source, target, type, COALESCE(source_interface, uuid_nil), COALESCE(target_interface, uuid_nil)); Indizes (org, source), (org, target).

REL-03 [B] relationship_suppression verhindert das Wiederentstehen manuell entfernter Discovery-Kanten.

REL-04 [B] POST /relationships legt manuelle Kanten an; Discovery-Kanten werden nur mit force gelöscht, danach Suppression.

REL-05 [B] Manuelle Kanten verschwinden nie durch Discovery.

REL-06 [P2] Eigene Beziehungstypen pro Org mit allen Feldern aus REL-01; Seeds Phase 2: managed_by (none), backed_up_by (none), monitored_by (none), uses (source_depends_on_target), belongs_to (source_depends_on_target).

REL-07 [P2] Beziehungsattribute: Typ definiert ein Attributschema für metadata, validiert wie CI-Attribute; Interface-Bezüge bleiben kanonische Portdarstellung.

REL-08 [B] Provenienz: manual, discovery, agent, import, api, integration, workflow; Verifikationsstatus; Quellen-Policy.

REL-09 [P2] Kanten bearbeiten und verifizieren; relationship_change als Historie (AUD-08).

17. Graph, Topologie & Impact

IMP-01 [B] Richtung folgt relationship_type.impact_direction (CH27).

IMP-02 [B] Impact per rekursivem CTE: Tiefe maximal 20, Pfad mit Beziehungstypen, Deduplizierung auf den kürzesten Pfad, Filter nach Kategorien; projizierte Strukturkanten (mounted_in, contains, located_in) sind in der Kategorie physical enthalten.

IMP-03 [B] connected_to ist ungerichtet, nur Kategorie network, im Pfad als Heuristik zweiter Klasse markiert.

IMP-04 [B] Go-Postfilter: Typen mit redundant = true (powered_by, member_of_cluster) betreffen ein CI nur, wenn alle Quellen ausgefallen sind; hosted_on ohne Redundanz.

IMP-05 [B] GET /impact?ci_ids=a,b,…&categories für mehrere gleichzeitig ausgefallene CIs (Redundanzanalyse, Wartungsfenster); GET /impact/{ci_id} bleibt als Kurzform; Antwort: ausgefallene CIs, betroffene CIs mit Pfaden, Zusammenfassung.

IMP-06 [B] GET /topology?root_ci_id&depth&categories; ohne root maximal 2.000 Knoten, sonst 422 mit Hinweis; UI startet dann mit Standort- oder Typfilter (UI-06).

IMP-07 [B] Traversierung upstream/downstream, Multi-Hop, Zyklenfestigkeit, gelöschte Knoten ausgeschlossen, Tenant- und Scope-Grenzen; AGE optional; Ergebnisse stimmen mit dem DB-Zustand überein.

IMP-08 [P2] Impact-Kette Service → App → DB → VM → Hypervisor → Switch → PDU liefert betroffene Services, Kunden, Standorte, Blast Radius und Single Points of Failure.

IMP-09 [B] Strommodell Gerät → PDU → USV → Quelle mit redundanten Einspeisungen; PDU-Outlet-Mapping erzeugt powered_by.

IMP-10 [P2] Custom-Typen nehmen gemäß impact_direction teil.

IMP-11 [P2] Subnetze und VLANs erscheinen in Topologie und Impact als projizierte Knoten aus den Tabellen (Kanten aus Interface/IP-Zuordnung), nicht als CIs.
