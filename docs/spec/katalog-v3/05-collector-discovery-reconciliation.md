KATALOGAUSZUG (Abschnitte 19, 20, 21, 22)
19. Collector

COL-01 [B] collector(client, site, name, enrollment_token_hash/expiry, cert_fingerprint, cert_not_after, status pending|active|offline|disabled|paused, version, last_heartbeat_at, config); paused durch Entitlement (ENT-07).

COL-02 [B] credential mit Typen wie v2; Envelope-Encryption (Org-DEK, AES-256-GCM, Master-Key). Klartext existiert nur im Server-Speicher zum Zeitpunkt der Übergabe und im Collector-Speicher; Übergabe über mTLS-NATS als Credential-Bundle, zusätzlich verschlüsselt auf den Collector-Schlüssel (aus dem Collector-Zertifikat abgeleitet); nie auf Disk im Collector; nie in API-Antworten oder Logs. „Klartext verlässt nie die EU" gilt für die SaaS-Serverseite.

COL-03 [P4] Zentrale KMS/Vault-Anbindung.

COL-04 [B] Enrollment: Start mit RETICORA_ENROLL_TOKEN und RETICORA_CLOUD_URL, CSR; Server prüft das Einmal-Token (24 h) und signiert mit interner CA, CN = Collector-ID; danach nur mTLS-NATS. Zertifikat 90 Tage, automatische Erneuerung ab 60 % der Laufzeit über den bestehenden Kanal; CA-Roll-over mit Übergangsphase (zwei Trust-Anker); disabled → Zertifikat auf Widerrufsliste, NATS-Account gesperrt, Verbindungen getrennt.

COL-05 [B] Heartbeat 60 s; nach 5 Minuten ohne Heartbeat offline. Offline-Spool mit Limit, Alterung, Metriken, Flush-Tests; Flush in Original-Reihenfolge; Reconciliation nach observed_at (GLO-12).

COL-06 [B] Auto-Update nur mit signierten Artefakten (TEC-15), Kanäle stable/beta, Rollback bei Fehlstart; Auslieferung als Docker, OVA, Windows-Installer; mehrere Collectors pro Standort; Air-Gapped: Offline-Update-Bundle.

COL-07 [A] Agent-Relay [P4] und IGA-Connector-Host: Connectoren mit Schreibrechten laufen als separater Prozess mit eigenem Enrollment, eigenen Credentials und Least-Privilege-Konten.

COL-08 [B] Inbound-Ports der SaaS-Seite: 443 (HTTPS) und 4222 (NATS-TLS); Firewall-Dokumentation für Kunden (ausgehend zu festen Hostnamen).

20. Discovery

DIS-01 [B] Sweep: ICMP mit maximal 256 parallelen Probes, ARP, optionale Ports 22/80/443/623/5985/8006, SNMP UDP 161, Reverse-DNS.

DIS-02 [B] Klassifikation: Reihenfolge SNMP → Redfish → Banner; OUI-Tabelle eingebettet und aktualisierbar; YAML-Profile bilden auf Typ, Hersteller, Modell, Plugin und System-Template ab; kein Treffer ergibt generic_device plus Review unclassified_device. Profile Phase 1 (CH30, V): Cisco Catalyst 9200/9300, Cisco ISR/ASR, Cisco ASA/Firepower, Aruba CX 6xxx, Aruba/HPE ProCurve, Ubiquiti UniFi Switch/AP, MikroTik RouterOS, FortiGate, OPNsense/pfSense, Dell PowerEdge (iDRAC), HPE ProLiant (iLO), Lenovo ThinkSystem (XCC), Supermicro (BMC), VMware ESXi, Proxmox VE, Synology DSM, QNAP, TrueNAS, APC Smart-UPS/NMC, Eaton UPS, APC/Raritan PDU, Windows Server (WMI), Linux (SSH).

DIS-03 [B] Plugin-Interface: Name, Probe, Collect (liefert DeviceRecord).

DIS-04 [B] Protokolle wie v2 (SNMP v2c/v3, SSH-MVP, Redfish, IPMI/WMI Best Effort, NAS, Strom).

DIS-05 [B] DeviceRecord: record_id, observed_at, source, target_ip, vrf_id, scope_id, site_id, client_id (aus dem Scope), identity, classification, system, attributes, interfaces inkl. IPs, neighbors, power.

DIS-06 [P3] SNMP-Trap-Receiver; optional mDNS, SSDP, CDP, NetFlow, sFlow, Spanning Tree.

DIS-07 [P4] Connector-Framework (Intune, vCenter, Active Directory, Hersteller-APIs) mit Provenienz-Rang; IGA-Connectoren [A].

DIS-08 [Q] Fehlerzustände in UI, Metriken und Logs sichtbar.

DIS-09 [B] Discovery-Scopes: discovery_scope(org, client, site, collector, kind cidr|host|range, target, vrf_id, schedule (Cron oder Intervall), credential_ids, plugins, enabled, last_run_at, next_run_at, last_result); CRUD mit discovery:manage. Zero-Config (INS-06): nach dem Enrollment meldet der Collector seine lokalen Subnetze; sie erscheinen als Review scope_suggested und werden per Klick zu Scopes.

