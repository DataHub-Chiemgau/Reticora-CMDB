# Abnahme ABN-03 – Nachweise Phase 2

WP-328 (`g2-concurrency`). ABN-03 verlangt eine Nebenläufigkeitsmatrix
(Reservierung, Discovery während Bearbeitung, Transfer, doppelter Ingest,
paralleler Merge) und danach eine Integritätsprüfung: keine Orphans, keine
Duplikate, keine negativen Bestände, keine Cross-Tenant-Referenzen, keine
Typ-, Location- und Kompositionszyklen
(`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:101`, Befund
`docs/audit/08-frontend-monitoring-nfr-tests.md:79`). Der [B]-Anteil ist seit
G1 abgenommen (WP-207, `docs/acceptance/g1-gate.md:511`). Dieses Dokument
ordnet jeden Teil seinem Nachweis in Phase 2 zu.

Alle Tests laufen gegen PostgreSQL (`TEST_DATABASE_URL`, `scopetest.Seed`) im
CI-Schritt „PostgreSQL integration tests (all packages)“ von
`.github/workflows/ci.yml` (`-p 1`). Nach jedem Fall ruft der Test
`scopetest.IntegrityViolations` (`backend/internal/database/scopetest/integrity.go`)
für die Fixture-Organisation auf. Der Helfer führt `tests/integrity-check.sql`
aus und erwartet keine Prüfung mit Verstößen. `load.yml` führt dasselbe Skript
nach jedem Lastjob aus.

## Nebenläufigkeitsmatrix

| Teil | Nachweis | WP |
|---|---|---|
| Reservierung: zwei parallele Reservierungen eines Assets | `TestReservationConcurrency`, Fall 1 (`backend/internal/reservation/concurrency_integration_test.go`) | WP-277 |
| Reservierung gegen Zuweisung an Dritte | `TestPhase2ConcurrencyMatrix`, Fall 1 (`backend/internal/tenant/integrity/phase2_matrix_integration_test.go`): genau eine gewinnt, die Zuweisung endet sonst mit 409 `asset-reserved`, die Reservierung mit 409 `asset-assigned`; außerdem `TestReservationConcurrency`, Fall 2 | WP-328, WP-277 |
| Reservierung: parallele Mengenreservierungen auf einen Bin | `TestPhase2ConcurrencyMatrix`, Fall 2: Σ aktiver Reservierungen ≤ `on_hand`, `reserved` = Σ, übrige 409 `insufficient-stock`; außerdem `TestConsumableReservation`, Fall 1 (`backend/internal/reservation/consumable_reservation_integration_test.go`) | WP-328, WP-284 |
| Transfer eines Assets (Halterwechsel) | `TestPhase2ConcurrencyMatrix`, Fall 4: zwei Transfers mit demselben `expected_holder`, genau einer gelingt, der andere 409 `holder-mismatch`; Halter-Cache = Bewegungen `assignment`/`return`; außerdem `TestAssignmentServiceConcurrency` (`backend/internal/assignment/transfer_integration_test.go`) | WP-328, WP-275 |
| Transfer von Mengenartikeln zwischen Bins | `TestPhase2ConcurrencyMatrix`, Fall 5: gegenläufige `transfer` gelingen beide ohne 40P01, `transfer` parallel zur Entnahme bis 0 überzieht nicht, Σ `on_hand` = Eingänge − Entnahmen; außerdem `TestStockBookingThroughMovementService` (`backend/internal/consumable/booking_integration_test.go`) | WP-328, WP-756, WP-283 |
| Entnahme bis 0 | `TestPhase2ConcurrencyMatrix`, Fall 3: `on_hand` nie < 0, unterlegene Buchung 409 `insufficient-stock` ohne Ledger-Eintrag; außerdem `TestStockBookingThroughMovementService` | WP-328, WP-756 |
| Discovery während Bearbeitung | `TestDiscoveryConcurrency` (`backend/internal/discovery/concurrency_integration_test.go`), `TestAutomatedWriteKeepsOverride` (`backend/internal/ci/concurrency_integration_test.go`) | WP-207 |
| Doppelter Ingest | `TestDiscoveryConcurrency`, auch über Spuren (WP-334, E-56) | WP-207 |
| Paralleler Merge | `TestParallelMerge` (`backend/internal/ci/concurrency_integration_test.go`) | WP-207 |
| ETag-Konkurrenz | `TestOptimisticConcurrency` (`backend/internal/ci/concurrency_integration_test.go`) | WP-207 |

