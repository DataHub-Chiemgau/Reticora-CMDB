KATALOGAUSZUG (Abschnitte 5, 6, 7, 8)
5. Mandanten, Scopes & RLS

TEN-01 [B] Hierarchie: (Reseller [P4]) → Organization → Client → Location-Baum (Abschnitt 10) → CI/Asset.

TEN-02 [B] Jede Mandantentabelle hat organization_id NOT NULL; unterhalb der Client-Ebene client_id (NULL = org-weit); Tabellen mit Standortbezug tragen denormalisiert site_id.

TEN-03 [B] DB-Rolle ohne Superuser, ohne BYPASSRLS; DDL-Recht nur auf den Anwendungsschemata (CH19).

TEN-04 [B] Pro Request setzt eine Transaktion: app.org_id (uuid), app.user_id, app.client_scope (uuid[] oder NULL = alle Clients der Org), app.site_scope (uuid[] oder NULL), app.team_scope (uuid[] oder NULL) (CH11, CH25).

TEN-05 [B] ENABLE und FORCE RLS auf allen Mandantentabellen:

- USING: organization_id = app.org_id AND (app.client_scope IS NULL OR client_id IS NULL OR client_id = ANY(app.client_scope)) AND (app.site_scope IS NULL OR site_id IS NULL OR site_id = ANY(app.site_scope)).
- WITH CHECK: dieselben Bedingungen für Org, Client und Site – nicht nur Org. Ein client-gescopter Nutzer kann keine org-weiten Zeilen (client_id NULL) anlegen oder ändern, sofern er nicht zusätzlich eine org-weite Rolle hat.
- Team-Scope: Tabellen mit owner_team_id filtern für team-gescopte Rollen zusätzlich owner_team_id = ANY(app.team_scope); Zeilen ohne Team bleiben sichtbar, aber nicht schreibbar.
- Tabellen ohne site_id (z. B. contact, subnet, vrf) unterliegen Org- und Client-Bedingung.

TEN-06 [B] Einziger DB-Einstieg ist WithTenant(ctx, orgID, scopes, fn). System-Jobs iterieren pro Mandant mit Scopes NULL. Eine falsche Org liefert 0 Zeilen; ein falscher Client-Scope liefert 0 Zeilen (Integrationstests).

TEN-07 [P4] Dedicated Schema oder Instance mit eigenem Schlüssel für KRITIS/Enterprise.

TEN-08 [P4] Reseller-Ebene: reseller(name, settings); organization.reseller_id. Reseller-Admins wechseln per expliziter Kontextwahl in eine Org (eigene Session, auditiert); keine Cross-Org-Abfragen.

TEN-09 [Q] Isolationstest über alle Zugriffspfade (wie v2) plus Cross-Client- und Cross-Site-Schreibpfade, Jobs, Benachrichtigungen, Review-Items, Locations. Jede Cross-Org- oder Cross-Client-Exposition ist Critical.

TEN-10 [B] VRF: vrf(org, name unique pro Org, description, is_default); Default global bei Org-Anlage (CH10); CRUD mit vrf:manage.

6. Authentifizierung

AUT-01 [B] OIDC Authorization Code + PKCE; Backend verifiziert das ID-Token und löst app_user über Org und E-Mail auf. Erstlogin nur mit gültiger Einladung (TLC-03), per Self-Signup (TLC-01) oder per SCIM-Provisionierung [P4].

AUT-02 [B] RS256-Session-JWT, 15 Minuten, Claims sub, org, scopes, cls[] (Client-Scope), sts[] (Site-Scope), tms[] (Team-Scope), name, email, Header kid. Rotierendes HttpOnly-Refresh-Cookie; Logout-Blacklist in Redis. Rollenänderungen wirken spätestens beim nächsten Refresh (≤ 15 min); Deaktivierung sofort über Blacklist.

AUT-03 [B] unverändert (Dev-Login nur außerhalb production).

