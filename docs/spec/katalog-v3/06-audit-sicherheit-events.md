KATALOGAUSZUG (Abschnitte 18, 31, 34)
18. Kontakte, Historie & Audit

AUD-01 [B] contact(client, name, phone, email, role, is_24_7, notes); Name, Telefon, E-Mail über PII-Vault (SEC-05).

AUD-02 [B] ci_contact mit contact_role operations, escalation, owner, vendor.

AUD-03 [B] ci_change(change_kind created|updated|deleted|restored|merged|type_changed|relationship_added|relationship_removed|status_changed, field_changes [{field, old, new}], actor_user_id (Surrogat), collector_id, source, request_id, correlation_id, observed_at). Nicht historisiert: last_seen_at, Heartbeats, Interface-Zähler, Metrikwerte. Alle Änderungen aus einem Ingest-Record ergeben genau eine ci_change und ein Event.

AUD-04 [B] audit_log mit Hash-Chain pro Org: erster prev_hash = 32 Null-Bytes; hash = SHA-256(prev_hash || SHA-256(canonical JSON)); Schreiben asynchron über Outbox-Tabelle in Commit-Reihenfolge (Sequenz pro Org) durch einen Single-Runner (JOB-02), Advisory-Lock nur im Runner; CLI audit-verify meldet die erste Verletzung; Einträge nicht editierbar; PII nur als Surrogat-ID (CH16).

AUD-05 [B] GET /audit-log mit Filtern; erfordert audit:read.

AUD-06 [P4] Verifikationsstatus im Admin-UI; erweitertes Audit-Log im Pro-Plan.

AUD-07 [B] Jede Änderung speichert Wer, Wann, Was, Alt, Neu, Quelle, Request- und Correlation-ID; Metamodell-Änderungen ab Phase 1 (Typen, Attributdefinitionen).

AUD-08 [P2] Zustand von CI/Asset inklusive Beziehungen zum Zeitpunkt T über Replay von ci_change/relationship_change ab dem letzten wöchentlichen ci_snapshot; Antwort blendet gelöschte PII aus (Vault).

AUD-09 [B] Retention: audit_log 10 Jahre (konfigurierbar, mindestens gesetzliche Frist), ci_change und Snapshots 3 Jahre, Events 30 Tage im Stream, Review-Items 1 Jahr nach Auflösung; Löschung per Job, selbst auditiert.

31. Sicherheit, Compliance & DSGVO

SEC-01 [B] TLS überall, Encryption-at-Rest, Envelope-Encryption, keine Secrets in UI oder Logs.

SEC-02 [P3] CVE-/Versionsabgleich; Findings pro CI mit Severity; angebunden an Tickets und Compliance.

SEC-03 [P3] compliance_rule und compliance_result (MET-45); Ergebnis pro CI, aggregierter Score.

SEC-04 [P3] Report-Generator ISO 27001, NIS2, KRITIS als PDF/CSV; Rechte-Doku aus AD/Entra ID [A].

SEC-05 PII-Vault: [B] pii_subject(org, surrogate_id, encrypted_payload, dek_ref) für Nutzer- und Kontaktdaten; Audit, ci_change, stock_movement, Assignments und Events referenzieren nur Surrogate; Anzeige löst Surrogate zur Laufzeit auf. [P3] Lösch- und Exportworkflows (Auskunft, Löschung mit Crypto-Shredding, Pseudonymisierung), EU-Residenz, AVV-fähig; historische Daten legen keine gelöschten PII offen (CH16).

SEC-06 [B] Schlüssel-Lifecycle: JWT-Signaturschlüssel Rotation alle 90 Tage mit kid und Überlappung; Master-Key-Rotation per DEK-Rewrap-Job ohne Downtime; Org-DEK-Rotation jährlich; Webhook-Secrets mit Überlappung (zwei gültige Secrets für 24 h); API-Key-Rotation; Collector-CA nach COL-04.

SEC-07 [B] Operator-Zugriff: personalisierte Operator-Identitäten (Keycloak-Gruppe operators, MFA Pflicht) für /admin/*; RETICORA_OPERATOR_TOKEN nur für Bootstrap und Break-Glass, jede Nutzung alarmiert; jede Operator-Aktion in einem org-übergreifenden operator_audit mit Hash-Chain.

SEC-08 [B] Egress-Schutz: Webhook-, Import- und Connector-URLs werden gegen private, link-local und Cloud-Metadata-Bereiche geblockt; DNS-Rebinding-Schutz; maximal 3 Redirects; Timeouts.

SEC-09 [P2] Uploads: MIME-Whitelist, Magic-Bytes-Prüfung, Größenlimit (Default 50 MB), Malware-Scan (ClamAV), Quarantäne bei Befund.

SEC-10 [B] GraphQL-Limits: Tiefe 10, Komplexitätsbudget pro Query, Timeout 10 s, keine Introspection in production ohne Session.

SEC-11 [Q] OWASP ASVS Level 2 als Prüfrahmen; Abhängigkeits- und Container-Scans in der CI; Pentest vor G1 und GA; CVE-Fix-SLA Critical 7 Tage, High 30 Tage.

34. Events & Webhooks

EVT-01 [B] NATS-Stream RETICORA_EVENTS, Subjects events.<org_id>.<type>; Envelope id, type, org_id, occurred_at, sequence (pro Org), entity_id, entity_seq (pro Entität), data; at-least-once, idempotente Konsumenten; Konsumenten verarbeiten Events derselben Entität in entity_seq-Reihenfolge. Retention 30 Tage / 10 GB pro Stream (konfigurierbar).

EVT-02 [B] Events: ci.created, ci.changed, ci.deleted, ci.restored, ci.merged, ci.offline, ci.online, relationship.created, relationship.removed, collector.enrolled, collector.offline, review.created, review.resolved, export.completed, job.failed, entitlement.warning.

EVT-03 [P2–P3] Zusätzlich: ci.status_changed, lifecycle.changed, relationship.changed, asset.*, stock.movement, reservation.*, ticket.updated, maintenance.scheduled, workflow.*, metamodel.published, finding.*.

WHK-01 [B] Webhooks: Subscriptions (Service-Account, Event-Filter, einmal angezeigtes Secret, Rotation nach SEC-06); Delivery-Status pending, delivering, delivered, failed, dead; Backoff 30 s, 2 min, 10 min, 60 min, 360 min, danach dead; nach 3 dead innerhalb 24 h wird die Subscription deaktiviert und der Owner benachrichtigt; Header X-Reticora-Signature (HMAC-SHA256), X-Reticora-Event, X-Reticora-Delivery; Delivery-Log, Retry, Test-Send, Isolation; Ziel-URLs nach SEC-08.