Die Fälle von WP-328 laufen über die Handler bzw. Dienste der liefernden WPs
mit echten parallelen Transaktionen und mehreren Runden. Jede Transaktion
wartet nach ihrer Buchung und vor dem Commit an einer Barriere auf die übrigen
Transaktionen der Runde (Zeitlimit 150 ms). Eine Transaktion, die auf eine
Sperre wartet, hält so die Sperrende bis zum Zeitlimit auf. Keine Anfrage
endet mit 5xx. Serialisierungs- und Deadlock-Abbrüche (40001/40P01) erscheinen
höchstens als 409 (`backend/internal/api/dberrors.go`).

## Integritätsprüfung

Ein Selbsttest des Skripts ist `TestIntegrityCheckCountsEveryViolation`
(`backend/internal/ci/integrity_check_integration_test.go`). Er injiziert je
Prüfung eine Verletzung in einer zurückgerollten Transaktion und erwartet ihre
Skriptzeile. Eine Prüfung ohne Fall lässt ihn scheitern.

| Teil | Skriptzeilen in `tests/integrity-check.sql` | Weitere Nachweise | WP |
|---|---|---|---|
| Keine Orphans | `orphan: relationship to a deleted or merged CI`, `orphan: merged CI still active`, `orphan: rack mount of a deleted CI` | — | WP-207, WP-437 |
| Keine Duplikate | `duplicate: serial number of active CIs`, `duplicate: management IP and sysObjectID of weak identities in one VRF`, `duplicate: hostname or FQDN of weak identities without review`, neu `duplicate: normalized serial number of assets not disposed` (AST-03) | Schema: `uq_ci_org_serial`, `uq_ci_org_hardware_uuid`, `uq_ci_org_primary_mac`; `idx_reservation_one_active_per_asset`; `idx_asset_tag_org`, `idx_asset_rfid`, `idx_asset_barcode_org` (WP-271); `consumable.sku` und `consumable.barcode` je Org (WP-283); `stock_level (consumable_id, location_id)` | WP-437, WP-328 |
| Keine negativen Bestände | neu `stock: negative on_hand`, `stock: reserved below zero or above on_hand`, `stock: reserved other than the active quantity reservations` (WP-284) | CHECKs `stock_level_on_hand_check`, `stock_level_reserved_check` (WP-283); Fälle 2, 3 und 5 der Matrix | WP-328 |
| Reservierung und Zuweisung konsistent | neu `reservation: active reservation of an asset held by another holder` (WP-277), `assignment: holder cache other than the assignment and return movements` (WP-275, WP-274) | Fälle 1 und 4 der Matrix | WP-328 |
| Keine Cross-Tenant-Referenzen | `cross-tenant: …` (Beziehungen, Rack-Montage, Schnittstellen, IP-Adressen, Feldwerte, Client, Location, Site, Raum, Typ, Rack-Raum) | Zusammengesetzte Fremdschlüssel mit Organisation (WP-838, u. a. `ci_asset_id_fkey`, 000088); Katalogtest `TestTenantReferencesCarryOrganization` | WP-437, WP-838 |
| Keine Typzyklen | `cycle: CI type lineage` (`cloned_from_id`), neu `cycle: CI type inheritance` (`parent_type_id`, WP-248) | Advisory-Lock `ci_type_tree` (WP-248); `TestCycles`, Fälle `type sequential` und `type parallel` (`backend/internal/tenant/integrity/cycles_integration_test.go`) | WP-437, WP-328, WP-825 |
| Keine Location-Zyklen | `cycle: location tree` | Phase-1-Nachweis: `TestLocationTreeMoveAndCycle` (`backend/internal/locations/pg_repository_integration_test.go`), Advisory-Lock `location_tree`, `location_no_cycle` und Elternmatrix `location_parent_kind` in 000062 (WP-026); kein neuer Fall in Phase 2 (siehe unten) | WP-207 |
| Keine Kompositionszyklen | `cycle: composition` | Trigger `trg_ci_parent_cycle` mit Advisory-Lock `ci_parent_tree` (000273); `TestCycles`, Fälle `composition sequential`, `composition parallel` und `composition three parties deterministic`; kein Konfigurationskind mit eigenem Asset: CHECK `ci_configuration_child_asset` (WP-273) | WP-437, WP-825 |

