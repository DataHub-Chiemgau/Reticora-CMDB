# Upgrade- und Rollback-Hinweise

Hinweise für Betreiber zu Migrationen, die bei einem Upgrade oder Rollback
mehr verlangen als `migrate up` bzw. `migrate down`. Anforderung: TEN-03
([B], Teil). Umsetzung: WP-427. Entscheidung: E-72 (Option 1), nach dem
Präzedenzfall E-26.

Rollbacks laufen mit golang-migrate und den Migrationsdateien des Releases,
das gerade installiert ist (`docs/README.md`, „Running migrations“); der
Installer wendet nur `up` an. Vor jedem Rollback ist ein Backup Pflicht
(`docs/backup-dr.md`): Ein Rollback entfernt die Tabellen späterer
Migrationen samt ihren Daten.

## Rollback in geteilten PostgreSQL-Clustern

Rollen gelten in PostgreSQL für den ganzen Cluster, Rechte dagegen je
Datenbank. Die Migrationen legen zwei Rollen an: `reticora_app` (000056,
Rolle aller mandantenbezogenen Verbindungen) und `reticora_owner` (000078,
Eigentümer der Tabellen). Laufen mehrere Reticora-Datenbanken im selben
Cluster, etwa Staging neben Produktion, teilen sie sich diese Rollen.

Ein Rollback wirkt nur auf die eigene Datenbank:

- `000056_rls_enforcement.down.sql` entzieht `reticora_app` alle Rechte und
  Standardrechte in der eigenen Datenbank. Die Rolle selbst entfernt die
  Migration nur, wenn keine andere Datenbank des Clusters mehr Rechte an sie
  vergeben hat. Sonst bleibt sie bestehen, und die Migration meldet
  (sichtbar mit `psql`, golang-migrate zeigt NOTICEs nicht an):

  ```text
  NOTICE:  role reticora_app is kept, other databases of the cluster still depend on it: 647 objects in database staging
  ```

  Die Mitgliedschaften bleiben dann erhalten, auch die des Laufzeit-Logins
  `reticora_runtime`. Die übrigen Datenbanken arbeiten unverändert weiter
  (`SET ROLE reticora_app`, ihre Rechte). Ein späteres `up` über 000056
  verwendet die vorhandene Rolle wieder.
- `000078_app_role_ddl_contract.down.sql` verfährt mit `reticora_owner`
  ebenso, ohne Meldung.

Welche Datenbanken die Rolle noch verwenden, zeigt eine beliebige Datenbank
des Clusters (`pg_shdepend` ist clusterweit):

```sql
SELECT d.datname, count(*) AS objects
  FROM pg_shdepend s JOIN pg_database d ON d.oid = s.dbid
 WHERE s.refclassid = 'pg_authid'::regclass
   AND s.refobjid = 'reticora_app'::regrole
 GROUP BY d.datname ORDER BY 1;
```

Verwendet keine andere Datenbank die Rolle mehr (nur eine
Reticora-Datenbank im Cluster, oder alle anderen sind schon unter Version 56
zurückgerollt), entfernt der Rollback sie wie bisher, und mit ihr die
Mitgliedschaften. Nach dem nächsten `up` über 000056 muss das Laufzeit-Login
wieder Mitglied werden: `install-cloud.sh` erledigt das bei jedem Lauf,
sonst mit dem Migrations-Login `GRANT reticora_app TO reticora_runtime;`
(`deploy/README.md`, Abschnitt zum Laufzeit-Login).

## Release-Hinweis: geänderte Down-Migration 000056 (F-193, E-72)

Die Down-Migration `000056_rls_enforcement.down.sql` wurde geändert. Frühere
Builds endeten mit einem unbedingten `DROP ROLE IF EXISTS reticora_app`. Hielt
eine andere Datenbank des Clusters noch Rechte an der Rolle, brach der
Rollback ab:

```text
error: migration failed: role "reticora_app" cannot be dropped because some objects depend on it, 647 objects in database staging in line 0: DROP INDEX IF EXISTS ci_type_system_key_uniq;
…
DROP ROLE IF EXISTS reticora_app;
 (details: pq: role "reticora_app" cannot be dropped because some objects depend on it)
```

Danach meldete jeder weitere Aufruf
`error: Dirty database version 55. Fix and force version.`

Wer betroffen ist:

- Die up-Migration 000056 ist unverändert. Installationen, die nur
  aktualisieren, sind nicht betroffen und müssen nichts tun.