AUT-04 [B] API-Keys: Format rk_live_/rk_test_ + 12 Zeichen Base62-Präfix + 40 Zeichen Base62-Secret; SHA-256-Hash, Vergleich in konstanter Zeit, Klartext nur bei Erstellung. Ein Key gehört einem Nutzer oder Service-Account (RBA-08); Rechte = Schnittmenge aus Inhaberrechten und Key-Scopes; optionales Ablaufdatum; Rotation mit Überlappung (SEC-06).

AUT-05, AUT-06 [B] unverändert.

AUT-07 [P4] SAML-SSO per Keycloak-Brokering pro Org (CH26).

AUT-08 [P4] SCIM 2.0 inbound und outbound.

AUT-09 [B] Keycloak-Topologie (CH26): ein Realm; Org-Zuordnung über Attribut; Identity-Brokering pro Org über die Reticora-Admin-API konfigurierbar; MFA für org_admin Pflicht, für weitere Rollen per Org-Policy; Passwortrichtlinie und Lockout in Keycloak.

AUT-10 [B] Pre-Auth-Rate-Limit pro IP für Login, Signup, Einladungsannahme und API-Key-Prüfung (Default 20/min); Lockout-Signal aus Keycloak wird angezeigt.

7. Autorisierung (RBAC, ABAC, feldbasiert)

RBA-01 [B] Permissions: ci:read/write/delete, citype:manage, location:read/write, rack:write, relationship:write, contact:write, topology:read, discovery:ingest, discovery:manage, collector:manage, credential:manage, vrf:manage, review:resolve, webhook:manage, export:run, job:read, notification:manage, user:manage, team:manage, role:manage, entitlement:manage, audit:read, apikey:manage. location:* ersetzt site:* aus v2.

RBA-02 [B] Systemrollen (vollständige Matrix; ✓ = enthalten, C = nur im Client-Scope):

| Permission | org_admin | engineer | viewer | client_technician |
|---|---|---|---|---|
| ci:read, location:read, topology:read | ✓ | ✓ | ✓ | C |
| ci:write, rack:write, relationship:write, contact:write | ✓ | ✓ | – | C |
| ci:delete, location:write | ✓ | ✓ | – | – |
| review:resolve | ✓ | ✓ | – | C |
| export:run, job:read | ✓ | ✓ | – | C |
| discovery:manage, collector:manage, credential:manage, vrf:manage | ✓ | ✓ | – | – |
| citype:manage, webhook:manage, notification:manage | ✓ | – | – | – |
| user:manage, team:manage, role:manage, entitlement:manage, audit:read, apikey:manage | ✓ | – | – | – |

RBA-03 [B] Custom-Rollen pro Org; role_assignment(role, scope_kind org|client|site|team, scope_id). Rechte = Vereinigung aller Rollen; Scopes = Vereinigung der Scope-Mengen je Ebene (CH11). Eine org-weite Rolle setzt den jeweiligen Scope auf NULL.

RBA-04 [B] Jede Route ist in authz.go gemappt (fail-closed); Prüfung serverseitig.

RBA-05 Permissions je Modul mit Phase: asset, assignment, stock, stocktake, reservation, order, document, contract, ticket [P2]; workflow, form, maintenance, compliance, finding [P3]; key, desk, training, agent, disposition [P4]; iga, ai [A].

RBA-06 [P2] Permissions für Lifecycle, Reconciliation-Einstellungen, Overrides, Instanzattribute und Beziehungstyp-Verwaltung.

RBA-07 [P2] Feldrechte auf Ebene Objekttyp, Objekt, Attribut, Relation, Aktion, Mandant, Standort und Team; pro Attribut und Rolle keine/lesen/bearbeiten; sensible Attribute separat schützbar. Kanal-Semantik:

- REST/GraphQL: nicht lesbare Felder fehlen im Payload; nicht editierbare Felder im PATCH ergeben 403 forbidden mit Feldliste.
- Suche: Treffer nur über lesbare Felder (tsvector pro Feld); keine Existenz-Leaks durch Volltext.
- Export: nur lesbare Felder; Export-Manifest listet ausgelassene Felder.
- Webhooks: Payload nach Rechten des Service-Accounts der Subscription.
- KI [A]: Chunk-Filter nach Feldrechten des fragenden Nutzers.
- Version/ETag (API-07): unabhängig von der Sichtbarkeit; der Client sendet den ETag unverändert zurück.

