## Zusammenfassung

Prüfstand 2026-09-22; Produktbasis unverändert gegenüber Audit Teil 2, Dokumentationsbasis `3400d351dba7a274995c98d399d3464f15ff32db`. Ausschließlich dieser Bericht wird angelegt; keine Produktkorrektur.

Zwischenstand: NET-01–09 und RCK-01–03 geprüft; CI/Lifecycle/Beziehungen/Impact sowie abschließende Zähler und G1-Blocker folgen. NET-04 ist mangels unverändert referenziertem v2-Wortlaut N/P.

## Ergebnis je Anforderung

Status und Severity gemäß `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/README.md:13–15`. Fehlende spätere Teilfunktionen ohne eigene Implementierung sind OFFEN; vorhandene widersprechende Implementierungen ABWEICHEND. Quelltextprüfung ist kein erfolgreicher Datenbank-/Laufzeitnachweis; Aufwand bezeichnet empfohlene, hier nicht umgesetzte Korrekturen.

| ID | Tag | Status | Evidenz | Tests | Befund (1–2 Sätze) | Severity | Aufwand |
|---|---|---|---|---|---|---|---|
| NET-01 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000015_network_ipam.up.sql:4–20,98–100`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ipam/model.go:82–124` | TestInterfaceCRUD in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ipam/handler_test.go:130`; kein vollständiger DB-Vertragstest. | Interfacegrundmodell existiert, aber if_index, mtu, vlan_id-FK, miss_count, UNIQUE(ci,name) und Index(org,mac) fehlen; mac_address/interface_type ersetzen die verlangten Feldnamen nicht wortgetreu. | High | M |
| NET-02 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000015_network_ipam.up.sql:22–37`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ipam/model.go:7–46` | TestSubnetCRUDAndCIDRValidation, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ipam/handler_test.go:38`; kein VRF-Test. | Org/Client/Site/CIDR/Gateway bestehen, aber vrf_id und Default global fehlen; UNIQUE(org,cidr) verhindert überlappende Netze in getrennten VRFs. vlan_id ist nur Integer, kein verwalteter VLAN-Bezug. | High | L |
| NET-03 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000015_network_ipam.up.sql:39–53`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000031_schema_corrections.up.sql:20–29` | TestIPAddressSubnetMembership, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ipam/handler_test.go:75`; kein VRF-/Unique-Integrationstest. | last_seen_at und optionale Subnetz-/Interfacebezüge bestehen; status active/reserved/deprecated/dhcp/available statt type discovered/reserved/static/dhcp. Finale Teilindizes unterscheiden Interface NULL, enthalten aber kein vrf_id. | High | L |
| NET-04 | [B] | N/P | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000015_network_ipam.up.sql:55–70`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/handler.go:36–40` | TestCableCRUD, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/handler_test.go:142`. | Kabelmodell und CRUD vorhanden; „cable(...) unverändert“ enthält nicht die vollständigen Sollfelder/-regeln aus v2. Keine Vollkonformität aus dem vorhandenen Modell rekonstruiert. | – | – |
| NET-05 | [B] | PARTIAL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ipam/handler.go:23–41`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/handler.go:36–40` | Vorhandene Subnetz-/IP-/Interface-/Kabel-Memory-CRUD-Tests; keine VRF-/VLAN-Tests. | Subnetz-/Kabel-CRUD, IPs pro Subnetz und Interfaces pro CI sind registriert. VRF-/VLAN-Ressourcen und deren CRUD fehlen vollständig. | High | L |
| NET-06 | [P2] | OFFEN | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ipam/`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/` | Keine duplicate_ip-/VRF-Review-Tests gefunden. | Keine VRF-bezogene Duplikaterkennung mit Review duplicate_ip implementiert; ein Unique-Constraint ist kein menschlicher Klärungsprozess. Späterer, fehlender Funktionsumfang. | – | – |
| NET-07 | [P4] | OFFEN | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/handler.go:36–40`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/api/openapi.yaml` | Nur Kabel-CRUD-Test, kein Verkabelungs-Workflow-Test. | Verkabelungs-Workflow nicht gefunden; CRUD ist keine Umsetzung des späteren Workflows. Dessen genauer Ablauf bleibt im gelieferten Wortlaut offen. | – | – |
| NET-08 | [B] | FAIL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000015_network_ipam.up.sql:29`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ipam/model.go:14`; Gesamtmigrations-/Routensuche ohne vlan-Tabelle. | Keine VLAN-Katalog-/NULL-Site-Unique-Tests. | vlan-Tabelle mit Org/Site/Nummer/Name/Beschreibung und UNIQUE mit COALESCE(site_id) fehlt; die Subnetznummer ersetzt den Katalog nicht. | High | M |
| NET-09 | [P2] | PARTIAL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ipam/model.go:132–135`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ipam/handler.go:240–290` | Bestehender IP-CRUD-Test, kein Reservierungskonflikt-/Review-Test. | reserved ist über generisches POST /ip-addresses nutzbar; verlangtes POST /subnets/{id}/reservations und Review bei Konflikt mit Discovery fehlen. Der vorhandene Asset-Reservierungsdienst ist keine IP-Reservierung. | High | M |
| RCK-01 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000021_spec_alignment.up.sql:112–131`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/repository.go:47–67`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/pg_repository.go:231–257` | TestMountFitAndOverlap, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/handler_test.go:80–139`: front/rear, kein both-/DB-/Parallelitätstest. | DB-Constraint schließt both aus; Go überspringt both auf beiden Seiten ebenfalls. Höhenformel stimmt, Fehler ist aber 400 statt 409; keine Sperre für Kompositionskinder. | High | M |
| RCK-02 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/handler.go:23–40,52–60` | TestMountFitAndOverlap erwartet ausdrücklich 400 für Überhöhe/Kollision. | GET layout und PUT mounts/{ci_id} fehlen; stattdessen GET/POST mounts und PATCH rack-mounts/{id}. Der vorhandene Funktionsersatz hat falschen Routen-/Fehlervertrag. | High | M |
| RCK-03 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/pg_repository.go:231–327`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/handler.go:257–286` | Memorytest für PATCH/DELETE vorhanden, keine Standort-/ci_change-/Auditassertion. | Umpositionieren und Unmount existieren, schreiben aber nur rack_mount; Standortübernahme nach DB-05 sowie ci_change und Audit fehlen. | High | L |

### Testnachweise und Grenzen

Lokaler Test-/Buildlauf ohne Dependencyinstallation beauftragt; Ergebnisse werden ergänzt. CI wurde über Actions-MCP erneut abgefragt: Run [35731844252](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/35731844252), Produktstand `b0f0ee2302e3af5afa41fbeaf738687fe1492504`, insgesamt failure. Migrationsjob [106759104594](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/35731844252/job/106759104594): up/down/up erfolgreich, danach PostgreSQL-Integrationstests fehlgeschlagen; kein vollständiger aktueller grüner DB-Nachweis. Keine CI ausgelöst.

## Befunde im Detail

### NET-01/02/03/05/08 — High: Unvollständiges Netzwerk-/VRF-/VLAN-Modell

**Beschreibung/Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000015_network_ipam.up.sql:4–53,98–106` enthält Interfaces ohne if_index/mtu/miss_count/VLAN-Referenz und ohne Namenseindeutigkeit pro CI; die getrennten Org-/CI-Indizes ersetzen (org,mac) nicht. Subnetze sind orgweit statt pro VRF eindeutig; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000031_schema_corrections.up.sql:20–29` erweitert nur IP-/Interface-Eindeutigkeit. Ein Integer vlan_id ist kein VLAN-Katalog. Die Request-/Responsemodelle bilden denselben eingeschränkten Zustand ab (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ipam/model.go:7–135`); die Router registrieren nur Subnetze, IPs, Interfaces und Kabel.