- Die Änderung wirkt nur bei einem Rollback unter Version 56, und nur mit den
  Migrationsdateien eines Releases, das sie enthält.
- Installationen, die schon erfolgreich unter 56 zurückgerollt haben, sind
  nicht betroffen. Der alte Build hat die Rolle nur dann entfernt, wenn keine
  andere Datenbank sie verwendete; genau dann entfernt sie auch der neue.
- Die anderen Datenbanken des Clusters waren auch bei einem gescheiterten
  Rollback nicht betroffen: Rolle, Rechte und Mitgliedschaften blieben
  bestehen.

### Rollback, der mit einem früheren Build auf halbem Weg gescheitert ist

golang-migrate führt jede Datei in einer Transaktion aus. Die
Down-Migrationen über 56 sind gelaufen, mit allem, was sie entfernen
(Tabellen späterer Migrationen und deren Daten, Katalogzeilen wie die
globalen CI-Typen späterer Migrationen). Die Down-Migration 000056 ist
vollständig zurückgerollt. Das Schema steht also auf Version 56, während
`schema_migrations` Version 55 als dirty führt.

Mit den Migrationsdateien eines Releases, das diese Änderung enthält:

1. Prüfen, dass das Schema auf Version 56 steht. 000056 legt die Tabelle
   `migration_quarantine` an, ihre Down-Migration entfernt sie:

   ```sql
   SELECT version, dirty FROM schema_migrations;                 -- 55 | t
   SELECT to_regclass('public.migration_quarantine') IS NOT NULL; -- t
   ```

   Zeigt die Abfrage etwas anderes, nicht weitermachen, sondern aus dem
   Backup wiederherstellen.
2. Die Version korrigieren:

   ```bash
   migrate -path backend/migrations -database "$DATABASE_URL" force 56
   ```

3. Entweder den Rollback fortsetzen (`migrate … down 1` für 000056 oder
   `down -all`; die Rolle bleibt mit der NOTICE oben bestehen, solange andere
   Datenbanken sie verwenden), oder auf den neuesten Stand zurückkehren
   (`migrate … up`). Die up-Migrationen über 56 legen Tabellen und
   Katalogzeilen wieder an. Daten in Tabellen, die der Rollback entfernt hat,
   kommen nur aus dem Backup zurück.

Beide Wege sind mit golang-migrate v4.18.1 nachgestellt: zwei auf den
neuesten Stand migrierte Datenbanken in einem Cluster, Rollback der ersten
mit der alten Datei (dirty 55, globale CI-Typen 23 → 17), danach mit der
neuen Datei `force 56` und `up` bzw. `down -all` und `up`: Version 163 nicht
dirty, 95 Berechtigungen, 23 globale CI-Typen, Demo-Organisation vorhanden;
die zweite Datenbank arbeitet weiter mit `SET ROLE reticora_app`.

## Release-Hinweis: Down-Migration 000056 ohne `migration_quarantine` (F-224, E-72)

Umsetzung: WP-574. Installationen, die 000056 vor E-26 angewendet haben,
besitzen keine Tabelle `migration_quarantine` (`docs/backup-dr.md`, Abschnitt
„Migration 000056“). Frühere Builds der Down-Migration lasen die Tabelle ohne
Prüfung und brachen dort mit `relation "migration_quarantine" does not exist`
ab; `schema_migrations` stand danach auf 55 dirty, das Schema auf 56.

- Die Down-Migration prüft jetzt `to_regclass('migration_quarantine')`. Ohne
  Tabelle entfällt das Zurückspielen, alles andere läuft wie bisher. Mit
  Tabelle ändert sich nichts.
- Die up-Migration 000056 ist unverändert. Installationen, die nur
  aktualisieren, sind nicht betroffen.
- Ein mit einem früheren Build gescheiterter Rollback: `migrate … force 56`,
  dann `migrate … down 1` mit den Migrationsdateien dieses Releases. Wer bei
  den älteren Dateien bleibt, legt vorher eine leere Tabelle an
  (`docs/backup-dr.md`).

Nachgestellt mit golang-migrate auf einer eigenen Datenbank: Migrationen 1
bis 56, danach `DROP TABLE migration_quarantine`. Mit der alten Datei endet
`down 1` mit dem Fehler oben und 55 dirty; nach `force 56` läuft `down 1` mit
der neuen Datei durch (55, nicht dirty). Mit der alten Datei und einer zuvor
angelegten leeren Tabelle läuft es ebenfalls durch.