RBA-08 [B] team(org, name unique pro Org), team_member; service_account(org, name, role_assignments) für API-Keys und Webhook-Subscriptions; Service-Accounts sind Nutzern gleichgestellt in Audit und Scopes.

8. Entitlements

ENT-01 [B] entitlement(org, feature_key unique pro Org, enabled, limits jsonb, valid_until, source manual|selfsignup|billing|reseller).

ENT-02 [B] Feature-Keys Phase 1: cmdb_core (immer aktiv), discovery, topology, rack_view, export_csv, webhooks, api_access, notifications_email. Limits: max_cis, max_collectors, max_users, max_api_keys. export_datev → [P2].

ENT-03 [B] RequireFeature-Middleware plus Limitprüfung in REST-Schreibpfaden; Überschreitung ergibt 403 entitlement-limit. Ingest über max_cis (CH21): bestehende CIs werden weiter aktualisiert; neue CIs entstehen als Review-Item unlicensed_ci mit DeviceRecord-Snapshot; Metrik und Benachrichtigung an org_admin; kein stiller Verlust. Auflösung: Limit erhöhen → CIs werden angelegt; dismiss → verworfen, auditiert. Redis-Cache 60 s, Invalidierung bei Änderung.

ENT-04 [B] GET /entitlements; Schreiben über /admin/orgs/{id}/entitlements mit Operator-Identität (SEC-07).

ENT-05 [B] Durchsetzung in REST und GraphQL; Pläne pro Mandant isoliert; UI spiegelt den Plan.

ENT-06 Paketmatrix (V, zu bestätigen; Add-ons separat gemäß CH14):

| Feature-Key | Phase | Essential | Standard | Pro | Enterprise |
|---|---|---|---|---|---|
| cmdb_core, discovery, topology, rack_view, export_csv, webhooks, api_access, notifications_email | [B] | ✓ | ✓ | ✓ | ✓ |
| assets_basic (Asset, Zuweisung ohne Signatur), documents, import | [P2] | ✓ | ✓ | ✓ | ✓ |
| ticketing_essential | [P2] | ✓ | ✓ | ✓ | ✓ |
| ticketing_standard, signature, stock, stocktake_standard, barcode, gps_standard, workplace, export_datev | [P2] | – | ✓ | ✓ | ✓ |
| notifications_sms_pager, workflows, compliance_findings | [P3] | – | – | ✓ | ✓ |
| ticketing_pro, stocktake_pro, disposition, audit_extended, usage_tracking, internal_orders | [P2–P4] | – | – | ✓ | ✓ |
| monitoring (Polling/Alerting), reseller, rfid, direct_access_labels, gps_advanced, training, gis, compliance_automation, keys, endpoint_agent, dedicated_instance | [P3–P4] | – | – | – | ✓ |
| iga, ai | [A] | Add-on | Add-on | Add-on | Add-on |

Limits pro Paket (Default, V): max_cis 500 / 2.500 / 10.000 / 50.000; max_collectors 2 / 5 / 20 / unbegrenzt; max_users 5 / 25 / 100 / unbegrenzt.

ENT-07 [B] Ablauf (CH21): nach valid_until erhält der Collector den Zustand paused und scannt nicht mehr; Ingest wird serverseitig abgelehnt (Zähler, Log, kein Spool-Aufbau); Dashboard-Banner und Benachrichtigung 14/7/1 Tage vor Ablauf. Alle anderen Funktionen bleiben. Trial (Self-Signup, V): 30 Tage Standard-Funktionsumfang mit max_cis 100, danach Essential-Limits.

ENT-08 [B] Downgrade: keine Löschung von Daten; bei Überschreitung des neuen Limits nur Sperre für Neuanlage (REST) bzw. unlicensed_ci (Ingest).