**Empfohlene Korrektur:** Kanonischen VRF-/VLAN-Katalog samt Default-global-Anlage, Null-Site-Eindeutigkeit und vollständigem CRUD ergänzen; Feld-/Enumvertrag und zusammengesetzte Indizes/Constraints angleichen. Migrationen bestehender IP-/Subnetzdaten mit getrennten VRFs, gleichen IPs auf Interfaces und NULL-Bezügen testen; CRUD-Mocks ersetzen diese Datenbanktests nicht.

### NET-09 — High: IP-Reservierung ohne Konfliktklärung

**Beschreibung/Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ipam/handler.go:240–290` nimmt reserved als normalen Status an und prüft Subnetzmitgliedschaft. Ein eigener Reservierungsauftrag oder Discovery-vs-reserved-Reviewfluss existiert nicht; passende Review-Typen fehlen im Migrationsbestand.

**Empfohlene Korrektur:** Dedizierte Subnetzreservierung und atomare VRF-bezogene Konfliktbehandlung einschließlich duplicate_ip implementieren, mit Tests gegen bereits reservierte und neu entdeckte Adressen. Dies betrifft einen vorhandenen P2-Teilumfang, ist kein zusätzlicher G1-Blocker allein wegen seiner Phase.

### RCK-01/02 — High: both kollidiert weder im Go-Prüfer noch im DB-Constraint

**Beschreibung/Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000021_spec_alignment.up.sql:125–131` verwendet face-Gleichheit und WHERE face != both statt einer überlappenden face_range. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/repository.go:57–63` lässt neue both-Mounts direkt durch und ignoriert bestehende both-Mounts beim Vergleich. Das ist schon sequenziell falsch, nicht lediglich ein hypothetischer Race.

Höhenprüfung position+height−1 ist vorhanden; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/handler.go:52–60` übersetzt ErrValidation jedoch zu 400. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/pg_repository.go:231–257` prüft keine Kompositionskind-Eigenschaft. Gewünschte layout-/PUT-ci_id-Routen fehlen; vorhandene POST-/PATCH-Routen sind nicht wortgleich.

**Empfohlene Korrektur:** Katalogformel für Facebereiche DB-seitig und im Vorprüfer angleichen; 409-Vertrag und geforderte Routen herstellen, Kompositionskinder ausschließen. Tests front/front, rear/rear, front/rear, beide Reihenfolgen mit both, both/both, Randhöhen und parallele DB-Schreibversuche ergänzen. Vorhandener Test `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/handler_test.go:80–139` prüft gerade nicht both und erwartet den falschen Status.

### RCK-03 — High: Rackplatzierung aktualisiert weder kanonischen Ort noch Historie

**Beschreibung/Dateien:** CreateMount/UpdateMount/DeleteMount in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/pg_repository.go:231–327` schreiben ausschließlich die Mounttabelle. Die Handler erhalten nur das Rackrepository; keine Kopplung an Asset-/CI-Standort, ci_change oder Audit vorhanden.

**Empfohlene Korrektur:** Standortänderung am kanonischen Eigentümer gemäß DB-05 und Mountänderung atomar koordinieren; CI-Sicht aus Asset ableiten, Umpositionieren/Unmount mit nachvollziehbaren Änderungen und Audit protokollieren. Integrationstests für verknüpfte/unverknüpfte CIs und Rollback ergänzen.

## Offene Fragen

- NET-04: vollständigen v2-Kabelvertrag nachliefern; vorhandene Spalten und CRUD allein erlauben keine PASS-Bewertung.
- NET-07: Schritte, Freigaben und Abnahme des späteren Verkabelungs-Workflows konkretisieren.
- NET-08 nennt COALESCE(site_id) ohne Ersatzwert; UUID-Sentinel entsprechend REL-02 oder gleichwertige Null-Eindeutigkeit verbindlich festlegen.

## Stand

UNVOLLSTÄNDIG – fortsetzen ab ID GLO-13.