Migration 000179 (WP-574, F-243) ist eine neue Datenmigration: Sie bricht
Enthaltensein-Zyklen in `ci.parent_ci_id`, die 000089 aus zyklischen
Alt-`contains`-Zeilen übernommen hat, am jüngsten CI auf und legt dessen
bisherigen Parent in `migration_quarantine` ab (`migration = '000179'`). Ihre
Down-Migration nimmt die Reparatur nicht zurück. Prüfung und Nacharbeit:
`docs/backup-dr.md`, Abschnitt „Migration 000179“.

## Release-Hinweis: Upgrade von Datenbanken ohne `migration_quarantine` (F-378, E-100)

Stand: 2026-10-28. Umsetzung: WP-649. Entscheidung: E-100 (Option 1,
entschieden 2026-10-28, nach dem Präzedenzfall E-26 und E-94).

Datenbanken, die 000056 vor E-26 angewendet haben (zwischen 2026-09-02 und
2026-10-01), besitzen keine Tabelle `migration_quarantine`. Frühere Builds
von 000089 schrieben ohne Prüfung in diese Tabelle; das Upgrade solcher
Datenbanken brach dort ab:

```text
error: migration failed: relation "migration_quarantine" does not exist (column 13) in line 38: -- WP-079 (DB-05, REL-01, AST-05, CH27): …
…
 (details: pq: relation "migration_quarantine" does not exist)
```

Danach meldete jeder weitere Aufruf
`error: Dirty database version 89. Fix and force version.` golang-migrate
führt jede Datei in einer Transaktion aus, das Schema stand also weiter auf
Version 88.

- 000089 legt die Tabelle jetzt an, wenn `to_regclass('public.migration_quarantine')`
  leer ist, mit genau der Definition von 000056: Spalten, Index, RLS ENABLE
  und FORCE, Policy `migration_quarantine_isolation`, Eigentümer
  `reticora_owner` und die Rechte von `reticora_app`. Danach läuft das
  Upgrade bis zur neuesten Version durch.
- Auf jeder anderen Datenbank, auch bei Neuinstallationen, bewirkt die
  Prüfung nichts; das Schema ist unverändert.
- Installationen, die bereits über 89 hinaus migriert sind, sind nicht
  betroffen und müssen nichts tun.
- Ein mit einem früheren Build bei 000089 gescheitertes Upgrade wird mit den
  Migrationsdateien dieses Releases fortgesetzt:

  ```sql
  SELECT version, dirty FROM schema_migrations;                  -- 89 | t
  SELECT to_regclass('public.migration_quarantine') IS NULL,     -- t
         to_regclass('public.ci_structure_edge') IS NULL;        -- t (Schema auf 88)
  ```

  Zeigen die Abfragen etwas anderes, nicht weitermachen, sondern aus dem
  Backup wiederherstellen. Sonst:

  ```bash
  migrate -path backend/migrations -database "$DATABASE_URL" force 88
  migrate -path backend/migrations -database "$DATABASE_URL" up
  ```

Ein Rollback unter 56 entfernt die von 000089 angelegte Tabelle wie bisher
mit der Down-Migration 000056; die Down-Migration 000089 lässt sie stehen.

Nachgestellt mit golang-migrate auf eigenen Datenbanken: Migrationen 1 bis
56, danach `DROP TABLE migration_quarantine`. Mit der alten Datei endet `up`
mit dem Fehler oben und 89 dirty; nach `force 88` läuft `up` mit der neuen
Datei bis 179 durch, die Tabelle hat RLS ENABLE und FORCE, Eigentümer
`reticora_owner` und die Rechte `reticora_app=arwd`. Eine Datenbank im
selben Zustand ohne gescheiterten Versuch migriert mit der neuen Datei
direkt bis 179.
`TestUpgradeFromPreQuarantine56`
(`backend/internal/database/migration_000089_prequarantine_upgrade_integration_test.go`)
prüft das Upgrade samt der von 000089 abgelegten Zeile und vergleicht das
Schema mit einer frischen Installation.

Seit WP-560 schreibt bereits 000066 in die Tabelle und legt sie ebenso an,
wenn sie fehlt (nächster Abschnitt). 000089 legt sie dann nur noch auf
Datenbanken an, die 000066 in der früheren Fassung angewendet haben. Der
Test prüft beide Fälle (Tabelle auf Version 56 bzw. 66 entfernt).

