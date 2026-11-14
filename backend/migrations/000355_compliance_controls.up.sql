-- Migration 000355: control catalog ISO/IEC 27001:2022, NIS2 and KRITIS and
-- the assignment of compliance rules to controls (SEC-04, WP-1128, E-238
-- option 1).
--
-- compliance_control is a global catalog without organization_id (E-10):
-- maintained by migrations only, the application reads it (no row level
-- security, SELECT only, rls.GlobalCatalogTables). It holds the identifier
-- of each control with a short title of our own in German and English, no
-- normative text: ISO/IEC 27001:2022 Annex A 5.1 to 8.34 (93 controls), NIS2
-- Art. 21 para. 2 letters a to j (10), KRITIS § 8a para. 1, 1a and 3 BSIG (3).
-- compliance_rule_control assigns the rules of an organization to controls;
-- the composite foreign key carries the organization (Konvention 19).

CREATE TABLE compliance_control (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    standard   text NOT NULL CONSTRAINT compliance_control_standard_check CHECK (standard IN ('iso27001', 'nis2', 'kritis')),
    control_id text NOT NULL CONSTRAINT compliance_control_control_id_check CHECK (length(control_id) BETWEEN 1 AND 32),
    title_de   text NOT NULL CONSTRAINT compliance_control_title_de_check CHECK (length(title_de) BETWEEN 1 AND 120),
    title_en   text NOT NULL CONSTRAINT compliance_control_title_en_check CHECK (length(title_en) BETWEEN 1 AND 120),
    sort_order integer NOT NULL DEFAULT 0,
    CONSTRAINT compliance_control_standard_control_key UNIQUE (standard, control_id)
);
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON compliance_control FROM reticora_app;
GRANT SELECT ON compliance_control TO reticora_app;