Das Skript prüft nur, was das Schema nicht schon verhindert (F-281). Die
Zeilen zu negativen Beständen sind trotz der CHECKs aus WP-283 enthalten, als
ausdrücklicher Nachweis für ABN-03. Der Selbsttest entfernt die CHECKs dafür
in seiner zurückgerollten Transaktion.

## Typ- und Kompositionszyklen (WP-825)

Schreibweise: A→B heißt „A hat den Elternteil B“. `TestCycles`
(`backend/internal/tenant/integrity/cycles_integration_test.go`) ändert
Typ-Elternteile über `PATCH /api/v1/ci-types/{id}` (WP-248) und
CI-Elternteile über `ci.PGRepository.Update` (WP-273). Jede Änderung läuft in
einer Request-Transaktion und wartet nach dem Schreiben und vor dem Commit an
der Barriere der Matrix (150 ms).

| Fall | Ablauf | Erwartung |
|---|---|---|
| Typ sequenziell | (a) A→B, dann B→A; (b) A→B, B→C, dann C→A | Die schließende Änderung endet mit 409 auf `parent_type_id` |
| Typ parallel, drei Runden | (a) A→B ∥ B→A; (b) bei bestehendem A→B parallel B→C ∥ C→A | Höchstens eine Änderung wird angenommen, die andere endet mit 409 (Typzyklus oder Konflikt), nie mit 5xx; gespeichert ist genau die angenommene |
| Komposition sequenziell | (a) und (b) wie oben auf `parent_ci_id` | Die schließende Änderung endet mit 422 auf `parent_ci_id` |
| Komposition parallel, drei Runden | (a) und (b) wie oben auf `parent_ci_id` | Höchstens eine Änderung wird angenommen, die andere endet mit 422 (`parent_ci_id`) oder 409 (40P01/40001), nie mit 5xx |
| Komposition, deterministischer Dreierfall | Bei bestehendem A→B setzt T2 C→A und hält die Transaktion offen (`database.WithTenant` auf `scopetest.Fixture.App`); T1 setzt danach B→C; T2 committet | T1 wartet auf die Advisory-Sperre und endet nach dem Commit von T2 mit `23514` (`ci_parent_cycle`); gespeichert ist C→A, B ohne Elternteil |

Nach jedem Fall meldet `scopetest.IntegrityViolations` keine Zeile
`cycle: composition`, `cycle: CI type inheritance` oder
`cycle: CI type lineage` und keinen anderen Verstoß.

Typänderungen serialisiert seit WP-248 der Advisory-Lock `ci_type_tree`
(000197). `ci_check_parent_cycle` (000089) las den Elternpfad dagegen ohne
Sperre: Ohne Migration 000273 committen im deterministischen Dreierfall
beide Transaktionen, und der Zyklus A→B→C→A entsteht; auch der parallele
Fall (b) nimmt dann beide Änderungen an (lokal gegen den Stand 000272
ausgeführt). Migration 000273 (`ci_parent_cycle_lock`) nimmt bei INSERT mit
`parent_ci_id` und bei geändertem `parent_ci_id` vor der rekursiven Prüfung
`pg_advisory_xact_lock(hashtextextended('ci_parent_tree:' || organization_id, 0))`
und liest den Pfad danach mit frischem Snapshot (READ COMMITTED). SQLSTATE
`23514` und Constraint `ci_parent_cycle` bleiben. Der Ingest schreibt
`parent_ci_id` nicht, NFR-03 bleibt unberührt.

Location-Zyklen bekommen keinen neuen Fall: Die Elternmatrix
`location_parent_kind` (000062) weist jeder Art genau eine andere Elternart
zu, und der Advisory-Lock `location_tree` serialisiert Umzüge. Nachweis
bleiben `TestLocationTreeMoveAndCycle` und die Skriptzeile
`cycle: location tree`.

## Offene Teile anderer WPs

| Teil | WP |
|---|---|
| Automatisierte Schreiber (Import, Bulk, Template-Migration, `attribute.computed_refresh`) gegen manuelle Änderung, Ingest und `max_cis` | WP-826 |
| Cross-Tenant-Referenzen aller P2-Schreibpfade, Datenbank-Fremdschlüssel und Prüfskript | WP-827 |
| Kanalmatrix nach TEN-09 | WP-828 |

Diese WPs ergänzen ihre Zeilen in diesem Dokument.