## Release-Hinweis: Bericht der von 000066 geleerten Storage-Keys (F-187, E-72)

Stand: 2026-10-28. Umsetzung: WP-560. Entscheidung: E-72 (Option 1,
entschieden 2026-10-26, nach dem Präzedenzfall E-26).

Migration 000066 (WP-030) leert Storage-Keys von Dokumenten, die nicht unter
dem Präfix der eigenen Organisation liegen (`org/<organization_id>/…` oder
das Altformat `documents/<organization_id>/<Dokument-ID>/…`, jeweils ohne
`..`), und legt danach die CHECK-Constraint
`document_storage_key_org_prefix` an. Frühere Builds meldeten nur die Anzahl
(`NOTICE: cleared 1 document storage keys outside the organization prefix`;
golang-migrate zeigt NOTICEs nicht an). Welche Dokumente ihren
Inhaltsverweis verloren haben, ließ sich danach nicht mehr feststellen.

- 000066 schreibt jetzt vor dem Leeren je betroffenem Dokument eine Zeile in
  `migration_quarantine`: `source_table = 'document'`,
  `migration = '000066'`, `row_data` mit `id`, `organization_id` und dem
  alten `storage_key`. Die Zeile gehört der Organisation des Dokuments
  (RLS `migration_quarantine_isolation`).
- Abfrage für Betreiber (Wartungsverbindung, wie für 000056 in
  `docs/backup-dr.md`):

  ```sql
  SELECT organization_id,
         row_data->>'id'          AS document_id,
         row_data->>'storage_key' AS old_storage_key,
         quarantined_at
    FROM migration_quarantine
   WHERE migration = '000066' AND source_table = 'document'
   ORDER BY organization_id, document_id;
  ```

  Keine Zeile: Kein Key wurde geleert. Sonst hat das Dokument keinen Inhalt
  mehr; der Inhalt wird mit `PUT /api/v1/documents/{id}/content` neu
  hochgeladen. Der alte Key wird bewusst nicht wiederhergestellt, weil er auf
  ein Objekt einer anderen Organisation zeigen kann. Erledigte Zeilen dürfen
  gelöscht werden.
- Die Down-Migration 000066 löscht die Berichtszeilen von 000066; die
  geleerten Keys bleiben leer.
- Datenbanken, die 000056 vor E-26 angewendet haben, besitzen keine
  `migration_quarantine` (F-378, E-100). 000066 ist für sie jetzt die erste
  Migration, die hineinschreibt, und legt die Tabelle wie 000089 an, wenn
  `to_regclass('public.migration_quarantine')` leer ist (Definition von
  000056; Eigentümer `reticora_owner` setzt 000078). Die Down-Migration
  prüft die Tabelle ebenso. Das Upgrade solcher Datenbanken bricht dadurch
  nicht ab; auf allen anderen Datenbanken und bei Neuinstallationen ist das
  Schema unverändert.
- Die Änderung wirkt nur für Installationen, die 000066 noch nicht
  angewendet haben. Installationen über Version 65 haben keinen Bericht;
  welche Keys geleert wurden, zeigt nur ein Backup von vor dem Upgrade auf
  66, isoliert wiederhergestellt (`docs/backup-dr.md`, nie über die
  Produktion), mit `SELECT version FROM schema_migrations` unter 66:

  ```sql
  SELECT id, organization_id, storage_key
    FROM document
   WHERE storage_key <> ''
     AND NOT ((storage_key LIKE 'org/' || organization_id::text || '/%'
               OR storage_key LIKE 'documents/' || organization_id::text || '/' || id::text || '/%')
              AND position('..' IN storage_key) = 0);
  ```

Nachgestellt mit golang-migrate auf eigenen Datenbanken: Migrationen 1 bis
65, zwei Organisationen, ein Dokument mit dem Key einer fremden Organisation
und eines mit eigenem Key, dann `up`. Mit der alten Datei läuft `up` bis 184
durch, der fremde Key ist leer, `migration_quarantine` enthält keine Zeile
von 000066. Mit der neuen Datei enthält sie genau eine Zeile mit Dokument-ID
und altem Key; der eigene Key bleibt. Dasselbe nach
`DROP TABLE migration_quarantine` auf Version 65: `up` läuft bis 184 durch
(nicht dirty), die Tabelle hat RLS ENABLE und FORCE, Eigentümer
`reticora_owner` und die Rechte `reticora_app=arwd`.
`TestMigration000066ReportsClearedStorageKeys` und
`TestMigration000066WithoutMigrationQuarantine`
(`backend/internal/database/migration_000066_report_integration_test.go`)
prüfen den Bericht (Migration 65 → 66), die Sichtbarkeit je Organisation als
`reticora_app`, die Down-Migration mit und ohne Tabelle und die angelegte
Tabelle.