DIS-10 [B] Default-Intervalle: Sweep 24 h, SNMP/Redfish-Poll 15 min, Heartbeat 60 s; pro Scope und Org konfigurierbar (Minimum 5 min für Polls). REC-09 und TOP-02 beziehen sich auf das Scope-Intervall.

21. Ingest, Identity-Resolution & Reconciliation

REC-01 [B] Konsum von ingest.>, transaktional in WithTenant; Idempotenz über record_id für 48 h; Verarbeitung pro CI seriell (Ordering nach observed_at).

REC-02 [B] Identity-Resolution innerhalb desselben Clients (bzw. org-weit bei org-weitem Scope), in dieser Reihenfolge:

1. Seriennummer bei gleichem Typ oder generic_device; Blocklist für Platzhalter („0", „N/A", „To be filled by O.E.M.", „Default string", „System Serial Number").
2. hardware_uuid; Blocklist bekannter Klon-UUIDs (Null-UUID, 03000200-0400-0500-0006-000700080009); virtuelle Typen erfordern zusätzlich MAC- oder Hostname-Übereinstimmung.
3. eindeutige MAC-Schnittmenge; lokal administrierte MACs und VRRP/HSRP/CARP-Bereiche werden ignoriert; mehrere Treffer erzeugen Review ambiguous_identity.
4. IP + sysObjectID innerhalb derselben VRF.
5. Hostname/FQDN: nie Auto-Merge (CH29); Treffer erzeugt Review probable_duplicate; bis zur Entscheidung entsteht ein neues CI. Kein Treffer ergibt ein neues CI. Soft-gelöschte CIs werden gemäß CI-13 behandelt.

REC-03 [B] Ränge: manual/override 100, import 95, workflow 92, redfish 90, agent 85, snmp 80, api 75, integration 75, wmi 70, ssh/nas 60, sweep 20. import und workflow schreiben nie über Overrides (CH9).

REC-04 [P3] Quellpriorität pro Attribut konfigurierbar, ausschließlich für Automationsquellen; manual bleibt 100.

REC-05 [B] Überschreiben nur ohne Override und bei Rang ≥ Provenienz-Rang oder leerem Altwert; zusätzlich nur, wenn observed_at neuer als der gespeicherte Provenienz-Zeitstempel derselben oder höherrangigen Quelle ist (GLO-12). Provenienz in _provenance.

REC-06 [B] Konflikt mit niedrigerem Rang bei serial/uuid erzeugt Review conflicting_values.

REC-07 [B] last_seen_at wird immer gesetzt (ohne ci_change); unknown wird zu active; Interfaces/IPs werden nach 3 Fehlläufen entfernt.

REC-08 [B] Review-Items: unclassified_device, ambiguous_identity, conflicting_values, probable_duplicate, override_conflict, resurrected_device, unlicensed_ci, scope_suggested, serial_mismatch, duplicate_ip. Auflösung merge | new | dismiss | accept; Zuständigkeit review:resolve; Benachrichtigung bei Anlage (NTF-05); offene Items > 14 Tage werden im Dashboard eskaliert; Deduplizierung pro Objekt und Typ.

REC-09 [B] Offline: last_seen_at älter als 3× Scope-Intervall ergibt Status unknown und Event ci.offline; nur für observable CIs (LCY-05).

REC-10 [B] Erklärbarkeit pro Feld: Quelle, gemeldeter Wert, Zeitstempel, effektiver Wert, Auswahlgrund (Provenienz-Tooltip).

REC-11 [P4] Agent- und Discovery-Beobachtungen desselben Geräts ergeben ein CI.

REC-12 [B] Override-Konflikt (CH9): meldet eine Quelle einen Wert ≠ Override, wird _observed aktualisiert und ein Review override_conflict erzeugt (dedupliziert pro Feld); accept übernimmt den Wert und entfernt den Override; dismiss behält den Override und unterdrückt den Wert bis zur nächsten Änderung.

TOP-01 [B] LLDP → connected_to mit Confidence 1.0; FDB nur ohne LLDP mit 0.6; mehr als 4 MACs gelten als Trunk; ARP nur zur Anreicherung.

TOP-02 [B] Unbestätigte Discovery-Kanten werden nach max(7 Tage, 5× Scope-Intervall) entfernt, mit Event; Suppression wird geprüft.

22. Geschützte Overrides & Automationssicherheit

OVR-01 [B] Geschützter Override: beide Werte bleiben erhalten (_overrides, _observed); Autor, Zeit, Grund gespeichert; nach Entfernen gilt wieder der beobachtete Wert; stiller Verlust ist Critical.

OVR-02 [B] Automation darf nicht: manuelle Kanten, Instanzfelder oder Overrides löschen; Asset-Daten überschreiben; Duplikate erzeugen; Inventar anhand fremder Discovery-Daten bewegen; verifizierte Graph-Daten entfernen. Import und Workflows gelten als Automation (REC-03).

OVR-03 [P2] Template-Migrationen und Bulk-Änderungen respektieren Overrides; Bulk auf ein überschriebenes Feld erzeugt einen neuen Override (manueller Akt), auditiert.
