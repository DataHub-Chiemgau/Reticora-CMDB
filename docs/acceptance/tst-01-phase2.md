# TST-01 Phase 2 – Sollfallliste der Pflichtfälle

WP-326 (`g2-tst01`). TST-01 verlangt automatisierte Tests für die
Pflichtfälle der Phase 2: Vererbung, Template-Komposition (MET-16),
Feldrechte und DATEV (`docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:63`,
Befund `docs/audit/08-frontend-monitoring-nfr-tests.md:66`: keine
nachvollziehbare Sollfallmatrix). Den [B]-Anteil von TST-01 hat WP-202
geschlossen (`docs/traceability.csv`, Zeilen 418–426; G1 PASS in
`docs/acceptance/g1-gate.md`, bei gemischten Tags zählt dort der [B]-Anteil).
Dieses Dokument ordnet jedem [P2]-Pflichtfall genau einen benannten Test zu
und führt die Teilnachweise der liefernden WPs. Es schließt außerdem den
Gesamtnachweis für MET-06 (Typvererbung) und für MET-45 (Regel-AST, Go/TS)
ab.

Lauf auf Basis `5ce93e64` mit den Änderungen dieses WP, lokal gegen
PostgreSQL 16 ohne TimescaleDB (`TEST_DATABASE_URL`, `scopetest.Seed`); in
der CI laufen alle Tests im Schritt „PostgreSQL integration tests (all
packages)“ von `.github/workflows/ci.yml` (`-p 1`). Das Laufergebnis steht
unter [Laufergebnis](#laufergebnis).

## Sollfallliste

Je Pflichtfall genau ein benannter Test. Die Spalte „Teilnachweise“ führt
die Tests der liefernden WPs, die der Bericht referenziert, ohne sie zu
duplizieren.

| Pflichtfall | Testfunktion | Datei:Zeile | Lieferndes WP | Ergebnis |
|---|---|---|---|---|
| Vererbung (MET-06) | `TestResolveFieldsTypeChainLayers` | `backend/internal/citype/inherit_test.go:200` | WP-326 (Kette aus WP-248) | PASS |
| Template-Komposition (MET-16) | `TestCompositionConformanceMatrix` | `backend/internal/citype/compose_test.go:354` | WP-252 | PASS |
| Feldrechte (RBA-07, E-43) | `TestFieldRightsChannelMatrix` | `backend/internal/tenant/rls/fieldrights_matrix_integration_test.go:202` | WP-730 (Objektarten ergänzt durch WP-326) | PASS |
| DATEV (EXP-01 [P2], E-50) | `TestDATEVRenderGolden` | `backend/internal/export/datev_test.go:113` | WP-310 | PASS |

## Vererbung (MET-06)

Benannter Test: `TestResolveFieldsTypeChainLayers`
(`backend/internal/citype/inherit_test.go:200`, neu in WP-326). Er
komponiert die Typkette root → mid → leaf als Ebenen über `ResolveFields`
(Fake-Lister `layeredLister`): Das Blatt erbt die vererbbaren Attribute,
`inheritable = false` bleibt am eigenen Typ (und erzeugt am Blatt keinen
Konflikt), das Kind verfeinert (engere Grenzen 4..24, Schnittmenge [b c] der
erlaubten Werte, späterer Default) und entfernt nichts (geerbtes `required`
und das Pattern des Elterntyps bleiben). Ein Konflikt über die Kette
(Datentyp, leerer Bereich, kein erlaubter Wert) ist ein `CompositionError`
mit Schlüssel und Code, auch über zwei Ebenen.

| Teilfall | Nachweis | WP |
|---|---|---|
| Kette Root → Blatt mit Systemtyp als Wurzel, Untertypbaum | `TestTypeInheritanceAgainstPostgres` (`backend/internal/citype/inherit_integration_test.go:145`), ohne Datenbank `TestMemoryTypeChainAndSubtree` (`backend/internal/citype/inherit_test.go:59`) | WP-248 |
| Zyklus → 409, 7. Ebene → 422 (auch beim Verschieben eines Teilbaums), fremder oder unbekannter Elterntyp → 422 gleichlautend, Org-Typ nicht Elterntyp eines Systemtyps | `TestTypeParentRulesAgainstPostgres` (`backend/internal/citype/inherit_integration_test.go:340`), Fehlervertrag `TestTypeHandlerErrorContract` (`backend/internal/citype/inherit_test.go:291`) | WP-248 |
| Vererbte Attribute im Speicher-Repository | `TestResolveFieldsOverChain` (`backend/internal/citype/inherit_test.go:157`) | WP-248 |
| `ci_type_id`, `filter[ci_type_id]`, `filter[ci_type_id][in]` und Topologie mit `ci_type` schließen Untertypen ein | `TestSubtypeFilterAgainstPostgres` (`backend/internal/ci/subtype_filter_integration_test.go:90`) | WP-708 |
| Katalogregeln werden vererbt | `TestCatalogRulesOnWriteAndDiscovery` (`backend/internal/ci/rules_eval_integration_test.go:45`) | WP-257 |
| Layouts werden vererbt | `TestResolvedFormAgainstPostgres` (`backend/internal/citype/form_integration_test.go:98`) | WP-719 |
| `lifecycle_model_id` wird vererbt | `TestLifecycleModels`, Teil „inheritance and scope“ (`backend/internal/lifecycle/model_integration_test.go:66`) | WP-263 |

## Template-Komposition (MET-16)

Benannter Test: `TestCompositionConformanceMatrix`
(`backend/internal/citype/compose_test.go:354`, WP-252): 30 Fälle (Minimum
25), je Regel ein komponierender Fall und ein Fall mit Code, wo die Regel
scheitern kann, dazu die Ebenenfälle. Die Ebenenreihenfolge ist System-Basis
→ Org-global → Typkette → Sets → Templates → Instanz
(`compositionLayers`, dieselbe Reihenfolge in `ResolveFields` und
`CheckComposition`); `TestResolveFieldsComposesLayers`
(`backend/internal/citype/compose_test.go:278`) komponiert alle Ebenen in
einem Fall.

| Teilfall | Nachweis | WP |
|---|---|---|
| Kompositionsregeln, Konfliktcodes | `TestComposeRules`, `TestComposeConflicts` (`backend/internal/citype/compose_test.go:23`, `:86`), Referenzziele `TestComposeReferenceTargetOf` (`:157`), mehrere Konflikte `TestComposeAllCollectsConflicts` (`:587`), Sets `TestResolveFieldsComposesSetLayers` (`:632`), berechnete Attribute `TestComposeComputed` (`:764`) | WP-119, WP-252, WP-709, WP-254, WP-258 |
| Parität Go ↔ SQL für die materialisierten Ebenen (System-Basis, Typkette, Sets): `ci_type.attribute_schema`/`required_fields` gegen `Compose`, Code der Ablehnung (`attribute_definition_composition`) gegen den Go-Code | `TestCompositionParityAgainstPostgres` (`backend/internal/citype/compose_integration_test.go:173`) | WP-252, ergänzt durch WP-326 |
| Laufzeitprüfung mit 422 je Ebene | `TestCheckCompositionAgainstPostgres` (`backend/internal/citype/compose_integration_test.go:474`) | WP-252 |

Abgleich Matrix ↔ Paritätstest: Vor WP-326 fehlten im Paritätstest die
Matrixfälle `validation.options` (Schnittmenge und ohne gemeinsamen Wert),
`enum_values` mit `validation.options`, alter Datentypname
(`textarea` → `multiline`), Referenz-Alias (`user` und `reference` auf
`user`) und gleiches `multi_value` (sowie `multi_value` gegen ein Set). WP-326
ergänzt sie in `newComposeEnv` (Schlüssel `wp326_*`) mit Prüfung des
komponierten Blatts und, für die Konfliktfälle, des Datenbankcodes; alle
stimmen mit `Compose` überein.

Nach E-108 komponiert nur `Compose` zur Laufzeit die org-globalen
Attribute, die Templates und die Instanzdefinitionen (`CheckComposition`,
422 der API); materialisiert werden nur System-Basis, Typkette und Sets.
Diese Ebenen haben kein SQL-Gegenstück; das ist keine Testlücke.

## Feldrechte (RBA-07, E-43)

Benannter Test: `TestFieldRightsChannelMatrix`
(`backend/internal/tenant/rls/fieldrights_matrix_integration_test.go:202`,
WP-730): Kanalmatrix REST/GraphQL/Suche/strukturierte Abfrage/Export/Webhook
× lesbar/nicht lesbar/sensibel für die Objektart `ci`. Für die übrigen
Objektarten (Konvention 11) ergänzt WP-326 in derselben Datei
`TestFieldRightsChannelMatrixObjectKinds` (`:621`, 45 Fälle) mit je einem
lesbaren Feld, einem Feld mit Grant `none` und einem sensiblen Feld ohne
Grant:

| Objektart | Kanäle mit Zeilen | Kanäle ohne Projektion der Objektart |
|---|---|---|
| `ci` | REST Detail und Liste mit Filter, GraphQL `ci`, `cis`, `searchEverything`, Suche, strukturierte Abfrage, Export `/export/cis` und Job `/exports`, Webhook (gebunden und ungebunden) | – |
| `asset` | REST Detail und Liste, GraphQL `asset`, Suche, strukturierte Abfrage (Spalte), Webhook `asset.created` und `asset.changed` | Export: `POST /api/v1/exports` nimmt keine Assets (WP-782, `POST /api/v1/assets/bulk`), DATEV unter [DATEV](#datev-exp-01-p2-e-50) |
| `contract` | REST Detail und Liste | GraphQL, Suche und strukturierte Abfrage liefern keine Verträge; Webhook nur `contract.expiring` (Projektion in `webhook/project.go`, Unit-Tests WP-730) |
| `ticket` | REST Detail und Liste, Suche | GraphQL und strukturierte Abfrage liefern keine Tickets; Webhook-Ereignisse `ticket.*` werden einer Policy, die Tickets einschränkt, ganz vorenthalten (`unruledKinds`) |
| `document` | Suche | REST und GraphQL: siehe Befund F-1 |
| `relationship` | REST `GET /cis/{id}/relationships`, Webhook `relationship.created` | GraphQL `relationships` liefert weder `notes` noch `attributes` |

| Teilfall | Nachweis | WP |
|---|---|---|
| Auflösungsmatrix: Objektausnahme > Typ + Feld > Feld ohne Typ > Typ bzw. Objektart > Standard, Gleichstand innerhalb einer Stufe → `none` | `TestFieldResolutionStages` (`backend/internal/permission/fields_test.go:142`) | WP-264 |
| Über mehrere Rollen das höchste Level, Deckel durch Objektrecht und Principal | `TestFieldResolutionUnionAndCaps` (`backend/internal/permission/fields_test.go:214`) | WP-264 |
| Sensibles Feld (auch am Obertyp markiert) bleibt ohne `field_grant` mit genau diesem `field_key` `none` | `TestFieldSensitivity` (`backend/internal/permission/fields_test.go:267`) | WP-264 |
| Grant am Obertyp gilt für Untertypen, Grant am näheren Typ der Kette gewinnt, je Stufe; Sensibilität am Obertyp über drei Ebenen, Öffnen nur mit dem Schlüssel | `TestFieldResolutionTypeChain` (`backend/internal/permission/fields_test.go:709`) | WP-326 |
| REST: nicht lesbare Felder fehlen, PATCH auf nicht editierbares Feld → 403 `forbidden` mit `fields`, `version`/ETag unabhängig von der Sichtbarkeit | `TestFieldRightsRESTAgainstPostgres` (`backend/internal/ci/fieldrights_integration_test.go:199`) | WP-265 |
| Kanalmatrix `ci` | `TestFieldRightsChannelMatrix` | WP-730 |
| Kanalmatrix `asset`, `contract`, `ticket`, `document`, `relationship` | `TestFieldRightsChannelMatrixObjectKinds` | WP-326 |

## DATEV (EXP-01 [P2], E-50)

Benannter Test: `TestDATEVRenderGolden`
(`backend/internal/export/datev_test.go:113`, WP-310): Die Datei des
Default-Mappings stimmt byteweise mit `testdata/datev_expected.csv` überein
(Windows-1252 ohne BOM, Semikolon, CRLF, Umlaute und `€` als 0x80), die
abgelehnten Zeilen stehen nur im Fehlerreport.

| Formatfall | Nachweis | WP |
|---|---|---|
| Windows-1252, Semikolon, CRLF, Umlaute und `€` | `TestDATEVRenderGolden` | WP-310 |
| Kopfzeile aus dem Mapping (`export/datev_mapping.json`), auch ohne Zeilen | `TestDATEVHeaderAndClosedColumn` (`backend/internal/export/datev_test.go:512`) | WP-326 |
| Beträge mit Dezimalkomma ohne Tausenderpunkt, Datum `TT.MM.JJJJ`, Formelschutz nur für Text | `TestDATEVCellFormats` (`backend/internal/export/datev_test.go:154`), Golden-Datei (123456789,99) | WP-310 |
| Fehlendes Pflichtfeld oder nicht kodierbares Zeichen → Zeile im Fehlerreport statt in der Datei | `TestDATEVRenderGolden` (`datevExpectedReport`) | WP-310 |
| Gesperrtes Quellfeld → leere Spalte in jeder Zeile mit Grund `field_permission` im Manifest | `TestDATEVFieldPermissions` (`backend/internal/export/datev_test.go:413`), `TestDATEVHeaderAndClosedColumn` | WP-310, WP-326 |
| Job über `POST /api/v1/exports` mit `format=datev` liest Assets, keine CIs | `TestDATEVJob` (`backend/internal/export/datev_test.go:300`) | WP-310 |
| Gate (Feature `export_datev`, Modul `inventory`) | `TestDATEVGateMatrix` und folgende (`backend/internal/export/datev_gate_test.go:127`) | WP-790 |

Die Formatfälle prüfen das Default-Mapping nach E-50. Der CI-basierte
Altfall `TestExportDATEV` (`backend/internal/export/export_test.go`) zählt
nicht als Nachweis. `GET /api/v1/export/cis?format=datev`
(`backend/internal/export/export.go`, deprecated, Sunset 2027-10-15) liefert
weiter CI-Daten im CI-Layout, nach E-141 nur hinter dem Gate aus WP-790
(`TestDATEVLegacyPathKeepsCILayout`, `datev_gate_test.go:215`). Ein Job mit
dem Formatwert `datev` über `POST /api/v1/exports` liest keine CI-Daten
(`TestDATEVJob`); ein Befund gegen WP-310 entfällt.

## Regel-AST (TST-01 [B], MET-45)

| Teil | Nachweis | WP |
|---|---|---|
| Jeder Operator von `api/rules.schema.json` hat Fälle in `api/rules.conformance.json`; Go wertet alle Abschnitte aus | `TestConformance`, `TestConditionCoversEveryOperator`, `TestSchemaMatchesOperators`, `TestExpressionConformance`, `TestExpressionCoversEveryOperator` (`backend/internal/rules/rules_test.go:71`, `:135`, `:202`, `:327`, `:384`) | WP-118, WP-715, WP-258 |
| TS wertet `cases`/`invalid_rules`, `condition_cases`/`invalid_conditions` (`frontend/src/rules/rules.ts`) und `expressions`/`invalid_expressions` (`frontend/src/rules/expr.ts`) aus | `frontend/src/rules/rules.test.ts` | WP-715, WP-802 |
| Filter-AST `filter_cases`/`invalid_filters` | Go `TestFilterConformance`, `TestFilterSchemaMatchesOperators` (`backend/internal/savedview/filter_test.go:1183`, `:1235`), TS `frontend/src/rules/filter.test.ts` | WP-789 |
| Guards (Bedingungsgrammatik an Übergängen) | `TestLifecycleGuards`, `TestLifecycleGuardsFailClosed` (`backend/internal/lifecycle/guards_integration_test.go:71`, `:414`), `TestLifecycleTransition` (`backend/internal/lifecycle/transition_integration_test.go:76`) | WP-724 |
| `attribute_definition.validation` als Regel-AST | `TestAttributeDefinitionASTValidation` (`backend/internal/citype/attrdef_integration_test.go:505`) | WP-589 |

Alle Abschnitte der Konformanzdatei laufen auf beiden Seiten: `rules.test.ts`
führt die Abschnitte von Regeln, Bedingungen und Ausdrücken aus,
`filter.test.ts` die Filterabschnitte. Zu Referenzen in Bedingungen siehe
Befund F-2.

## Befunde

| Nr. | Befund | Gegen | Wirkung |
|---|---|---|---|
| F-1 | Die REST-DTOs der Dokumente (`GET/PATCH /api/v1/documents…`, `backend/internal/document/handler.go`) und der GraphQL-Typ `Document` (`Asset.documents`) laufen durch keine Feldrechte-Projektion: `description`, `tags`, `file_name` und `category` sind auch bei Grant `none` oder Sensibilität lesbar, Schreibzugriffe werden nicht geprüft. `field_grant` und die Suche kennen die Objektart `document` (die Suche projiziert sie, `TestFieldRightsChannelMatrixObjectKinds`), Konvention 11 verlangt die Bindung im WP der Objektart. | WP-267 | Lücke RBA-07 für `document` in REST und GraphQL; die Matrix führt für `document` nur die Suche |
| F-2 | `api/rules.conformance.json` enthält keine Fälle für Referenzen in Bedingungen (Vergleich auf Referenzattribute wie `user`, `location`, `reference`). WP-254 hat Referenzen in Regelbedingungen ausdrücklich an ein Folge-WP gegeben. | WP-254 (Folge-WP) | MET-45 bleibt PASS für die vorhandenen Operatoren; Referenzbedingungen sind nicht konformanzgeprüft |

Beim Lückenschluss sind keine Produktfehler aufgefallen; die ergänzten Fälle
laufen gegen den bestehenden Produktionscode grün. Dieses WP ändert keinen
Produktionscode.

## Laufergebnis

Commit: siehe Commit von WP-326 auf Basis `5ce93e64`.

| Lauf | Ergebnis |
|---|---|
| `go test -count=1 -p 1 ./internal/citype/ ./internal/permission/ ./internal/lifecycle/ ./internal/export/ ./internal/rules/ ./internal/ci/ ./internal/tenant/rls/` mit `TEST_DATABASE_URL` | PASS (`citype`, `permission`, `lifecycle`, `export`, `rules`, `tenant/rls`; `ci` PASS bis auf `TestCIQueryBudget`, der eine eigene Datenbank mit TimescaleDB migriert und nur in der CI läuft) |
| `npx vitest run src/rules` (`rules.test.ts`, `filter.test.ts`) | PASS (248 Tests) |
| `TRACEABILITY_WPS=WP-326 go test ./internal/traceability/` | PASS |

Nur in der CI (lokal ohne TimescaleDB, NATS, MinIO, clamd nicht lauffähig):
`TestCIQueryBudget`, `TestMigrationsRoundtrip`,
`TestSchemaBaselineMatchesMigratedSchema` und die NATS-Tests; keiner davon
ist ein Nachweis dieses Dokuments.