INSERT INTO compliance_control (standard, control_id, title_de, title_en, sort_order) VALUES
    ('iso27001', '5.1', 'Richtlinien zur Informationssicherheit', 'Information security policies', 10),
    ('iso27001', '5.2', 'Rollen und Verantwortlichkeiten', 'Security roles and responsibilities', 20),
    ('iso27001', '5.3', 'Aufgabentrennung', 'Separation of duties', 30),
    ('iso27001', '5.4', 'Pflichten der Leitung', 'Management duties', 40),
    ('iso27001', '5.5', 'Kontakt zu Behörden', 'Contact with authorities', 50),
    ('iso27001', '5.6', 'Kontakt zu Interessengruppen', 'Contact with interest groups', 60),
    ('iso27001', '5.7', 'Bedrohungsinformationen', 'Threat intelligence', 70),
    ('iso27001', '5.8', 'Sicherheit im Projektmanagement', 'Security in project management', 80),
    ('iso27001', '5.9', 'Inventar von Informationen und Werten', 'Inventory of information and assets', 90),
    ('iso27001', '5.10', 'Zulässige Nutzung von Werten', 'Acceptable use of assets', 100),
    ('iso27001', '5.11', 'Rückgabe von Werten', 'Return of assets', 110),
    ('iso27001', '5.12', 'Klassifizierung von Informationen', 'Classification of information', 120),
    ('iso27001', '5.13', 'Kennzeichnung von Informationen', 'Labelling of information', 130),
    ('iso27001', '5.14', 'Übertragung von Informationen', 'Information transfer', 140),
    ('iso27001', '5.15', 'Zugangssteuerung', 'Access control', 150),
    ('iso27001', '5.16', 'Identitätsverwaltung', 'Identity management', 160),
    ('iso27001', '5.17', 'Authentisierungsinformationen', 'Authentication information', 170),
    ('iso27001', '5.18', 'Zugangsrechte', 'Access rights', 180),
    ('iso27001', '5.19', 'Lieferantenbeziehungen', 'Supplier relationships', 190),
    ('iso27001', '5.20', 'Sicherheit in Lieferantenvereinbarungen', 'Security in supplier agreements', 200),
    ('iso27001', '5.21', 'IKT-Lieferkette', 'ICT supply chain', 210),
    ('iso27001', '5.22', 'Überwachung von Lieferantendiensten', 'Monitoring of supplier services', 220),
    ('iso27001', '5.23', 'Nutzung von Cloud-Diensten', 'Use of cloud services', 230),
    ('iso27001', '5.24', 'Planung des Vorfallmanagements', 'Incident management planning', 240),
    ('iso27001', '5.25', 'Bewertung von Sicherheitsereignissen', 'Assessment of security events', 250),
    ('iso27001', '5.26', 'Reaktion auf Vorfälle', 'Response to incidents', 260),
    ('iso27001', '5.27', 'Lernen aus Vorfällen', 'Learning from incidents', 270),
    ('iso27001', '5.28', 'Sammlung von Beweismaterial', 'Collection of evidence', 280),
    ('iso27001', '5.29', 'Sicherheit bei Störungen', 'Security during disruption', 290),
    ('iso27001', '5.30', 'IKT-Bereitschaft für Kontinuität', 'ICT readiness for continuity', 300),
    ('iso27001', '5.31', 'Rechtliche und vertragliche Anforderungen', 'Legal and contractual requirements', 310),
    ('iso27001', '5.32', 'Geistiges Eigentum', 'Intellectual property', 320),
    ('iso27001', '5.33', 'Schutz von Aufzeichnungen', 'Protection of records', 330),
    ('iso27001', '5.34', 'Datenschutz und personenbezogene Daten', 'Privacy and personal data', 340),
    ('iso27001', '5.35', 'Unabhängige Überprüfung der Sicherheit', 'Independent review of security', 350),
    ('iso27001', '5.36', 'Einhaltung von Richtlinien und Regeln', 'Compliance with policies and rules', 360),
    ('iso27001', '5.37', 'Dokumentierte Betriebsabläufe', 'Documented operating procedures', 370),
    ('iso27001', '6.1', 'Überprüfung von Personal', 'Screening', 380),
    ('iso27001', '6.2', 'Beschäftigungsbedingungen', 'Terms of employment', 390),
    ('iso27001', '6.3', 'Sensibilisierung und Schulung', 'Awareness and training', 400),
    ('iso27001', '6.4', 'Maßregelungsprozess', 'Disciplinary process', 410),
    ('iso27001', '6.5', 'Pflichten nach Beschäftigungsende', 'Duties after employment ends', 420),
    ('iso27001', '6.6', 'Vertraulichkeitsvereinbarungen', 'Confidentiality agreements', 430),
    ('iso27001', '6.7', 'Telearbeit', 'Remote working', 440),
    ('iso27001', '6.8', 'Meldung von Sicherheitsereignissen', 'Reporting of security events', 450),
    ('iso27001', '7.1', 'Physische Sicherheitszonen', 'Physical security perimeters', 460),
    ('iso27001', '7.2', 'Physischer Zutritt', 'Physical entry', 470),
    ('iso27001', '7.3', 'Sicherung von Büros und Einrichtungen', 'Securing offices and facilities', 480),
    ('iso27001', '7.4', 'Physische Sicherheitsüberwachung', 'Physical security monitoring', 490),
    ('iso27001', '7.5', 'Physische und umweltbedingte Bedrohungen', 'Physical and environmental threats', 500),
    ('iso27001', '7.6', 'Arbeiten in Sicherheitsbereichen', 'Working in secure areas', 510),
    ('iso27001', '7.7', 'Aufgeräumter Arbeitsplatz und Bildschirmsperre', 'Clear desk and clear screen', 520),
    ('iso27001', '7.8', 'Platzierung von Geräten', 'Siting of equipment', 530),
    ('iso27001', '7.9', 'Werte außerhalb der Räumlichkeiten', 'Assets off-premises', 540),
    ('iso27001', '7.10', 'Speichermedien', 'Storage media', 550),
    ('iso27001', '7.11', 'Versorgungseinrichtungen', 'Supporting utilities', 560),
    ('iso27001', '7.12', 'Sicherheit der Verkabelung', 'Cabling security', 570),
    ('iso27001', '7.13', 'Instandhaltung von Geräten', 'Equipment maintenance', 580),
    ('iso27001', '7.14', 'Sichere Entsorgung oder Wiederverwendung', 'Secure disposal or reuse', 590),
    ('iso27001', '8.1', 'Endgeräte der Benutzer', 'User endpoint devices', 600),
    ('iso27001', '8.2', 'Privilegierte Zugangsrechte', 'Privileged access rights', 610),
    ('iso27001', '8.3', 'Beschränkung des Informationszugangs', 'Restriction of information access', 620),
    ('iso27001', '8.4', 'Zugriff auf Quellcode', 'Access to source code', 630),
    ('iso27001', '8.5', 'Sichere Authentisierung', 'Secure authentication', 640),
    ('iso27001', '8.6', 'Kapazitätssteuerung', 'Capacity management', 650),
    ('iso27001', '8.7', 'Schutz vor Schadsoftware', 'Protection against malware', 660),
    ('iso27001', '8.8', 'Technische Schwachstellen', 'Technical vulnerabilities', 670),
    ('iso27001', '8.9', 'Konfigurationsmanagement', 'Configuration management', 680),
    ('iso27001', '8.10', 'Löschung von Informationen', 'Deletion of information', 690),
    ('iso27001', '8.11', 'Datenmaskierung', 'Data masking', 700),
    ('iso27001', '8.12', 'Verhinderung von Datenabfluss', 'Data leakage prevention', 710),
    ('iso27001', '8.13', 'Datensicherung', 'Backup of information', 720),
    ('iso27001', '8.14', 'Redundanz von Einrichtungen', 'Redundancy of facilities', 730),
    ('iso27001', '8.15', 'Protokollierung', 'Logging', 740),
    ('iso27001', '8.16', 'Überwachungsaktivitäten', 'Monitoring activities', 750),
    ('iso27001', '8.17', 'Uhrensynchronisation', 'Clock synchronization', 760),
    ('iso27001', '8.18', 'Privilegierte Hilfsprogramme', 'Privileged utility programs', 770),
    ('iso27001', '8.19', 'Installation von Software', 'Installation of software', 780),
    ('iso27001', '8.20', 'Netzwerksicherheit', 'Network security', 790),
    ('iso27001', '8.21', 'Sicherheit von Netzwerkdiensten', 'Security of network services', 800),
    ('iso27001', '8.22', 'Trennung von Netzwerken', 'Segregation of networks', 810),
    ('iso27001', '8.23', 'Webfilterung', 'Web filtering', 820),
    ('iso27001', '8.24', 'Einsatz von Kryptografie', 'Use of cryptography', 830),
    ('iso27001', '8.25', 'Sicherer Entwicklungslebenszyklus', 'Secure development life cycle', 840),
    ('iso27001', '8.26', 'Sicherheitsanforderungen an Anwendungen', 'Application security requirements', 850),
    ('iso27001', '8.27', 'Sichere Systemarchitektur', 'Secure system architecture', 860),
    ('iso27001', '8.28', 'Sichere Programmierung', 'Secure coding', 870),
    ('iso27001', '8.29', 'Sicherheitsprüfungen', 'Security testing', 880),
    ('iso27001', '8.30', 'Ausgelagerte Entwicklung', 'Outsourced development', 890),
    ('iso27001', '8.31', 'Getrennte Umgebungen', 'Separated environments', 900),
    ('iso27001', '8.32', 'Änderungssteuerung', 'Change management', 910),
    ('iso27001', '8.33', 'Testinformationen', 'Test information', 920),
    ('iso27001', '8.34', 'Schutz bei Audits', 'Protection during audits', 930),
    ('nis2', '21.2.a', 'Risikoanalyse und Sicherheitskonzepte', 'Risk analysis and security policies', 10),
    ('nis2', '21.2.b', 'Bewältigung von Sicherheitsvorfällen', 'Handling of security incidents', 20),
    ('nis2', '21.2.c', 'Betriebskontinuität und Krisenmanagement', 'Business continuity and crisis management', 30),
    ('nis2', '21.2.d', 'Sicherheit der Lieferkette', 'Supply chain security', 40),
    ('nis2', '21.2.e', 'Sicherheit bei Erwerb und Entwicklung', 'Security in acquisition and development', 50),
    ('nis2', '21.2.f', 'Bewertung der Wirksamkeit von Maßnahmen', 'Assessing the effectiveness of measures', 60),
    ('nis2', '21.2.g', 'Cyberhygiene und Schulungen', 'Cyber hygiene and training', 70),
    ('nis2', '21.2.h', 'Kryptografie und Verschlüsselung', 'Cryptography and encryption', 80),
    ('nis2', '21.2.i', 'Personalsicherheit, Zugriffskontrolle und Anlagen', 'Personnel security, access control and assets', 90),
    ('nis2', '21.2.j', 'Mehrfaktor-Authentisierung und gesicherte Kommunikation', 'Multi-factor authentication and secure communication', 100),
    ('kritis', '8a.1', 'Angemessene technische und organisatorische Vorkehrungen', 'Appropriate technical and organizational precautions', 10),
    ('kritis', '8a.1a', 'Systeme zur Angriffserkennung', 'Systems for attack detection', 20),
    ('kritis', '8a.3', 'Nachweis der Erfüllung alle zwei Jahre', 'Evidence of compliance every two years', 30);

