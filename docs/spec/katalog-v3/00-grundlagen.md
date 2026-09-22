## GRUNDLAGEN

### Legende

| Tag | Bedeutung | Gate | Bewertung bei Fehlen |
|---|---|---|---|
| [B] | Phase 1 – Baseline, eigenständig auslieferbares Release (CH13) | G1 | FAIL G1 |
| [P2] … [P5] | Phase 2–5, verpflichtend (CH2) | G2 … G5 | FAIL des Phasen-Gates |
| [A] | Add-on (IGA, KI); eigenes Entitlement, eigenes Gate (CH14) | GA | FAIL Add-on-Gate |
| [O] | Optional/Backlog; kein Gate | – | Hinweis |
| [Q] | Qualität, Prozess, Betrieb; gilt ab der genannten Phase für jedes Gate | – | FAIL |

### Entscheidungen

CH8 (S) Betriebsmodell: SaaS (EU-Hosting) mit optionalem On-Prem-Collector ist das Referenzmodell. Dedicated Instance bleibt Enterprise-Option. Air-Gapped-On-Prem ist ein optionales Deployment-Profil [O] mit eingeschränktem Funktionsumfang (INS-08). Kein Aufwand für Air-Gapped darf Phase-1-Gates gefährden.

CH9 (S) Manuell gewinnt: Manuelle Werte und Overrides haben in jeder Prioritätskette Rang 100. Meldet eine Automationsquelle einen abweichenden Wert, entsteht ein Review-Item override_conflict; Übernahme nur manuell.

CH10 (S) VRFs werden auf Org-Ebene verwaltet. Subnetz-Eindeutigkeit gilt pro (org, vrf, cidr). Jede Org erhält bei Anlage die Default-VRF global.

CH11 (S) Ein Nutzer kann für mehrere Clients und Sites berechtigt sein. Scopes sind mengenwertig und werden in RLS erzwungen.

CH12 (S) Das Asset ist die Hoheit für Seriennummer, Standort und physischen Lifecycle. CI-Felder sind beobachtete Werte; bei Verknüpfung mit einem Asset werden sie read-only aus dem Asset abgeleitet (DB-05).

CH13 (S) Phase 1 ist ein eigenständig auslieferbares Release. Jede Phase hat ein eigenes Gate.

CH14 (S) IGA und KI sind Add-ons [A] mit eigenem Entitlement und Gate; sie sind kein Bestandteil der Pakete Essential–Enterprise, sondern zubuchbar.

CH15 (S) Skalierungsziel: 1.000–10.000 Objekte pro Kunde. Auslegung auf 10.000, Headroom 50.000 (NFR-01). Hinweis: Die Antwort lautete „1000-1000"; die Interpretation als 1.000–10.000 ist zu bestätigen.

CH16 (V) DSGVO vs. Audit: Audit-Log und Historie enthalten keine Klartext-PII, sondern Surrogat-IDs aus dem PII-Vault. Löschung erfolgt durch Löschen des Vault-Eintrags (Crypto-Shredding); die Hash-Kette bleibt intakt. Vault-Basis bereits in Phase 1 (SEC-05).

CH17 (S) Fremde Ticketsysteme: Eine Ticket-Provider-Schnittstelle wird vorgesehen (TKT-03); konkrete Integrationen sind [O] ohne Priorität.

CH18 (S) Regeln, Bedingungen und Abfragen werden als JSON-AST gespeichert und im UI per Klick-Builder gepflegt; eine Text-DSL ist nicht erforderlich. Eine JSON-Regelsprache für Metamodell, Workflows, Compliance und Suche (MET-45).

CH19 (S) Die Anwendung darf zur Laufzeit Indizes anlegen. Die App-Rolle erhält DDL-Rechte auf den Anwendungsschemata; kein Superuser, kein BYPASSRLS (DB-04).

CH20 (S) Installierte Software wird als Inventartabelle geführt, nicht als CI pro Installation. Der CI-Typ software bleibt für Software-Produkte/Katalogeinträge.

CH21 (S) Lizenzablauf: Nach valid_until stoppt ausschließlich Discovery/Ingest; Lesen, Bearbeiten, Export und alle anderen Funktionen bleiben verfügbar. (V, analog) Limitüberschreitung max_cis: Discovery-CIs über dem Limit werden nicht verworfen, sondern als Review-Item unlicensed_ci gehalten.

CH22 (S) install.sh unterstützt Ubuntu 22.04/24.04 LTS und Debian 12.

CH23 (S) Organisationen entstehen per Self-Signup (E-Mail-Verifikation, Trial-Plan) sowie durch Operator und Reseller.

CH24 (S) Benachrichtigungskanäle: E-Mail (Phase 1), SMS und Pager (Phase 3) über eine Provider-Abstraktion.

CH25 (S) Site- und Team-Scopes werden DB-seitig (RLS) erzwungen.

CH26 (V) Keycloak: ein Realm pro Instanz; Org-Zuordnung über Nutzerattribut; Kunden-IdPs (OIDC/SAML) per Identity-Brokering pro Org. MFA (TOTP/WebAuthn) in Keycloak; für org_admin und Operatoren verpflichtend.

