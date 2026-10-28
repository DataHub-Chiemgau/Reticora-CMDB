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

Stand: 2026-10-28. Umsetzung: WP-649. Entscheidung: E-100 (Empfehlung
Option 1 als Übergangsregel, nach dem Präzedenzfall E-26 und E-94).

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