-- The composite key of compliance_rule (Konvention 19); compliance_result
-- references it since 000028 already, it is created only where it is
-- missing.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint c
        WHERE c.conrelid = 'compliance_rule'::regclass AND c.contype IN ('u', 'p')
          AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM unnest(c.conkey) k JOIN pg_attribute a
               ON a.attrelid = c.conrelid AND a.attnum = k) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE compliance_rule ADD CONSTRAINT compliance_rule_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;

CREATE TABLE compliance_rule_control (
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    rule_id         uuid NOT NULL,
    control_id      uuid NOT NULL REFERENCES compliance_control (id) ON DELETE CASCADE,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (rule_id, control_id),
    CONSTRAINT compliance_rule_control_rule_fkey FOREIGN KEY (rule_id, organization_id)
        REFERENCES compliance_rule (id, organization_id) ON DELETE CASCADE
);
CREATE INDEX idx_compliance_rule_control_control ON compliance_rule_control (organization_id, control_id);

ALTER TABLE compliance_rule_control ENABLE ROW LEVEL SECURITY;
ALTER TABLE compliance_rule_control FORCE ROW LEVEL SECURITY;
CREATE POLICY compliance_rule_control_isolation ON compliance_rule_control
    USING (organization_id = current_setting('app.org_id')::uuid)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON compliance_rule_control
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