CH27 (V) Beziehungstypen tragen eine explizite Impact-Semantik (impact_direction). Strukturbeziehungen (mounted_in, located_in, contains) werden aus Strukturtabellen projiziert, nicht gespeichert.

CH28 (V) Site, Building, Room, Rack, Warehouse, Zone, Shelf und Bin sind Knoten eines gemeinsamen Location-Baums (LOC-10).

CH29 (V) Kein Auto-Merge über Hostname allein; Hostname-Treffer erzeugen ein Review-Item.

CH30 (V) Die Top-20-Geräteprofile sind in DIS-02 konkret gelistet; Liste zu bestätigen.

### Glossar

- Organisation (Org): Mandant; Vertrags- und Isolationsgrenze.
- Reseller: übergeordnete Ebene, die mehrere Orgs verwaltet (Phase 4).
- Client: Endkunde oder Geschäftsbereich innerhalb einer Org; Scope-Ebene für Rechte und RLS. client_id NULL = org-weit.
- Location: jeder Knoten des Location-Baums (Site, Building, Room, Rack, Warehouse, Zone, Shelf, Bin). Einsatzort = Location im Betriebszweig (Site…Rack). Lagerort = Location im Lagerzweig (Warehouse…Bin).
- CI: technisches oder logisches Konfigurationsobjekt; entsteht meist durch Discovery.
- Asset: kaufmännisches physisches Objekt (Inventarnummer, Kaufdaten, Owner, Lifecycle, Standort); trägt 0..n CIs.
- Status (ci.status): operativer Zustand. Lifecycle: kaufmännischer/organisatorischer Zustand (am Asset bzw. am Software-CI). Health: aus Monitoring und Findings berechneter Zustand.
- Owner: verantwortliche Person oder Team. Team: Nutzergruppe innerhalb einer Org.
- Override: manuell gesetzter Wert, der einen beobachteten Wert übersteuert. Provenienz: Quelle, Rang und Zeitpunkt eines Wertes.
- Review-Item: offene Entscheidung für Menschen (Identität, Konflikt, Lizenz, Scope).
- Service-Account: nicht-personenbezogene Identität für API-Keys und Webhook-Subscriptions.
- Scope: Menge von Clients, Sites oder Teams, auf die eine Rollenzuweisung wirkt.

### 1. Leitprinzipien

PRI-01 bis PRI-06, PRI-08, PRI-09 unverändert aus v2.

PRI-07 Hybrid-Konnektivität: Collector und Agent kommunizieren ausschließlich ausgehend (HTTPS 443, NATS-mTLS 4222) zu festen Hostnamen; die SaaS-Seite öffnet keine Verbindung in Kundennetze.

PRI-10 Invarianten:
- Kein Gerät wird zu mehreren CIs; kein Auto-Merge ohne belastbaren Identitätsbeweis (REC-02).
- Asset und CI sind getrennt, aber komponierbar; das Asset ist Hoheit für kaufmännische Daten, Serial, Standort und physischen Lifecycle (CH12).
- Jede Information hat genau einen kanonischen Speicherort (DB-05); alle anderen Vorkommen sind abgeleitet und read-only.
- Manuelle Werte werden nie automatisch überschrieben (CH9); Automation zerstört nie still manuelle Daten.
- Automation verwirft keine Daten still; unklare Fälle werden zu Review-Items (REC-08).
- Bewegungen und Änderungen werden historisiert; Zeitbezug ist observed_at der Quelle (GLO-12).
- Isolation wird server- und DB-seitig erzwungen, einschließlich Client-, Site- und Team-Scope (CH11, CH25).
- Entitlements werden serverseitig durchgesetzt.
- Erweiterungen ergänzen die Architektur, sie ersetzen sie nicht.

PRI-11 [B] Betriebsmodelle (CH8): (1) SaaS EU mit On-Prem-Collector – Referenzmodell; (2) Dedicated Instance (TEN-07); (3) Air-Gapped-On-Prem [O]. Alle Anforderungen gelten für alle Modelle, sofern nicht als „nur SaaS" oder „nur Air-Gapped" markiert.

### 2. Globale Regeln

GLO-01 bis GLO-09 unverändert.

GLO-10 [B] Enum-Werte, Status-, Lifecycle- und Review-Schlüssel sind englisch (snake_case); Anzeige ausschließlich über i18n.

GLO-11 [Q] Jede Anforderung ist auf ein Epic und mindestens eine Testart rückführbar (Abschnitt 46); neue Anforderungen erhalten ID, Tag und Epic vor der Umsetzung.

GLO-12 [B] Zeitvergleiche in Ingest, Reconciliation und Historie basieren auf observed_at der Quelle, nicht auf der Ankunftszeit; Records mit observed_at mehr als 5 Minuten in der Zukunft werden abgelehnt und gezählt.

GLO-13 [B] Reservierte Namensräume (_overrides, _observed, _provenance, _instance) sind als Attributschlüssel unzulässig (CI-10).
