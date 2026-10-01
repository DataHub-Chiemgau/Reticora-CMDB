KATALOGAUSZUG (Abschnitte 23, 24, 25, 26, 27, 29, 30, 32, 33)
23. Asset-Modul

AST-01 [P2] asset(org, client, location_id, serial_number, inventory_no unique pro Org, barcode unique pro Org, rfid_tag unique pro Org, manufacturer, model, purchase_date, purchase_price, currency (Default Org), warranty_until, cost_center, supplier_id, contract_id, owner_user_id, owner_team_id, lifecycle_state, primary_ci_id, version); kanonische Felder gemäß DB-05.

AST-02 [P2] Zuweisung, Rückgabe, Transfer an Nutzer/Team mit Fälligkeit; digitale Unterschrift ab Paket Standard; Historie ist eine Sicht auf stock_movement (AST-07).

AST-03 [P2] CI ohne Asset, Asset ohne CI, Verknüpfung beider; Discovery aktualisiert nur das CI; keine doppelten physischen Objekte.

AST-04 [P2] Parent-Asset mit n CIs über ci.asset_id; primary_ci_id optional; Parent-Daten erscheinen im CI read-only. Verknüpfung per UI/API oder automatisch bei eindeutigem Serial-Treffer (auditiert, Review serial_mismatch bei Abweichung). Der Fall 1:1 bleibt einfach; keine Zusammenlegung von CI und Asset.

AST-05 [P2] Physische Komposition über ci.parent_ci_id (Chassis → Blades, UPS → Module); child_mode configuration|standalone; standalone-Kinder können eigenes Asset, Serial, Zuweisung und Lifecycle haben; keine rekursive Eigentümerschaft (Zyklus-Check).

AST-06 [P2] Reservierung serieller Assets: reservieren, freigeben, Ablauf, Umwandlung in Zuweisung; konkurrenzsicher über Zeilensperre (ABN-03).

AST-07 [P2] Es gibt genau eine Bewegungs- und Zuweisungshistorie: stock_movement (STK-04). Assignment-Ansichten sind Projektionen daraus.

24. Lager-Modul

STK-01 [P2] consumable und stock_movement: Bestand, Zu-/Abgang, Mindestbestand mit Benachrichtigung (NTF-05).

STK-02 [P2] Lagerhierarchie (LOC-08/10) in stock_movement und Bestand pro Bin.

STK-03 [P2] Mengeninventar: Eingang, Entnahme, Korrektur, Transfer, Reservierung, Verfügbarkeit; Mengenartikel werden nie zu CIs; kein negativer Bestand; keine Überreservierung (CHECK + Sperre).

STK-04 [P2] stock_movement ist die einheitliche, unveränderliche Bewegungshistorie für serielle Assets und Mengenartikel: Arten receipt, transfer, assignment, return, deployment, retrieval, reservation, release, repair, disposal, correction; erfasst Quelle, Ziel (Location), Objekt, Menge, Nutzer (Surrogat), Zeit, Grund, Bezug (Ticket/Bestellung/Workflow).

STK-05 [P2] Internes Bestellsystem (internal_order) mit Genehmigung; Wareneingang bucht in stock_movement.

25. Stocktake-Modul

INV-01 [P2] stocktake und stock_scan: Soll-Snapshot, Scan per Barcode/RFID, Kategorien found/missing/unexpected, Differenzliste, Abschluss mit Bestandskorrektur über stock_movement, unveränderliches Ergebnis.

INV-02 [P2] Inventur Standard; [P4] Inventur Pro und delegierte Inventur; Lager-Bins als Inventurbereich.

INV-03 [P2] Barcode: Zuordnung, Dublettenschutz, Kamera-Scan, Lookup; Etikettendruck (Code128/QR, PDF-Vorlagen); [P4] RFID und Direct-Access-Etiketten.

26. Weitere Management-Module

MGT-01 [P2] Dokumente (document, document_link): polymorph für CI, Asset, Zuweisung, Vertrag, Entsorgung, Ticket, Location; S3 mit Presigned URLs; Sicherheit nach SEC-09; Versionen, Audit, Isolation; Speicherlimit pro Org (Entitlement).

MGT-02 [P2] Revisionssichere Entsorgungsdoku (disposal_record) nach BSI/ISO.