## Release-Hinweis: Upgrade mit vorhandenen Interface-Suffixnamen (F-310, E-94)

Stand: 2026-10-28. Umsetzung: WP-592. Entscheidung: E-94 (Empfehlung
Option 1 als Übergangsregel, nach dem Präzedenzfall E-26).

Migration 000110 macht den Namen eines Interfaces je CI eindeutig und
benennt vorhandene Duplikate vorher um. Frühere Builds hängten an jedes
Duplikat nach dem ältesten seine Rangnummer an, ohne vorhandene Namen zu
prüfen. Hatte ein CI zweimal `eth0` und schon ein `eth0 (1)`, entstand ein
zweites `eth0 (1)`, und das Upgrade brach ab:

```text
error: migration failed: could not create unique index "network_interface_ci_name_key", Duplicate keys exist. in line 0: -- Migration 000110: network_interface after NET-01 (WP-123).
```

Danach meldete jeder weitere Aufruf
`error: Dirty database version 110. Fix and force version.` golang-migrate
führt jede Datei in einer Transaktion aus, das Schema stand also weiter auf
Version 109.

- 000110 vergibt jetzt je CI und Basisname die nächste freie Nummer: Das
  älteste `eth0` behält seinen Namen, ein vorhandenes `eth0 (1)` ebenfalls,
  das zweite `eth0` wird `eth0 (2)`. Ohne vorhandene Suffixnamen entstehen
  dieselben Namen wie bisher.
- Installationen, die bereits über 110 hinaus migriert sind, sind nicht
  betroffen und müssen nichts tun. Das Schema frischer Installationen ist
  unverändert.
- Ein mit einem früheren Build bei 000110 gescheitertes Upgrade wird mit den
  Migrationsdateien dieses Releases fortgesetzt:

  ```sql
  SELECT version, dirty FROM schema_migrations;                 -- 110 | t
  SELECT count(*) FROM information_schema.columns
   WHERE table_name = 'network_interface' AND column_name = 'mac_address'; -- 1 (Schema auf 109)
  ```

  Zeigen die Abfragen etwas anderes, nicht weitermachen, sondern aus dem
  Backup wiederherstellen. Sonst:

  ```bash
  migrate -path backend/migrations -database "$DATABASE_URL" force 109
  migrate -path backend/migrations -database "$DATABASE_URL" up
  ```

Nachgestellt mit golang-migrate auf einer eigenen Datenbank: Migrationen 1
bis 109, ein CI mit `vfy-eth0` (zweimal) und `vfy-eth0 (1)`. Mit der alten
Datei endet `up` mit dem Fehler oben und 110 dirty; nach `force 109` läuft
`up` mit der neuen Datei bis 180 durch, die Interfaces heißen `vfy-eth0`,
`vfy-eth0 (2)` und `vfy-eth0 (1)`. `TestNet01DedupeAvoidsExistingSuffix`
(`backend/internal/ipam/net01_migration_integration_test.go`) prüft die
Umbenennung samt doppelt vorhandener Suffixnamen.

## Prüfung

`TestMigrationsRoundtrip` (`backend/internal/database/migrations_roundtrip_integration_test.go`)
läuft in einer eigenen temporären Datenbank auf dem Server von
`TEST_DATABASE_URL` und lässt die geteilte Testdatenbank unberührt. Eine
zweite, vollständig migrierte Datenbank im selben Cluster hält während des
ganzen Roundtrips Rechte an `reticora_app`. Verlangt werden: down bis 0 und
up ohne Fehler und ohne dirty-Version, nach dem up dieselben Zeilen wie in
der frisch migrierten Datenbank, keine Abhängigkeit der Datenbank von
`reticora_app` auf Version 0, unveränderte Mitglieder der Rolle, und die
zweite Datenbank unverändert und mit `SET ROLE reticora_app` nutzbar.