MGT-03 [P4] Schlüsselmanagement.

MGT-04 [P3] Wartungsfenster mit Benachrichtigung betroffener Kunden über den Graphen (IMP-05, NTF).

MGT-05 [P4] Desk-Booking.

MGT-06 [P4] Arbeitsplatzverwaltung.

MGT-07 [P4] Schulungsverwaltung.

MGT-08 [P4] Disposition.

MGT-09 [P4] Leistungs-/Verbrauchstracking.

MGT-10 [P5] Servicekosten-Analyse.

MGT-11 [P5] Cloud-Inventar.

MGT-12 [P2] contract(org, client, supplier_id, number, kind, start, end, notice_period, value, currency, documents); Referenzziel für Attribute (MET-12) und Assets (AST-01); Ablaufbenachrichtigung.

27. Tickets

TKT-01 [P2] Ticketing Essential und Standard; [P4] Pro. Status, Priorität, Assignee, CI-/Asset-Bezug, Kommentare, SLA; Tickets aus Reviews, Monitoring [P3], Findings [P3] und Automationen [P3].

TKT-02 [P2] SLA mit Business-Calendar (Arbeitszeiten, Feiertage, Wochenenden) in der Zeitzone von Client oder Org (LOC-01/02).

TKT-03 [O] Ticket-Provider-Schnittstelle (CH17): Interface create, update, comment, link, status_sync; das interne Modul ist der Referenz-Provider; alle Objekte tragen generische external_ref; Integrationen (Jira, ServiceNow) ohne Priorität.

29. Endpoint-Agent

AGT-01 [P4] Vollständige Implementierung; Windows, Linux (systemd), macOS (launchd); Single Binary.

AGT-02 [P4] Hardware-Inventar, CPU/RAM/Disk/Netz, Patch-Stand, Dienste/Prozesse; installierte Software in installed_software(ci, name, version, vendor, install_date, source) (CH20); Remote-Actions nur mit Sicherheitsfreigabe.

AGT-03 [P4] Ausgehend über mTLS, per Relay über den Collector oder direkt.

AGT-04 [P4] Signierte Pakete (MSI/DEB/RPM/PKG), signierter Self-Update-Kanal, Heartbeat 5 min (offline nach 30 min), Policies, Kill-Switch.

AGT-05 [P4] Reconciliation-Quelle mit Rang 85; keine Duplikate (REC-11).

AGT-06 [P4] Enrollment-Token pro Client und Site; wandernde Geräte (Laptops) behalten Client, Site wird aus Netz-Fingerprint vorgeschlagen und manuell bestätigt.

30. Workflows, Formulare &amp; Automationen

WFL-01 [P3] form_def versioniert, JSON-Schema-basiert, bedingte Logik nach MET-45; form_submission.

WFL-02 [P3] workflow_def: Trigger Event/manuell/Zeitplan (JOB-02); Bedingungen und Aktionen (Ticket, Webhook, Benachrichtigung, Feld setzen, Genehmigung); workflow_run idempotent mit Retries über den Event-Bus; Schreibaktionen tragen Provenienz workflow mit Rang 92 und respektieren Overrides; Ausführung pro CI seriell (EVT-01).

WFL-03 [P3] Automation-Regeln für CMDB-Objekte (Beispiele wie v2).

WFL-04 [P3] Anwendungsfälle: Übergabe mit Unterschrift, delegierte Inventur [P4], Bestellgenehmigung, Wartungsbenachrichtigung, IGA-Genehmigungen [A].

32. IGA [A]

IGA-01 bis IGA-04 wie v2 (vollständige Implementierung, Connector-Framework, JML, Access-Reviews). Eigenes Entitlement iga, eigenes Gate GA; Connector-Host nach COL-07; Remediation nur mit Vier-Augen-Freigabe.

33. KI [A]

AI-01 LLM über Provider-API mit RAG auf mandantengescopten Daten; Berechtigungsfilter auf Chunks inklusive Feldrechte (RBA-07); Zitierpflicht; keine mandantenübergreifenden Daten.

AI-02 Provider mit EU-Verarbeitung oder Self-Hosting; kein Training auf Kundendaten; Opt-in pro Org; PII-Surrogate werden vor dem Prompt nicht aufgelöst; im Air-Gapped-Profil deaktiviert.
