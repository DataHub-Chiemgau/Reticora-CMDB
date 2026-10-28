# Entscheidungen – Phase 2 (Verwaltung, G2)

Stand: 2026-10-28 (Abgleich mit main `3a9669a` nach Gate G1; Beschluss des Auftraggebers vom 2026-10-28). Gehört zu [`implementierungsplan.md`](implementierungsplan.md) und setzt [`../entscheidungen.md`](../entscheidungen.md) fort. Die Nummern sind über beide Entscheidungsdateien eindeutig: Phase 1 belegt E-01 bis E-36 und E-56 bis E-58 sowie die Aufgaben A-01 bis A-14; diese Datei führt E-37 bis E-55 und E-59 bis E-99 sowie die Aufgaben A-15 bis A-53. Neue Entscheidungen und Aufgaben beider Phasen erhalten die nächste freie Nummer über beide Dateien (Stand dieser Fassung: E-100 und A-54). Jede Entscheidung nennt Status, Festlegung, Frage, Optionen, Empfehlung, Auswirkung, Quelle sowie betroffene IDs und WPs. Seit dem Beschluss vom 2026-10-28 sind alle Entscheidungen dieser Datei entschieden. Die Festlegung ist die verbindliche Regel; Frage, Optionen und Quelle bleiben als Dokumentation.

**Nachtrag vom 2026-10-19 (Abgleich nach Gate G1):** Gate G1 ist seit 2026-10-15 mit Freigabe des Auftraggebers bestanden (`docs/acceptance/g1-gate.md`). In Phase 1 sind seitdem E-17, E-56, E-57 und E-58 entschieden; E-02 und E-35 bleiben offen, bis die erklärten Lieferungen vorliegen. Jede Entscheidung dieser Datei ist gegen den Code geprüft. Neu gefasst sind vor allem E-38 (Pfadinventar nach dem Bestand, Alias nur für G1-Pfade, Regeln für `x-phase` und `x-feature`), E-40 (Altledger und Übergangsspalte), E-43 (Auflösung über mehrere Rollen), E-44 (Zuordnung der Routen in `gatedRoutes`, Module `disposal`, `desks` und GPS, Limits, Trial), E-48 (keine Ausnahme nach Audit-Status N/P; die Stufe `P2` zählt nur Zeilen aus Phase-2-WPs), E-53 (`duplicate_ip` besteht seit WP-088), E-54 (zwei AST-Arten) und E-55 (Pflichtfelder je Methode). Neu aufgenommen sind E-59 bis E-63 und aus der Konsolidierung der WP-Neufassungen E-64 bis E-99. Die Aufgaben A-12 bis A-19 heißen jetzt A-15 bis A-22, weil Phase 1 A-12 bis A-14 belegt; neu sind A-23 bis A-53. Stand jetzt: 60 Entscheidungen, davon 30 vorläufig entschieden und 30 offen.

**Nachtrag vom 2026-10-28 (Beschluss des Auftraggebers):** Der Auftraggeber hat alle 60 Entscheidungen dieser Datei nach ihrer Empfehlung entschieden. Verlangte die Empfehlung eine Lieferung oder Bestätigung, gilt der Beschluss als Bestätigung. Fehlt eine Lieferung, ist die bisherige Übergangsregel die endgültige Regel. Alle Aufgaben dieser Datei sind erledigt, außer A-28: Die Konsolidierung von Teil 03 ist Arbeit im Plan (WP-464), keine Lieferung. Außerdem sind die Nachweise B3 (Pentest-Befundliste), B4 (v2-Texte zu AUT-05, AUT-06, NET-04 und ABN-01 (a)/(b)) und B6 (Subprozessorenliste) gestrichen: Die Erklärung des Auftraggebers vom 2026-10-15 gilt als Nachweis für G1 und G2 ([E-59](#e-59)). Nichts davon wird nachgereicht, und kein WP gleicht mit v2-Texten ab. Weiter nachzuweisen sind nur der CI-Lauf auf dem Release-Kandidaten und der nächtliche `load.yml`-Lauf (WP-463). In Phase 1 sind damit [E-02](../entscheidungen.md#e-02) und [E-35](../entscheidungen.md#e-35) entschieden, weil die Erklärung die Lieferung ersetzt; A-02, A-07 und A-11 sind ohne Nachreichung erledigt. Nach Empfehlung entschieden sind dort auch E-11, E-13, E-16 und E-34. Stand jetzt: 60 Entscheidungen, alle entschieden.

## Übersicht

| Nr. | Thema | Status | Betroffene WPs |
|---|---|---|---|
| [E-37](#e-37) | Fehlende Metamodell-Texte MET-20, MET-30 bis MET-34, MET-40 bis MET-44 (Sets, Templates, Regeln, berechnete Attribute) | entschieden (2026-10-28, Empfehlung: Option 1 mit Option 2; die Texte werden nicht geliefert, daher gilt Option 2 als Regel) | WP-243, WP-246, WP-464, WP-250, WP-341, WP-251, WP-342, WP-252, WP-253, WP-344, WP-257, WP-347, WP-348, WP-258, WP-349, WP-350, WP-261, WP-353, WP-262, WP-354, WP-307, WP-429, WP-314, WP-431, WP-432, WP-433, WP-434, WP-325, WP-326, WP-330, WP-331 |
| [E-38](#e-38) | API-06: REST-Ressourcen der Phase 2 und Ablösung abweichender Bestandspfade | entschieden (2026-10-28, Option 1: Pfadinventar) | WP-337, WP-338, WP-245, WP-247, WP-250, WP-251, WP-342, WP-344, WP-256, WP-348, WP-259, WP-351, WP-260, WP-352, WP-261, WP-353, WP-262, WP-354, WP-355, WP-428, WP-358, WP-267, WP-363, WP-443, WP-269, WP-368, WP-272, WP-374, WP-274, WP-275, WP-378, WP-379, WP-380, WP-277, WP-382, WP-385, WP-387, WP-283, WP-388, WP-389, WP-284, WP-390, WP-391, WP-286, WP-392, WP-287, WP-393, WP-394, WP-395, WP-396, WP-397, WP-291, WP-292, WP-400, WP-296, WP-299, WP-301, WP-406, WP-303, WP-413, WP-305, WP-311, WP-425, WP-427, WP-429, WP-314, WP-430, WP-431, WP-432, WP-316, WP-435, WP-318, WP-438, WP-440, WP-441, WP-320, WP-444, WP-445, WP-321, WP-447, WP-331 |
| [E-39](#e-39) | TKT-01/ENT-06: Abgrenzung Ticketing Essential und Standard | entschieden (2026-10-28, Option 1) | WP-244, WP-337, WP-290, WP-396, WP-397, WP-291, WP-398, WP-399, WP-292, WP-400, WP-401, WP-446, WP-447, WP-452, WP-331 |
| [E-40](#e-40) | Mengenartikel: `consumable` und `quantity_item` zu einem Modell zusammenführen | entschieden (2026-10-28, Option 1) | WP-274, WP-376, WP-283, WP-388, WP-389, WP-284, WP-390, WP-391, WP-392, WP-320, WP-451 |
| [E-41](#e-41) | Lieferantenstammdaten (`supplier_id` in AST-01 und MGT-12) | entschieden (2026-10-28, Option 1) | WP-269, WP-368, WP-271, WP-370, WP-283, WP-286, WP-440, WP-444, WP-451 |
| [E-42](#e-42) | SEC-09: Betrieb des Malware-Scans (ClamAV) und Verhalten bei Ausfall | entschieden (2026-10-28, Option 1) | WP-363, WP-365, WP-366, WP-367, WP-301, WP-321, WP-452, WP-329, WP-461 |
| [E-43](#e-43) | RBA-07: Granularität und Auflösung der Feldrechte in Phase 2 | entschieden (2026-10-28, Option 1; Audit-Log ohne Projektion, `audit:read` legt alle Feldwerte offen) | WP-264, WP-357, WP-358, WP-265, WP-359, WP-360, WP-266, WP-361, WP-362, WP-402, WP-269, WP-397, WP-296, WP-308, WP-419, WP-420, WP-309, WP-311, WP-424, WP-325, WP-451, WP-326 |
| [E-44](#e-44) | ENT-06 (V): Feature-Keys der Phase 2, Paketzuordnung und Zuordnung der Routen | entschieden (2026-10-28, Option 1; ergänzt E-34) | WP-244, WP-337, WP-338, WP-247, WP-251, WP-363, WP-268, WP-269, WP-368, WP-270, WP-276, WP-380, WP-278, WP-383, WP-283, WP-388, WP-284, WP-391, WP-286, WP-288, WP-394, WP-395, WP-290, WP-291, WP-400, WP-401, WP-406, WP-420, WP-309, WP-422, WP-311, WP-318, WP-438, WP-439, WP-440, WP-320, WP-444, WP-445, WP-321, WP-446, WP-447, WP-322, WP-324, WP-325, WP-326, WP-329, WP-331 |
| [E-45](#e-45) | Epic-Zuordnung und Traceability-Epics der Phase 2 | entschieden (2026-10-28, Option 1) | WP-243, WP-463, WP-464 |
| [E-46](#e-46) | Verschobene Phase-1-WPs (WP-215 bis WP-239) durch Phase-2-WPs ersetzen | entschieden (2026-10-28, Option 1) | WP-243, WP-436, WP-317, WP-329, WP-331 |
| [E-47](#e-47) | TKT-03 [O] und CH17: Ticket-Provider-Schnittstelle in Phase 2 | entschieden (2026-10-28, Option 1) | WP-293 |
| [E-48](#e-48) | Traceability-Vollständigkeit für [P2] (Erweiterung von E-30) | entschieden (2026-10-28, Option 1) | WP-243, WP-331 |
| [E-49](#e-49) | IMP-IO-01: Import-Dateiformate, Bibliotheken und Grenzen | entschieden (2026-10-28, Option 1) | WP-301, WP-406, WP-302, WP-407, WP-303, WP-322, WP-455, WP-329, WP-461 |
| [E-50](#e-50) | EXP-01: DATEV-Exportformat für die Anlagenbuchhaltung | entschieden (2026-10-28, Option 1; das Default-Mapping ist das Zielformat) | WP-310, WP-422, WP-423, WP-326, WP-331 |
| [E-51](#e-51) | AST-02: Art der digitalen Unterschrift bei Übergaben | entschieden (2026-10-28, Option 1; Altsignaturen nach (a) übernommen) | WP-376, WP-379, WP-276, WP-380, WP-381, WP-439 |
| [E-52](#e-52) | INV-03: Barcode-Symbologie, Etikettenvorlagen und Bibliotheken | entschieden (2026-10-28, Option 1) | WP-289, WP-394, WP-395, WP-439, WP-445, WP-329 |
| [E-53](#e-53) | NET-06/NET-09: Auflösungen des Review-Typs `duplicate_ip` | entschieden (2026-10-28, Option 1) | WP-299, WP-404, WP-300, WP-405 |
| [E-54](#e-54) | MET-50/CH18: Technische Basis für Form Builder und Klick-Builder | entschieden (2026-10-28, Option 1) | WP-347, WP-421, WP-429, WP-314, WP-432, WP-315, WP-433, WP-434, WP-316, WP-329 |
| [E-55](#e-55) | MGT-02: Norm und Nachweiskriterien für die Entsorgungsdokumentation | entschieden (2026-10-28, Option 1) | WP-383, WP-447, WP-331 |
| [E-59](#e-59) | Fortgeltung der in G1 durch Erklärung abgenommenen Punkte (B3 Pentest-Befundliste, B4 v2-Texte, B6 Subprozessoren) für G2 | entschieden (2026-10-28, Beschluss des Auftraggebers: Die Erklärung vom 2026-10-15 gilt als Nachweis für G1 und G2; die Nachweise B3, B4 und B6 sind gestrichen) | WP-463, WP-329, WP-330, WP-331 |
| [E-60](#e-60) | Form von `total` in Listen über etwa 25.000 Objekten (Folge aus E-17) | entschieden (2026-10-28, Option 1) | WP-471, WP-372, WP-274, WP-421, WP-319, WP-327, WP-331 |
| [E-61](#e-61) | Release-Gate für P2-Funktionen ohne eigenes Modul (Metamodell, Tags, Feldrechte, Verträge/Lieferanten, Import) | entschieden (2026-10-28, Option 1) | WP-337, WP-338, WP-247, WP-248, WP-250, WP-251, WP-342, WP-344, WP-345, WP-255, WP-256, WP-348, WP-258, WP-350, WP-259, WP-351, WP-260, WP-352, WP-261, WP-353, WP-262, WP-354, WP-355, WP-358, WP-269, WP-368, WP-270, WP-273, WP-281, WP-385, WP-394, WP-395, WP-291, WP-400, WP-296, WP-299, WP-301, WP-406, WP-407, WP-408, WP-304, WP-412, WP-420, WP-422, WP-424, WP-429, WP-314, WP-430, WP-431, WP-432, WP-433, WP-316, WP-317, WP-439, WP-440, WP-322, WP-449, WP-454, WP-329, WP-331, WP-462 |
| [E-62](#e-62) | RBA-05/RBA-06: Rechteschlüssel der Phase 2 und Rollenmatrix der Standardrollen | entschieden (2026-10-28, Option 1) | WP-245, WP-250, WP-251, WP-256, WP-348, WP-259, WP-351, WP-260, WP-352, WP-261, WP-353, WP-355, WP-358, WP-269, WP-368, WP-277, WP-385, WP-283, WP-388, WP-284, WP-285, WP-286, WP-392, WP-393, WP-400, WP-301, WP-406, WP-307, WP-424, WP-316, WP-435, WP-440, WP-320, WP-444, WP-445, WP-322, WP-449, WP-323, WP-325, WP-329, WP-330 |
| [E-63](#e-63) | Admin-Oberfläche für Feldrechte und Lifecycle-Modelle in Phase 2 | entschieden (2026-10-28, Option 2) | WP-355, WP-358, WP-313, WP-331 |
| [E-64](#e-64) | MET-16: Materialisierung org-spezifischer Kompositionsebenen an Systemtypen | entschieden (2026-10-28, Option 1) | WP-250, WP-341, WP-251, WP-342, WP-252, WP-255, WP-261, WP-326 |
| [E-65](#e-65) | DIS-02: Zuordnung des Systemtemplates aus `attributes.system_template` beim Ingest | entschieden (2026-10-28, Option 3; Option 1 erst mit einem Katalogstand, der MET-30 bis MET-34 liefert) | WP-251, WP-342 |
| [E-66](#e-66) | RBA-03/VIE-01: IdP-gemappte Standardrollen in rollengebundener Konfiguration (Layouts, geteilte Views) | entschieden (2026-10-28, Option 1 als Ziel; bis WP-357 gilt Option 2) | WP-259, WP-351, WP-357, WP-307 |
| [E-67](#e-67) | MET-53: Versionsbindung der Template-Bestandteile (Katalogregeln und Layouts) | entschieden (2026-10-28, Option 1) | WP-251, WP-348, WP-259, WP-260, WP-261, WP-316 |
| [E-68](#e-68) | MET-53/API-06: Verweise auf Items desselben Metamodell-Drafts | entschieden (2026-10-28, Option 1) | WP-260, WP-261, WP-354 |
| [E-69](#e-69) | UI-11/MET-53: Lesesicht eines Drafts in den Metamodell-Editoren und in der Formular-Vorschau | entschieden (2026-10-28, Option 3) | WP-351, WP-260, WP-352, WP-314, WP-430, WP-316, WP-435, WP-330 |
| [E-70](#e-70) | MET-45: Ablösung der attributgebundenen Regeln neben dem Regelkatalog | entschieden (2026-10-28, Option 3) | WP-257, WP-348, WP-433, WP-434 |
| [E-71](#e-71) | RBA-07/MET-53: Import von Feldrechten aus dem Metamodell-Export | entschieden (2026-10-28, Option 3) | WP-354, WP-358 |
| [E-72](#e-72) | LCY-02: Bindung des Lifecycle-Modells `software` an Systemtypen | entschieden (2026-10-28, Option 1) | WP-263, WP-355 |
| [E-73](#e-73) | Kategorie und Icon der globalen Systemtypen | entschieden (2026-10-28, Option 1) | WP-248, WP-313, WP-317 |
| [E-74](#e-74) | MGT-01/NFR-05/NFR-06: Endgültiges Löschen von Dokumentinhalten im versionierten Bucket | entschieden (2026-10-28, Option 1) | WP-364 |
| [E-75](#e-75) | MGT-12: Verantwortliche am Vertrag und Wertebereich von `contract.kind` | entschieden (2026-10-28, Option 2) | WP-269, WP-270, WP-440 |
| [E-76](#e-76) | AST-01/LCY-02: Zustand und Altfelder am Asset (Backfill `lifecycle_state`, Ablösung von `status`, Freitext `location` und Spiegelspalten) | entschieden (2026-10-28, Option 1) | WP-263, WP-271, WP-370, WP-371, WP-278, WP-279, WP-287, WP-319 |
| [E-77](#e-77) | AST-01/NFR-06: Lösch- und Archivsemantik für Assets | entschieden (2026-10-28, Option 2) | WP-372, WP-274, WP-376, WP-413 |
| [E-78](#e-78) | AST-01/AST-04: Benennung des Primär-CI (`primary_ci_id`) | entschieden (2026-10-28, Option 2) | WP-370, WP-371, WP-272 |
| [E-79](#e-79) | AST-05: Vorgabe für `child_mode` und Löschen mit Konfigurationskindern | entschieden (2026-10-28, Option 1) | WP-273 |
| [E-80](#e-80) | SEC-05/Konvention 16: Nutzerverweise in Tabellen der Phase 2 | entschieden (2026-10-28, Option 1) | WP-271, WP-377, WP-276, WP-280 |
| [E-81](#e-81) | API-01: Verschärfungen an G1-Routen (`attributes.tags`, Beziehungstypen) | entschieden (2026-10-28, Option 3) | WP-281, WP-385, WP-294 |
| [E-82](#e-82) | API-02: Operator `all` für Mengenfelder | entschieden (2026-10-28, Option 1) | WP-386 |
| [E-83](#e-83) | TKT-01: Ticket-Tags und Tag-Katalog | entschieden (2026-10-28, Option 1) | WP-396 |
| [E-84](#e-84) | STK-01/TEN-05: Gesamtbestand von Mengenartikeln über Scopes | entschieden (2026-10-28, Option 1: (a) ja, (b) nein) | WP-283, WP-388, WP-285 |
| [E-85](#e-85) | NTF-04 [B]: Digest bei Benachrichtigungen | entschieden (2026-10-28, Option 1) | WP-463, WP-331 |
| [E-86](#e-86) | STK-05: Benachrichtigung des Antragstellers bei Statuswechsel einer Bestellung | entschieden (2026-10-28, Option 2) | WP-286 |
| [E-87](#e-87) | IMP: Transit über Subnetze und VLANs im Impact | entschieden (2026-10-28, Option 1) | WP-298 |
| [E-88](#e-88) | BLK-02: Semantik der Bulk-Aktionen Archivierung, Template-Wechsel und Zuweisung | entschieden (2026-10-28, Option 1) | WP-304, WP-412, WP-413 |
| [E-89](#e-89) | SR-04/NFR-02: Indexnutzung nicht-leakproof Operatoren unter RLS (Volltext, Attributfilter, Eindeutigkeit) | entschieden (2026-10-28, Option 2 für Bezeichner und Attributgleichheit, Option 3 für Volltext) | WP-468, WP-471, WP-350, WP-266, WP-308, WP-309, WP-327 |
| [E-90](#e-90) | SRC-02: Durchsuchbarkeit von `asset.custom_fields` und Wirkung von `searchable` | entschieden (2026-10-28, Option 1) | WP-308, WP-418 |
| [E-91](#e-91) | INV-03: Auslieferung des Batch-Etiketten-PDFs an den Browser | entschieden (2026-10-28, Option 2) | WP-395, WP-445 |
| [E-92](#e-92) | ABN-01 (c) [Q]: Ausführungsform der Metamodell-Abnahme | entschieden (2026-10-28, Option 1) | WP-324, WP-330 |
| [E-93](#e-93) | Termin der G1-Restbefunde ohne Phase-2-WP (Rest von SR-03, SR-10) | entschieden (2026-10-28, Option 2) | WP-470, WP-329 |
| [E-94](#e-94) | RBA-05/STK-03: Recht für die Umwandlung von Mengenreservierungen (`reservations/<id>/convert`) | entschieden (2026-10-28, Option 1) | WP-277, WP-391 |
| [E-95](#e-95) | ENT-06/INV-03: Entitlement `barcode` an den Barcode-Feldern von Assets und Standorten | entschieden (2026-10-28, Option 2) | WP-289, WP-394 |
| [E-96](#e-96) | RBA-07: Feldrechte an Kanten (Scope beider Enden, Objektart) | entschieden (2026-10-28, Option 1) | WP-402 |
| [E-97](#e-97) | API-01/EXP-01: Formatwert `datev` des Exports und seine Sperre | entschieden (2026-10-28, Option 1) | WP-310, WP-422, WP-423, WP-326 |
| [E-98](#e-98) | AST-07: Schreibpfad für den Standortwechsel serieller Assets | entschieden (2026-10-28, Option 1) | WP-274, WP-388, WP-438, WP-441 |
| [E-99](#e-99) | TKT-01/E-39: Datenquelle der Queue-Ansicht „meine Teams“ | entschieden (2026-10-28, Option 1) | WP-290, WP-401, WP-446 |

### Weiter gültige Entscheidungen aus Phase 1

| Nr. | Thema | Status | Bedeutung für Phase 2 |
|---|---|---|---|
| [E-02](../entscheidungen.md#e-02) | Fehlende v2-Originaltexte (Verweise „wie v2“, „V §…“) | entschieden | Entschieden am 2026-10-28: Die Erklärung des Auftraggebers vom 2026-10-15 ersetzt die v2-Texte zu AUT-05, AUT-06, NET-04 und ABN-01 (a)/(b) (B4, A-02 erledigt). Sie gilt nach E-59 auch für G2. Nichts wird nachgereicht, und kein WP gleicht mit v2-Texten ab. WP-330 führt (a)/(b) in der Spalte „Status G2“ von `docs/acceptance/abn-01.md` mit diesem Nachweis. Den [A]-Anteil von UI-14 (Self-Service, Access-Reviews) umfasst B4 nicht; er liegt außerhalb von Phase 2, und für ihn gilt die Regel aus E-02: keine erfundenen Funktionen (A-22 erledigt). Keine Blockade für P2-WPs. |
| [E-03](../entscheidungen.md#e-03) | Umgang mit vorhandenem Code späterer Phasen im G1-Release | entschieden | `LateModules` (`backend/internal/server/modules.go`, WP-214) bleibt die Release-Einheit. WP-244, WP-337 und WP-338 stellen Schlüssel, Org-Gate und Release-Gate um: `disposal` wird P2, das neue Modul `gps` (GPS-Verlauf, LOC-09) bleibt als P4 abgeschaltet, P3, P4 und [A] bleiben aus. WP-462 macht G2 zum Standard von `RETICORA_MODULES` (leer = alle Module mit Phase P2); das G1-Verhalten bleibt als Testprofil. Prüfungen gegen den Release-Standard (WP-329, WP-331) setzen WP-462 voraus. Das Release-Gate neuer P2-Funktionen ohne eigenes Modul regelt E-61. |
| [E-04](../entscheidungen.md#e-04) | Teilbericht 03 nicht konsolidiert; N/P-IDs des Metamodells | entschieden | Metamodell-Befunde werden weiter aus Teil 03 zitiert; Indexnamen `idx_attr_<id>`; N/P-Texte siehe E-37. A-03 (Konsolidierung von Teil 03, seit G1 überfällig) übernimmt WP-464 (M2.0b, A-28), Voraussetzung von WP-331: GATE-01 für G2 braucht die Critical-Befunde LOC-10, LOC-11, MET-14, X-SCOPE und X-MANUAL, die nur in Teil 03 stehen. Mit Abschluss des WP sind A-03 und A-28 erledigt; den Text zu DB-01 fordert nach dem Beschluss vom 2026-10-28 niemand mehr an (E-37). |
| [E-10](../entscheidungen.md#e-10) | Tabellen ohne Org-Spalte und globale Katalogzeilen | entschieden | Neue Tabellen der Phase 2 erhalten eine Org-Spalte mit RLS. Globale Systemzeilen (Systemtypen, Systemtemplates, Systemsets, Lifecycle-Seeds) sind für die App-Rolle schreibgeschützt. Deshalb lassen sich Systemtypen (`ci_type.organization_id` NULL) nicht deaktivieren: 403 mit dem neuen Fehler `citype.ErrTypeReadOnly` aus WP-249. Eine Organisation ersetzt sie durch Klonen. Ein Ausblenden je Organisation gibt es nicht. |
| [E-11](../entscheidungen.md#e-11) | Semantik des Team-Scopes | entschieden | Es gilt die Schnittmenge nach TEN-05: Zeilen ohne Team bleiben für team-gescopte Rollen sichtbar, aber nicht schreibbar. Das gilt für Ticket-Teams, Teamzuweisungen, `document_link.team_id` (WP-267) und in Phase 2 auch für den CI- und Asset-Owner. `ci.owner_team_id` und `asset.owner_team_id` (WP-271, WP-280) sind die Team-Spalte; legt ein team-gescopter Principal an, ist ein `owner_team_id` aus seinem Scope Pflicht. Team-beschränkte Principals legen keine Dokument-Links auf Ziele ohne Team (CI, Asset, Location) an. CI-Kindtabellen (`ci_change`, `ci_contact`, `ci_field_value`, `network_interface`, `ip_address` …) erben den Team-Scope in Phase 2 nicht. In den Feldrechten (E-43) wirkt Team nur über `object_grant` mit Team als Empfänger. Einschränkung: Rollenzuweisungen kennen keinen Team-Scope (`scope_type` organization|client|site, `identity.Grant.Teams` wird nirgends befüllt). Die Team-Prädikate greifen deshalb erst, wenn ein Folge-WP RBA-03 `scope_kind team` umsetzt. Am 2026-10-28 nach Empfehlung bestätigt, mit den Phase-2-Zusätzen (A-31 erledigt). |
| [E-13](../entscheidungen.md#e-13) | RBA-02-Rollenmatrix und AUT-04-Schlüsselformat | entschieden | Rechte werden nur erweitert, wo Katalog oder Entscheidung sie tragen. Für Phase 2 gilt eine Ausnahme: Über den RBA-02-Text hinaus legt E-62 die neuen Schlüssel (RBA-05, Aktionsschlüssel), ihre Routen und die P2-Zeilen der Rollenmatrix fest. Grundlage ist WP-245. |
| [E-17](../entscheidungen.md#e-17) | CH15: Skalierungsziel bestätigen | entschieden | Entschieden am 2026-10-15 (Option 1): Budgets aus NFR-02 und NFR-03 bei 10.000 Objekten (CI + Asset) je Organisation; Headroom 50.000 funktionsfähig ohne Budgetzusage. Listen und Suche über etwa 25.000 Objekten sind eine bekannte Einschränkung; Folge-WPs sind WP-471 (Form von `total` nach E-60) und WP-472, beide vor WP-327. Messbasis von WP-453 und WP-327; WP-454 misst konservativ mit 10.000 CIs und 10.000 Assets (20.000 Objekte nach NFR-01). |
| [E-23](../entscheidungen.md#e-23) | Abgrenzung GLO-13 (reservierte Namensräume) zu CI-10 (Speichermodell) | entschieden | Instanzattribute liegen in `attributes._instance` (WP-256). Die vier Namensräume sind auch als Schlüssel von Sets, Templates und Regeln unzulässig. |
| [E-25](../entscheidungen.md#e-25) | API-05/JOB: verbindliche Pfade, Revert, Snapshot | entschieden | Vorbild für E-38: Alias mit `deprecated`, `x-deprecated-at`, `x-sunset` (mindestens 12 Monate) und `x-successor` für Pfade, die im G1-Release ausgeliefert sind. `jobs/<id>/revert` besteht; Phase 2 ergänzt den Reverter für Bulk-Jobs (WP-305). |
| [E-30](../entscheidungen.md#e-30) | Traceability-Prüfung: ab wann blockierend? | entschieden | [B]/[Q] blockieren seit WP-213 in jedem Lauf; erweitert durch E-48 um die Stufe `P2` für G2. |
| [E-34](../entscheidungen.md#e-34) | ENT-06 (V)/SIM-01: Planmatrix und Demo-Plan | entschieden | Entschieden am 2026-10-28 (Option 1, Planmatrix bestätigt), ergänzt durch E-44: Die Matrix aus E-44 ersetzt für die P2-Schlüssel die Ist-Pläne. Limits und das neue `max_storage_mb` folgen der Festlegung von E-44 (WP-244, WP-338, WP-363). |
| [E-35](../entscheidungen.md#e-35) | NFR-09: Inhalt der Subprozessorenliste | entschieden | Entschieden am 2026-10-28: Die Erklärung des Auftraggebers vom 2026-10-15 ersetzt die Subprozessorenliste (B6, A-11 erledigt). Sie gilt nach E-59 auch für G2, auch für die neuen Datenkategorien der Phase 2 (Dokumentenspeicher, Unterschriften, Entsorgungsnachweise, Importdateien). Die Liste wird nicht nachgereicht. |
| [E-36](../entscheidungen.md#e-36) | DB-05: Zeilen für Vertrag, installierte Software und Health-Findings | entschieden | WP-269 liefert die Vertragszeile von DB-05; WP-369 macht den Vertrag zum Referenz- und Linkziel. WP-280 spiegelt den Owner nach DB-05 vom Asset ans CI; der Findings-Anteil von Health bleibt [P3] (LCY-06), installierte Software kommt mit dem Agent [P4]. |
| [E-56](../entscheidungen.md#e-56) | NFR-03: Reihenfolge der Verarbeitung bei parallelen Ingest-Spuren | entschieden | Jeder Pfad der Phase 2, der CIs anlegt oder über Identitätsmerkmale abgleicht, nimmt die Identitäts- und `max_cis`-Sperre und zählt in der Transaktion neu (K21, WP-467). Das ist der Import (WP-302, über ihn auch WP-303). WP-304, WP-272 und WP-300 legen keine CIs an; Interface-IPs sind keine Identitätssperren. Audit und Outbox schreiben zuletzt vor dem Commit. Den Outbox-Zähler je Organisation liefert WP-466 (`docs/betrieb/ingest-spuren.md`). |
| [E-57](../entscheidungen.md#e-57) | Impact-Antwort: kompakte Form und API-01 | entschieden | Erweiterungen von Impact und Topologie (WP-294, WP-297, WP-298) sind additiv und im Sparse-Fieldset `fields=` verfügbar; die Standardform bleibt. Die Oberfläche fragt die kompakte Form über `fields=` an (`IMPACT_RESULT_FIELDS` in `frontend/src/api/cmdb.ts`). Die Budgets `impact` und `impact_full` aus WP-335 gelten weiter (K23). Option C nur nach 12 Monaten Deprecation. |
| [E-58](../entscheidungen.md#e-58) | Referenzumgebung des G1-Lasttests (SR-06) | entschieden | Entschieden für G1 (Option 1); GATE-03 gilt nur für G1. WP-327 übernimmt das Nachweisprotokoll als Festlegung dieses Plans: `load.yml`, ein Job je Szenario mit frischem Seed und `ANALYZE`, 4 Spuren, 4 vCPU. Dedizierter Referenzhost vor GA (SR-06). |
| [E-28](../entscheidungen.md#e-28) | Pentest: Dienstleister, Umfang, Termin; NFR-08 | entschieden | B3 ist durch die Erklärung des Auftraggebers vom 2026-10-15 nachgewiesen. Nach dem Beschluss vom 2026-10-28 ersetzt sie Zusammenfassung und Befundliste (A-07 erledigt) und gilt nach E-59 auch für G2. Nichts wird nachgereicht. WP-329 vermerkt diesen Nachweis in der Nachverfolgung G1. |

## E-37

**Thema:** Fehlende Metamodell-Texte MET-20, MET-30 bis MET-34, MET-40 bis MET-44 (Sets, Templates, Regeln, berechnete Attribute)

**Status:** entschieden (2026-10-28, Empfehlung: Option 1 mit Option 2; die Texte werden nicht geliefert, daher gilt Option 2 als Regel)

**Festlegung:** Beschluss des Auftraggebers vom 2026-10-28: Die Texte MET-20, MET-30 bis MET-34 und MET-40 bis MET-44 werden nicht geliefert. Option 2 ist die verbindliche Regel für Phase 2. Die Metamodell-WPs setzen nur die Bestandteile um, die aus den vorliegenden Querverweisen folgen: MET-10 „validation (JSON-AST)“ und „computed (MET-44)“, MET-16 „Attribute Sets (in definierter Reihenfolge)“ und „Templates (in definierter Reihenfolge)“, MET-45 Regelsprache, MET-53 „Draft → Test → Publish für Typen, Templates, Sets, Regeln, Layouts“, UI-11 „Sets, Templates, Regeln (Klick-Builder)“, API-06 „Sets, Templates/Versionen, Regeln“, VIE-02 „Garantie läuft bald ab (MET-44)“, JOB-02 „Computed-Attribute-Refresh“ und ABN-01 (c) „Attribute Set zuordnen; zwei Templates kombinieren; Regel im Klick-Builder definieren; Template-Version migrieren, Overrides bleiben erhalten“. Daraus folgt:

- Attribute Sets (WP-250, WP-341, Set-Editor WP-429) folgen nur MET-16 (Reihenfolge nach der Typkette), MET-53 und ABN-01 (c). Overrides und Versionen je Set sind nicht Bestandteil.
- Templates führen keine eigenen Attributschlüssel ein. Template-Versionen enthalten nur einengende Overrides vorhandener Definitionen des Typs, seiner Typkette oder globaler Definitionen (`scope = 'global'`) in Reihenfolge, keine Sets (WP-251, WP-314). Systemtemplates tragen die `systemTemplate`-Schlüssel der Collector-Profile (DIS-02, `collector/profiles/data/*.yaml`) und bleiben ohne Items. Eigene Template-Schlüssel wären ein neuer Auftrag. Sie erweitern `attribute_definition` um den scope `template`. Das ändert `attribute_definition_scope_check` (aus 000055 als `ci_type_attribute_scope_check`, in 000105 umbenannt; von WP-250 um `set` erweitert) und `attribute_definition_scope_type_check` (000105) und schließt die Template-Definitionen aus `generate_attribute_schema` und aus der Typebene in `ResolveFields` aus.
- Die Template-Migration je CI (WP-253, WP-344) stützt sich nur auf ABN-01 (c) und MET-16.
- Der Regelkatalog (WP-348, WP-257, Oberfläche WP-433) kommt ohne Warnstufe `severity` aus: Jede aktive Katalogregel blockiert mit 422. Ein nicht blockierender Warnkanal ist nicht Bestandteil der Phase 2. Die Bedingungsgrammatik (WP-347) folgt MET-45.
- Berechnete Attribute (WP-258, WP-349, WP-350, Ausdrucksmodus WP-434) folgen MET-10, VIE-02 und JOB-02. „Garantie läuft bald ab“ setzt WP-307 über das berechnete CI-Attribut `warranty_days_left` (`entity_kind` `ci`) mit `lte` 30 um; die Schwelle 30 Tage ist eine Planannahme ohne Katalogwert.
- `metamodel.published`: WP-246 legt nur den Rahmen-`$def` fest. Die Fachfelder ergänzt WP-261 nach den Modellen aus WP-250, WP-251, WP-257 und WP-258; dasselbe gilt für das Format v2 von Export und Import (WP-262, WP-354).
- WP-330 prüft in ABN-01 (c) nur die aus dieser Regel abgeleiteten Bestandteile.
- Die N/P-IDs tragen in `docs/plan/phase2/abdeckung.csv` den Tag „nicht geliefert“ und `abschluss_wp` „–“. Sie erhalten keine Traceability-Zeilen und sind von der Vollständigkeit ausgenommen (E-48). Die WPs tragen die verweisenden IDs (MET-10, MET-16, MET-45, MET-53, UI-11).
- DB-01 (ebenfalls A-03) hat keinen Text und in Phase 2 kein WP (A-28, E-04).

**Aufgabe:** **erledigt 2026-10-28** A-15: Der Beschluss des Auftraggebers ersetzt die Lieferung. Die Texte MET-20, MET-30 bis MET-34 und MET-40 bis MET-44 werden nicht nachgeliefert; Option 2 gilt als Regel.

**Frage:** Elf Metamodell-IDs sind nur in der Vollständigkeitsliste enthalten; der Wortlaut fehlt (Audit Teil 03). Sie betreffen vermutlich Attribute Sets, Templates mit Versionen, den Regelkatalog und berechnete Attribute, also Kernbestandteile der Phase 2. Der Katalog nennt MET-44 nur als Querverweis.

**Optionen:**

1. Texte nachliefern; die WPs prüfen ihre Akzeptanzkriterien vor Beginn gegen den Text.
2. Umsetzung ausschließlich nach den Querverweisen (Übergangsregel); die IDs bleiben N/P und zählen nicht für GATE-02.
3. Phase 2 ohne Sets, Templates, Regeln und berechnete Attribute ausliefern (verletzt MET-16, MET-53, UI-11 und ABN-01 c).

**Empfehlung:** Option 1 mit Option 2 als Übergangsregel. Option 3 scheidet aus, weil MET-16, MET-53 und ABN-01 (c) die Bestandteile verbindlich verlangen. Die Einschränkungen der Übergangsregel (keine eigenen Template-Schlüssel, keine Overrides je Set, keine Warnstufe) halten die Modelle offen für die Texte.

**Blockiert / Auswirkung:** Die Regel bestimmt die Akzeptanzkriterien von WP-250, WP-341, WP-251, WP-342, WP-252, WP-253, WP-344, WP-347, WP-348, WP-257, WP-258, WP-349, WP-350, WP-353, WP-261, WP-262, WP-354, WP-246, WP-307, WP-429, WP-314, WP-431, WP-432, WP-433, WP-434, WP-325, WP-326 und WP-330. Die N/P-IDs sind dauerhaft von GATE-02 ausgenommen (E-48, WP-243, WP-331). Kein WP wartet auf Texte.

**Quelle:** docs/audit/03-datenmodell-standorte-metamodell.md:61–71; docs/plan/entscheidungen.md#e-04 (A-03); docs/plan/phase2/abdeckung.csv (MET-20 bis MET-44); collector/profiles/profiles.go (`SystemTemplate`); backend/migrations/000055_cmdb_extensions.up.sql (`ci_type_attribute_scope_check`); backend/migrations/000105_attribute_definition.up.sql (`attribute_definition_scope_check`, `attribute_definition_scope_type_check`)

**Betroffene IDs:** ABN-01, API-06, API-09, AUD-07, DIS-08, DOD-01, EVT-03, GATE-01, GATE-02, GATE-05, GLO-11, INS-01, INS-04, JOB-02, JOB-04, MET-06, MET-10, MET-16, MET-20, MET-30, MET-31, MET-32, MET-33, MET-34, MET-40, MET-41, MET-42, MET-43, MET-44, MET-45, MET-53, MET-54, OPS-06, OVR-03, SEQ-01, SIM-01, TEN-09, TST-01, TST-04, UI-11, VIE-02

**Betroffene WPs:** WP-243 (`p2-trace`), WP-246 (`p2-events`), WP-464 (`g1-befunde-teil03`), WP-250 (`met-sets`), WP-341 (`met-sets-compose`), WP-251 (`met-templates`), WP-342 (`met-ci-templates`), WP-252 (`met-compose-rules`), WP-253 (`met-migrate`), WP-344 (`met-migrate-job`), WP-257 (`met-rules`), WP-347 (`met-rules-ast`), WP-348 (`met-rules-catalog`), WP-258 (`met-computed`), WP-349 (`met-computed-write`), WP-350 (`met-computed-refresh`), WP-261 (`met-publish`), WP-353 (`met-test`), WP-262 (`met-export-all`), WP-354 (`met-import`), WP-307 (`vie-shared`), WP-429 (`ui-meta-sets`), WP-314 (`ui-meta-templates-publish`), WP-431 (`ui-meta-drafts-publish`), WP-432 (`ui-meta-ci-templates-migrate`), WP-433 (`ui-rule-catalog`), WP-434 (`ui-rule-computed`), WP-325 (`g2-seed`), WP-326 (`g2-tst01`), WP-330 (`g2-abnahme`), WP-331 (`g2-gate`)

## E-38

**Thema:** API-06: REST-Ressourcen der Phase 2 und Ablösung abweichender Bestandspfade

**Status:** entschieden (2026-10-28, Option 1: Pfadinventar)

**Festlegung:** Verbindliches Pfadinventar (Option 1, vom Auftraggeber am 2026-10-28 bestätigt) nach dem Stand von main `3a9669a` und den Neufassungen der WPs. Alle Pfade liegen unter `/api/v1`; Pfadparameter sind hier als `<id>` geschrieben. Ressourcen sind englische Plural-Substantive wie in API-05. WP-247 trägt das Inventar als Tabelle in diese Entscheidung ein: je Zeile Klasse, „im G1-Release ausgeliefert“ (ohne `x-phase`) und das zuständige WP.

**(a) Besteht und bleibt.** Phase 2 ergänzt hier nur Felder, Operationen oder Verhalten.
- G1-Pfade ohne `x-phase`: `jobs/<id>/revert` (JOB-03 [B], WP-182, E-25; Phase 2 liefert nur den Reverter der Jobart `ci.bulk` in WP-305); `ci-types/templates` (Katalog der Typvorlagen mit Schema `CIType`, `backend/internal/citype/templates.go`; kein Template nach MET-16, weder Alias noch Vorgänger von `templates`); `ci-types/export` (Format v2 nur als Opt-in `format_version=2`, die v1-Antwort bleibt; WP-262); `cis/<id>/field-definitions*` (Definitionen der Instanzattribute nach MET-14, WP-045; ohne Umbenennung und ohne Alias, die Werte liegen unter `cis/<id>/instance-attributes`, siehe (c)); `history/snapshots` und `history/<entityType>/<id>`; `cis/<id>/lifecycle-transitions` (neu `GET` mit den erlaubten Übergängen, WP-355); `attribute-definitions` (neu die Aktion `…/<id>/reindex`, siehe (c)).
- P2-Pfade mit `x-phase: P2`: `assets`, `assets/<id>`, `assets/<id>/lifecycle-transitions` (neu `GET`, WP-355), `assets/<id>/children` (ab WP-374 die Kind-CIs aus `ci.parent_ci_id` mit `child_mode`, neue Antwortform, cursor-paginiert), `assignments` und `assignments/<id>` nur lesend (Projektion aus `stock_movement`; die Zuweisungen eines Assets liefert `assignments?asset_id=`, ein Pfad `assets/<id>/assignments` entsteht nicht; WP-378), `reservations` (`POST` bleibt neben `assets/<id>/reserve`, beide über denselben Dienst, WP-277), `reservations/<id>`, `reservations/<id>/release`, `stock-movements` (`GET` mit Filtern; `POST` für Mengenbuchungen und den Asset-Umzug `transfer`, siehe Regel „Schreibseite der Bewegungen“), `consumables`, `consumables/<id>`, `orders`, `orders/<id>`, `orders/<id>/submit|approve|reject`, `orders/<id>/items` (nur im Status `draft`, bucht nie; der Wareneingang läuft über `orders/<id>/receive`, WP-286), `stocktakes`, `stocktakes/<id>`, `stocktakes/<id>/scans`, `stocktakes/<id>/complete`, `documents` (neu Listenfilter `entity_type` und `entity_id`, kein neuer Pfad, WP-443), `documents/<id>`, `documents/<id>/content` (`GET` liefert die aktuelle Version oder `?version=n`; `PUT` bleibt als kompatibler Upload neben `POST documents/<id>/versions`, WP-363), `documents/<id>/links`, `tickets`, `tickets/<id>`, `tickets/<id>/comments`, `tickets/<id>/sla` und `slas/breaches` (additive Felder, WP-292), `slas`, `slas/<id>`.
- `disposal-records` und `disposal-records/<id>` tragen heute `x-phase: P4`; das Modul `disposal` wird P2 (MGT-02 [P2], E-44, WP-338).

**(b) Besteht und wird ersetzt.**
- G1-Pfade (ohne `x-phase`, im G1-Release ausgeliefert) bleiben mindestens 12 Monate als Alias mit `deprecated: true`, `x-deprecated-at`, `x-sunset` und `x-successor` in `api/openapi.yaml` (E-25, WP-167; Header über `ParseDeprecations` und `writeDeprecation` in `backend/internal/middleware/openapi.go`, Alias durch Doppelregistrierung im Fach-Handler nach WP-168). Die Abkündigung liefert das WP, das den Nachfolger liefert:
  - `lifecycle-definitions*` → `lifecycle-models*` (WP-355; die Tabellen behalten ihre Namen, das ci-type-DTO führt `lifecycle_model_id` neben dem veralteten `lifecycle_definition_id`)
  - `cis/<id>/state?at=` → `cis/<id>/as-of?at=` (WP-425; derselbe Handler)
  - `cis/<id>/parent` → `cis/<id>` mit `parent_ci_id` (WP-374; die Antwort bleibt bis zum Sunset kompatibel)
  Der Nachfolger eines G1-Pfads trägt kein `x-phase`, solange der Alias ausgeliefert wird.
- P2-Pfade (mit `x-phase: P2`, im G1-Release nie registriert, E-03) brauchen keinen Alias. Ruft das Frontend einen solchen Pfad bis zu einem späteren WP noch auf, führt ihn das liefernde WP als deprecated Alias. Dann gelten dieselben Felder wie bei G1-Aliasen, denn `ParseDeprecations` verlangt für jede Abkündigung `x-sunset` ≥ 12 Monate (`TestDeprecationsNeedTwelveMonthsNotice`). Das liefernde WP stellt `frontend/src/api/client.ts` bzw. `frontend/src/api/cmdb.ts` um oder nennt das Oberflächen-WP, das es tut. Ausnahme: `assets/<id>/movements` hat keine Aufrufer im Frontend (`inventoryApi.assetMovements` entfällt). WP-247 kündigt den Pfad trotzdem schon ab, weil sein Nachfolger `stock-movements?asset_id=` heute ausgeliefert wird.
  - Mit deprecated Alias: `assets/<id>/movements` → `stock-movements?asset_id=` (WP-247); `consumables/<id>/movements` → `stock-movements?consumable_id=` und `POST stock-movements` (WP-388; `in` → `receipt`, `out` → `retrieval`); `inventory/items*` → `consumables*` und `consumables/<id>/stock` (WP-389; die Antwortform `QuantityItem` bleibt); `inventory/availability` → `consumables/<id>/availability` für Mengenartikel und `active_reservation` in `GET assets/<id>` für Assets (WP-391, WP-382); `stocktakes/<id>/difference` → `stocktakes/<id>/differences` (WP-287); `assets/<id>/state?at=` → `assets/<id>/as-of?at=` (WP-427). Bis zum Sunset von `inventory/items*` gelten `quantity_item_id` und `item_kind: quantity_item` als deprecated Eingabe- bzw. Filteraliase an `reservations`, `stock-movements` und `inventory/availability` (WP-389, WP-284).
  - Ohne Alias: `POST assignments`, `DELETE assignments/<id>` und `assignments/<id>/return|transfer` → `assets/<id>/assign|return|transfer` (WP-275, Entfernen in WP-378); `assets/<id>/label.svg` → `assets/<id>/label?format=svg|pdf` (WP-395); `compositions*` entfällt (WP-374).
  - `inventory/items/<id>/convert` hat keinen Nachfolger (keine Katalogfunktion; ein serielles Asset entsteht über `POST assets` oder den Wareneingang). Der Pfad arbeitet ab WP-389 auf `consumable`. Er trägt keine Abkündigung, weil `x-successor` einen Nachfolger verlangt, und entfällt mit dem Sunset von `inventory/items*`. Nach WP-320 ruft das Frontend ihn nicht mehr auf.
  - `inventory/availability` liefert bis zum Sunset die org-weiten Asset-Buckets für den Tab „Verfügbarkeit“ der InventoryPage (WP-320). Phase 2 legt für die org-weite Asset-Verfügbarkeit keinen Nachfolger an. Der Tab entfällt mit dem Sunset, wie `inventory/items/<id>/convert`.

**(c) Neu.** WP-247 legt jede neue Operation als Gerüst an; das genannte WP implementiert sie.
- Metamodell: `attribute-sets` und `ci-types/<id>/attribute-sets` (`GET`/`PUT`, geordnete Liste; WP-250); `templates` mit `…/clone`, `…/versions`, `…/versions/<version>` (`PUT`) und `…/versions/<version>/publish` (WP-251) sowie `…/migrations` (`POST`, 202 mit `Location: /api/v1/jobs/<id>`, Job `template.migrate`, WP-344); `cis/<id>/templates` (WP-342); `rules` (WP-348); `layouts` (WP-259); `cis/<id>/form` und `ci-types/<id>/form` (WP-351); `metamodel/drafts` mit `…/items` und `…/items/<item_id>` (WP-260), `…/test` (`POST` startet den Lauf, `GET` liefert den Bericht, WP-353) und `…/publish` (WP-261); der Query-Parameter `draft` (Antwort 202) an den Schreibrouten von `ci-types`, `ci-fields`, `attribute-definitions`, `attribute-sets`, `templates`, `rules` und `layouts`, Ausnahmen in `metamodel.DraftExemptRoutes` (WP-352); `metamodel/import` (legt einen Draft an; der Export bleibt `ci-types/export`, WP-354); `attribute-definitions/<id>/reindex` (WP-428); die Nachfolger `lifecycle-models` (WP-355) und `cis/<id>/as-of` (WP-425) aus (b); `cis/<id>/instance-attributes` (Werte-Ressource: `GET` Liste, `PUT`/`DELETE` je Schlüssel, WP-256).
- Tags und Feldrechte: `tags` mit `…/<id>/merge` und `PUT cis/<id>/tags` (WP-385); `PUT assets/<id>/tags` (WP-387); `field-permissions` mit `…/object-grants` und `…/sensitive-fields` (WP-358).
- Assets und Verträge: `assets/<id>/cis` und `assets/<id>/cis/<ci_id>` (WP-272); `assets/<id>/assign`, `…/return`, `…/transfer` (WP-275); `assets/<id>/reserve` und `reservations/<id>/convert` (WP-277); `assets/<id>/label` (WP-395); `assets/<id>/as-of` (WP-427); `assets/bulk` (`POST`, BLK-02, WP-413; `cis/bulk` bleibt Bestand aus WP-181); `stock-movements/<id>/signature` (`GET`, Verifikation, WP-380); `suppliers` (WP-368); `contracts` (WP-269).
- Lager und Inventur: `consumables/<id>/stock` (WP-283); `consumables/<id>/availability` (WP-391); `orders/<id>/place|cancel|close` (WP-286); `orders/<id>/receive` (WP-392); `stocktakes/<id>/start`, `…/items`, `…/differences` und `…/cancel` (WP-287); `labels/lookup` (WP-394); `labels/batch` und `labels/batch/<job_id>/download` (WP-395).
- Dokumente: `documents/<id>/versions` (`GET`, `POST`) und `documents/usage` (WP-363); `DELETE documents/<id>/links/<link_id>` (WP-267).
- Tickets: `tickets/<id>/links` mit `…/<link_id>` (WP-396); `tickets/<id>/attachments` (`GET`, Dokumente über `document_link`, WP-397); `business-calendars` (WP-400); `review-items/<id>/ticket` (`POST`, kein Alias unter `discovery/review-items`, WP-291).
- Import: `import-profiles` (WP-406); `imports` mit `POST`, `…/<id>` und `…/<id>/report` sowie `imports/preview` (`POST` mit `document_id` und optional `sheet`, synchrone Spaltenvorschau ohne Job) (WP-301). `imports` hat keine Sammel-GET-, PATCH- oder DELETE-Operation: Die Liste läuft über `jobs?kind=import.run`, der Abbruch über `DELETE jobs/<id>`.
- Netz und Graph: `subnets/<id>/reservations` (wörtlich NET-09) und `subnets/<id>/reservations/<ip_id>` (WP-299); `relationships/<id>/changes` (WP-296).

**(d) Nicht Phase 2:** `asset-locations` und `assets/<id>/locations` sind der GPS-Verlauf (`backend/internal/location`, LOC-09 [P4]), nicht AST-07. Sie wechseln in ein eigenes P4-Modul und bleiben im G2-Release abgeschaltet (E-44, WP-338).

**Regeln.**
- Gerüst: WP-247 legt jede neue Operation aus (c) in `api/openapi.yaml` mit `x-status: planned` an (Pfad- oder Operationsebene). Gelesen wird der Wert von `speccheck.Planned`, das WP-247 neu anlegt; ein anderer Wert ist ein Fehler. `TestRoutesAndSpecificationAreInParity` nimmt geplante Operationen aus dem Abgleich und schlägt fehl, wenn eine geplante Operation registriert ist. Das liefernde WP ersetzt das Gerüst durch die Fachschemas, entfernt `x-status: planned` und streicht nicht umgesetzte Gerüstoperationen. Bestandspfade bekommen kein Gerüst. Zum G2-Release trägt keine Operation mehr `x-status: planned`; WP-331 prüft das.
- `x-phase: P2` tragen genau die Pfade unter einem Präfix eines P2-Moduls in `LateModules` (`backend/internal/server/modules.go`, `TestLateRoutesAreMarkedInSpecification` in `modules_test.go`), auch im Gerüst. Die Module der neuen Präfixe legt E-61 fest (Option 1): `metamodel` mit `attribute-sets`, `ci-types/<id>/attribute-sets`, `templates`, `rules`, `layouts`, `metamodel` und `field-permissions`; `import` mit `imports` und `import-profiles`; `suppliers`, `contracts` und `labels` bei `inventory`; `business-calendars` und `review-items/<id>/ticket` bei `ticketing`. Die übrigen Unterpfade von Kernressourcen (`cis/<id>/…`, die übrigen `ci-types/<id>/…`, `attribute-definitions/<id>/reindex`, `subnets/<id>/reservations`, `relationships/<id>/changes`) sind Kern und tragen kein `x-phase`. Die Zuordnung setzt das Fach-WP mit der ersten Route um (`metamodel` ab WP-250). Die Nachfolger von G1-Pfaden (`lifecycle-models`, `cis/<id>/as-of`) und `cis/<id>/instance-attributes` bleiben ohne `x-phase`, solange ihr G1-Gegenstück ohne `x-phase` ausgeliefert wird. `templates` ist kein Nachfolger eines G1-Pfads und folgt E-61 wie `attribute-sets`.
- `x-feature` gibt es bisher weder in `api/openapi.yaml` noch in `speccheck` (K12). Phase 2 setzt es nur an Pfaden mit `x-phase`, denen `entitlement.RequiredFeature` einen Feature-Key zuordnet. Heute ist das `RequiredFeature(path)` über `gatedRoutes` und `gatedSuffixes` in `backend/internal/entitlement/middleware.go`. Ab WP-337 ist es `RequiredFeature(method, path)` über die Zuordnungstabelle mit Methoden- und Suffixregeln. `x-feature` steht auf Pfadebene. Wo eine Methodenregel für eine Operation einen anderen Schlüssel festlegt, steht es zusätzlich auf Operationsebene (`POST stock-movements` → `stock`). WP-247 setzt den Wert am Gerüst; WP-338 prüft ihn je Operation gegen `RequiredFeature`. Pfade ohne Feature-Key tragen kein `x-feature`.
- Für jede Operation mit `deprecated: true` ist `x-successor` ein dokumentierter Pfad mit mindestens einer nicht geplanten Operation, dessen `x-phase` leer oder gleich der des Alias ist (`TestDeprecationSuccessorsAreServed` in WP-247).
- Schreibseite der Bewegungen: Eine Operation trägt genau ein Recht (`PermissionForRoute` in `backend/internal/server/authz.go`) und einen Feature-Key. Deshalb trägt `POST stock-movements` ab WP-388 das Recht `stock:write` und das Feature `stock` und bucht Mengenartikel (`receipt`, `retrieval`, `correction`, `transfer`). Den Standortwechsel eines seriellen Assets bucht dieselbe Route als `transfer` mit `asset_id`; der Handler prüft dafür zusätzlich `asset:move` (E-98, Option 1). Andere Arten mit `asset_id` ergeben 422. `assignment`, `return`, `reservation`, `release` und `disposal` entstehen nur in ihren Fachpfaden (422 `movement-type-reserved`). Vor WP-388 nimmt `POST stock-movements` Asset-Bewegungen mit `asset:move` an (WP-274). Den Feature-Key `stock` verlangt die Route ab WP-337. Organisationen ohne `stock` buchen über diese Route deshalb ab WP-337 keine Bewegungen, auch keinen Asset-Umzug. Die Oberfläche für das Verschieben folgt E-98 (WP-438).
- Felder an G1-Antworten ändern sich nur additiv (API-01, E-57), etwa `CIAssetFacts` in `GET cis/<id>`. Ein umbenanntes Feld bleibt mindestens 12 Monate als `deprecated` neben dem neuen.
- Ereignisse: Die abonnierbaren Altnamen `inventory.movement` und `asset.movement` bleiben als veraltete Aliase von `stock.movement` in `webhook.ValidEvents`, mit derselben Frist wie Pfade; `composition.*` wird als veraltet markiert (WP-374).
- Jede neue oder geänderte Operation ändert `backend/internal/server/testdata/route_permissions.txt`; neue Listen sind cursor-paginiert (K6).

**Aufgabe:** **erledigt 2026-10-28** A-16: Der Beschluss des Auftraggebers bestätigt das Pfadinventar. P2-Bestandspfade ohne G1-Auslieferung entfallen ohne Alias, die deprecated P2-Aliase bleiben bis zum `x-sunset`. `cis/<id>/state` und `assets/<id>/state` gehen auf `…/as-of`. `inventory/items/<id>/convert` und der Tab „Verfügbarkeit“ entfallen mit dem Sunset von `inventory/items*` bzw. `inventory/availability`. `cis/<id>/field-definitions` bleibt neben `cis/<id>/instance-attributes`, `ci-types/templates` bleibt Katalog der Typvorlagen, die additiven Bestandspfade bleiben.

**Frage:** API-06 nennt die Ressourcen der Phase 2 nur als deutsche Sammelbegriffe („Sets, Templates/Versionen, Regeln, Layouts, Publish, Tags, Lifecycles, Assets, Verträge, Lager, Stocktake, Reservierungen, Bewegungen, Tickets, Dokumente, Importe“). Der Bestand führt 48 Pfade mit `x-phase: P2` aus Phase 1 (WP-214), die teils andere Namen tragen, sowie G1-Pfade, deren Nachfolger Phase 2 liefert. Welche Pfade gelten, und wie werden abweichende Bestandspfade abgelöst?

**Optionen:**

1. Pfadinventar wie oben: englische Plural-Substantive wie API-05; Alias mit Deprecation und Sunset für G1-Pfade (E-25); nie freigegebene P2-Pfade entfallen ohne Alias, außer das Frontend nutzt sie bis zu einem späteren WP (dann deprecated Alias mit Sunset ≥ 12 Monate).
2. Alle abweichenden Bestandspfade 12 Monate als Alias führen, auch die nie freigegebenen P2-Pfade (Mehraufwand ohne externe Nutzer).
3. Bestandspfade beibehalten und nur neue Ressourcen ergänzen (uneinheitliche Benennung, widerspricht der Logik von E-25 für API-06).
4. Andere Benennung durch den Auftraggeber.

**Empfehlung:** Option 1. Die P2-Bestandspfade waren im G1-Release nie registriert (E-03, `RETICORA_MODULES` leer); externe Nutzer gibt es nur für G1-Pfade. Ein Einschalten der P2-Module per `RETICORA_MODULES` vor G2 ist nicht unterstützt; die Release-Notes der Phase 2 vermerken das (WP-331). P2-Aliase mit Sunset entstehen nur, wo die eigene Oberfläche den Pfad bis zu einem späteren WP noch aufruft. Dazu kommt `assets/<id>/movements`, dessen Nachfolger schon ausgeliefert wird (WP-247).

**Blockiert / Auswirkung:** Das Inventar ist für WP-247, WP-338, WP-337, WP-331 und alle WPs mit neuen oder ersetzten Routen, u. a. WP-250, WP-251, WP-342, WP-344, WP-256, WP-348, WP-259, WP-351, WP-260, WP-352, WP-353, WP-261, WP-354, WP-262, WP-428, WP-355, WP-358, WP-385, WP-387, WP-272, WP-374, WP-274, WP-275, WP-378, WP-379, WP-277, WP-382, WP-380, WP-368, WP-269, WP-283, WP-388, WP-389, WP-284, WP-391, WP-286, WP-392, WP-287, WP-393, WP-394, WP-395, WP-267, WP-363, WP-443, WP-396, WP-397, WP-291, WP-292, WP-400, WP-299, WP-296, WP-406, WP-301, WP-413, WP-305, WP-425 und WP-427; die Oberflächen-WPs der M2.9; `frontend/src/api/client.ts`, `frontend/src/api/cmdb.ts`, `frontend/src/api/generated/schema.d.ts` verbindlich. WP-247 startet ohne weitere Freigabe.

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:4 (API-01), :14 (API-06); docs/plan/entscheidungen.md#e-25; api/openapi.yaml (`x-phase`, `deprecated`, `x-successor`); backend/internal/server/modules.go (`LateModules`), modules_test.go (`TestLateRoutesAreMarkedInSpecification`); backend/internal/server/authz.go (`PermissionForRoute`, `writePermissionOverrides`, `routeRules`); backend/internal/server/parity_test.go; backend/internal/api/speccheck/speccheck.go; backend/internal/entitlement/middleware.go (`RequiredFeature`, `gatedRoutes`, `gatedSuffixes`); backend/internal/middleware/openapi.go (`ParseDeprecations`), openapi_test.go (`TestDeprecationsNeedTwelveMonthsNotice`); backend/internal/movement/handler.go; backend/internal/citype/templates.go; backend/internal/location/handler.go; frontend/src/api/cmdb.ts, frontend/src/api/client.ts

**Betroffene IDs:** API-06, API-09, AST-01, AST-02, AST-03, AST-04, AST-05, AST-06, AST-07, AUD-07, AUD-08, BLK-01, BLK-02, CI-08, DIS-08, DOD-01, EVT-02, EVT-03, GATE-01, GATE-02, GATE-05, GLO-11, GQL-03, IMP-IO-01, INS-01, INS-04, INV-01, INV-03, JOB-03, JOB-04, LCY-02, MET-14, MET-16, MET-50, MET-52, MET-53, MET-54, MGT-01, MGT-12, NET-09, OPS-06, RBA-05, RBA-06, REL-09, SEQ-01, STK-01, STK-02, STK-03, STK-04, STK-05, TEN-02, TEN-09, TKT-01, TKT-02, TST-04, UI-11, UI-14, UI-15

**Betroffene WPs:** WP-337 (`p2-feature-gates`), WP-338 (`p2-module-gates`), WP-245 (`p2-permissions`), WP-247 (`p2-paths`), WP-250 (`met-sets`), WP-251 (`met-templates`), WP-342 (`met-ci-templates`), WP-344 (`met-migrate-job`), WP-256 (`met-instance`), WP-348 (`met-rules-catalog`), WP-259 (`met-layouts`), WP-351 (`met-form`), WP-260 (`met-drafts`), WP-352 (`met-drafts-capture`), WP-261 (`met-publish`), WP-353 (`met-test`), WP-262 (`met-export-all`), WP-354 (`met-import`), WP-355 (`met-lifecycle-model-api`), WP-428 (`met-index-status`), WP-358 (`fr-admin`), WP-267 (`doc-model`), WP-363 (`doc-versions`), WP-443 (`doc-entity-filter`), WP-269 (`ast-supplier-contract`), WP-368 (`ast-supplier`), WP-272 (`ast-ci-link`), WP-374 (`ast-composition-legacy`), WP-274 (`ast-movement`), WP-275 (`ast-assignment`), WP-378 (`ast-assignment-paths`), WP-379 (`ast-assignment-projection`), WP-380 (`ast-signature-verify`), WP-277 (`ast-reservation`), WP-382 (`ast-reservation-scope`), WP-385 (`ci-tags-api`), WP-387 (`ast-tags`), WP-283 (`stk-consumable-model`), WP-388 (`stk-stock-bookings`), WP-389 (`stk-quantity-item-merge`), WP-284 (`stk-availability`), WP-390 (`stk-quantity-item-drop`), WP-391 (`stk-availability-api`), WP-286 (`stk-orders`), WP-392 (`stk-orders-receive`), WP-287 (`inv-stocktake`), WP-393 (`inv-stocktake-complete`), WP-394 (`inv-lookup`), WP-395 (`inv-labels`), WP-396 (`tkt-links`), WP-397 (`tkt-attachments`), WP-291 (`tkt-from-review`), WP-292 (`tkt-sla-calendar`), WP-400 (`tkt-sla-calendar-api`), WP-296 (`rel-history`), WP-299 (`net-ip-reservation`), WP-301 (`io-import-model`), WP-406 (`io-import-profile`), WP-303 (`io-import-api`), WP-413 (`blk-ops-assets`), WP-305 (`blk-undo`), WP-311 (`gql-aggregates`), WP-425 (`aud-as-of-ci`), WP-427 (`aud-snapshot-asset`), WP-429 (`ui-meta-sets`), WP-314 (`ui-meta-templates-publish`), WP-430 (`ui-meta-draft-context`), WP-431 (`ui-meta-drafts-publish`), WP-432 (`ui-meta-ci-templates-migrate`), WP-316 (`ui-form-builder`), WP-435 (`ui-form-preview`), WP-318 (`ui-pickers-flows`), WP-438 (`ui-flows-stock`), WP-440 (`ui-contracts-suppliers`), WP-441 (`ui-asset-detail`), WP-320 (`ui-stock-orders-stocktake`), WP-444 (`ui-orders`), WP-445 (`ui-stocktake-labels`), WP-321 (`ui-documents-tickets`), WP-447 (`ui-sla-calendar-disposal`), WP-331 (`g2-gate`)

## E-39

**Thema:** TKT-01/ENT-06: Abgrenzung Ticketing Essential und Standard

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1, vom Auftraggeber am 2026-10-28 bestätigt. Der Katalog nennt nur die Paketstufen; die Abgrenzung lautet:

- Essential (`ticketing_essential`) umfasst Tickets mit Status, Priorität, Zuweisung an Nutzer und an `team_id` samt Listenfilter `team_id` (Bestand mit RLS-Team-Scope seit WP-029, WP-290), Objektbezug zu CI, Asset, Standort, Vertrag und Review-Item (WP-396), Kommentaren, Tags, Anhängen (WP-397) und Tickets aus Reviews (WP-291, WP-398, WP-399).
- Standard (`ticketing_standard`) umfasst zusätzlich SLA mit Business-Kalender und Feiertagen (TKT-02, WP-292, WP-400) sowie „Team-Queues“. Darunter fallen nur die Eskalation an Assignee und Teamleiter bei SLA-Verletzung (WP-401) und die Queue-Ansicht „meine Teams“.
- `slas`, `tickets/<id>/sla` und `business-calendars` sind an `ticketing_standard` gebunden. Die Zuordnung liefert WP-337 in der Routentabelle des Pakets `entitlement`. Sie ersetzt `gatedRoutes` und `gatedSuffixes` und prüft Suffixregeln vor Präfixregeln. Heute trifft `RequiredFeature` für `tickets/<id>/sla` zuerst das Präfix `/api/v1/tickets` (`ticketing`); Suffixregeln gibt es nur unter `/api/v1/cis/`. Die Schlüssel legt WP-244 an. In den Ticket-Hooks prüft `sla.TicketHooks` das Feature über einen `entitlement.Checker` (WP-401). Ein Gate für `team_id` gibt es nicht. Die Oberfläche zeigt SLA-Abschnitt und Kalenderpflege nur bei `ticketing_standard` (WP-446, WP-447).
- Die Datenquelle der Queue-Ansicht „meine Teams“ regelt E-99 (Filter `team_id=mine` an `GET /api/v1/tickets`, Ansicht hinter `ticketing_standard` in WP-446).
- Die Prioritäten bleiben `low`, `medium`, `high` und `critical` wie im Bestand (`ticket_priority_check` in 000013, `sla_priority_check` in 000026, Standardwert `medium` in `backend/internal/ticket/handler.go`). Pro [P4] bleibt außerhalb.

Die wörtliche Lesart (Teamzuweisung gehört zu Standard, Option 3) ist verworfen. Ein Gate für `team_id` an `POST`/`PATCH /api/v1/tickets` und im Listenfilter entsteht nicht; WP-290 bleibt dabei.

**Aufgabe:** **erledigt 2026-10-28** A-17: Der Beschluss des Auftraggebers bestätigt die Funktionsmatrix nach Option 1 (Teamzuweisung und Filter `team_id` Essential; SLA, Kalender, Eskalation und Queue-Ansicht „meine Teams“ Standard) und die Prioritäten `low`, `medium`, `high`, `critical`.

**Frage:** TKT-01 verlangt „Ticketing Essential und Standard“, und ENT-06 führt `ticketing_essential` (alle Pakete) und `ticketing_standard` (ab Standard) als getrennte Schlüssel, ohne die Funktionsgrenze zu benennen. Unklar ist besonders, ob „Team-Queues“ die bestehende Teamzuweisung (`ticket.team_id`, WP-029) einschließt. Prioritätswerte nennt der Katalog nicht.

**Optionen:**

1. Übergangsregel wie oben: Teamzuweisung und Filter `team_id` sind Essential; Standard sind SLA, Kalender, Eskalation und die Queue-Ansicht „meine Teams“.
2. Standard umfasst nur SLA (TKT-02); Eskalation und Team-Queues sind Essential.
3. Wörtliche Lesart: Teamzuweisung gehört zu Standard; Gate für `team_id` am Ticket und im Listenfilter.
4. Andere Abgrenzung durch den Auftraggeber.

**Empfehlung:** Option 1. Sie ordnet TKT-02 eindeutig dem Standard-Schlüssel zu und hält Essential ohne Kalenderpflege nutzbar. Die Teamzuweisung besteht mit RLS-Team-Scope und braucht kein Feld-Gate. Die Prioritäten bleiben, weil eine Umbenennung die SLA-Zuordnung je Priorität und den API-Vertrag (API-01) bräche.

**Blockiert / Auswirkung:** Die Abgrenzung gilt für WP-244, WP-337, WP-290, WP-396, WP-397, WP-291, WP-398, WP-399, WP-292, WP-400, WP-401, WP-446, WP-447, WP-452, WP-331. WP-290 startet ohne Gate für `team_id`.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:66–68; docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:97–106; backend/migrations/000013_phase2_tickets.up.sql; backend/migrations/000026_permissions_sla.up.sql; backend/internal/ticket/handler.go (Filter `team_id`); backend/internal/entitlement/middleware.go (`gatedRoutes`, `gatedSuffixes`, `RequiredFeature`); backend/internal/sla/handler.go (`TicketHooks`)

**Betroffene IDs:** API-06, DIS-08, DOD-01, ENT-02, ENT-06, EVT-03, GATE-01, GATE-02, GATE-05, GLO-11, INS-01, INS-04, OPS-06, RBA-05, SEQ-01, SIM-01, TEN-09, TKT-01, TKT-02, TST-04, UI-14

**Betroffene WPs:** WP-244 (`p2-entitlements`), WP-337 (`p2-feature-gates`), WP-290 (`tkt-core`), WP-396 (`tkt-links`), WP-397 (`tkt-attachments`), WP-291 (`tkt-from-review`), WP-398 (`tkt-from-review-service`), WP-399 (`tkt-from-review-resolve`), WP-292 (`tkt-sla-calendar`), WP-400 (`tkt-sla-calendar-api`), WP-401 (`tkt-sla-breach`), WP-446 (`ui-tickets`), WP-447 (`ui-sla-calendar-disposal`), WP-452 (`g2-seed-tickets-docs`), WP-331 (`g2-gate`)

## E-40

**Thema:** Mengenartikel: `consumable` und `quantity_item` zu einem Modell zusammenführen

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1 (vom Auftraggeber am 2026-10-28 bestätigt): Ein Modell `consumable` (STK-01) mit Bestand pro Bin in `stock_level` (CHECK ≥ 0). Dazu gilt:

- Altledger: Der Name `stock_movement` bezeichnet heute das veränderbare Consumable-Ledger aus 000045 (`direction` in/out, ON DELETE CASCADE). WP-376 benennt es in `stock_movement_legacy` um, legt das kanonische append-only Ledger `stock_movement` an und übernimmt die Zeilen ID-gleich (`in` → `receipt`, `out` → `retrieval`) sowie die Zeilen aus `asset_movement` (000055). Danach entfallen `stock_movement_legacy` und `asset_movement`.
- Übergang: Bis zur Übernahme von `quantity_item` führt das Ledger die Spalte `quantity_item_id` mit `item_kind` `quantity_item`. `POST /api/v1/stock-movements` nimmt Mengenbuchungen auf `quantity_item` bis dahin mit `receipt`, `retrieval`, `correction` und `transfer` an (WP-274, WP-388).
- Reihenfolge: WP-283 → WP-388 → WP-389 → WP-284 (`reservation.consumable_id`) → WP-390.
- Bestand aus `consumable`: Der Freitext `consumable.location` lässt sich keinem Bin zuordnen. Jeder Wert `stock_level <> 0` geht mit Menge und Altstandort nach `migration_quarantine` (reason `stock_without_bin`, bei negativem Wert `negative_stock`; Muster E-26). Der Betreiber bucht ihn per `correction` in einen Bin (WP-283).
- Übernahme von `quantity_item` (WP-389): ID-gleich nach `consumable`, damit Bewegungen, Reservierungen und Bestellpositionen gültig bleiben. Bestand in einem Bin mit verträglichem Client ergibt eine `stock_level`-Zeile. Bestand an einem anderen Standort, ohne Standort oder im Bin eines fremden Clients geht in die Quarantäne (`stock_without_bin`, `bin_client_mismatch`); negative Mengen werden 0 (`negative_stock`). Bei SKU-Kollision wird `sku = NULL` gesetzt (`sku_conflict`), nicht leere `attributes` werden abgelegt (`attributes_dropped`). Dasselbe WP schreibt die Übergangsspalte auf `consumable_id` um und entfernt sie. Die Tabelle `quantity_item` bleibt bestehen.
- Reservierungen (WP-284): `consumable_id` und Bin (`location_id`), Fremdschlüssel mit `organization_id` (K19). Reservierungen ohne eindeutigen Bin gehen nach `migration_quarantine` (`reservation_without_bin`). Bei Altüberreservierung werden die jüngsten aktiven Reservierungen freigegeben und kopiert (`over_reservation`). Der Client einer Mengenreservierung ist der Client des Bins (`stock_level.client_id`).
- WP-390 löscht `quantity_item` ohne CASCADE erst, wenn kein Fremdschlüssel mehr darauf zeigt, und sichert vorher jede Zeile in `migration_quarantine`.
- Für die Altpfade `inventory/items*` gilt E-38.

**Frage:** Der Bestand führt zwei Tabellen und Routen für Mengenartikel (`consumable` aus 000045, `quantity_item` aus 000055) mit teils widersprüchlicher Semantik (Audit STK-03/STK-05) und zwei Buchungspfade (`consumables/<id>/movements` auf das alte `stock_movement`, `stock-movements` auf `asset_movement`). Der Katalog kennt nur `consumable`.

**Optionen:**

1. Zusammenführen auf `consumable` (Katalogwortlaut).
2. Beide behalten und synchron halten.
3. `quantity_item` behalten und umbenennen.

**Empfehlung:** Option 1. Die ID-gleiche Übernahme und die Quarantäne vermeiden stillen Datenverlust (PRI-10).

**Blockiert / Auswirkung:** WP-376, WP-274, WP-283, WP-388, WP-389, WP-284, WP-391, WP-390, WP-392, WP-320, WP-451.

**Quelle:** docs/audit/09-module-phase2plus.md:38–40; docs/audit/00-schema-ist.md:114–138; backend/migrations/000045_consumables.up.sql; backend/migrations/000055_cmdb_extensions.up.sql (`asset_movement`, `quantity_item`)

**Betroffene IDs:** API-06, AST-07, EVT-03, SIM-01, STK-01, STK-02, STK-03, STK-04, STK-05, UI-14

**Betroffene WPs:** WP-274 (`ast-movement`), WP-376 (`ast-movement-ledger`), WP-283 (`stk-consumable-model`), WP-388 (`stk-stock-bookings`), WP-389 (`stk-quantity-item-merge`), WP-284 (`stk-availability`), WP-390 (`stk-quantity-item-drop`), WP-391 (`stk-availability-api`), WP-392 (`stk-orders-receive`), WP-320 (`ui-stock-orders-stocktake`), WP-451 (`g2-seed-assets-stock`)

## E-41

**Thema:** Lieferantenstammdaten (`supplier_id` in AST-01 und MGT-12)

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1 (vom Auftraggeber am 2026-10-28 bestätigt): Eigene Mandantentabelle `supplier(organization_id, client_id NULL, name, external_ref, contact_id NULL, notes, version)` mit `UNIQUE (organization_id, name)` als Spalten-Constraint (kein Ausdrucksindex, K22), `UNIQUE (id, organization_id)` und REST `suppliers` mit ETag aus `version` (WP-368). `client_id` verweist mit Organisation auf `client` (K19). `contact_id` verweist über `(contact_id, organization_id)` auf `contact (id, organization_id)` mit `ON DELETE SET NULL (contact_id)`; `contact` erhält dafür `UNIQUE (id, organization_id)` (Muster `client_id_organization_key` in 000136), sofern WP-470 ihn nicht schon anlegt. Name, Telefon und E-Mail der Kontaktperson liegen im PII-Vault (`pii_subject`, WP-242) und kommen über `contacts`; das Lieferanten-DTO führt nur `contact_id`. Der Lieferant ist eine Organisation; `supplier.name` ist keine PII, und Lieferanten sind keine Kontakte.

Verträge (WP-269), Assets (WP-271), Mengenartikel (WP-283) und Bestellungen (WP-286) referenzieren `supplier` per Fremdschlüssel mit Organisation; ein verwendeter Lieferant lässt sich nicht löschen (422). Den Freitext überführen die Verbraucher mit `INSERT … ON CONFLICT (organization_id, name) DO NOTHING` in org-weite Zeilen (`client_id` NULL): `asset.supplier` (000009) in WP-271, `internal_order.supplier` (000046) in WP-286. `asset.supplier` bleibt als abgekündigter Spiegel von `supplier.name` (WP-370) und entfällt aus der Asset-API (WP-371). Das Bestellfeld `supplier` nimmt als abgekündigter Alias nur den Namen eines bestehenden Lieferanten an (sonst 422), bis die Bestellseite umgestellt ist (WP-444). Lieferanten entstehen nur über `suppliers`; gepflegt werden sie in WP-440.

**Frage:** AST-01 und MGT-12 verlangen `supplier_id`, der Katalog definiert aber keine Lieferantentabelle. Der Bestand speichert Lieferanten als Freitext in `asset.supplier` und `internal_order.supplier`.

**Optionen:**

1. Eigene Tabelle `supplier` (minimal), Kontaktperson als Verweis auf `contact`.
2. Lieferant als `contact` mit Rolle vendor (AUD-02).
3. Freitext beibehalten (verletzt AST-01).

**Empfehlung:** Option 1. Kontakte bleiben Personen (PII-Vault), Lieferanten sind Organisationen. Ein eigenes Surrogat am Lieferanten würde Vault-Auflösung, Blind-Index und Crypto-Shredding doppeln.

**Blockiert / Auswirkung:** WP-368, WP-269, WP-271, WP-370, WP-283, WP-286, WP-440, WP-444, WP-451.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:4, :62; backend/migrations/000009_phase2_assets.up.sql:17; backend/migrations/000016_contacts.up.sql; backend/migrations/000046_internal_orders.up.sql:11; backend/migrations/000124_pii_subject.up.sql; backend/migrations/000136_client_reference_organization.up.sql

**Betroffene IDs:** API-06, AST-01, DB-05, MGT-12, SIM-01, STK-01, STK-02, STK-03, STK-05, UI-14

**Betroffene WPs:** WP-269 (`ast-supplier-contract`), WP-368 (`ast-supplier`), WP-271 (`ast-core`), WP-370 (`ast-field-names-db`), WP-283 (`stk-consumable-model`), WP-286 (`stk-orders`), WP-440 (`ui-contracts-suppliers`), WP-444 (`ui-orders`), WP-451 (`g2-seed-assets-stock`)

## E-42

**Thema:** SEC-09: Betrieb des Malware-Scans (ClamAV) und Verhalten bei Ausfall

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1 (vom Auftraggeber am 2026-10-28 bestätigt): `clamd` als eigener, optionaler Dienst: Kustomize-Komponente `deploy/k8s/base/components/clamav` (Muster `components/opensearch`, eingebunden im Overlay `prod`) bzw. Dienst `clamd` mit Compose-Profil `documents` in `deploy/docker-compose/docker-compose.yml`; der Server erhält `RETICORA_CLAMD_ADDR` (WP-367). Signaturen kommen über freshclam, im Air-Gapped-Profil über ein Offline-Signaturbundle. Dazu gilt:

- Jede Dokumentversion trägt `scan_status` an `document_version` (`pending_scan`, `clean`, `infected`, `scan_failed`; Standard `pending_scan`, WP-363, WP-365). Abrufbar ist nur `clean`; sonst antwortet der Download mit 409 (`document-pending-scan` bzw. `document-quarantined`). Ein neuer Link auf ein `infected`-Dokument ergibt ebenfalls 409.
- Der Scan ist die Jobart `document.scan` (clamd INSTREAM) über die Work-Queue (WP-130), eingereiht nach dem Commit des Uploads (K20, WP-366). Status, Audit und Ereignis `document.scanned` schreibt der Job in einer Transaktion. Nach dem letzten Versuch setzt er `scan_failed`. Der Sweep `document.scan_sweep` (alle 15 Minuten) reiht `pending_scan` und `scan_failed` ohne offenen Job neu ein; so werden auch Inhalte aus Phase 1 mit gesetztem `storage_key` nachgescannt.
- Fail-closed gilt auch ohne Job-Queue (kein `RETICORA_NATS_URL` oder `--no-db`): Die Version bleibt `pending_scan`. Außerhalb von `development` bricht der Serverstart ab, wenn das Modul `documents` aktiv ist und `RETICORA_CLAMD_ADDR` oder die Job-Queue fehlt (OPS-01).
- Ein Befund setzt den Status `infected` (Quarantäne als Status), benachrichtigt Uploader und `org_admin` und schreibt das Audit `document.quarantined`. Blob und `document_link` bleiben bestehen. Rückstau und Fehlerrate melden die Alarme `ReticoraDocumentScanBacklog` und `ReticoraDocumentScanFailing` (WP-367).
- Das Größenlimit von SEC-09 (Default 50 MB) setzt die neue Variable `RETICORA_DOCUMENT_MAX_UPLOAD_MB` (Standard 50 = 52.428.800 Byte, WP-268); heute gilt fest 25 MiB (`maxDocumentUpload` in `backend/internal/document/handler.go`). Heute fehlt `client_max_body_size` im ganzen Repository; nginx lässt daher nur 1 MiB durch. WP-367 setzt `client_max_body_size 51m` im `http`-Block von `frontend/nginx.conf`.

**Frage:** SEC-09 verlangt einen Malware-Scan mit ClamAV und Quarantäne, sagt aber nichts zu Betrieb, Ausfallverhalten und Bestandsdaten.

**Optionen:**

1. clamd als optionaler Dienst, fail-closed.
2. clamd als Dienst, fail-open mit nachträglichem Scan.
3. Externer Scan-Dienst (verletzt die EU-Residenz, wenn nicht in der EU).

**Empfehlung:** Option 1. Fail-open würde ungeprüfte Dateien an Nutzer ausliefern. Optional, weil clamd nur für Dokumente und Importdateien (E-49) gebraucht wird; beide gehören zu P2-Modulen (`documents`, `import` nach E-61), die im G1-Standard abgeschaltet sind.

**Blockiert / Auswirkung:** WP-363, WP-365, WP-366, WP-367, WP-268 (Größenlimit), WP-301 (Datei nur bei `clean`), WP-321 (Anzeige des Scanstatus), WP-452, WP-329, WP-461; Deploy-Manifeste und Sizing-Doku.

**Quelle:** docs/spec/katalog-v3/06-audit-sicherheit-events.md:40 (SEC-09); docs/spec/katalog-v3/01-installation-stack.md (INS-08 Air-Gapped); deploy/k8s/base/components/; backend/internal/document/handler.go (`maxDocumentUpload`); backend/cmd/server/main.go (`jobQueue` nur mit `RETICORA_NATS_URL`); frontend/nginx.conf

**Betroffene IDs:** API-06, API-09, IMP-IO-01, INS-01, JOB-04, MGT-01, OPS-06, SEC-09, SEC-11, SIM-01, UI-14

**Betroffene WPs:** WP-363 (`doc-versions`), WP-365 (`doc-scan-status`), WP-366 (`doc-scan-job`), WP-367 (`doc-scan-ops`), WP-301 (`io-import-model`), WP-321 (`ui-documents-tickets`), WP-452 (`g2-seed-tickets-docs`), WP-329 (`g2-security`), WP-461 (`g2-security-tests`)

## E-43

**Thema:** RBA-07: Granularität und Auflösung der Feldrechte in Phase 2

**Status:** entschieden (2026-10-28, Option 1; Audit-Log ohne Projektion, `audit:read` legt alle Feldwerte offen)

**Festlegung:** Option 1 (vom Auftraggeber am 2026-10-28 bestätigt, einschließlich der Präzisierungen) mit drei Tabellen (WP-264): `field_grant(Rolle | Custom-Rolle, object_kind, ci_type (inkl. Untertypen), field_key | relationship_type | action, level none|read|write)`, `object_grant(Rolle | Custom-Rolle | Team, object_kind, object_id, level)` als Ausnahme je Einzelobjekt und die Mandantentabelle `field_sensitivity(object_kind, ci_type, field_key)`. `attribute_definition` erhält kein Kennzeichen `sensitive`; so lassen sich auch System-Attributdefinitionen und typisierte Spalten markieren.

- **Feldbezug** über Schlüssel statt Attribut-IDs (`ParseFieldKey`): `core.<spalte>` aus einer festen Liste je Objektart, `attributes.<key>` und beim CI `_instance.<key>`. Die `core.`-Liste des CI entspricht genau den projizierbaren Fachspalten (`ci.typedFields` ohne `name` und `status`: `manufacturer`, `model`, `serial_number`, `hardware_uuid`, `management_ip`, `primary_mac`, `hostname`, `fqdn`, `os_name`, `os_version`, `firmware_version`, `sys_object_id`).
- **Ebenen Mandant, Standort, Team:** Mandant und Standort gelten je Rollenzuweisung (`Grant.Clients`/`Grant.Sites` aus `role_assignment`, `user_custom_role` und `service_account_role`); ein Grant wirkt nur auf Objekte im Scope seiner Zuweisung. Team wirkt nur über `object_grant` mit Team als Empfänger, weil Rollenzuweisungen keinen Team-Scope haben (`scope_type` organization|client|site; RBA-03 `scope_kind team` ist nicht umgesetzt, siehe E-11).
- **Auflösung je Rolle:** `object_grant` (der Rolle oder eines Teams des Principals) vor `field_grant` mit Typ und Feld (der nächste Typ der Typkette gewinnt, ein Grant am Untertyp also gegen den Obertyp) vor `field_grant` mit Feld ohne Typ vor `field_grant` nur mit Typ bzw. Objektart vor dem Standard: `write` mit `<kind>:write`, `read` mit `<kind>:read`, sonst `none`. Bei Gleichstand innerhalb einer Rolle gewinnt `none`. Das Level einer Rolle ist höchstens ihr Objektrecht.
- **Über mehrere Rollen** gilt das höchste Level (Vereinigung nach RBA-03), zusätzlich begrenzt durch `Principal.Permissions` (Schnitt der API-Keys nach AUT-04). Ein `none` sperrt ein Feld also nur, wenn keine andere Rolle des Principals es lesen darf.
- **Sensitivität:** Ein Feld aus `field_sensitivity` (Markierung am Typ, an einem Obertyp oder ohne Typ; ein Untertyp kann sie nicht aufheben) ist `none`, solange keine Rolle des Principals einen `field_grant` mit genau diesem `field_key` hat. Typ-Grants und `object_grant` heben sie nicht auf.
- **Aktionen:** Aktion ist der Rechteschlüssel der Route; sie kennt nur `none` und `write` und ist ohne Grant erlaubt, wenn die Rolle das Recht hält. Auf den CI-Routen (WP-360): `ci:delete` für `DELETE cis/<id>`, `restore`, `merge`, `unmerge` und den Job-Revert von `ci.merge`; `ci:write` für `POST /cis`, `PATCH`, `type` und Bulk `set_status`; `ci_instance_attribute:manage` für `instance-attributes`; `override:write` für Overrides; `lifecycle:transition` für `lifecycle-transitions`. `none` ergibt 403 mit `action`. Andere Objektarten setzen Aktionen nach Konvention 11 in ihren WPs durch.
- **object_grant** setzt Feld- und Aktionslevel des Objekts: `none` blendet die projizierbaren Felder aus und sperrt die Aktionen, `read` sperrt das Schreiben. Die Sichtbarkeit des Objekts ändert er nicht; sie regeln RLS und Scope, Listen und `total` bleiben. Soll `none` ein Objekt verbergen, braucht es ein eigenes WP mit `backend/internal/ci/pg_repository.go` (SQL-Prädikat, `total`).
- **Strukturfelder** des CI unterliegen keinen Feldrechten: `id`, `organization_id`, `ci_type_id`, `client_id`, `location_id`, `site_id`, `room_id`, `parent_ci_id`, `asset_id`, `merged_into_id`, `name`, `status`, `lifecycle_state`, `health`, `health_updated_at`, `discovery_source`, `first_seen_at`, `last_seen_at`, `is_manual`, `version`, `schema_version`, `deleted_at`, `created_at`, `updated_at`. `version` und ETag steigen auch bei Änderungen unsichtbarer Felder.
- **Listenfilter:** Ein Filter auf ein projizierbares Feld ergibt 403 mit `fields` und ohne `total`, wenn das Feld für mindestens einen abgefragten Typ in mindestens einem Zuweisungs-Scope (Client, Standort) des Principals nicht lesbar ist oder der Principal ein `object_grant` mit `none` für CIs hat. Die Regel ist konservativ, weil Scope- und Objektausnahmen nicht im SQL-Filter abgebildet sind; sie gilt auch für die strukturierte Abfrage (WP-361). `fields=` ist eine Auswahl und kein Fehler. `sort` ist nur auf Strukturfeldern erlaubt.
- **Suche:** Für Objektarten mit Feldrechten (ci, asset, document, ticket) zählen bei eingeschränkter Policy nur Titel und lesbare Felder, auch für `summary`, `highlights`, `score` und `total`. Im Hybrid-Betrieb beantwortet PostgreSQL solche Abfragen; reines OpenSearch ohne PostgreSQL liefert bis TEC-12 [P5] keine Treffer (fail-closed).
- **Webhooks:** Gebundene Subscriptions erhalten Payloads nach der Policy ihres Service-Accounts. Ungebundene Subscriptions erhalten Payloads nach der Standardregel ohne Rolle: Als sensibel markierte Felder fehlen, rollenbezogene Grants greifen nicht. Feldgenau einschränken lässt sich nur über gebundene Service-Accounts. Eine Bindungspflicht wird nicht eingeführt.
- **Kanäle:** Projiziert werden CI-REST, GraphQL, Topologie und Impact, `/cis/<id>/changes`, `/cis/<id>/state` und ihr Nachfolger `/cis/<id>/as-of`, `/history/ci/<id>`, der Snapshot-Job, Suche, strukturierte Abfrage, Export und Webhooks. Ohne Feldrechte bleiben das Audit-Log (`/api/v1/audit-log`, `audit:read`), Review-Payloads (`discovery:read`, GraphQL `reviewInbox`), `GET /api/v1/reconciliation/conflicts` (`discovery:read`) sowie Ingest und Reconciliation ohne Principal. Auf Relationsebene setzt WP-402 die Regeln in REST durch.
- **Audit-Log:** `audit:read` ist ein Prüferrecht und legt alle Feldwerte offen; von den Standardrollen hält es nur org_admin. Das Audit-Log wird nicht projiziert. `GET /api/v1/history/<entityType>/<id>` und `POST /api/v1/history/snapshots` verlangen wie das Audit-Log `audit:read`. Die Historie wird nach WP-359 trotzdem projiziert. Für Inhaber von `audit:read` schützt diese Projektion keine Werte, weil das Audit-Log dieselben Werte offenlegt.

**Aufgabe:** **erledigt 2026-10-28** A-30: Der Beschluss des Auftraggebers bestätigt die Präzisierungen (Team nur über `object_grant`, Sensitivität in `field_sensitivity`, konservative Listenfilter-Regel, `object_grant` ohne Wirkung auf die Sichtbarkeit, Webhooks ohne Service-Account nach der Standardregel ohne Rolle) und entscheidet die Teilfrage Audit-Log nach der Empfehlung: `audit:read` legt alle Feldwerte offen, das Audit-Log wird nicht projiziert.

**Frage:** RBA-07 zählt acht Ebenen auf, ohne Auflösungsregeln. Offen sind die Wirkung über mehrere Rollen (RBA-03: Rechte sind die Vereinigung aller Rollen), die Bedeutung von Aktionen und Objektausnahmen und welche Felder überhaupt projizierbar sind, ohne Pflichtfelder der API zu entfernen (API-01). Dazu kommen die Ebene Team, obwohl Rollenzuweisungen keinen Team-Scope tragen, Listenfilter als Wert-Orakel und die Frage, ob das Audit-Log projiziert wird oder `audit:read` alle Feldwerte offenlegt. Eine vollständige Matrix über alle Ebenen wäre in Paketen von 0,5 bis 2 PT nicht umsetzbar und für Nutzer kaum verständlich.

**Optionen:**

1. Wie oben: Rolle × Typ × Feld/Relation/Aktion, Mandant und Standort aus der Rollenzuweisung, Team und Einzelobjekt über `object_grant`, Sensitivität je Mandant, Vereinigung über Rollen; Audit-Log ohne Projektion.
2. Nur Rolle × Attribut.
3. Vollständige achtdimensionale Matrix.

**Empfehlung:** Option 1. Sie deckt alle genannten Ebenen mit drei Tabellen ab, bleibt mit RBA-03 vereinbar und ändert keine Pflichtfelder der API. Beim Audit-Log empfiehlt sich: `audit:read` ist ein Prüferrecht und legt alle Feldwerte offen. Eine Projektion des generischen Audit-Logs wäre eigener Aufwand für alle Objektarten.

**Blockiert / Auswirkung:** Die Regeln gelten für WP-264, WP-357, WP-358, WP-265, WP-359, WP-360, WP-266, WP-361, WP-362, WP-402, WP-311 und WP-326. Die Regeln wenden außerdem an: die Suche (WP-308, WP-419, WP-420, WP-309), WP-296, WP-424, WP-397, WP-269 sowie die Seeds WP-325 und WP-451. Die TST-01-Prüfliste übernimmt die Auflösungsfälle aus WP-264: Gleichstand → `none` innerhalb einer Rolle, höchstes Level über mehrere Rollen, Sensitivität aus `field_sensitivity` nur über `field_grant` mit `field_key` aufhebbar, Grant am Obertyp gilt für Untertypen, Grant am Untertyp gewinnt. RBA-07 PASS im Ausstieg von M2.2 setzt WP-360 voraus. Andere Objektarten wenden die Regeln nach Konvention 11 in ihren WPs an.

**Quelle:** docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:66, :74 (RBA-03, RBA-07); backend/internal/identity/principal.go (`ResolveAccess`, `Grant.Teams`); backend/internal/permission/pg_repository.go (`AccessGrants` ohne Team-Scope); backend/migrations/000014_phase2_users_teams_roles.up.sql (`scope_type`); backend/internal/ci/provenance.go (`typedFields`); backend/internal/ci/handler.go (Aktionsrouten); backend/internal/webhook/dispatcher.go (`mayDeliver`); backend/internal/server/testdata/route_permissions.txt (`history`, `audit-log` mit `audit:read`)

**Betroffene IDs:** API-06, AUD-07, EVT-02, EVT-03, GQL-03, GQL-04, MET-06, MET-10, MET-45, MET-54, MGT-12, RBA-07, REL-09, SIM-01, SRC-01, SRC-02, SRC-03, TKT-01, TST-01

**Betroffene WPs:** WP-264 (`fr-model`), WP-357 (`fr-principal-roles`), WP-358 (`fr-admin`), WP-265 (`fr-rest-gql`), WP-359 (`fr-gql-graph`), WP-360 (`fr-actions`), WP-266 (`fr-search-export-webhooks`), WP-361 (`fr-query-export`), WP-362 (`fr-webhooks-matrix`), WP-402 (`rel-attributes-grants`), WP-269 (`ast-supplier-contract`), WP-397 (`tkt-attachments`), WP-296 (`rel-history`), WP-308 (`src-fields`), WP-419 (`src-fields-opensearch`), WP-420 (`src-fields-users-tags`), WP-309 (`src-ast-assets`), WP-311 (`gql-aggregates`), WP-424 (`gql-ci-aggregates`), WP-325 (`g2-seed`), WP-451 (`g2-seed-assets-stock`), WP-326 (`g2-tst01`)

## E-44

**Thema:** ENT-06 (V): Feature-Keys der Phase 2, Paketzuordnung und Zuordnung der Routen

**Status:** entschieden (2026-10-28, Option 1; ergänzt E-34)

**Festlegung:** Option 1, vom Auftraggeber am 2026-10-28 bestätigt (ENT-06 (V) gilt damit als bestätigt):

- **Schlüssel und Pakete (ENT-06):** `assets_basic`, `documents`, `import` und `ticketing_essential` in allen Paketen; `ticketing_standard`, `signature`, `stock`, `stocktake_standard`, `barcode` und `export_datev` ab Standard; `internal_orders` ab Pro. `gps_standard` und `workplace` werden angelegt (ab Standard), haben in Phase 2 aber kein Modul (LOC-09 und MGT-06 sind [P4]). `ticketing_pro`, `stocktake_pro`, `disposition`, `audit_extended` und `usage_tracking` (ENT-06 [P2–P4]) legt Phase 2 nicht an: Ihre Funktionen liegen in P3/P4 (TKT-01 Pro und INV-02 Pro [P4], `disposition` gehört zu MGT-08 [P4]). `planFeatures` in `backend/internal/entitlement/entitlement.go` folgt dieser Matrix; `Validate` lehnt unbekannte Schlüssel mit 422 ab, auch die Altschlüssel `inventory`, `ticketing` und `stocktake` und die nicht angelegten Schlüssel.
- **Routen:** Einzige Quelle ist die Zuordnungstabelle in `backend/internal/entitlement/middleware.go` (WP-337; sie ersetzt `gatedRoutes` und `gatedSuffixes`): Methode optional, Präfix, Suffix optional → ein oder mehrere Feature-Keys. Methoden- und Suffixregeln gehen den Präfixregeln vor; verlangt eine Regel mehrere Schlüssel, fordert die Middleware jeden. Die Modulschlüssel von `RETICORA_MODULES` bleiben. Zuordnung: `assets`, `assignments`, `reservations`, GET `stock-movements`, `compositions` (bis zum Wegfall), `disposal-records`, `suppliers` und `contracts` → `assets_basic`; `consumables`, `inventory` (bis zum Wegfall) und POST `stock-movements` (Mengenbuchungen und Asset-Umzug `transfer`, E-38) → `stock`; GET `stock-movements/<id>/signature` → `signature`; `assets/<id>/label` → `assets_basic` und `barcode`; `labels` → `barcode`; `stocktakes` und `stocktake` → `stocktake_standard`; `orders` → `internal_orders` (bisher `workflow_forms`); `documents` → `documents`; `tickets` und POST `review-items/<id>/ticket` → `ticketing_essential` (GET `review-items*` bleibt als G1-Kern ungegatet); `slas`, `tickets/<id>/sla` und `business-calendars` → `ticketing_standard` (E-39); `imports` und `import-profiles` → `import`; `desks` und `desk-bookings` (Modul `desks`, P4) → `workplace` (bisher `inventory`); `asset-locations` und `assets/<id>/locations` (GPS-Verlauf, LOC-09 [P4]) → `gps_standard`, in einem eigenen P4-Modul `gps`, das im G2-Release abgeschaltet bleibt (`x-phase` wechselt von P2 auf P4).
- **Entsorgung:** Das Modul `disposal` (MGT-02 [P2], `/api/v1/disposal-records`) wird P2 mit `assets_basic` in allen Paketen (bisher P4 mit `compliance`); `x-phase` an `disposal-records` wechselt auf P2. Die frühere P4-Einstufung setzte RBA-05 „disposition“ mit `disposal:*` gleich; der ENT-06-Schlüssel `disposition` gehört zu MGT-08 [P4].
- **Teilfunktionen ohne eigenen Pfad** prüft der Handler bzw. Dienst über `entitlement.Checker` (WP-337): `signature` im Body von assign, return und transfer (WP-276); `stock` bei Mengenreservierungen und ihrer Umwandlung über `/reservations` (WP-284, WP-391); `ticketing_standard` für die automatische SLA-Anwendung der Ticket-Hooks und den Lauf `sla.check` je Organisation (WP-401); `ticketing_essential` für den Ticketbezug an Review-Items (WP-291); `assets_basic` zusammen mit dem Modul `inventory` für Asset-Bezüge in `POST /api/v1/search/query` und in gespeicherten Sichten (`entity_kind` asset, `asset.*`), der Pfad selbst bleibt ungegatet (WP-309); dieselbe Bedingung mit `asset:read` gilt für die Asset-Felder der GraphQL-BFF (WP-311, ENT-05); `export_datev` und `assets_basic` zusammen mit Modul `inventory` und `asset:read` für den Formatwert `datev` auf `POST /api/v1/exports`, `POST /api/v1/export/jobs` und `GET /api/v1/export/cis` (WP-422), ohne Pfad-Gate. Der veröffentlichte Formatwert `datev` bleibt und wird gesperrt; Umstellung und Sperre gelten als Korrektur (E-97, Option 1). Den Umfang der Team-Queues regelt E-39. Fehlt das Feature: 403 `feature-not-entitled`; Limit überschritten: 403 `entitlement-limit`.
- **Kern ohne Feature-Key** (`cmdb_core`): Metamodell (Sets, Templates, Regeln, Layouts, Drafts, Lifecycle-Modelle), Tags, Feldrechte, IP-Reservierungen, Kantenhistorie und der Zustand zum Zeitpunkt T an CIs (AUD-08). Ihr Release-Gate regelt E-61.
- **Datenmigration (WP-244):** `inventory` → `assets_basic`, `ticketing` → `ticketing_essential`, `stocktake` → `stocktake_standard`; `plan`, `enabled`, `limits`, `valid_until` und `source` bleiben. Kopien `stock` aus `inventory` und `ticketing_standard` aus `ticketing` nur für Organisationen ab Standard. Jede Organisation mit Entitlement-Zeilen erhält die fehlenden Schlüssel ihres Plans (Essential also `documents` und `import`) per `INSERT … SELECT … ON CONFLICT DO NOTHING`. `navFeatureFor` in `frontend/src/hooks/useEntitlements.ts` und der E2E-Mock `frontend/e2e/support/backend.ts` folgen den neuen Schlüsseln; neue Seiten: `contracts` und `suppliers` → `assets_basic`, `import` → `import`; `locations`, `tags` und die Metamodell-Seiten hängen an keinem Feature-Key. Die Demo-Org des Seeds erhält die Schlüssel über `entitlement.PlanFeatures(entitlement.PlanPro)` (`backend/cmd/seed/seed.go`), also einschließlich `internal_orders` (WP-325).
- **Limits:** `max_storage_mb` (Schlüssel `documents`) wird in MiB (2^20 Byte) ausgewertet; Staffel 1.000 / 10.000 / 50.000 / unbegrenzt (Katalogwert, bestätigt). Die Staffel ist kein Plandefault: Ein Limit wirkt nur, wenn es in `limits` gespeichert ist (E-34). Das Größenlimit je Datei ist ein Betriebsparameter (E-42), kein Entitlement.
- **Trial:** WP-244 übernimmt `valid_until` und `source` der Quellzeilen unverändert. Nach CH21 stoppt `valid_until` nur `discovery`; Standard-Funktionen der Phase 2 enden nicht mit dem Trial. Ein Trial-Downgrade nach ENT-07 („danach Essential-Limits“) ist nicht Bestandteil der Phase 2.
- **Verhältnis zu E-34:** Diese Matrix ersetzt für die P2-Schlüssel die Ist-Pläne aus E-34. WP-244 startet ohne weitere Freigabe.

**Aufgabe:** **erledigt 2026-10-28** A-18: Der Beschluss des Auftraggebers bestätigt Paketmatrix, Routenzuordnung mit Methoden- und Mehrfachregeln, die Nichtanlage der Pro-Schlüssel, das Speicherlimit (Staffel ohne Plandefault), das DATEV-Gate (E-97) und die Trial-Wirkung (`valid_until` stoppt nur `discovery`).

**Frage:** ENT-06 ist mit (V) „zu bestätigen“ markiert. `gps_standard` und `workplace` sind P2 getaggt, obwohl die zugehörigen Module in P4 liegen; `ticketing_pro`, `stocktake_pro`, `disposition`, `audit_extended` und `usage_tracking` stehen als [P2–P4] in der Matrix, ihre Funktionen aber in P3/P4. Ein Speicherlimit pro Organisation (MGT-01) ist nicht beziffert. Der Bestand ordnet Routen anders zu: `orders` hängt an `workflow_forms`, `disposal-records` an `compliance`, `desks` und der GPS-Verlauf an `inventory`; `reservations`, `stock-movements`, `compositions`, `inventory` und `slas` sind ungeprüft. `RequiredFeature` kennt weder Methode noch mehrere Schlüssel. Teilfunktionen ohne eigenen Pfad (Signatur im Body, Mengenreservierung, DATEV-Format, Asset-Bezüge in der Suche) lassen sich nicht über Präfixe gaten. Für Lieferanten, Verträge, Metamodell, Tags und Feldrechte nennt ENT-06 keinen Schlüssel.

**Optionen:**

1. Übergangsregel wie oben.
2. `gps_standard` und `workplace` erst in Phase 4 anlegen; `desks` behält bis dahin einen P2-Schlüssel.
3. `ticketing_pro` und `stocktake_pro` schon in Phase 2 ohne Modul anlegen (Pro und Enterprise), wie `gps_standard`.
4. Andere Matrix durch den Auftraggeber.

**Empfehlung:** Option 1. Schlüssel ohne Modul schaden nicht und halten die Matrix katalogtreu, solange sie zu einer P2-Einstufung gehören; Pro-Schlüssel ohne P2-Funktion würden nur tote Grants erzeugen. Die Zuordnung je Präfix, Methode und Suffix entspricht der bestehenden Middleware; ein Modul umfasst mehrere Schlüssel (z. B. `stocktake` mit `stock`, `stocktake_standard` und `internal_orders`).

**Blockiert / Auswirkung:** Die Matrix gilt für WP-244, WP-337, WP-338, WP-247, WP-363 (`max_storage_mb`), WP-268 (Dateigröße kein Entitlement), WP-251 (kein Schlüssel), WP-368, WP-269, WP-270, WP-276, WP-380, WP-383, WP-278, WP-283, WP-388, WP-284, WP-391, WP-286, WP-288, WP-394, WP-395, WP-290, WP-291, WP-400, WP-401, WP-406, WP-309, WP-311, WP-422, Oberflächen (WP-318, WP-438, WP-439, WP-440, WP-320, WP-444, WP-445, WP-321, WP-446, WP-447, WP-322), WP-324, WP-325, WP-326, WP-329, WP-331.

**Quelle:** docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:91–112 (ENT-03 bis ENT-07); docs/spec/katalog-v3/03-datenmodell-standorte-metamodell.md:54 (LOC-09); docs/spec/katalog-v3/09-module-phase2plus.md:34 (INV-02), :66 (TKT-01); docs/audit/02-mandanten-auth-entitlements.md:432 (RBA-05); docs/plan/entscheidungen.md#e-34; backend/internal/entitlement/middleware.go; backend/internal/entitlement/entitlement.go (`planFeatures`, `PlanFeatures`, `Quotas`, `QuotaFeature`); backend/cmd/seed/seed.go:161; backend/internal/server/modules.go

**Betroffene IDs:** API-01, API-06, AST-02, AST-07, AUD-07, DIS-08, DOD-01, ENT-02, ENT-05, ENT-06, EVT-03, GATE-01, GATE-02, GATE-05, GLO-11, GQL-03, IMP-IO-01, INS-01, INS-04, INV-02, INV-03, MET-06, MET-16, MET-45, MET-53, MGT-01, MGT-02, MGT-12, NTF-05, OPS-06, RBA-05, SEC-09, SEC-11, SEQ-01, SIM-01, SRC-02, SRC-03, STK-01, STK-02, STK-03, STK-05, TEN-09, TKT-01, TKT-02, TST-01, TST-03, TST-04, UI-11, UI-14, UI-15

**Betroffene WPs:** WP-244 (`p2-entitlements`), WP-337 (`p2-feature-gates`), WP-338 (`p2-module-gates`), WP-247 (`p2-paths`), WP-251 (`met-templates`), WP-363 (`doc-versions`), WP-268 (`doc-upload-security`), WP-269 (`ast-supplier-contract`), WP-368 (`ast-supplier`), WP-270 (`ast-contract-expiry`), WP-276 (`ast-signature`), WP-380 (`ast-signature-verify`), WP-278 (`ast-disposal`), WP-383 (`ast-disposal-record`), WP-283 (`stk-consumable-model`), WP-388 (`stk-stock-bookings`), WP-284 (`stk-availability`), WP-391 (`stk-availability-api`), WP-286 (`stk-orders`), WP-288 (`inv-standard`), WP-394 (`inv-lookup`), WP-395 (`inv-labels`), WP-290 (`tkt-core`), WP-291 (`tkt-from-review`), WP-400 (`tkt-sla-calendar-api`), WP-401 (`tkt-sla-breach`), WP-406 (`io-import-profile`), WP-420 (`src-fields-users-tags`), WP-309 (`src-ast-assets`), WP-422 (`exp-datev-gate`), WP-311 (`gql-aggregates`), WP-318 (`ui-pickers-flows`), WP-438 (`ui-flows-stock`), WP-439 (`ui-flows-scan`), WP-440 (`ui-contracts-suppliers`), WP-320 (`ui-stock-orders-stocktake`), WP-444 (`ui-orders`), WP-445 (`ui-stocktake-labels`), WP-321 (`ui-documents-tickets`), WP-446 (`ui-tickets`), WP-447 (`ui-sla-calendar-disposal`), WP-322 (`ui-import-bulk-views`), WP-324 (`ui-e2e-p2`), WP-325 (`g2-seed`), WP-326 (`g2-tst01`), WP-329 (`g2-security`), WP-331 (`g2-gate`)

## E-45

**Thema:** Epic-Zuordnung und Traceability-Epics der Phase 2

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1 (vom Auftraggeber am 2026-10-28 bestätigt) nach Katalogabschnitt 46: Datenmodell, Metamodell, Rechte, Module (Abschnitte 9–18, 23–27) und Graph tragen Epic **B**. API, Import/Export, GraphQL und Frontend (Abschnitte 34–38) tragen **C**, also auch WP-247 (API-06, Abschnitt 35). Seed, Tests, Abnahme und Gate tragen **G2** (analog LT/G1 in Phase 1). Die G1-Folgearbeiten im Meilenstein M2.0b tragen **B**; ausgenommen sind WP-463 und WP-464, die Gate-Nachweise für G2 liefern (GATE-01, GATE-02) und **G2** tragen. Die Spalte `epic` in `docs/traceability.csv` folgt dem Feld „Epic (Traceability)“ des WP-Blocks; die Traceability-Prüfung vergleicht beide (`epicField` in `backend/internal/traceability/traceability.go`, für beide Pläne nach WP-243).

**Frage:** Der Katalog ordnet Phase-2-Module „B (Phase 2)“ zu, nennt aber keine Epics für Oberflächen und Qualitätssicherung der Phase 2 und keine für die Folgearbeiten aus G1.

**Optionen:**

1. B/C/G2 wie oben.
2. Eigener Epic-Buchstabe je Modul.
3. Alle Phase-2-WPs unter B.

**Empfehlung:** Option 1 (konsistent mit E-06 und dem Phase-1-Plan).

**Blockiert / Auswirkung:** Alle Traceability-Zeilen der Phase 2; WP-243 (Prüfung des Epics je Zeile), WP-463 und WP-464 (Epic G2).

**Quelle:** docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:105–117; docs/plan/entscheidungen.md#e-06; backend/internal/traceability/traceability.go (`epicField`)

**Betroffene IDs:** DOD-01, GATE-01, GLO-11, SEC-11

**Betroffene WPs:** WP-243 (`p2-trace`), WP-463 (`g1-nachweise`), WP-464 (`g1-befunde-teil03`)

## E-46

**Thema:** Verschobene Phase-1-WPs (WP-215 bis WP-239) durch Phase-2-WPs ersetzen

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1 (vom Auftraggeber am 2026-10-28 bestätigt): Die nach E-03 verschobenen WPs mit [P2]-IDs werden durch WPs dieses Plans ersetzt (Tabelle „Verschobene Arbeitspakete“). Ihre Blöcke im Phase-1-Plan bleiben unverändert, sind aber keine gültige PR-Nummer mehr. WP-243 setzt das durch: WP-Blöcke mit der Kopfzeile „**G1-Umfang:** nein, verschoben (E-03)“ sind weder in `TRACEABILITY_WPS` noch in der Spalte `wp` von `docs/traceability.csv` zulässig. Maßgeblich ist die Kopfzeile, nicht das Epic; WP-214 bleibt gültig. Die Sperre gilt für alle 25 Blöcke WP-215 bis WP-239, auch für die weiter verschobenen WP-219, WP-220, WP-228 und WP-230 bis WP-234 mit ausschließlich P3-, P4- oder [A]-IDs; eine spätere Phase ersetzt sie ebenso durch neue Nummern. Teile verschobener WPs hat Phase 1 schon umgesetzt; die Tabelle nennt sie: WP-215 (Lifecycle-Graph aus der Datenbank, WP-122), WP-216 (Review-Typ `duplicate_ip`, WP-088), WP-218 (SPOF der direkten Abhängigen, WP-179), WP-222 (`ci.asset_id` und `serial_mismatch`, WP-077; `parent_ci_id`, WP-079), WP-223 (Ablauf je Organisation, WP-022 und WP-131) und WP-238 (bis auf Zweigfilter und Traceability, WP-026 und WP-053); den Frontend-Teil von WP-239 ersetzen WP-317 und WP-436. WP-331 belegt über die Tabelle die Audit-Critical-Reste AST-01, AST-05, MGT-02 und SRC-03 (GATE-01) und die 21 verschobenen Audit-High-Befunde mit P2-Anteil (GATE-02) mit dem ersetzenden WP. Neue oder geteilte WPs erhalten Nummern ab WP-336 ohne Suffix (K26).

**Frage:** Die verschobenen WPs haben Voraussetzungen und Dateilisten aus der Zeit vor Phase 1 und decken ihre IDs nur teilweise (meist nur den Befund, nicht die ganze Anforderung). Ihre Blöcke und Traceability-Zeilen stehen weiter im Phase-1-Plan.

**Optionen:**

1. Ersetzen durch WPs dieses Plans; die Prüfung lehnt verschobene Nummern ab.
2. Verschobene WPs unverändert ausführen und Lücken mit Zusatz-WPs schließen.
3. Verschobene Blöcke im Phase-1-Plan umschreiben (ändert ein eingefrorenes Dokument).

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** PR-Nummern, Traceability-Prüfung (WP-243), Tabelle der verschobenen WPs, WP-317 und WP-436 (Ersatz WP-239), GATE-01 und GATE-02 in WP-331 und WP-329.

**Quelle:** docs/plan/implementierungsplan.md (Meilenstein M-S, Kopfzeilen „G1-Umfang“); docs/plan/phase2/implementierungsplan.md (Tabelle „Verschobene Arbeitspakete“); docs/plan/entscheidungen.md#e-03

**Betroffene IDs:** DIS-08, DOD-01, GATE-01, GATE-02, GATE-05, GLO-11, INS-01, INS-04, MET-51, OPS-06, SEC-11, SEQ-01, TEN-09, TST-03, TST-04, UI-12

**Betroffene WPs:** WP-243 (`p2-trace`), WP-436 (`ui-create-typeid`), WP-317 (`ui-guided-create`), WP-329 (`g2-security`), WP-331 (`g2-gate`)

## E-47

**Thema:** TKT-03 [O] und CH17: Ticket-Provider-Schnittstelle in Phase 2

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1. WP-293 wird in Phase 2 ausgeführt (M2.6, 2,0 PT). Das WP behält „G2-Umfang: optional“ und hat keine Gate-Wirkung, weil TKT-03 [O] ist (GATE-05). CH17 (S) ist damit erfüllt. Voraussetzungen sind WP-290, WP-396 (`ticket_link`), WP-398 (`ticket.Service`) und WP-401. Verbindliche Lesart von TKT-03: „alle Objekte tragen generische external_ref“ meint in Phase 2 Ticket und Ticketkommentar. CI, Asset, Standort, Vertrag und `ticket_link` erhalten kein `external_ref`. Für die Vollständigkeit nach E-48 zählt TKT-03 nicht.

**Aufgabe:** **erledigt 2026-10-28** Beschluss des Auftraggebers: WP-293 wird in Phase 2 ausgeführt, ohne Gate-Wirkung; die Lesart von `external_ref` (Ticket und Ticketkommentar) ist bestätigt (A-19).

**Frage:** Soll die minimale Provider-Schnittstelle (Interface create/update/comment/link/status_sync, interner Referenz-Provider, generische `external_ref`) jetzt angelegt werden, obwohl TKT-03 [O] ist? Und meint „alle Objekte tragen generische external_ref“ in Phase 2 nur Ticket und Ticketkommentar?

**Optionen:**

1. Ja, als optionales WP in Phase 2 mit `external_ref` an Ticket und Ticketkommentar (erfüllt CH17).
2. Ja, mit `external_ref` an allen Objektarten (CI, Asset, Standort, Vertrag; deutlich mehr Aufwand).
3. Nein, Backlog.

**Empfehlung:** Option 1. Die Schnittstelle betrifft nur den Ticketfluss; `external_ref` an weiteren Objektarten hat ohne externen Provider keinen Abnehmer.

**Blockiert / Auswirkung:** WP-293 wird in M2.6 ausgeführt; `external_ref` nur an Ticket und Ticketkommentar. WP-331 prüft weiter „G2-Umfang: optional“ und führt TKT-03 ohne Gate-Wirkung. WP-243 nimmt TKT-03 von der Stufe P2 aus (E-48).

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:70; docs/spec/katalog-v3/00-grundlagen.md (CH17)

**Betroffene IDs:** TKT-03

**Betroffene WPs:** WP-293 (`tkt-provider`)

## E-48

**Thema:** Traceability-Vollständigkeit für [P2] (Erweiterung von E-30)

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1, angepasst an den Stand nach WP-213:

- Fehlende [B]/[Q]-IDs blockieren seit WP-213 in jedem Lauf (`MissingForGate`, `backend/internal/traceability/traceability_test.go`). Daran ändert Phase 2 nichts.
- `TRACEABILITY_REQUIRE_COMPLETE` nimmt eine kommagetrennte Stufenliste. `B` und `Q` sind zulässig und wirkungslos; `P2` macht die Phase-2-Vollständigkeit blockierend; `1` bleibt Alias für „alle [B]/[Pn]/[A]“ (`docs/acceptance/g1-gate.md`); leer heißt nur berichten; jeder unbekannte Wert (auch `P3`) ist ein Testfehler. WP-331 setzt im CI-Job `traceability` den Wert `B,Q,P2`. Die Prüfung liest beide Pläne und beide Abdeckungsdateien (WP-243).
- Die Stufe `P2` verlangt für jede ID aus `docs/plan/phase2/abdeckung.csv` eine Zeile, deren `wp` ihr `abschluss_wp` ist. Zeilen anderer WPs, insbesondere aus Phase 1, zählen nicht; liegt `abschluss_wp` im Phase-1-Plan (heute SRC-04 mit WP-034), genügt dessen Zeile. Zusätzlich verlangt die Stufe über `CheckWorkPackages` alle Planzeilen aller nicht optionalen WPs des Phase-2-Plans.
- In jedem Lauf blockierend prüft ein Konsistenztest, dass jede Katalog-ID mit P2-Anteil in einer der Schreibweisen des Katalogs (z. B. `[P2]`, `P2/G2`, `B/G1, Teil P2/G2`, `[P2–P3]`, `[P2–P4, A]`) in der Phase-2-Abdeckung steht und jedes `abschluss_wp` „–“ oder ein WP mit einer Traceability-Zeile dieser ID ist.
- Ausgenommen sind nur IDs ohne Wortlaut nach E-37 (Tag „nicht geliefert“, `abschluss_wp` „–“: MET-20, MET-30 bis MET-34, MET-40 bis MET-44) und WPs mit „**G2-Umfang:** optional“ samt den IDs, die sie abschließen (WP-293, TKT-03 [O]).
- Der Audit-Status N/P (Spalte `status`) ist kein Ausnahmegrund und wirkt nie auf `MissingForGate`. AUT-05, AUT-06, NET-04, NFR-08, GATE-01, GATE-04, GATE-05 und SEQ-01 bleiben Pflicht.
- Fehlende IDs anderer Tags ([P3], [P4], [A]) werden weiter nur berichtet.
- Änderungen an Abdeckung und Plänen bringt ein Planungs-PR ein („Arbeitspakete: keine“).

**Frage:** E-30 regelt nur Phase 1. Seit WP-213 ist die Stufe [B]/[Q] fest verdrahtet. Ohne Erweiterung würde die Prüfung entweder alle [P3]/[P4]/[A]-IDs blockieren oder [P2] nie verlangen. Eine Zählung „irgendeine Zeile je ID“ wäre für 44 der 93 IDs der Phase-2-Abdeckung mit `abschluss_wp` im Phase-2-Plan schon durch Phase-1-Zeilen erfüllt. Eine Ausnahme nach Audit-Status N/P träfe acht [B]/[Q]-IDs mit G1-Nachweis.

**Optionen:**

1. Phasenstufe `P2` über `abschluss_wp` und die Planzeilen der Phase-2-WPs wie oben.
2. `TRACEABILITY_REQUIRE_COMPLETE=1` ab G2: alle [B]/[Pn]/[A] blockierend (nicht erfüllbar, weil [P3]/[P4]/[A]-IDs keine Zeilen haben).
3. Ausnahme nach Audit-Status N/P (Fassung vom 2026-10-13; nimmt [B]/[Q]-IDs mit Wortlaut aus).
4. Nie blockierend für [P2].

**Empfehlung:** Option 1. Sie belegt GATE-02 für G2 mit Phase-2-Arbeit und lässt den G1-Nachweis unberührt.

**Blockiert / Auswirkung:** WP-243 (Stufenliste, Prüfung über `abschluss_wp` und Planzeilen, Konsistenztest); WP-331 (CI-Job `traceability` mit `B,Q,P2`, blockierend ab G2).

**Quelle:** docs/plan/entscheidungen.md#e-30; backend/internal/traceability/traceability.go (`requiredTag`, `gateTag`, `MissingForGate`); backend/internal/traceability/traceability_test.go (`RequireCompleteEnv`); docs/plan/phase2/abdeckung.csv; .github/workflows/ci.yml (Job `traceability`)

**Betroffene IDs:** DIS-08, DOD-01, GATE-01, GATE-02, GATE-05, GLO-11, INS-01, INS-04, OPS-06, SEQ-01, TEN-09, TST-04

**Betroffene WPs:** WP-243 (`p2-trace`), WP-331 (`g2-gate`)

## E-49

**Thema:** IMP-IO-01: Import-Dateiformate, Bibliotheken und Grenzen

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1: CSV (UTF-8 mit oder ohne BOM; Trennzeichen automatisch aus der Kopfzeile: Semikolon, Komma, Tab) und Excel `.xlsx` (Bibliothek `github.com/xuri/excelize/v2`, nur das erste oder ein benanntes Blatt); JSON-Zeilen über die API (WP-303). `.xls` und `.ods` werden nicht unterstützt. Grenzen:

- Datei: höchstens 50 MB = 52.428.800 Byte (wie der Default von `RETICORA_DOCUMENT_MAX_UPLOAD_MB`, E-42). Größe und Format prüft `POST /api/v1/imports` beim Start; sonst 422 (`import-file-too-large`, `import-format-unsupported`).
- Zeilen: höchstens 50.000 Datenzeilen je Import. Der Lauf prüft die Grenze in einem Zähldurchgang des Parsers vor der ersten Schreibung; darüber endet der Job mit `failed` und Code `import-row-limit` und schreibt nichts (WP-301, WP-407).
- Schutz gegen Speicherüberlauf: XLSX entpackt höchstens 200 MiB (`UnzipSizeLimit`); über 500 Spalten oder über 1 MiB je Zeile ergeben `import-file-too-complex`.
- API: höchstens 5.000 Zeilen (sonst 422) und 16 MiB Body (sonst 413) je Aufruf.

Die Datei kommt als Dokument (WP-301) und wird erst nach dem Scanergebnis `clean` gelesen (E-42). Der Dry Run ist ein eigener Parameter `dry_run` neben `mode` (`create_missing`, `update_existing`, `both`; WP-407). Jede Zeile läuft in einer eigenen Transaktion der Organisation (`database.WithRequestTenantTx`, K20); ein Dry Run setzt sie zurück. Jede angelegte oder geänderte CI-Zeile nimmt die Identitäts- und `max_cis`-Sperre aus E-56 und zählt in der Transaktion neu (K21, WP-302, WP-467).

**Frage:** IMP-IO-01 nennt „CSV, Excel, API“ ohne Formate, Bibliotheken und Grenzen.

**Optionen:**

1. Wie oben.
2. Nur CSV und API.
3. Zusätzlich `.xls` und `.ods`.

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** Formate und Grenzen gelten für WP-301, WP-406, WP-407, WP-302, WP-303 und WP-322; WP-455 misst gegen die Zeilengrenzen; WP-329 und WP-461 prüfen Parser und neue Bibliothek `github.com/xuri/excelize/v2` (Abhängigkeitsscan).

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:52; docs/betrieb/ingest-spuren.md

**Betroffene IDs:** ABN-02, API-06, API-09, IMP-IO-01, JOB-04, NFR-03, SEC-11, TST-03, UI-11, UI-14

**Betroffene WPs:** WP-301 (`io-import-model`), WP-406 (`io-import-profile`), WP-302 (`io-import-run`), WP-407 (`io-import-pipeline`), WP-303 (`io-import-api`), WP-322 (`ui-import-bulk-views`), WP-455 (`g2-perf-writes`), WP-329 (`g2-security`), WP-461 (`g2-security-tests`)

## E-50

**Thema:** EXP-01: DATEV-Exportformat für die Anlagenbuchhaltung

**Status:** entschieden (2026-10-28, Option 1; das Default-Mapping ist das Zielformat)

**Festlegung:** Option 1. Der Beschluss des Auftraggebers vom 2026-10-28 bestätigt das Default-Mapping als DATEV-Zielformat für die Anlagenbuchhaltung. Eine Schnittstellenbeschreibung der Buchhaltung wird nicht nachgereicht. WP-310 rendert `format: datev` auf `POST /api/v1/exports` und `POST /api/v1/export/jobs` aus Asset-Daten im Stil der DATEV-Importschnittstelle für die Anlagenbuchführung (ASCII mit Semikolon, Windows-1252):

- Default-Mapping als eingebettete Datei `export/datev_mapping.json` (je Spalte Kopfzeilentext, Quellfeld, Pflicht, Format) mit den Quellfeldern `inventory_no`, `name`, `manufacturer`, `model`, `purchase_date`, `purchase_price`, `currency` (leer → `organization.currency`), `supplier` (Name aus `supplier` nach E-41), `cost_center`, `warranty_until`, `location` und `lifecycle_state`. Ein Vertragsfeld gehört nicht zum Default.
- Org-Override in `organization.settings.datev_mapping`; ein ungültiger Override setzt den Job mit Meldung auf `failed`. Abweichende Vorgaben einer Buchhaltung setzt die Organisation über diesen Override um. Eine Änderung des Defaults betrifft nur `export/datev_mapping.json` und die Formatfälle.
- Format: Windows-1252, Semikolon, CRLF, Kopfzeile aus dem Mapping, Beträge mit Dezimalkomma ohne Tausenderpunkt, Datum `TT.MM.JJJJ`.
- Zeilen mit fehlendem Pflichtfeld oder nicht kodierbaren Zeichen stehen nicht in der Datei, sondern im Fehlerreport `<org>/<job>.errors.csv` (UTF-8 mit BOM), abrufbar über `error_report_url`; die Exportseite zeigt ihn an (WP-423).
- Gate: Entitlements `export_datev` und `assets_basic` (ab Standard, E-44), Recht `asset:read` und Modul `inventory` (WP-422); Feldrechte nach E-43.

Den Formatwert regelt E-97: `datev` bleibt Formatwert; Umstellung auf Asset-Daten und Sperre gelten als Korrektur. `GET /api/v1/export/cis?format=datev` liefert bis zum Sunset 2027-10-15 das CI-Layout, aber nur hinter dem Gate (WP-422).

**Aufgabe:** **erledigt 2026-10-28** Beschluss des Auftraggebers: Das Default-Mapping aus E-50 gilt als bestätigtes DATEV-Zielformat; eine Lieferung von Schnittstellenbeschreibung, Pflichtspalten und Beispiel entfällt (A-20). Die Formatfälle in WP-326 prüfen das Default-Mapping; eine Nachprüfung nach Lieferung entfällt.

**Frage:** EXP-01 verlangt „DATEV-Mapping mit Default für die Anlagenbuchhaltung (Assets)“, ohne das Format zu nennen.

**Optionen:**

1. Format nachliefern; Default-Mapping wie in der Übergangsregel.
2. Generischer Semikolon-Export mit DATEV-ähnlicher Kopfzeile ohne Nachweis.

**Empfehlung:** Option 1. Das Mapping ist als Datei austauschbar; nach Lieferung ändert sich nur `export/datev_mapping.json` und die Formatfälle.

**Blockiert / Auswirkung:** WP-310 setzt das Default-Mapping als endgültiges Format um; die Bestätigung des Zielformats durch die Buchhaltung ist kein offener Punkt mehr. WP-422 setzt Gate und Formatwert nach E-97 um. WP-423 zeigt den Fehlerreport. WP-326 prüft die Formatfälle gegen das Default-Mapping (TST-01), ohne Nachprüfung. WP-331 führt EXP-01 nicht mehr unter „PASS unter Übergangsregel“.

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:50; backend/internal/export/export.go (`format=datev`); backend/internal/tenantapi/model.go (`Settings`)

**Betroffene IDs:** API-01, DIS-08, DOD-01, ENT-02, ENT-05, EXP-01, GATE-01, GATE-02, GATE-05, GLO-11, INS-01, INS-04, MET-06, MET-45, OPS-06, SEQ-01, TEN-09, TST-01, TST-04

**Betroffene WPs:** WP-310 (`exp-datev`), WP-422 (`exp-datev-gate`), WP-423 (`exp-datev-ui`), WP-326 (`g2-tst01`), WP-331 (`g2-gate`)

## E-51

**Thema:** AST-02: Art der digitalen Unterschrift bei Übergaben

**Status:** entschieden (2026-10-28, Option 1; Altsignaturen nach (a) übernommen)

**Festlegung:** Option 1: Einfache elektronische Signatur (WP-276):

- Eingabe `SignatureInput` an `POST /api/v1/assets/{id}/assign|return|transfer`: PNG aus Canvas (`image_png_base64`, dekodiert höchstens 512 KiB) und optional `signer` mit genau einem von `user_id`, `contact_id` oder `name`. Das OpenAPI-Schema `Signature` (`image_base64`, `object_key`, `signer_name`, `signed_at`, `method` touchscreen/drawn/typed) entfällt; `typed` entfällt, weil es kein Bild ist. `Signature` steht nur in `CreateAssignmentRequest` und `ReturnAssignmentRequest` der nie freigegebenen P2-Pfade `assignments*` (E-38); API-01 ist deshalb nicht berührt.
- Das Bild wird als Dokument der Kategorie `signature` mit Link auf die Bewegung gespeichert (`document_link` mit `entity_type` `stock_movement`; Zieltyp in CHECK und Trigger aus WP-376, Registry in WP-276).
- Unterzeichner: eine Nutzer-ID in `stock_movement.signer_user_id` (pseudonymer Schlüssel nach WP-241; ohne `signer` der Halter der Bewegung) oder ein Vault-Subjekt in `signer_surrogate` (Kontakt über `pii_subject_id` oder neues Subjekt der Art `person`, WP-242). Ledger, Dokument, Audit und Ereignisse enthalten keinen Klartextnamen. Zeitpunkt ist `occurred_at` (Serverzeit).
- `stock_movement.signature_hash` = SHA-256 über PNG-Bytes, Trennbyte 0x00 und das kanonische JSON der Bewegung mit fester Schlüsselreihenfolge (`occurred_at` in UTC auf Mikrosekunden). Die reine Funktion `movement.SignatureHash` nutzen Schreibpfad, Verifikation und Altdatenlauf. Bewegungen sind append-only.
- Nachweis (WP-380): Verifikation über `GET /api/v1/stock-movements/{id}/signature`; Dokumente der Kategorie `signature` sind gesperrt (409 bei Änderung, Löschung und weiteren Links).
- Ohne Entitlement `signature` (ab Standard, E-44) antwortet die API mit 403 `feature-not-entitled`, ohne Bewegung, Dokument oder Blob.
- Altsignaturen aus `assignment.checkout_signature` und `assignment.return_signature` (000047) werden übernommen (a). WP-379 archiviert sie vor dem Drop von `assignment` unverändert in der Sicherungstabelle `assignment_signature_legacy` mit Verweis auf die übernommene Bewegung; `signature_hash` bleibt dort NULL. WP-381 legt sie im Systemlauf `signature.migrate` als Dokument an der Bewegung ab und versiegelt `signer_name` im Vault. Ein Verzicht auf die Übernahme (b) ist ausgeschlossen.
- Qualifizierte Signaturen (eIDAS) sind nicht Bestandteil.

**Aufgabe:** **erledigt 2026-10-28** Beschluss des Auftraggebers: Die Übernahme der Altsignaturen aus 000047 ist bestätigt (a). WP-381 wird ausgeführt; WP-379 legt die Sicherungstabelle `assignment_signature_legacy` an (A-53).

**Frage:** AST-02 verlangt „digitale Unterschrift ab Paket Standard“, ohne Verfahren. Der Bestand speichert Unterschriften als JSON an `assignment`, mit Klartextnamen. Sollen die Altsignaturen aus 000047 übernommen werden?

**Optionen:**

1. Einfache elektronische Signatur wie oben.
2. Fortgeschrittene oder qualifizierte Signatur über einen externen Vertrauensdienst.

**Empfehlung:** Option 1. Option 2 würde einen Subprozessor und ein eigenes Entitlement verlangen. Für die Altsignaturen (a): Die Übernahme kostet ein WP (1,5 PT) und vermeidet den stillen Verlust von Nachweisen (PRI-10).

**Blockiert / Auswirkung:** WP-276 (Eingabe, Ablage, Hash, Entitlement), WP-380 (Verifikation, Sperre), WP-381 wird ausgeführt (Systemlauf `signature.migrate`), WP-379 legt die Sicherungstabelle `assignment_signature_legacy` an, WP-376 (Spalte `signature_hash`, Linkziel `stock_movement`), WP-439 (Unterschriftsfeld in den Übergabedialogen).

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:6; backend/migrations/000047_assignment_signature.up.sql; backend/internal/assignment/model.go (`Signature`); api/openapi.yaml (`Signature`, `CreateAssignmentRequest`, `ReturnAssignmentRequest`)

**Betroffene IDs:** AST-02, AST-07, INV-03, STK-04, UI-15

**Betroffene WPs:** WP-376 (`ast-movement-ledger`), WP-379 (`ast-assignment-projection`), WP-276 (`ast-signature`), WP-380 (`ast-signature-verify`), WP-381 (`ast-signature-legacy`), WP-439 (`ui-flows-scan`)

## E-52

**Thema:** INV-03: Barcode-Symbologie, Etikettenvorlagen und Bibliotheken

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1: Code128 und QR über `github.com/boombuler/barcode` (SVG/PNG), PDF-Etiketten über `github.com/go-pdf/fpdf` mit drei festen Vorlagen: `asset_50x25` (Asset-Etikett 50×25 mm), `bin_100x50` (Bin-Etikett 100×50 mm) und `a4_24` (A4-Bogen mit 24 Etiketten) (WP-395). Die Tests dekodieren die PNG-Ausgabe mit `github.com/makiuchi-d/gozxing` (nur in `_test.go`). Barcode-Inhalt ist bei Assets die Inventarnummer, sonst die URN `urn:reticora:<asset|consumable|location>:<uuid>` (Namensraum wie `urn:reticora:problem:*`). Kamera-Scan im Browser über `BarcodeDetector` mit `@zxing/browser` als dynamisch nachgeladenem Fallback (WP-439, WP-445). Pfade nach E-38: `assets/<id>/label?format=svg|png|pdf&template=…` (ersetzt `assets/<id>/label.svg` ohne Alias), `labels/batch` (Jobart `labels.batch`) und `labels/lookup` (WP-394: zuerst URN, dann Register); Entitlement `barcode` (E-44). Barcode und Inventarnummer sind je Organisation typübergreifend eindeutig über die Tabelle `barcode_registry` mit `UNIQUE (organization_id, code)` für Asset, Mengenartikel und Standort (WP-289). `asset.barcode` hat heute keinen Unique-Index; WP-271 legt `idx_asset_barcode_org` an. RFID bleibt je Organisation eindeutig (`idx_asset_rfid`); RFID-Etiketten sind [P4].

**Frage:** INV-03 nennt Code128/QR und PDF-Vorlagen, aber keine Bibliotheken oder Vorlagenformate.

**Optionen:**

1. Wie oben.
2. Nur SVG ohne PDF (verletzt INV-03).
3. Externer Etikettendienst.

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** Symbologie, Bibliotheken und Vorlagen gelten für WP-289, WP-394, WP-395, WP-439 und WP-445; WP-329 erfasst die neuen Abhängigkeiten im Abhängigkeitsscan.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:36; backend/migrations/000051_rfid_gps.up.sql; backend/internal/platform/httpx/problems.go (`urn:reticora:problem:*`)

**Betroffene IDs:** INV-03, SEC-11, UI-14, UI-15

**Betroffene WPs:** WP-289 (`inv-barcode`), WP-394 (`inv-lookup`), WP-395 (`inv-labels`), WP-439 (`ui-flows-scan`), WP-445 (`ui-stocktake-labels`), WP-329 (`g2-security`)

## E-53

**Thema:** NET-06/NET-09: Auflösungen des Review-Typs `duplicate_ip`

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1: `duplicate_ip` ist seit WP-088 REC-08-Typ (`reviews.KindDuplicateIP` in `backend/internal/reviews/model.go`; CHECK `review_item_kind_check` in 000097, seit 000130 mit elf Typen, der elfte ist `unknown_relationship`). Ein neuer Typ und eine Migration entfallen. Die Auflösung folgt dem REC-08-Vokabular; `review_item_action_check` (merge, new, dismiss, accept) bleibt unverändert:

- `reviews.AllowedActions[KindDuplicateIP]` wird {`accept`, `dismiss`} (heute nur `dismiss`), ebenso `REVIEW_ACTIONS.duplicate_ip` in `frontend/src/hooks/useReviews.ts` und die Beschreibung von `resolveReviewItem` in `api/openapi.yaml` (WP-404). Neue i18n-Schlüssel für Aktionen gibt es nicht.
- Konflikt Reservierung ↔ Beobachtung (NET-09, WP-404): `accept` heißt, die Beobachtung gilt, und die Reservierung wird freigegeben. `dismiss` lässt beide Einträge bestehen.
- Doppelte Beobachtungen (NET-06, WP-300, WP-405): `accept` mit `ci_id` heißt, die Adresse an der genannten CI-Seite ist veraltet; der Effekt entfernt die `ip_address`-Zeilen dieser Seite. Ist nur ein CI beteiligt, ist es diese Seite. Bei mehreren CI-Seiten ist `ci_id` aus `candidate_ci_ids` Pflicht (sonst 400); hält das CI die Adresse nicht mehr, folgt 422. `dismiss` lässt alle Seiten bestehen.
- Alle Items haben dieselbe Form: `object_key` = `ip:<vrf_id>/<host>`, Payload mit `address`, `vrf_id`, `holders` und `side_keys` (gemeinsamer Aufbau in `backend/internal/ipam/duplicate.go`). Je Adresse und VRF ist höchstens ein Item offen. Nach einer Auflösung (`dismissed` oder `resolved`) öffnet sich kein neues Item, solange `side_keys` gleich bleibt (Variante von `OpenUnlessDismissed`).
- `POST /api/v1/subnets/{id}/reservations` auf einer belegten Adresse antwortet mit 409; ein Review entsteht nur bei Discovery (WP-299).
- Die Review-Inbox zeigt bei `duplicate_ip` Adresse, VRF und alle Seiten aus `payload.holders`, weil `reviewDiff` verschachtelte Payloads ausblendet; die seitenbezogene Aktion sendet `ci_id` (WP-405, UI-16).

**Frage:** REC-08 nennt `duplicate_ip` als Typ und die Auflösungen merge, new, dismiss und accept. NET-06 und NET-09 verlangen das Review bei doppelter IP bzw. beim Konflikt einer Reservierung mit einer entdeckten Adresse, ohne Aktionen zu nennen. Der Typ besteht seit WP-088, aber nur mit `dismiss` und ohne Erzeuger. Welche Aktionen lösen ihn auf, und was bewirken sie?

**Optionen:**

1. REC-08-Vokabular ohne Migration: `accept` (seitenbezogen, mit `ci_id` bei mehreren CIs) und `dismiss`.
2. Neue Aktionen `keep_both` und `mark_stale` mit Migration von `review_item_action_check` und Änderungen an `ReviewItem.action` und `ReviewItemResolveRequest.action` in `api/openapi.yaml`, an `reviews.AllowedActions`, `useReviews.ts` und i18n.

**Empfehlung:** Option 1. REC-08 legt die Aktionen fest, und die Netz-WPs setzen sie ohne Migration um. Option 2 bleibt, wenn der Auftraggeber eigene Aktionsnamen in der Review-Inbox verlangt; dann liefert WP-404 die Migration. Die frühere Option „`conflicting_values` wiederverwenden“ ist gegenstandslos, weil der Typ besteht.

**Blockiert / Auswirkung:** WP-299 (409-Regel), WP-404 (Aktionssatz {`accept`, `dismiss`} und Effekt, ohne Migration), WP-300 (Erzeuger), WP-405 (seitenbezogenes `accept`); Review-Inbox (UI-16). Eigene Aktionsnamen (Option 2) sind nicht Bestandteil.

**Quelle:** docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:66, :72; docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:64 (REC-08); backend/internal/reviews/model.go (`AllowedActions`); backend/internal/reviews/service.go (`OpenUnlessDismissed`); backend/migrations/000097_review_model.up.sql; backend/migrations/000130_review_unknown_relationship.up.sql

**Betroffene IDs:** NET-06, NET-09, REC-08

**Betroffene WPs:** WP-299 (`net-ip-reservation`), WP-404 (`net-ip-reservation-conflict`), WP-300 (`net-duplicate-ip`), WP-405 (`net-duplicate-ip-resolve`)

## E-54

**Thema:** MET-50/CH18: Technische Basis für Form Builder und Klick-Builder

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1: Drag & Drop mit `@dnd-kit/core` und `@dnd-kit/sortable` (Tastaturalternative und Bildschirmleser-Ansagen nach WCAG; `@dnd-kit/utilities` nur bei direktem Import von `CSS.Transform`). WP-429 führt die Abhängigkeiten und den Baustein `SortableList` ein. Ihn nutzen WP-314 (Reihenfolge der Template-Items), WP-432 (Reihenfolge der Templates am CI) und WP-316; WP-313 und WP-435 nutzen kein Drag & Drop.

Regel- und Filter-Editor ist die Komponente `RuleBuilder` ohne Text-DSL, mit festem Ausgabeformat je Modus (`frontend/src/rules/builderModes.ts`):

- Filtermodus (WP-315): `FilterExpr` aus `api/openapi.yaml` in der Fassung nach WP-309 und WP-421; Vorschau serverseitig über `POST /api/v1/search/query`, ohne Auswertung im Frontend.
- Bedingungs- und Validierungsmodus (WP-433): Bedingungsgrammatik aus WP-347 bzw. Regel-AST aus WP-118 nach `api/rules.schema.json`, Vorschau über `evaluateCondition` bzw. `evaluateRule` (`frontend/src/rules/rules.ts`) mit den Fällen aus `api/rules.conformance.json`.
- Ausdrucksmodus (WP-434): `$defs/expr` aus WP-258, Vorschau über `evalExpr`.

Der Filter-AST steht als `$defs/filter` in `api/rules.schema.json` (WP-421). Er behält die Knotenform von `FilterExpr` (and/or/not/related/Vergleich), weil `POST /api/v1/search/query` und `saved_view.filter_spec.where` G1-API sind (API-01: nur additiv). Die Vergleichsoperatoren `eq`, `ne`, `in`, `lt`, `lte`, `gt`, `gte` tragen dieselben Namen und Werteformen wie in der Bedingungsgrammatik aus WP-347; ihre SQL-Auswertung (Feldart, NULL-Werte, Zeitvergleich) beschreibt `$defs/filter`, weil sie von `fieldmeta.Predicate` abweicht. Filterspezifisch sind nur `like`, `exists`, `between`, `has_any`, `has_all` und `related`. Die Konformanzfälle stehen in `api/rules.conformance.json` (`filter_cases`, `invalid_filters`); `savedview/filter_test.go` und der TS-Validator `parseFilter` (`frontend/src/rules/filter.ts`) bestehen dieselben Fälle. Der Filtermodus erzeugt `FilterExpr`. Eine Umstellung von `where` auf eine gemeinsame Bedingungsgrammatik ist nicht Bestandteil von Phase 2; sie bräuchte eine eigene neue Entscheidung, weil sie nach API-01 nicht additiv ist (Option 3).

**Frage:** Der Katalog verlangt Klick-Builder und Drag & Drop, nennt aber keine Bibliotheken. Der Regel-AST aus Phase 1 kennt keine UND/ODER/NICHT-Gruppen; diese gibt es nur im Filter-AST.

**Optionen:**

1. dnd-kit und eine eigene `RuleBuilder`-Komponente mit festem Ausgabeformat je Modus.
2. Fertige Form- oder Query-Builder-Bibliothek (meist nicht WCAG-konform und nicht AST-kompatibel).
3. Ein gemeinsamer neuer AST für Regeln und Filter (bricht `api/rules.schema.json` und `FilterExpr`, nicht additiv nach API-01).

**Empfehlung:** Option 1. Beide ASTs bestehen und sind mit Tests abgesichert; gemeinsame Operatornamen und Konformanzfälle halten sie ohne Vertragsbruch beieinander.

**Blockiert / Auswirkung:** WP-429 (Abhängigkeiten, `SortableList`), WP-314, WP-432 und WP-316 (Drag & Drop); WP-315 (Filtermodus mit `FilterExpr`), WP-433, WP-434; WP-421 (`$defs/filter`), WP-347 (gemeinsame Operatornamen); WP-329 (Abhängigkeitsscan).

**Quelle:** docs/spec/katalog-v3/03-datenmodell-standorte-metamodell.md:93; docs/spec/katalog-v3/00-grundlagen.md (CH18); api/rules.schema.json; api/rules.conformance.json; api/openapi.yaml (`FilterExpr`); frontend/src/rules/rules.ts (`evaluateRule`); backend/internal/savedview/filter.go

**Betroffene IDs:** MET-45, MET-50, SEC-11, SRC-03, SRC-04, UI-11

**Betroffene WPs:** WP-347 (`met-rules-ast`), WP-421 (`src-ast-related`), WP-429 (`ui-meta-sets`), WP-314 (`ui-meta-templates-publish`), WP-432 (`ui-meta-ci-templates-migrate`), WP-315 (`ui-rule-builder`), WP-433 (`ui-rule-catalog`), WP-434 (`ui-rule-computed`), WP-316 (`ui-form-builder`), WP-329 (`g2-security`)

## E-55

**Thema:** MGT-02: Norm und Nachweiskriterien für die Entsorgungsdokumentation

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1. Der Beschluss des Auftraggebers vom 2026-10-28 bestätigt Norm und Pflichtfelder. WP-383 dokumentiert die Entsorgung nach DIN 66399 (Schutzklasse, Sicherheitsstufe) bzw. der BSI-Empfehlung für Datenträger. `disposal_record` erhält `protection_class` (1–3), `security_level` (`^[POTEFH]-[1-7]$`), `supplier_id` (Dienstleister, E-41), `evidence_document_id` (Nachweisdokument, Fremdschlüssel ON DELETE RESTRICT), `performer_surrogate` und `witness_surrogate` (Vault-Subjekte der Art `person`) sowie `record_hash`; `method`, `data_carrier`, `certificate_ref`, `performed_at` und `notes` bleiben. Die API nimmt `performed_by` und `witness` als Namen an und speichert nur die Surrogate. Die Pflichtfelder je Methode (`method` aus 000049) prüft der Handler anhand einer Tabelle im Code, die auch OpenAPI beschreibt; DB-CHECKs dafür gibt es nicht:

- immer: `method`, `performed_at`, `performed_by`, `asset_id` oder `ci_id`;
- `destruction` und `physical_destruction`: zusätzlich `protection_class`, `security_level`, `data_carrier`, `supplier_id` oder `witness` sowie `evidence_document_id`;
- `secure_erasure`: `data_carrier`, das Verfahren in `certificate_ref` oder `notes` sowie `evidence_document_id`;
- `reuse`, `recycling` und `return_to_vendor`: nur die allgemeinen Pflichtfelder.

Fehlt ein Pflichtfeld, antwortet die API mit 400 (RFC 7807) und nennt das Feld. Weitere Anlagen über `document_link` (Ziel `disposal_record`, WP-267) sind kein Pflichtnachweis. Der Datensatz ist append-only. `record_hash` (SHA-256 über das kanonische JSON aller Fachfelder einschließlich der Surrogat-IDs, nie Klartext) setzt ein BEFORE-INSERT-Trigger nach dem Muster von `audit_entry_hash` (000117); Altzeilen werden nachgefüllt. Ein Audit-Eintrag `disposal_record.created` in derselben Transaktion referenziert ihn in der Audit-Hashkette. Das Formular in WP-447 spiegelt die Tabelle (`DISPOSAL_REQUIRED_FIELDS`); ändert sich die Tabelle, wird das Formular im selben PR mitgezogen.

**Aufgabe:** **erledigt 2026-10-28** Beschluss des Auftraggebers: DIN 66399 und BSI-Datenträgerempfehlung mit den Pflichtfeldern je Methode aus E-55 sind bestätigt (A-21). WP-383 setzt die Tabelle ohne weitere Anpassung um.

**Frage:** Welche Norm und welche Pflichtangaben gelten für „revisionssicher nach BSI/ISO“? Der Bestand speichert den Durchführenden als Klartext (`performed_by`) und als Nachweis nur den Freitext `certificate_ref` (000049).

**Optionen:**

1. DIN 66399 und BSI-Datenträgerempfehlung mit Pflichtfeldern je Methode (Übergangsregel).
2. ISO/IEC 27001 Annex A 7.14 (Entsorgung von Betriebsmitteln) mit frei definierten Feldern.
3. Andere Vorgabe des Auftraggebers.

**Empfehlung:** Option 1. Die Pflichtfeldtabelle steht im Code und in OpenAPI und wird nach A-21 angepasst.

**Blockiert / Auswirkung:** WP-383 setzt Normfelder, Pflichtfeldtabelle und `record_hash` als endgültige Regel um; ein Anpassungsvorbehalt nach A-21 entfällt. WP-447 spiegelt die Tabelle. WP-331 führt MGT-02 nicht mehr unter „PASS unter Übergangsregel“.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:42; docs/audit/09-module-phase2plus.md:45; backend/migrations/000049_disposal.up.sql; backend/migrations/000117_audit_chain_per_org.up.sql

**Betroffene IDs:** DIS-08, DOD-01, GATE-01, GATE-02, GATE-05, GLO-11, INS-01, INS-04, MGT-02, OPS-06, SEQ-01, TEN-09, TST-04, UI-14

**Betroffene WPs:** WP-383 (`ast-disposal-record`), WP-447 (`ui-sla-calendar-disposal`), WP-331 (`g2-gate`)

## E-59

**Thema:** Fortgeltung der in G1 durch Erklärung abgenommenen Punkte (B3 Pentest-Befundliste, B4 v2-Texte, B6 Subprozessoren) für G2

**Status:** entschieden (2026-10-28, Beschluss des Auftraggebers: Die Erklärung vom 2026-10-15 gilt als Nachweis für G1 und G2; die Nachweise B3, B4 und B6 sind gestrichen)

**Festlegung:** Die Erklärung des Auftraggebers vom 2026-10-15 gilt als Nachweis für G1 und G2. Die Nachweise B3 (Pentest-Befundliste), B4 (v2-Texte zu AUT-05, AUT-06, NET-04 und ABN-01 (a)/(b)) und B6 (Subprozessorenliste) sind gestrichen. Sie werden nicht nachgereicht. Kein WP und keine Aufgabe sieht ihre Lieferung vor. Abgleiche mit v2-Texten entfallen.

- WP-463 nimmt B3, B4 und B6 nicht auf und gleicht AUT-05, AUT-06, NET-04 und ABN-01 (a)/(b) nicht mit v2-Texten ab. Die Zeilen „PASS (Erklärung des Auftraggebers)“ in `docs/acceptance/g1-gate.md` bleiben. Die Vermerke „nachzureichen“ zu B3, B4 und B6 in `docs/acceptance/g1-gate.md`, `docs/security/pentest-g1.md` und `docs/legal/subprozessoren.md` ersetzt WP-463 durch den Verweis auf die Erklärung vom 2026-10-15 und auf E-59.
- Die Phase-1-Entscheidungen E-02 und E-35 sind entschieden (2026-10-28, Erklärung des Auftraggebers ersetzt die Lieferung). Die Phase-1-Aufgaben A-02, A-07 und A-11 und die Aufgabe A-23 sind erledigt. A-22 ist für ABN-01 (a)/(b) erledigt.
- GATE-01 in G2: Die Quelle „Pentest-Befundliste G1“ in `docs/acceptance/g2-gate.md` steht auf „kein offenes Critical laut Erklärung des Auftraggebers vom 2026-10-15“. WP-329 vermerkt im Abschnitt „Nachverfolgung G1“ die Erklärung statt einer Befundliste. Neue Critical-Befunde aus `docs/security/review-phase2.md`, den Scans und den Audit-Quellen prüft GATE-01 unverändert.
- ABN-01 (a)/(b): WP-330 führt in der Spalte „Status G2“ von `docs/acceptance/abn-01.md` „PASS (Erklärung des Auftraggebers, E-59)“. Für (c), (d) und (e) gelten die eigenen Nachweise.
- AUT-05, AUT-06, NET-04, NFR-08 und NFR-09 ([B]) führt `g2-gate.md` informativ mit „PASS (Erklärung des Auftraggebers)“, ohne Abgleich. WP-331 führt statt „Nachreichungen aus G1“ den Unterabschnitt „Abnahme durch Erklärung aus G1“ mit diesen IDs und ABN-01 (a)/(b).
- Andere Nachweise bleiben: der CI-Lauf mit den Scan-Jobs auf dem Release-Kandidaten und der nächtliche `load.yml`-Lauf (WP-463).
- Für GA (GATE-04) verlangt SEC-11 einen eigenen Pentest. Diese Entscheidung regelt nur G1 und G2.

**Aufgabe:** **erledigt 2026-10-28** Beschluss des Auftraggebers: Die Erklärung vom 2026-10-15 gilt als Nachweis für G1 und G2. Die Nachweise B3, B4 und B6 sind gestrichen und werden nicht geliefert (A-23); A-02, A-07 und A-11 aus Phase 1 sind damit erledigt, A-22 für ABN-01 (a)/(b).

**Frage:** Der Auftraggeber hat am 2026-10-15 die G1-Blocker B3 (Pentest ohne offene High/Critical), B4 (v2-Texte zu AUT-05, AUT-06, NET-04 und ABN-01 (a)/(b)) und B6 (Subprozessorenliste, NFR-09) für erledigt erklärt. Die Nachweise liegen im Repository nicht vor (`docs/acceptance/g1-gate.md`, Abschnitt „Ergebnis“). [Q]-Punkte gelten für jedes Gate: GATE-01 verlangt „kein offenes Critical im jeweiligen Gate“, und ABN-01 ist [Q]. Gilt die Erklärung für G2 fort, oder müssen die Nachweise vor G2 vorliegen?

**Optionen:**

1. Nachweise vor G2: Pentest-Befundliste (GATE-01) und v2-Texte zu ABN-01 (a)/(b) (GATE-02) liegen vor der G2-Gateprüfung vor, sonst sind GATE-01 bzw. ABN-01 offen; die Subprozessorenliste wird ebenfalls nachgereicht, die [B]-Punkte stehen informativ.
2. Die Erklärung vom 2026-10-15 gilt bis GA fort; G2 übernimmt die G1-Bewertung unverändert.
3. Neue Erklärung des Auftraggebers zum G2-Gate.

**Empfehlung:** Option 1. [Q]-Punkte gelten für jedes Gate, und eine nachgereichte Befundliste mit offenem Critical würde G2 sonst unbemerkt verfehlen. Phase 2 bringt neue Datenkategorien (Dokumentenspeicher, Unterschriften, Entsorgungsnachweise, Importdateien); die Subprozessorenliste gewinnt damit an Gewicht.

**Blockiert / Auswirkung:** WP-463 liefert nur den CI-Lauf auf dem Release-Kandidaten und den nächtlichen `load.yml`-Lauf. Es ersetzt die Vermerke „nachzureichen“ zu B3, B4 und B6 durch den Verweis auf die Erklärung. Eingang von B3, B4, B6 und Abgleich mit v2-Texten entfallen. WP-329 übernimmt keine Befundliste und vermerkt die Erklärung. WP-330 führt ABN-01 (a)/(b) per Erklärung. WP-331 führt die GATE-01-Quelle Pentest G1 per Erklärung und den Unterabschnitt „Abnahme durch Erklärung aus G1“. Phase 1: E-02 und E-35 entschieden; A-02, A-07, A-11 erledigt; A-22 für ABN-01 (a)/(b) erledigt.

**Quelle:** docs/spec/katalog-v3/00-grundlagen.md:11 ([Q]); docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:85–87 (GATE-01, GATE-02), :48–50 (NFR-08, NFR-09); docs/acceptance/g1-gate.md; docs/acceptance/abn-01.md; docs/security/pentest-g1.md; docs/plan/entscheidungen.md#e-02, #e-28, #e-35

**Betroffene IDs:** ABN-01, AUT-05, AUT-06, DIS-08, DOD-01, GATE-01, GATE-02, GATE-05, GLO-11, INS-01, INS-04, NET-04, NFR-08, NFR-09, OPS-06, SEC-11, SEQ-01, TEN-09, TST-04

**Betroffene WPs:** WP-463 (`g1-nachweise`), WP-329 (`g2-security`), WP-330 (`g2-abnahme`), WP-331 (`g2-gate`)

## E-60

**Thema:** Form von `total` in Listen über etwa 25.000 Objekten (Folge aus E-17)

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1, gedeckelte Zählung, für `GET /api/v1/cis` und `GET /api/v1/search`.

- `total` bleibt Pflichtfeld von `PaginationEnvelope` (API-01). Bis 10.000 Treffer ist `total` exakt. Gezählt wird über höchstens 10.001 Zeilen. Darüber ist `total` gleich 10.000, und die Antwort trägt das additive Feld `total_capped: true`. Unterhalb der Schwelle ist `total_capped` `false` oder fehlt.
- WP-471 setzt die Form um: `countQuery` in `backend/internal/ci/pg_repository.go`, `COUNT(*) OVER()` in `backend/internal/search/pg_repository.go`, `ListResponse` in `backend/internal/api/api.go`, `PaginationEnvelope` in `api/openapi.yaml` und `schema.d.ts`. Die CI-Liste (`frontend/src/pages/CIListPage.tsx`) zeigt bei `total_capped` „mehr als 10.000“. Indizes, Keyset und die Neubewertung der Schwelle von `ListNodes` für 50.000 Objekte (`nodeOrgReadAbove`: heute liest es ab 2.000 IDs die ganze Organisation) gehören ebenfalls zu WP-471.
- Die übrigen Listen mit Zählung je Anfrage (`/api/v1/assets`, `/api/v1/tickets`, `/api/v1/consumables` und die neuen Listen der Phase 2) behalten die exakte Zählung. Ihr p95 bei 10.000 Objekten einschließlich `total` weist WP-327 nach (K23). Keine neue Liste zählt je Anfrage über die ganze Organisation ohne diesen Nachweis.
- Über 10.000 Objekten bleiben diese Listen Teil der bekannten Einschränkung aus E-17 (Release-Notes in WP-331). Die Deckelung auch für sie braucht ein eigenes WP; der Phase-2-Plan enthält keines.
- Eine Schätzung mit `total_estimated` gibt es nicht.

**Aufgabe:** **erledigt 2026-10-28** A-24: Der Beschluss des Auftraggebers bestätigt Option 1, die gedeckelte Zählung mit Schwelle 10.000 und `total_capped`. Sie gilt für `GET /api/v1/cis` und `GET /api/v1/search`; die übrigen Listen behalten die exakte Zählung. Zuständig war: Product Owner/API-Verantwortlicher.

**Frage:** E-17 (entschieden am 2026-10-15) nimmt Listen und Suche über etwa 25.000 Objekten als bekannte Einschränkung mit Folge-WP hin. Ursache ist die Zählung: `GET /api/v1/cis` ermittelt `total` je Anfrage mit `SELECT COUNT(*) FROM ci WHERE …` über die ganze Organisation (`backend/internal/ci/pg_repository.go`), die Suche mit `COUNT(*) OVER()`; bei 28.000 CIs lag p95 von `GET /cis` bei 1,9 s (`docs/acceptance/lasttest-g1.md`, Offene Punkte 4). `PaginationEnvelope` in `api/openapi.yaml` verlangt `total`. Welche Form hat `total` bei großen Ergebnismengen, und für welche Listen gilt sie?

**Optionen:**

1. Gedeckelte Zählung: exakt bis zu einer Schwelle (Vorschlag 10.000, Messbasis aus E-17), gezählt über höchstens Schwelle + 1 Zeilen; darüber ist `total` die Schwelle und additiv `total_capped: true`. `total` bleibt Pflichtfeld; die Oberfläche zeigt „mehr als 10.000“.
2. Exakte Zählung beibehalten und beschleunigen (Zähler je Organisation für ungefilterte Listen, Index-only-Scan); gefilterte Listen zählen weiter über die Treffermenge, ein Budget bei 50.000 Objekten ist nicht zugesagt.
3. Geschätzte Gesamtzahl aus der Planerschätzung mit additivem Kennzeichen `total_estimated: true` (ungenau unter RLS und mit Filtern).
4. Neue Listen ohne `total`, nur mit `has_more` und `next_cursor`; bestehende Listen behalten `total` (zwei Listenverträge nebeneinander).

**Empfehlung:** Option 1. Die Kosten sind begrenzt, die Zahl bleibt bis zur Messbasis von E-17 exakt, und die Änderung ist additiv. Option 3 liefert unter RLS keine verlässlichen Werte, Option 4 teilt den Listenvertrag.

**Blockiert / Auswirkung:** WP-471 setzt die gedeckelte Zählung um und beginnt diesen Teil ohne weitere Wartezeit; dazu gehört die Anzeige „mehr als 10.000“ in der CI-Liste. WP-327 weist den p95 der Listen mit exakter Zählung bei 10.000 Objekten nach (K23), u. a. für WP-372, WP-274, WP-421 und WP-319. WP-331 führt diese Listen über 10.000 Objekten als bekannte Einschränkung aus E-17.

**Quelle:** docs/plan/entscheidungen.md#e-17; docs/acceptance/lasttest-g1.md (Offene Punkte 4 und 7); api/openapi.yaml (`PaginationEnvelope`); backend/internal/api/api.go (`ListResponse`); backend/internal/ci/pg_repository.go (`countQuery`, `nodeOrgReadAbove`); backend/internal/search/pg_repository.go

**Betroffene IDs:** ABN-02, API-01, AST-01, AST-07, DIS-08, DOD-01, EVT-03, GATE-01, GATE-02, GATE-05, GLO-11, INS-01, INS-04, MET-45, NFR-01, NFR-02, NFR-03, OPS-06, SEQ-01, SRC-03, SRC-04, STK-04, TEN-09, TST-04, UI-12, UI-14

**Betroffene WPs:** WP-471 (`g1-scale`), WP-372 (`ast-version-events`), WP-274 (`ast-movement`), WP-421 (`src-ast-related`), WP-319 (`ui-assets-contracts`), WP-327 (`g2-perf`), WP-331 (`g2-gate`)

## E-61

**Thema:** Release-Gate für P2-Funktionen ohne eigenes Modul (Metamodell, Tags, Feldrechte, Verträge/Lieferanten, Import)

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1. Der Katalog regelt nur Entitlements (ENT-05, ENT-06), nicht die Release-Einheit; nach E-03 sind Funktionen späterer Phasen im G1-Release abgeschaltet. Mit WP-462 wird G2 der Standard von `RETICORA_MODULES` (leer = alle Module mit Phase P2). Die Regel wirkt also auf Releases von main zwischen G1 und G2 und auf das Testprofil G1:

- **Neues P2-Modul `metamodel`** in `LateModules` (`backend/internal/server/modules.go`) mit den Präfixen `/api/v1/attribute-sets`, `/api/v1/ci-types/<id>/attribute-sets`, `/api/v1/templates`, `/api/v1/rules`, `/api/v1/layouts`, `/api/v1/metamodel` (Drafts, Test, Publikation, Import) und `/api/v1/field-permissions`. Nav: `metamodel` (die Reiter der G1-Seite `ci-types` blenden sich über `useModules().isModuleEnabled('metamodel')` aus) und `rules` (Seite aus WP-433). Das Modul hat keinen Feature-Key (`cmdb_core`, E-44). Die Prüfung aus WP-337, dass jedes Präfix eines P2-Moduls einen Feature-Key hat, nimmt `metamodel` aus.
- **Neues P2-Modul `import`** mit `/api/v1/import-profiles` und `/api/v1/imports`, Nav `import`, Feature-Key `import`.
- **Neue Präfixe bestehender Module:** `/api/v1/suppliers`, `/api/v1/contracts` und `/api/v1/labels` → `inventory` (Nav zusätzlich `contracts` und `suppliers`); `/api/v1/business-calendars` und `/api/v1/review-items/<id>/ticket` → `ticketing`. Ein eigenes Modul `contracts` entfällt: Verträge und Garantien teilen den Lauf `expiry.check` und den Feature-Key `assets_basic`. `labels` hat keine eigene Navigationsseite, die `TestG1DefaultSwitchesLateModulesOff` für ein eigenes Modul verlangt. Die Module `disposal` (wird P2) und `gps` (neu, P4) regelt E-44.
- **Kern ohne Modul-Gate** (kein `x-phase`, in jedem Release registriert): Tags (`/api/v1/tags`, `/api/v1/cis/<id>/tags`), weil WP-281 den G1-Schreibweg über `attributes.tags` sperrt und ein abgeschaltetes Modul keinen Schreibweg ließe; Asset-Tags unter `/api/v1/assets` folgen `inventory`. Ebenso die Nachfolger von G1-Pfaden (`/api/v1/lifecycle-models`, `/api/v1/cis/<id>/instance-attributes`) und der G1-Pfad `/api/v1/cis/<id>/lifecycle-transitions`, den Phase 2 erweitert (`/api/v1/assets/<id>/lifecycle-transitions` bleibt bei `inventory`), die aufgelösten Formulare `/api/v1/cis/<id>/form` und `/api/v1/ci-types/<id>/form` (ohne gespeichertes Layout das Standardlayout), die Template-Zuordnung `/api/v1/cis/<id>/templates` und die Kern-Unterpfade `/api/v1/subnets/<id>/reservations` (NET-09) und `/api/v1/relationships/<id>/changes` (REL-09); für beide gilt die Freigabe ohne Modul (A-25). `/api/v1/templates` ist kein Nachfolger von `ci-types/templates` (Katalog der Typvorlagen, E-38) und gehört zu `metamodel`.
- **P2-Funktionen auf G1-Pfaden** lassen sich nicht per Präfix abschalten und sind Kern: `parent_type_id` an `ci-types`, `applies_to*` und `computed` an `attribute-definitions`, `include=references`, `child_mode`, das Feld `templates` an `POST /api/v1/cis`, `format_version=2` an `ci-types/export`, das DTO-Feld `tags`, `filter[tags]` und die Feldrechte-Projektion. Ihre Systemjobs und Seeds laufen ohne Modulbedingung (`attribute.computed_refresh`, `attribute.computed_backfill`, Seed `warranty_days_left`). Die Sensitivität von Feldern pflegt `field-permissions/sensitive-fields` im Modul `metamodel`; ein Kennzeichen an Attributdefinitionen gibt es nicht.
- **Abhängige Wege folgen dem Modul:** Bei abgeschaltetem `metamodel` antworten die G1-Metamodellrouten mit `?draft` mit 404 und schreiben nichts (WP-352). `backend/cmd/server/main.go` registriert den Ausführer von `template.migrate` nur bei aktivem Modul (WP-344). Querschnittskanäle übernehmen Modul und Feature der Zielressource: die Vorprüfung des Imports (WP-407, WP-408; Verträge über `inventory`), Bulk (WP-304, WP-412; Tag-Aktionen und `change_template` ohne Modulprüfung), Suche und gespeicherte Sichten (Asset-Treffer nur mit `inventory`, Tag-Treffer immer), der DATEV-Export (400 ohne `inventory`, WP-422), der Lauf `expiry.check` (nur mit `inventory`) und die Oberfläche über `isModuleEnabled` bzw. `isPageEnabled`.
- **Umsetzung:** `ModuleForRoute` wählt den längsten passenden Präfix; Präfixe dürfen `<id>` enthalten (WP-338). Wer ein Präfix einem Modul zuordnet, setzt `x-phase: P2` an allen Pfaden darunter, auch an Gerüstpfaden aus WP-247. Das Register der E-38-Pfade in `modules_test.go` (WP-338) führt jede neue Ressource mit Modul oder als Kern und legt das Modul `import` an. `metamodel` legt das erste Fach-WP mit einer Route an (WP-250), neue Präfixe bestehender Module das Fach-WP mit der ersten Route. `docs/README.md` führt `metamodel` und `import` bei `RETICORA_MODULES`. `TestRoutesAndSpecificationAreInParity` prüft das Release G2 (WP-338); den Standard stellt WP-462 um.

**Aufgabe:** **erledigt 2026-10-28** A-25: Der Beschluss des Auftraggebers bestätigt Option 1: Module `metamodel` und `import`; Verträge, Lieferanten und Etiketten im Modul `inventory`; Kalender und Ticket aus Review im Modul `ticketing`. `subnets/<id>/reservations` und `relationships/<id>/changes` sind ohne Modul freigegeben (Kern). Zuständig war: Product Owner.

**Frage:** Neue P2-Ressourcen ohne bestehendes Modul (Metamodell-Erweiterungen, Tags, Feldrechte, Lieferanten und Verträge, Etiketten, Import) haben kein Release-Gate. `x-phase` ist nur an Pfaden eines Moduls aus `LateModules` erlaubt (`TestLateRoutesAreMarkedInSpecification`), und der Paritätstest erwartet `x-phase`-Pfade im Release „G1“ als nicht registriert. Ohne Modul wären diese Funktionen ab dem Merge in jedem Release von main aktiv. P2-Felder auf G1-Pfaden (`parent_type_id`, `applies_to*`, `computed`, `templates` an `POST /cis`, `format_version=2`) lassen sich über Präfixe gar nicht abschalten. Tags ersetzen einen G1-Schreibweg (`attributes.tags`). Für Metamodell, Tags und Feldrechte kennt ENT-06 keinen Feature-Key.

**Optionen:**

1. Übergangsregel wie oben: Module `metamodel` und `import`; Verträge, Lieferanten und Etiketten im Modul `inventory`; Kalender und Ticket aus Review im Modul `ticketing`; Tags, Nachfolger von G1-Pfaden und P2-Felder auf G1-Pfaden als Kern ohne Modul-Gate.
2. Kein Modul-Gate: Alle neuen Pfade sind ohne `x-phase` in jedem Release registriert; Gate nur über Entitlements (für Metamodell, Tags und Feldrechte also keines).
3. Weitergehende Module: zusätzlich eigene Module `contracts` (Nav `contracts`, `suppliers`) und `tags` (Nav `tags`); P2-Felder auf G1-Pfaden werden bei abgeschaltetem Modul mit 422 abgewiesen. CI- und Typdienst brauchen dafür den Modulzustand aus `backend/cmd/server/main.go`, und für Tags braucht es bei abgeschaltetem Modul einen Ersatz-Schreibweg.

**Empfehlung:** Option 1. Sie hält E-03 für die neuen Verwaltungsfunktionen ein: Unfertiges bleibt bis G2 abgeschaltet, und die bestehenden Tests prüfen das ohne neuen Mechanismus. Sie nimmt keinem G1-Nutzer einen Weg weg (Tags, Instanzwerte, Lifecycle) und braucht keinen Modulzustand in den Kerndiensten. Offen bleiben nur `subnets/<id>/reservations` und `relationships/<id>/changes` (A-25). Option 2 liefert halbfertige Metamodell-Funktionen in Folgereleases von G1 aus. Option 3 nimmt bei abgeschaltetem Modul den Schreibweg für Tags und braucht Modulprüfungen in CI- und Typdienst.

**Blockiert / Auswirkung:** Die Festlegung gilt für folgende WPs. Modul-Register und Org-Gate: WP-338 (Register, Modul `import`, längster Präfix), WP-337 (Ausnahme `metamodel`), WP-247 (Gerüst ohne `x-phase`). Modul `metamodel` mit `x-phase: P2`: WP-250, WP-251, WP-348, WP-259, WP-260, WP-352, WP-353, WP-261, WP-354, WP-344, WP-358. Kern ohne `x-phase`: WP-248, WP-255, WP-258, WP-350, WP-345, WP-342, WP-351, WP-256, WP-355, WP-262, WP-273, WP-281, WP-385, WP-424, WP-299 (`subnets/<id>/reservations`), WP-296 (`relationships/<id>/changes`). Modul `inventory`: WP-368, WP-269, WP-270, WP-394, WP-395. Modul `ticketing`: WP-400, WP-291. Modul `import`: WP-406, WP-301. Querschnitt: WP-407, WP-408, WP-304, WP-412, WP-420, WP-422. Oberfläche: WP-429, WP-314, WP-430, WP-431, WP-432, WP-433, WP-316, WP-317, WP-440, WP-439, WP-322, WP-449. Release: WP-462, WP-454, WP-329, WP-331. WP-338 legt das Register ohne Wartezeit nach dieser Regel an.

**Quelle:** backend/internal/server/modules.go (`LateModules`, `ModuleForRoute`, `RouteAllowed`); backend/internal/server/modules_test.go (`TestLateRoutesAreMarkedInSpecification`, `TestG1DefaultSwitchesLateModulesOff`); backend/internal/server/parity_test.go (`TestRoutesAndSpecificationAreInParity`); backend/internal/entitlement/middleware.go (`gatedRoutes`, `gatedSuffixes`); frontend/src/hooks/useEntitlements.ts (`useModules`, `isModuleEnabled`, `isPageEnabled`); backend/internal/savedview/filter.go (G1-Filter `tags`); api/openapi.yaml (`/api/v1/ci-types/templates`, `/api/v1/lifecycle-definitions`, `/api/v1/cis/<id>/lifecycle-transitions`); docs/README.md (`RETICORA_MODULES`); docs/plan/entscheidungen.md#e-03; docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:97–106

**Betroffene IDs:** ABN-02, API-01, API-06, API-09, AST-04, AST-05, AUD-07, BLK-02, CI-07, CI-08, CI-12, DIS-08, DOD-01, ENT-02, ENT-05, ENT-06, EVT-02, EVT-03, GATE-01, GATE-02, GATE-05, GLO-11, GQL-03, IMP-IO-01, INS-01, INS-04, INV-03, JOB-02, JOB-04, LCY-02, MET-01, MET-06, MET-10, MET-12, MET-13, MET-14, MET-16, MET-45, MET-50, MET-51, MET-52, MET-53, MET-54, MGT-12, NET-09, NFR-02, NTF-05, OPS-06, OVR-03, RBA-05, REL-09, SEC-11, SEQ-01, SRC-02, TEN-09, TKT-01, TKT-02, TST-03, TST-04, UI-11, UI-12, UI-14, UI-15, VIE-02

**Betroffene WPs:** WP-337 (`p2-feature-gates`), WP-338 (`p2-module-gates`), WP-247 (`p2-paths`), WP-248 (`met-inherit`), WP-250 (`met-sets`), WP-251 (`met-templates`), WP-342 (`met-ci-templates`), WP-344 (`met-migrate-job`), WP-345 (`met-ref-view`), WP-255 (`met-global-attrs`), WP-256 (`met-instance`), WP-348 (`met-rules-catalog`), WP-258 (`met-computed`), WP-350 (`met-computed-refresh`), WP-259 (`met-layouts`), WP-351 (`met-form`), WP-260 (`met-drafts`), WP-352 (`met-drafts-capture`), WP-261 (`met-publish`), WP-353 (`met-test`), WP-262 (`met-export-all`), WP-354 (`met-import`), WP-355 (`met-lifecycle-model-api`), WP-358 (`fr-admin`), WP-269 (`ast-supplier-contract`), WP-368 (`ast-supplier`), WP-270 (`ast-contract-expiry`), WP-273 (`ast-composition`), WP-281 (`ci-tags`), WP-385 (`ci-tags-api`), WP-394 (`inv-lookup`), WP-395 (`inv-labels`), WP-291 (`tkt-from-review`), WP-400 (`tkt-sla-calendar-api`), WP-296 (`rel-history`), WP-299 (`net-ip-reservation`), WP-301 (`io-import-model`), WP-406 (`io-import-profile`), WP-407 (`io-import-pipeline`), WP-408 (`io-import-entities`), WP-304 (`blk-ops`), WP-412 (`blk-ops-delegate`), WP-420 (`src-fields-users-tags`), WP-422 (`exp-datev-gate`), WP-424 (`gql-ci-aggregates`), WP-429 (`ui-meta-sets`), WP-314 (`ui-meta-templates-publish`), WP-430 (`ui-meta-draft-context`), WP-431 (`ui-meta-drafts-publish`), WP-432 (`ui-meta-ci-templates-migrate`), WP-433 (`ui-rule-catalog`), WP-316 (`ui-form-builder`), WP-317 (`ui-guided-create`), WP-439 (`ui-flows-scan`), WP-440 (`ui-contracts-suppliers`), WP-322 (`ui-import-bulk-views`), WP-449 (`ui-views-tags`), WP-454 (`g2-perf-reads`), WP-329 (`g2-security`), WP-331 (`g2-gate`), WP-462 (`g2-release`)

## E-62

**Thema:** RBA-05/RBA-06: Rechteschlüssel der Phase 2 und Rollenmatrix der Standardrollen

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1. Grundlage sind `backend/internal/permission/role_matrix.go` und `seed_standard_roles` (000081). Nach E-13 werden Rechte nur erweitert, wo Katalog oder Entscheidung sie tragen; die P2-Zeilen der Rollenmatrix legt diese Entscheidung fest.

- **Neue Schlüssel (13):** `stock:read`, `stock:write`, `reservation:read`, `reservation:write`, `contract:read`, `contract:write` (Module aus RBA-05) sowie die Aktionsschlüssel `stocktake:complete`, `import:run`, `import:manage`, `tag:manage`, `field_permission:manage`, `metamodel:publish` und `saved_view:manage`. `document:manage` wird nicht angelegt, weil kein P2-WP ihn nutzt. `citype:manage` bleibt RBA-01-Schlüssel ohne Route; `ci_type:manage` und `citype:manage` werden nicht zusammengeführt.
- **Übersetzung** nach dem Muster von 000081 in `role_permission`, `role.permissions`, `custom_role.permissions` und `api_key.permissions`; erst danach werden ersetzte Schlüssel entfernt, sonst löscht die Kaskade die Grants still. Ersetzend: `consumable:read` → `stock:read`, `consumable:write` → `stock:write`, `inventory:manage` → `stock:write`, `asset:reserve` → `reservation:write`, `asset:assign` → `assignment:write`. Ergänzend (der Altschlüssel bleibt): `asset:read` → + `reservation:read`, `stocktake:write` → + `stocktake:complete`. `asset:move` bleibt. Einen Alias-Mechanismus für Rechte gibt es nicht.
- **org_admin** erhält den ganzen Katalog.
- **engineer** erhält zusätzlich `stock:read`, `stock:write`, `reservation:read`, `reservation:write`, `contract:read`, `contract:write`, `stocktake:complete`, `import:run` und `tag:manage`; er behält seine Bestandsrechte (u. a. `assignment:*`, `document:*`, `order:read`, `order:write`, `ci_instance_attribute:manage`). Er erhält nicht `order:approve`, `sla:write`, `lifecycle:manage`, `relationship_type:manage`, `metamodel:publish`, `field_permission:manage`, `import:manage` und `saved_view:manage`, wie bisher auch nicht `ci_type:manage` und `ci_attribute:manage`.
- **viewer** erhält `stock:read`, `reservation:read` und `contract:read`.
- **client_technician** (Client-Scope, C) erhält `stock:read`, `reservation:read`, `contract:read` und zum Bestandsschutz `stocktake:complete`. Er behält `ticket:write` und `stocktake:write`. Er erhält weder `tag:manage` noch `ci_instance_attribute:manage`.
- **Grundsatz:** `metamodel:publish`, `field_permission:manage` und `import:manage` erhält keine client- oder standortgebundene Standardrolle. Der Test-Bericht von WP-353 enthält CI-IDs aller Clients.
- **Metamodell:** `attribute-sets`, `templates`, `rules` und `layouts` lesen mit `ci:read` und schreiben mit `ci_type:manage` wie `ci-types`. Set-Attribute schreiben weiter über `attribute-definitions` mit `ci_attribute:manage`. Die Formular-Endpunkte (`cis/<id>/form`, `ci-types/<id>/form`, GraphQL `ciForm`) lesen mit `ci:read`. Die Vorschau (`preview=true`) und die Auflösung als fremde Rolle verlangen zusätzlich `ci_type:manage` (Prüfung im Handler). `metamodel/drafts*` verlangt für alle Methoden `ci_type:manage`, auch lesend, denn Entwürfe sind unveröffentlichter Admin-Arbeitsstand. Ausgenommen sind `POST /api/v1/metamodel/drafts/<id>/publish` und `/api/v1/metamodel/drafts/<id>/test`. Ihre routeRules (WP-261, WP-353) verlangen `metamodel:publish` statt `ci_type:manage`, denn die erste passende Regel gilt. Jedes Draft-Item verlangt zusätzlich das Schreibrecht seiner Zielroute (`metamodel.ItemPermission`): `ci_type:manage` bzw. `ci_attribute:manage`, für `template_version`/`publish` `metamodel:publish`. Die Erfassung über `?draft` verlangt zusätzlich `ci_type:manage`. `metamodel:publish` schützt das Publizieren von Drafts, das direkte Publizieren von Template-Versionen (`POST /api/v1/templates/<id>/versions/<version>/publish`), das Lesen des Test-Berichts (`GET /api/v1/metamodel/drafts/<id>/test`) und den Abbruch von `metamodel.test`-Jobs. Das Pflegen von Templates und Entwürfen bleibt bei `ci_type:manage`.
- **Lifecycle:** `lifecycle-models` liest mit `asset:read` wie `lifecycle-definitions` und schreibt (POST, PATCH, DELETE) mit `lifecycle:manage`. `GET /api/v1/cis/<id>/lifecycle-transitions` verlangt `ci:read`, `GET /api/v1/assets/<id>/lifecycle-transitions` `asset:read`; Übergänge verlangen `lifecycle:transition`.
- **Feldrechte, Tags, Views:** `field-permissions` liest und schreibt mit `field_permission:manage`. `tags` liest mit `ci:read` und schreibt mit `tag:manage`; `PUT /api/v1/cis/<id>/tags` verlangt `ci:write`. `PATCH` und `DELETE /api/v1/saved-views/<id>` sind nur dem Owner oder einem Inhaber von `saved_view:manage` erlaubt (WP-307).
- **Lager, Bestellungen, Verträge:** `consumables` und `inventory` lesen mit `stock:read` und schreiben mit `stock:write`, `reservations` mit `reservation:read`/`reservation:write`. `POST /api/v1/stocktakes/<id>/complete` verlangt `stocktake:complete`. `POST assets/*/assign|return|transfer` verlangt `assignment:write`, `POST assets/*/reserve` `reservation:write`. `POST /api/v1/orders/<id>/receive` verlangt `stock:write` über eine eigene routeRule, weil der Wareneingang Bestand bucht (WP-392); ohne die Regel würde `order:write` abgeleitet. `orders/*/approve|reject` bleiben bei `order:approve`, `place`, `cancel` und `close` bei `order:write`. Die Vier-Augen-Regel (Genehmiger ≠ `requested_by`) braucht einen zweiten Inhaber von `order:approve`: Mit dieser Matrix genehmigt org_admin die Bestellungen von engineer; eigene Bestellungen eines org_admin brauchen einen zweiten org_admin. `suppliers` und `contracts` lesen mit `contract:read` und schreiben mit `contract:write`. Jede Standardrolle mit `asset:write` oder `order:write` (org_admin und engineer) hält `contract:read`; damit sind Lieferanten in WP-271 und WP-286 auswählbar. Custom-Rollen mit `asset:write` oder `order:write` ohne `contract:read` erhalten es durch die Übersetzung nicht; der PR von WP-245 nennt das.
- **SLA und Import:** `business-calendars` liest mit `sla:read` und schreibt mit `sla:write`; engineer pflegt deshalb auch keine Business-Kalender (WP-400). `import:run` liest Mapping-Profile und Läufe, startet Läufe und die Vorschau und steuert über `WithKind` den Abbruch in der Job-API (WP-301). `import:manage` schreibt Mapping-Profile (WP-406); engineer startet Läufe also nur mit vorhandenen Profilen.
- **Folgen im Bestand:** (1) engineer schreibt über `stock:write` auch `inventory/items`; bisher durfte das nur org_admin über `inventory:manage`. Das gilt, solange `inventory/items*` besteht: nach E-38 und E-40 bis WP-283, nach der Neufassung von WP-389 als Alias auf `consumable`. (2) Custom-Rollen und API-Keys, die nur `asset:read` halten, verlieren das Lesen von `inventory/items` und `inventory/availability`. (3) client_technician hat `ci:write`, aber nicht `ci_instance_attribute:manage`; er verliert mit WP-256 das Schreiben geänderter Instanzwerte per `PATCH /api/v1/cis/<id>` (MET-14). (4) Heute ändert jeder Inhaber von `saved_view:write` fremde geteilte Views (`(shared OR owner_id = …)` in `savedview/pg_repository.go`); nach WP-307 darf das nur noch org_admin. Die Änderungen erscheinen im Diff von `backend/internal/server/testdata/route_permissions.txt` bzw. in den Tests und werden im PR begründet.
- **Nachweis RBA-06:** `role_matrix_integration_test.go` prüft je Systemrolle `lifecycle:manage`, `lifecycle:transition`, `reconciliation:manage`, `override:write`, `ci_instance_attribute:manage` und `relationship_type:manage` (engineer: ✓ für `lifecycle:transition`, `reconciliation:manage`, `override:write` und `ci_instance_attribute:manage`; – für `lifecycle:manage` und `relationship_type:manage`; client_technician: – für alle). `TestSpecialActionsRequireTheirOwnPermission` wird um `relationship-types`, `lifecycle-definitions` und `stocktakes/<id>/complete` ergänzt; WP-392 ergänzt `orders/<id>/receive`.
- **Nachweise in G2:** WP-325 legt je Standardrolle einen Nutzer an; der Lasttest-Schlüssel entsteht über `-api-key-role`. Die Bestellungen im Seed genehmigt der Admin (WP-451), weil engineer `order:approve` nicht hält. Schritt 8 von ABN-01 (c) in WP-330 setzt voraus, dass engineer weder `ci_type:manage` noch `ci_attribute:manage` noch `metamodel:publish` hält. WP-329 prüft ASVS V4 gegen diese Matrix.

**Aufgabe:** **erledigt 2026-10-28** A-26: Der Beschluss des Auftraggebers bestätigt die Rollenmatrix nach Option 1. engineer erhält weder `order:approve` noch `sla:write` (auch nicht für `business-calendars`), `lifecycle:manage` oder `relationship_type:manage`. client_technician erhält `ci_instance_attribute:manage` nicht. `saved_view:manage` liegt nur bei org_admin. Zuständig war: Product Owner.

**Frage:** RBA-05 nennt die Module der Phase 2 (asset, assignment, stock, stocktake, reservation, order, document, contract, ticket), RBA-06 die Spezialrechte. Die Rollenmatrix aus RBA-02 deckt nur [B]-Schlüssel ab, und die Aktionsschlüssel stehen in keinem Katalogtext. Welche neuen Schlüssel gibt es, welche Route verlangt welches Recht, und welche Standardrolle erhält welches Recht? Insbesondere: Erhält engineer `order:approve`, `sla:write`, `lifecycle:manage` und `relationship_type:manage`? Erhält client_technician `ci_instance_attribute:manage`?

**Optionen:**

1. Matrix wie oben: engineer ohne Genehmigung von Bestellungen und ohne Konfigurationsrechte (SLA-Policies und Business-Kalender, Lifecycle-Modelle, Beziehungstypen, Metamodell-Publikation, Feldrechte, Mapping-Profile, fremde geteilte Views); client_technician ohne `ci_instance_attribute:manage`.
2. engineer erhält zusätzlich `order:approve`, `sla:write`, `lifecycle:manage` und `relationship_type:manage`; client_technician erhält `ci_instance_attribute:manage` (C).
3. Andere Matrix durch den Auftraggeber.

**Empfehlung:** Option 1. Sie hält den Bestand: engineer hat heute weder `order:approve` noch `sla:write`, `lifecycle:manage` oder `relationship_type:manage`. Konfiguration bleibt wie `ci_type:manage` in RBA-02 beim org_admin, und die Genehmigung von Bestellungen bleibt von der Anlage getrennt. `ci_instance_attribute:manage` erlaubt auch das Definieren von Instanzfeldern (`PUT /api/v1/cis/<id>/field-definitions`) und gehört deshalb nicht zu einer client-gebundenen Rolle.

**Blockiert / Auswirkung:** WP-245 legt den Katalog mit 13 neuen Schlüsseln einschließlich `saved_view:manage`, die Übersetzung und die Matrix nach der Festlegung an. Die WPs mit neuen Routen und Rechten übernehmen die Zuordnung: WP-250, WP-251, WP-256, WP-348, WP-259, WP-351, WP-424, WP-260, WP-352, WP-353, WP-261, WP-355, WP-358, WP-368, WP-269, WP-277, WP-385, WP-283, WP-284, WP-388, WP-285, WP-286, WP-392, WP-393, WP-400, WP-301, WP-406, WP-307 und die Oberflächen-WPs mit Rechteprüfung. Die Nachweise WP-325, WP-329 und WP-330 prüfen gegen diese Matrix.

**Quelle:** docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:70, :72 (RBA-05, RBA-06); docs/plan/entscheidungen.md#e-13; backend/internal/permission/role_matrix.go; backend/internal/permission/catalog.go; backend/migrations/000081_permission_catalog_rba01.up.sql; backend/internal/server/authz.go (`readPermissionFor`, `writePermissionOverrides`, `routeRules`); backend/internal/server/testdata/route_permissions.txt; backend/internal/savedview/pg_repository.go (Update/Delete mit `(shared OR owner_id = …)`); backend/internal/server/authz_test.go

**Betroffene IDs:** ABN-01, API-06, API-09, AST-06, AST-07, AUD-07, CI-08, EVT-03, GQL-03, IMP-IO-01, INV-01, JOB-04, LCY-02, MET-14, MET-16, MET-50, MET-52, MET-53, MET-54, MGT-12, NTF-05, RBA-02, RBA-05, RBA-06, SEC-11, SIM-01, STK-01, STK-02, STK-03, STK-05, TKT-02, TST-03, UI-11, UI-14, UI-15, VIE-02

**Betroffene WPs:** WP-245 (`p2-permissions`), WP-250 (`met-sets`), WP-251 (`met-templates`), WP-256 (`met-instance`), WP-348 (`met-rules-catalog`), WP-259 (`met-layouts`), WP-351 (`met-form`), WP-260 (`met-drafts`), WP-352 (`met-drafts-capture`), WP-261 (`met-publish`), WP-353 (`met-test`), WP-355 (`met-lifecycle-model-api`), WP-358 (`fr-admin`), WP-269 (`ast-supplier-contract`), WP-368 (`ast-supplier`), WP-277 (`ast-reservation`), WP-385 (`ci-tags-api`), WP-283 (`stk-consumable-model`), WP-388 (`stk-stock-bookings`), WP-284 (`stk-availability`), WP-285 (`stk-min-level`), WP-286 (`stk-orders`), WP-392 (`stk-orders-receive`), WP-393 (`inv-stocktake-complete`), WP-400 (`tkt-sla-calendar-api`), WP-301 (`io-import-model`), WP-406 (`io-import-profile`), WP-307 (`vie-shared`), WP-424 (`gql-ci-aggregates`), WP-316 (`ui-form-builder`), WP-435 (`ui-form-preview`), WP-440 (`ui-contracts-suppliers`), WP-320 (`ui-stock-orders-stocktake`), WP-444 (`ui-orders`), WP-445 (`ui-stocktake-labels`), WP-322 (`ui-import-bulk-views`), WP-449 (`ui-views-tags`), WP-323 (`ui-relationships-graph`), WP-325 (`g2-seed`), WP-329 (`g2-security`), WP-330 (`g2-abnahme`)

## E-63

**Thema:** Admin-Oberfläche für Feldrechte und Lifecycle-Modelle in Phase 2

**Status:** entschieden (2026-10-28, Option 2)

**Festlegung:** Option 2. Phase 2 liefert keine Admin-Oberfläche für Feldrechte und Lifecycle-Modelle. UI-11 zählt die Admin-Oberflächen der Phase 2 abschließend auf und nennt beide nicht; UI-14 bezieht sich auf die Module nach RBA-05.

- WP-358 (`field-permissions`, mit Objektausnahmen) und WP-355 (`lifecycle-models`) liefern die Verwaltung nur über REST mit OpenAPI und Tests. Beide führen die Admin-Oberfläche unter „Nicht Bestandteil“, ebenso WP-264, WP-265 und WP-360.
- WP-313 pflegt weder Feldrechte-Grants noch Sensitivität noch Lifecycle-Modelle. `frontend/src/pages/PermissionsPage.tsx` zeigt weiter nur den Rechtekatalog. Lifecycle-Übergänge bleiben auf den Detailseiten bedienbar.
- Ein Feldrechte- und Lifecycle-Editor steht im Backlog. M2.9 erhält kein zusätzliches WP.

**Aufgabe:** **erledigt 2026-10-28** A-27: Der Beschluss des Auftraggebers wählt Option 2. Phase 2 liefert keine Admin-Oberfläche für Feldrechte und Lifecycle-Modelle, nur REST. Zuständig war: Product Owner.

**Frage:** Feldrechte (WP-358) und Lifecycle-Modelle (WP-355) gibt es im Plan nur als REST; M2.9 plant keine Oberfläche dafür. UI-11 zählt die Admin-Oberflächen der Phase 2 auf (Typen, Vererbung, Attribute, Sets, Templates, Regeln, Form Builder, Preview, Draft/Publish, Import-Mapping, Indexstatus) und nennt beide nicht. UI-14 verlangt Oberflächen für alle Module der Phase. Soll Phase 2 eine Admin-Oberfläche für beide liefern?

**Optionen:**

1. Ja: ein zusätzliches WP in M2.9 mit Feldrechte-Editor (Rolle × Typ × Attribut/Relation/Aktion, Objektausnahmen, Sensitivität, Vorschau der Auflösung nach E-43) und Lifecycle-Editor (Zustände, Übergänge, Guards).
2. Nein: Verwaltung in Phase 2 nur über REST; Oberfläche als Backlog.
3. Teilweise: nur der Lifecycle-Editor als Erweiterung von WP-313; Feldrechte nur über REST.

**Empfehlung:** Option 2 als Übergangsregel. UI-11 zählt die Admin-Oberflächen der Phase 2 abschließend auf, und UI-14 bezieht sich auf die Module nach RBA-05; der Plan erfindet keine Anforderung. Ein Feldrechte-Editor ist fachlich sinnvoll; der Auftraggeber kann ihn mit Option 1 vor Beginn von M2.9 beauftragen.

**Blockiert / Auswirkung:** WP-358 und WP-355 bleiben reine REST-WPs. WP-313 enthält keine Pflege von Feldrechten und Lifecycle-Modellen. Der Umfang von M2.9 bleibt ohne zusätzliches WP. WP-331 führt keine Admin-Oberfläche für beide als Lieferumfang.

**Quelle:** docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:16 (UI-11), :22 (UI-14); docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:74 (RBA-07); docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:44 (LCY-02); frontend/src/pages/PermissionsPage.tsx

**Betroffene IDs:** API-06, DIS-08, DOD-01, GATE-01, GATE-02, GATE-05, GLO-11, INS-01, INS-04, LCY-02, MET-54, OPS-06, RBA-07, SEQ-01, TEN-09, TST-04, UI-11, UI-14

**Betroffene WPs:** WP-355 (`met-lifecycle-model-api`), WP-358 (`fr-admin`), WP-313 (`ui-meta-types`), WP-331 (`g2-gate`)

## E-64

**Thema:** MET-16: Materialisierung org-spezifischer Kompositionsebenen an Systemtypen

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1. Systemtypen (`ci_type.organization_id IS NULL`, 17 Seeds aus 000021 und 6 aus 000104) haben für alle Organisationen ein gemeinsames `ci_type.attribute_schema` und eine gemeinsame `schema_version` (000104). Eine Org-Ebene lässt sich dort nicht materialisieren, ohne andere Organisationen zu berühren (Querschnittsbefund M-M1). Daraus folgt:

- **Materialisiert** in `attribute_schema` und `schema_version` werden nur System-Basis, Typkette (WP-248) und Sets (WP-341).
- **Sets nur an Org-Typen:** Sets lassen sich wie Typattribute nur Org-Typen zuordnen. Das folgt dem Bestand: `checkAttributeType` (`backend/internal/citype/attribute_definition.go`) lehnt Org-Attribute an Systemtypen mit 422 ab. WP-250 antwortet bei einer Zuordnung an einen Systemtyp ebenso mit 422. Systemtypen erweitert eine Organisation über einen Org-Untertyp (WP-248).
- **Nur zur Laufzeit:** Org-Globale mit `applies_to` (WP-255), Templates (WP-251, WP-342) und Instanzattribute wirken nur in `citype.Compose` bzw. `ResolveFields` (`backend/internal/citype/compose.go`, `resolver.go`). Templates gehen nicht in `attribute_schema` ein; Org-Templates an Systemtypen sind deshalb zulässig. MET-16 („im CI referenziert“) erfüllt die Template-Ebene über `ci_template.version`.
- **Prüfung:** WP-252 prüft die Komposition aller Ebenen in Go; die SQL-Seite deckt nur die materialisierten Ebenen. WP-261 erhöht `schema_version` nur bei Änderungen materialisierter Ebenen. Der Paritätsnachweis Go ↔ SQL in WP-326 umfasst die materialisierten Ebenen; Org-Globale und Templates führt der Bericht als Ebenen ohne SQL-Gegenstück.

**Aufgabe:** **erledigt 2026-10-28** A-32: Der Beschluss des Auftraggebers bestätigt Option 1. Materialisiert werden nur System-Basis, Typkette und Sets; Sets gibt es nur an Org-Typen. Zuständig war: Product Owner/Architektur.

**Frage:** MET-16 verlangt ein materialisiertes, mit `schema_version` versioniertes effektives Schema über alle Ebenen. Systemtypen teilen `attribute_schema` und `schema_version` über alle Organisationen. Wo liegen org-spezifische Ebenen (Set-Zuordnungen, Org-Globale mit `applies_to`, Templates) an Systemtypen?

**Optionen:**

1. Materialisierung nur für System-Basis, Typkette und Sets; Sets nur an Org-Typen; Org-Globale, Templates und Instanzattribute nur zur Laufzeit in `Compose` (Übergangsregel).
2. Mandantentabelle `ci_type_effective_schema(organization_id, ci_type_id, schema_version, attribute_schema)` mit RLS; alle Org-Ebenen werden je Organisation und Typ materialisiert, auch an Systemtypen (+0,5 bis 1,0 PT in WP-341 oder eigenem WP).
3. Org-Ebenen an Systemtypen ganz verbieten (auch Templates und Org-Globale; Erweiterung nur über Org-Untertypen).

**Empfehlung:** Option 1. Sie folgt dem Bestand (`checkAttributeType`), braucht keine neue Tabelle und hält `schema_version` für alle Organisationen eindeutig. MET-16 nennt Org-Globale nicht als eigene Ebene, und die Template-Ebene ist über `ci_template.version` im CI referenziert. Option 3 schränkt Templates an Systemtypen ohne Not ein.

**Blockiert / Auswirkung:** WP-250 weist Set-Zuordnungen an Systemtypen mit 422 ab. WP-341 materialisiert nur System-Basis, Typkette und Sets. WP-251, WP-342 und WP-255 wirken nur zur Laufzeit. WP-252 prüft alle Ebenen in Go. WP-261 erhöht `schema_version` nur bei materialisierten Ebenen. WP-326 weist die Parität Go ↔ SQL für die materialisierten Ebenen nach. Keine neue Tabelle und kein Mehraufwand in M2.1.

**Quelle:** docs/spec/katalog-v3/03-datenmodell-standorte-metamodell.md:89 (MET-16); backend/migrations/000104_ci_type_fields_seeds.up.sql:42; backend/internal/citype/attribute_definition.go (`checkAttributeType`); backend/internal/citype/compose.go; backend/internal/citype/resolver.go; Neufassungen von WP-250, WP-251, WP-252, WP-255 und WP-326

**Betroffene IDs:** API-06, AUD-07, EVT-03, MET-06, MET-13, MET-16, MET-45, MET-53, TST-01

**Betroffene WPs:** WP-250 (`met-sets`), WP-341 (`met-sets-compose`), WP-251 (`met-templates`), WP-342 (`met-ci-templates`), WP-252 (`met-compose-rules`), WP-255 (`met-global-attrs`), WP-261 (`met-publish`), WP-326 (`g2-tst01`)

## E-65

**Thema:** DIS-02: Zuordnung des Systemtemplates aus `attributes.system_template` beim Ingest

**Status:** entschieden (2026-10-28, Option 3; Option 1 erst mit einem Katalogstand, der MET-30 bis MET-34 liefert)

**Festlegung:** Option 3 bis zur Lieferung von MET-30 bis MET-34, danach Option 1. Die Collector-Profile melden den Schlüssel seit WP-151 (`collector/profiles/classify.go`, Attribut `system_template`). Nach E-37 tragen Systemtemplates die `systemTemplate`-Schlüssel und haben keine Items, solange MET-30 bis MET-34 nicht vorliegen. Der Phase-2-Plan sieht keine Lieferung von MET-30 bis MET-34 vor (E-37, A-15 erledigt). In Phase 2 gilt deshalb verbindlich Option 3.

- **Phase 2:** `attributes.system_template` bleibt ein beobachtetes Attribut. Die Discovery legt keine Zeilen in `ci_template` an. Zuordnungen entstehen nur über `cis/<id>/templates` (WP-342).
- **Mit einem späteren Katalogstand, der MET-30 bis MET-34 liefert:** Die Discovery ordnet das Systemtemplate automatisch zu (Option 1). Sie schreibt die Zuordnung in `ci_template` über den Einzel- und den Batch-Schreibpfad (WP-332, WP-333), mit Spuren nach E-56 und mit Override-Schutz: Eine manuell entfernte Zuordnung entsteht nicht erneut.
- Option 1 setzt ein eigenes Folge-WP um. Es wird mit diesem Katalogstand geplant; der Phase-2-Plan enthält es nicht.

**Aufgabe:** **erledigt 2026-10-28** A-33: Der Beschluss des Auftraggebers wählt die Empfehlung: keine Auswertung von `attributes.system_template` bis zur Lieferung von MET-30 bis MET-34, danach automatische Zuordnung mit Override-Schutz über ein Folge-WP. Zuständig war: Product Owner.

**Frage:** Darf die Discovery Templates zuordnen? DIS-02 lässt Profile auf ein System-Template abbilden, sagt aber nicht, ob die Zuordnung automatisch entsteht.

**Optionen:**

1. Die Discovery legt die Zuordnung in `ci_template` an: Einzel- und Batch-Schreibpfad (WP-332, WP-333), Spuren nach E-56, Override-Schutz (eine manuell entfernte Zuordnung entsteht nicht erneut).
2. Die Discovery schlägt die Zuordnung nur vor (Review-Item oder Hinweis am CI); ein Nutzer bestätigt.
3. Keine Auswertung in Phase 2 (Übergangsregel).

**Empfehlung:** Option 3 bis zur Lieferung von MET-30 bis MET-34, danach Option 1. Solange Systemtemplates keine Items haben, ändert eine Zuordnung kein Feld; der Aufwand für Schreibpfade, Spuren und Override-Schutz lohnt erst mit Inhalt.

**Blockiert / Auswirkung:** WP-251 und WP-342 werten `attributes.system_template` nicht aus. Der Phase-2-Plan braucht kein WP dafür. Ein Folge-WP (Ingest-Schreibpfad über WP-332 und WP-333, Spuren nach E-56, Override-Schutz) entsteht mit einem Katalogstand, der MET-30 bis MET-34 liefert.

**Quelle:** docs/spec/katalog-v3/05-collector-discovery-reconciliation.md:24 (DIS-02); collector/profiles/classify.go; collector/profiles/profiles.go (`SystemTemplate`); E-37; Neufassung von WP-251

**Betroffene IDs:** API-06, AUD-07, DIS-02, MET-16, MET-53

**Betroffene WPs:** WP-251 (`met-templates`), WP-342 (`met-ci-templates`)

## E-66

**Thema:** RBA-03/VIE-01: IdP-gemappte Standardrollen in rollengebundener Konfiguration (Layouts, geteilte Views)

**Status:** entschieden (2026-10-28, Option 1 als Ziel; bis WP-357 gilt Option 2)

**Festlegung:** Option 1 als Ziel. Bis WP-357 vorliegt, gilt Option 2. Befund: `applyAccess` (`backend/internal/identity/handler.go`) führt die über `StandardRolesForGroups` gemappten Rollen nur in die Rechte der Session ein. `identity.Principal` trägt keine Rollen-IDs. `visibleTo` (`backend/internal/savedview/pg_repository.go`) prüft nur `role_assignment` und `user_custom_role`. Wer `engineer` nur über eine IdP-Gruppe hält, sieht deshalb heute keine mit `engineer` geteilte View. Feldrechte (WP-264) beziehen die gemappten Rollen ein, sobald WP-357 (M2.2) `Groups` in `identity.Principal` aufnimmt.

- **Regel:** Layouts und geteilte Views berücksichtigen die über IdP-Gruppen gemappten Standardrollen wie die Feldrechte (RBA-03). `visibleTo` und die Layoutauflösung prüfen zusätzlich `StandardRolesForGroups(Principal.Groups)`.
- **Geteilte Views:** WP-307 (M2.8) setzt das um und erhält WP-357 als Voraussetzung. Die Einschränkung in der OpenAPI von `listSavedViews` und `getSavedViewsId` entfällt.
- **Layouts:** WP-351 (M2.1) liegt vor M2.2. Es löst Layouts zunächst nur über gespeicherte Zuweisungen auf (Option 2) und nennt die Einschränkung in der OpenAPI an `cis/<id>/form`. WP-307 ergänzt nach WP-357 die Rollen des Aufrufers in der Layoutauflösung von WP-351 (`LayoutStore`) um die gemappten Standardrollen und entfernt den Hinweis an `cis/<id>/form`. Dafür erhält WP-307 auch WP-351 als Voraussetzung.

**Aufgabe:** **erledigt 2026-10-28** A-34: Der Beschluss des Auftraggebers wählt die Empfehlung: Layouts und geteilte Views berücksichtigen IdP-gemappte Standardrollen nach WP-357; bis dahin gelten nur gespeicherte Zuweisungen. Zuständig war: Product Owner.

**Frage:** Sollen Layouts und geteilte Views die über IdP-Gruppen gemappten Standardrollen berücksichtigen wie die Feldrechte?

**Optionen:**

1. Ja: Nach WP-357 prüfen `visibleTo` und die Layoutauflösung zusätzlich `StandardRolesForGroups(Principal.Groups)` (Änderung in WP-307 und WP-351; WP-307 erhält WP-357 als Voraussetzung).
2. Nein: bekannte Einschränkung, dokumentiert in der OpenAPI (Übergangsregel).

**Empfehlung:** Option 1 als Ziel, weil RBA-03 die Rechte aller Rollen vereinigt und dieselbe Rolle sonst je Kanal anders wirkt. Bis WP-357 vorliegt, gilt Option 2; WP-351 (M2.1) liegt vor M2.2 und setzt Option 1 bei Bedarf nachträglich um.

**Blockiert / Auswirkung:** WP-307 prüft die gemappten Rollen in `visibleTo` und in der Layoutauflösung von WP-351 und erhält WP-357 und WP-351 als Voraussetzungen. WP-351 liefert die Layoutauflösung zunächst ohne gemappte Rollen, mit Hinweis in der OpenAPI; WP-307 entfernt den Hinweis. WP-259 bleibt unverändert. WP-357 liefert `Principal.Groups` für alle drei Kanäle.

**Quelle:** backend/internal/identity/handler.go (`applyAccess`); backend/internal/identity/rbac.go (`StandardRolesForGroups`); backend/internal/identity/principal.go; backend/internal/savedview/pg_repository.go (`visibleTo`); Neufassungen von WP-259 und WP-307

**Betroffene IDs:** API-06, GQL-03, MET-50, MET-52, RBA-03, RBA-07, VIE-01, VIE-02

**Betroffene WPs:** WP-259 (`met-layouts`), WP-351 (`met-form`), WP-357 (`fr-principal-roles`), WP-307 (`vie-shared`)

## E-67

**Thema:** MET-53: Versionsbindung der Template-Bestandteile (Katalogregeln und Layouts)

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1: Katalogregeln binden an `template_version_id` (WP-348), Layouts versionsunabhängig an `template_id` (WP-259). Regeln ändern die Validierung und gehören deshalb zur Template-Version; eine Migration (WP-253) bringt sie mit. Layouts sind Darstellung; ihre Änderungen laufen über `metamodel_draft` (WP-260, WP-261) und wirken sofort für alle Versionen eines Templates.

**Frage:** Katalogregeln binden nach der Neufassung von WP-257 an die Template-Version, Layouts nach WP-259 an das Template. Bleiben beide Bindungen so?

**Optionen:**

1. Regeln an `template_version_id`, Layouts an `template_id` (Stand der Neufassungen).
2. Auch Layouts an `template_version_id`: Ein neues Layout erfordert eine neue Template-Version; die Migration bringt das Layout mit.
3. Auch Regeln an `template_id`: Regeländerungen wirken sofort auf alle Versionen, ohne Migration.

**Empfehlung:** Option 1. Validierung soll sich nur mit einer bewussten Migration ändern („Template-Version migrieren, Overrides bleiben erhalten“, ABN-01 c); für Layouts wäre eine Versionspflicht ohne Nutzen für die Daten.

**Blockiert / Auswirkung:** WP-348 bindet Katalogregeln an `template_version_id`. WP-259 bindet Layouts an `template_id`. WP-251, WP-260, WP-261 und WP-316 folgen dieser Bindung. WP-253 bringt die Regeln mit der Template-Version mit; Layoutänderungen wirken nach dem Publizieren für alle Versionen.

**Quelle:** docs/spec/katalog-v3/03-datenmodell-standorte-metamodell.md:99 (MET-53); Neufassungen von WP-257 und WP-259

**Betroffene IDs:** API-06, AUD-07, EVT-03, MET-16, MET-50, MET-53, UI-11

**Betroffene WPs:** WP-251 (`met-templates`), WP-348 (`met-rules-catalog`), WP-259 (`met-layouts`), WP-260 (`met-drafts`), WP-261 (`met-publish`), WP-316 (`ui-form-builder`)

## E-68

**Thema:** MET-53/API-06: Verweise auf Items desselben Metamodell-Drafts

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1. WP-354 legt abhängige Bestandteile in einem Draft an: Untertyp mit neuem Elterntyp, Attributdefinitionen an neuem Typ oder Set, Regeln an neuer Template-Version, Layouts an neuem Typ. WP-260 führt bei Neuanlage sonst nur ID-Verweise (`target_id` ist `null`, Payload mit `ci_type_id`); WP-261 wendet die Items über die Repository-Methoden an. Dafür gilt:

- Ein Payload darf statt einer ID einen Item-Verweis tragen: ein Objekt mit dem einzigen Schlüssel `$item` und der ID eines Items als Wert.
- WP-260 prüft bei der Erfassung, dass der Verweis auf ein Item desselben Drafts zeigt und keinen Zyklus bildet.
- WP-261 wendet die Items in Abhängigkeitsreihenfolge an und ersetzt jeden Verweis durch die neu angelegte ID, alles in einer Transaktion. Ein Fehler rollt alle Items zurück.
- WP-260 und WP-261 stellen den Item-Verweis vor Beginn von WP-354 bereit.

**Aufgabe:** **erledigt 2026-10-28** A-35: Der Beschluss des Auftraggebers bestätigt Option 1: Item-Verweise mit dem Schlüssel `$item` im Payload, aufgelöst in WP-261. Zuständig war: Architektur/Product Owner.

**Frage:** Wie verweisen Items eines Drafts auf andere, noch nicht angelegte Items desselben Drafts?

**Optionen:**

1. Item-Verweise im Payload, aufgelöst beim Publizieren (Ergänzung in WP-260 und WP-261).
2. WP-354 teilt den Import in mehrere Drafts, die nacheinander publiziert werden (nicht atomar; ein Fehler hinterlässt einen Teilstand).
3. WP-354 legt Elternbestandteile direkt produktiv an (verletzt MET-53: Drafts wirken nicht produktiv).

**Empfehlung:** Option 1. Nur sie hält den Import atomar und nicht produktiv bis zum Publizieren. Der Mehraufwand liegt in WP-260 (Prüfung) und WP-261 (Reihenfolge, Ersetzung).

**Blockiert / Auswirkung:** WP-260 prüft die Item-Verweise, WP-261 wendet sie in Abhängigkeitsreihenfolge an und ersetzt sie. WP-354 nutzt sie für abhängige Bestandteile. Der Import bleibt atomar und wirkt erst mit dem Publizieren produktiv (MET-53).

**Quelle:** docs/spec/katalog-v3/03-datenmodell-standorte-metamodell.md:99 (MET-53); Neufassung von WP-262

**Betroffene IDs:** API-06, AUD-07, EVT-03, MET-53, MET-54

**Betroffene WPs:** WP-260 (`met-drafts`), WP-261 (`met-publish`), WP-354 (`met-import`)

## E-69

**Thema:** UI-11/MET-53: Lesesicht eines Drafts in den Metamodell-Editoren und in der Formular-Vorschau

**Status:** entschieden (2026-10-28, Option 3)

**Festlegung:** Option 3. Nach einer Draft-Schreibung (202) zeigen die Editoren den Draft als Overlay; nur die Vorschau liest den Draft im Backend.

- **Editoren:** WP-430 legt die Draft-Items im Frontend über den produktiven Stand: Kennzeichnung „im Draft“ und Platzhalterzeilen. Die Metamodell-GET-Routen werten keinen Parameter `draft` aus, außer der Vorschau.
- **Vorschau:** `GET /api/v1/ci-types/<id>/form` nimmt den Parameter `draft=<draft_id>` an und löst das Formular mit den Items dieses Drafts auf. Ohne den Parameter gilt der veröffentlichte Stand. Der Aufruf verlangt wie `preview=true` zusätzlich `ci_type:manage` (Prüfung im Handler, E-62). Ein Draft einer anderen Organisation ergibt 404. Bei abgeschaltetem Modul `metamodel` ergibt der Parameter `draft` ebenfalls 404, wie die Erfassung über `?draft` (E-61). WP-351 setzt den Parameter um und erhält dafür WP-260 als Voraussetzung.
- **Oberfläche:** WP-435 zeigt bei aktivem Draft die Vorschau mit `draft=<draft_id>`. Der Layout-Editor (WP-316) schreibt weiter über `withDraft` in den Draft.
- **Abnahme:** ABN-01 (c) verlangt „Layout pro Rolle erstellen; Preview prüfen; publizieren“. WP-330 prüft die Vorschau des Drafts vor dem Publizieren.

**Aufgabe:** **erledigt 2026-10-28** A-36: Der Beschluss des Auftraggebers wählt Option 3: Overlay in den Editoren, Vorschau über `ci-types/<id>/form?draft=<draft_id>` im Backend. Zuständig war: Product Owner.

**Frage:** Wie sehen Editoren und Vorschau einen Draft-Stand?

**Optionen:**

1. Overlay der Draft-Items im Frontend; Vorschau nur veröffentlicht (Übergangsregel).
2. Lesepfade mit `?draft=<id>` im Backend für alle Metamodell-GET-Routen einschließlich `GET /api/v1/layouts` und `GET /api/v1/ci-types/<id>/form` (Erweiterung von WP-260 und WP-351).
3. Nur die Vorschau `ci-types/<id>/form?draft=<id>` im Backend, die Editoren mit Overlay.

**Empfehlung:** Option 3. Sie deckt den Abnahmeschritt „Preview prüfen“ vor dem Publizieren mit einer Route ab und vermeidet Draft-Parameter an allen Lesepfaden.

**Blockiert / Auswirkung:** WP-351 ergänzt den Parameter `draft` an `GET /api/v1/ci-types/<id>/form` mit Voraussetzung WP-260; bei abgeschaltetem Modul `metamodel` antwortet der Parameter mit 404. WP-430 und WP-314 zeigen den Draft als Overlay. WP-316 schreibt in den Draft, WP-435 zeigt die Vorschau des Drafts. WP-330 prüft die Vorschau vor dem Publizieren. Weitere Lesepfade mit `draft` gibt es nicht.

**Quelle:** docs/spec/katalog-v3/03-datenmodell-standorte-metamodell.md:99 (MET-53); docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:97 (ABN-01 c); Neufassungen von WP-314 und WP-316

**Betroffene IDs:** ABN-01, API-06, GQL-03, MET-50, MET-52, MET-53, UI-11

**Betroffene WPs:** WP-351 (`met-form`), WP-260 (`met-drafts`), WP-352 (`met-drafts-capture`), WP-314 (`ui-meta-templates-publish`), WP-430 (`ui-meta-draft-context`), WP-316 (`ui-form-builder`), WP-435 (`ui-form-preview`), WP-330 (`g2-abnahme`)

## E-70

**Thema:** MET-45: Ablösung der attributgebundenen Regeln neben dem Regelkatalog

**Status:** entschieden (2026-10-28, Option 3)

**Festlegung:** Option 3: `attribute_definition.validation` und `conditional` (`fieldmeta.ConditionalRules`, Phase 1) bestehen in Phase 2 neben dem Regelkatalog `/api/v1/rules` (WP-348) fort. Beide Quellen gelten. Die Oberfläche legt neue Bedingungen und Validierungen nur im Katalog an. Sie zeigt die Attributregeln nur lesend (WP-433, WP-434). Die API der Attributdefinitionen bleibt unverändert (API-01). Die Überführung in Katalogregeln (Option 2) ist nicht Teil von Phase 2; sie gehört in eine spätere Phase.

**Aufgabe:** Keine. A-37 ist erledigt (Beschluss des Auftraggebers vom 2026-10-28: Option 3).

**Frage:** Werden die attributgebundenen Regeln in Katalogregeln überführt oder dauerhaft parallel geführt?

**Optionen:**

1. Dauerhaft parallel, gleichrangig auch in der Oberfläche.
2. Überführung in Katalogregeln per Datenmigration; `validation` und `conditional` werden deprecated (Sunset ≥ 12 Monate nach API-01).
3. Parallel in Phase 2, neue Regeln nur im Katalog; Überführung in einer späteren Phase (Übergangsregel).

**Empfehlung:** Option 3. Sie bricht keine G1-Schnittstelle und hält die Oberfläche eindeutig. Die Überführung (Option 2) gehört in eine spätere Phase, wenn MET-40 bis MET-43 vorliegen (E-37).

**Blockiert / Auswirkung:** WP-257 und WP-348 führen beide Quellen nebeneinander. WP-433 und WP-434 legen neue Regeln nur im Katalog an und zeigen Attributregeln lesend. Die API der Attributdefinitionen ändert sich nicht.

**Quelle:** backend/internal/fieldmeta/fieldmeta.go (`ConditionalRules`, `Predicate`); backend/internal/citype/attribute_definition.go (`validation`, `conditional`); Neufassung von WP-315

**Betroffene IDs:** API-06, AUD-07, MET-10, MET-45, UI-11

**Betroffene WPs:** WP-257 (`met-rules`), WP-348 (`met-rules-catalog`), WP-433 (`ui-rule-catalog`), WP-434 (`ui-rule-computed`)

## E-71

**Thema:** RBA-07/MET-53: Import von Feldrechten aus dem Metamodell-Export

**Status:** entschieden (2026-10-28, Option 3)

**Festlegung:** Option 3: Phase 2 importiert keine Feldrechte. WP-358 exportiert `field_grant` und `field_sensitivity` mit Schlüsseln. `POST /api/v1/metamodel/import` (WP-354) legt nur einen Draft an, und Draft/Publish (WP-260, WP-261) kennen keine Grants. Der Import nimmt die Abschnitte an (DTO-Erweiterung in WP-358). Er übernimmt sie nicht und nennt sie im Importergebnis als nicht übernommen.

**Frage:** Wie kommen exportierte Feldrechte in eine andere Organisation?

**Optionen:**

1. Übernahme über den Draft: Draft und Publish werden um Grants erweitert.
2. Eigene Route `POST /api/v1/field-permissions/import`: sofort wirksam, Konfliktbericht für unbekannte Rollen und Typen.
3. Kein Import in Phase 2; die Abschnitte werden angenommen und als nicht übernommen gemeldet (Übergangsregel).

**Empfehlung:** Option 3. Grants verweisen auf Rollen, die je Organisation verschieden sind; ein Import braucht eine Rollenzuordnung, die kein Katalogtext beschreibt. Option 1 vergrößert Draft und Publish ohne Katalogauftrag.

**Blockiert / Auswirkung:** WP-358 erweitert die Import-DTO um `field_grant` und `field_sensitivity`. WP-354 meldet diese Abschnitte im Importergebnis als nicht übernommen. Draft und Publish bleiben ohne Grants.

**Quelle:** Neufassung von WP-264; E-43

**Betroffene IDs:** API-06, MET-54, RBA-07

**Betroffene WPs:** WP-354 (`met-import`), WP-358 (`fr-admin`)

## E-72

**Thema:** LCY-02: Bindung des Lifecycle-Modells `software` an Systemtypen

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1: WP-263 legt das System-Seed `software` an (LCY-02). Es bindet das Modell per Migration an die Systemtypen `application` und `software` (000104). Heute gibt es nur das Seed `physical_asset` (000055); kein Systemtyp hat `lifecycle_definition_id`, und `ci_check_lifecycle_model` (000090) lehnt `ci.lifecycle_state` ohne Modell ab. Nach WP-248 erben Org-Untertypen dieser Systemtypen das Modell. Sie können es durch ein eigenes ersetzen, aber nicht entfernen. `database` bleibt ohne Bindung, bis der Katalog ein Modell dafür nennt.

**Frage:** Bindet Phase 2 das Software-Modell an Systemtypen, oder bleibt die Bindung den Organisationen überlassen?

**Optionen:**

1. Bindung an `application` und `software` per Migration in WP-263.
2. Keine Bindung; Organisationen binden das Modell an Org-Untertypen über `lifecycle-models` (REST, E-63).

**Empfehlung:** Option 1. Ohne Oberfläche (E-63) bliebe das Modell sonst ungenutzt; `database` bleibt ohne Bindung, bis der Katalog ein Modell dafür nennt.

**Blockiert / Auswirkung:** WP-263 legt das Seed `software` und die Bindung an `application` und `software` per Migration an. WP-355 baut darauf auf. CIs dieser Typen und ihrer Org-Untertypen können Zustände des Modells `software` führen; `ci_check_lifecycle_model` lässt sie zu. Einen Zustand setzt die Migration keinem CI.

**Quelle:** docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:44 (LCY-02); backend/migrations/000055_cmdb_extensions.up.sql (Seed `physical_asset`); backend/migrations/000090_ci_health_lifecycle.up.sql (`ci_check_lifecycle_model`); backend/migrations/000104_ci_type_fields_seeds.up.sql; Neufassung von WP-263

**Betroffene IDs:** API-06, LCY-02, MET-01, MET-06

**Betroffene WPs:** WP-263 (`met-lifecycle-models`), WP-355 (`met-lifecycle-model-api`)

## E-73

**Thema:** Kategorie und Icon der globalen Systemtypen

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1: Kategorie und Icon der globalen Systemtypen liegen als Daten vor. Heute haben nur die 6 logischen Systemtypen aus 000104 `ci_type.category`; die 17 physischen aus 000021 haben keine, und keiner der 23 Systemtypen hat ein Icon. WP-248 ergänzt seine Migration, die `ci_type` ohnehin ändert, um eine Datenmigration. Sie setzt `ci_type.category` für die 17 physischen Systemtypen (Kategorien des Go-Katalogs `citype.Templates()`) und `ci_type.icon` (Name eines lucide-react-Icons) für alle 23 Systemtypen. Sie ändert nur globale Zeilen (`organization_id IS NULL`). Down setzt die ergänzten Werte auf NULL zurück. Der Filter `category` von `GET /api/v1/ci-types` und alle Oberflächen (WP-317, WP-313) nutzen diese Quelle. `TypeTiles.tsx` (WP-317) ordnet Systemtypen keine Kategorie und kein Icon mehr im Frontend zu. Typen ohne Kategorie stehen unter ‚Weitere‘ (`other`), Typen ohne Icon erhalten ein generisches Geräte-Icon.

**Aufgabe:** Keine. A-38 ist erledigt (Beschluss des Auftraggebers vom 2026-10-28: Option 1, Datenmigration in WP-248).

**Frage:** Liegen Kategorie und Icon der Systemtypen als Daten vor oder nur als Präsentations-Fallback?

**Optionen:**

1. Datenmigration auf `ci_type.category` und `ci_type.icon` für alle Systemtypen (Ergänzung der Migration von WP-248, die `ci_type` ohnehin ändert).
2. Fallback im Frontend für die geführte Anlage (Übergangsregel).

**Empfehlung:** Option 1. Der Filter `category` und alle Oberflächen nutzen dann dieselbe Quelle; die Migration ist klein und betrifft nur globale Zeilen.

**Blockiert / Auswirkung:** WP-248: Datenmigration für `category` und `icon` der Systemtypen mit Down und Migrations-Roundtrip. WP-317: Die Kacheln lesen Kategorie und Icon aus `GET /api/v1/ci-types`; die Zuordnung Typschlüssel → Kategorie bzw. Icon im Frontend entfällt. WP-313: zeigt Kategorie und Icon aus derselben Quelle.

**Quelle:** backend/migrations/000021_spec_alignment.up.sql:89–108; backend/migrations/000104_ci_type_fields_seeds.up.sql:72–79; Neufassung von WP-317 (F2)

**Betroffene IDs:** MET-01, MET-06, MET-51, TST-03, UI-11, UI-12

**Betroffene WPs:** WP-248 (`met-inherit`), WP-313 (`ui-meta-types`), WP-317 (`ui-guided-create`)

## E-74

**Thema:** MGT-01/NFR-05/NFR-06: Endgültiges Löschen von Dokumentinhalten im versionierten Bucket

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1: Eine Lifecycle-Regel der Dokument-Buckets entfernt nicht aktuelle Objektversionen nach `backup_retention_days` (Default 35 Tage, NFR-06). Die Frist ist bestätigt. Die Buckets bleiben versioniert (`backup_policy` in `deploy/terraform/main.tf`: `versioning = true`; NFR-05, OPS-04). Heute nennt `backup_policy` zwar `retention_days`, `deploy/` enthält aber keine Lifecycle-Regel für nicht aktuelle Versionen. Der Purge (WP-364) setzt nur Löschmarken; das endgültige Entfernen leistet die Lifecycle-Regel. Sie ist Betriebsaufgabe in `deploy/terraform` und nicht Teil von WP-364 (dort „Nicht Bestandteil“).

**Aufgabe:** Keine. A-39 ist erledigt (Beschluss des Auftraggebers vom 2026-10-28: Frist 35 Tage wie die Backup-Frist).

**Frage:** Ob und nach welcher Frist werden nicht aktuelle Objektversionen gelöscht? Abzuwägen sind Backup-Frist 35 Tage (NFR-06), Wiederherstellbarkeit (NFR-05) und Löschpflichten.

**Optionen:**

1. Lifecycle-Regel: nicht aktuelle Versionen nach der Backup-Frist (35 Tage) entfernen.
2. Keine Regel: Inhalte bleiben in alten Versionen unbegrenzt erhalten.
3. Der Purge löscht alle Versionen sofort (verliert die Wiederherstellbarkeit nach NFR-05).

**Empfehlung:** Option 1. Sie hält Wiederherstellbarkeit im Rahmen der Backup-Frist und erfüllt Löschpflichten mit fester Frist. Option 2 verletzt Löschpflichten.

**Blockiert / Auswirkung:** WP-364 bleibt beim Purge mit Löschmarken. Betrieb: Lifecycle-Regel für nicht aktuelle Versionen der Dokument-Buckets in `deploy/terraform` mit der Frist `backup_retention_days`.

**Quelle:** docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:42, :44 (NFR-05, NFR-06); docs/spec/katalog-v3/01-installation-stack.md:53 (OPS-04); deploy/terraform/main.tf (`backup_policy`); deploy/terraform/variables.tf (`backup_retention_days`); Neufassung von WP-267

**Betroffene IDs:** MGT-01, NFR-05, NFR-06

**Betroffene WPs:** WP-364 (`doc-softdelete`)

## E-75

**Thema:** MGT-12: Verantwortliche am Vertrag und Wertebereich von `contract.kind`

**Status:** entschieden (2026-10-28, Option 2)

**Festlegung:** Option 2. MGT-12 nennt `kind`, aber keinen Wertebereich und keinen Owner. Es gilt:

- **(a) Verantwortliche:** keine Owner-Spalten. Empfänger von `contract.expiring` ist nur `org_admin` mit `contract:read` im Client des Vertrags (WP-270).
- **(b) Vertragsart:** `contract.kind` mit CHECK und englischen Schlüsseln nach GLO-10: `maintenance`, `support`, `lease`, `license`, `purchase`, `service`, `other` (WP-269). Die Oberfläche zeigt die Werte über i18n (WP-440).

**Aufgabe:** Keine. A-40 ist erledigt (Beschluss des Auftraggebers vom 2026-10-28: Option 2, Werteliste bestätigt).

**Frage:** (a) Erhält ein Vertrag Verantwortliche (`owner_user_id`/`owner_team_id`) als Empfänger der Ablaufbenachrichtigung? (b) Welche Werte hat `contract.kind`?

**Optionen:**

1. (a) Owner-Spalten nach E-80 (offene Zuständigkeit mit zusammengesetztem Fremdschlüssel); `contract.expiring` geht zusätzlich an `UserIDs`/`TeamIDs`. (b) Werteliste mit CHECK.
2. (a) Kein Owner (Übergangsregel). (b) Werteliste mit CHECK (Übergangsregel).
3. (a) Kein Owner. (b) Freitext ohne CHECK.

**Empfehlung:** Option 2. Der Katalog verlangt keinen Owner; die Werteliste macht die Vertragsart nach GLO-10 filterbar und übersetzbar. Option 3 verletzt GLO-10.

**Blockiert / Auswirkung:** WP-269 legt `contract.kind` mit CHECK auf die sieben Schlüssel an, ohne Owner-Spalten. WP-270 benachrichtigt nur `org_admin` mit `contract:read`. WP-440 zeigt die Vertragsarten über i18n.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:62 (MGT-12); docs/spec/katalog-v3/00-grundlagen.md:99 (GLO-10); Neufassungen von WP-269, WP-270 und WP-319

**Betroffene IDs:** API-06, GLO-10, MGT-12, NTF-05, OPS-06, UI-14

**Betroffene WPs:** WP-269 (`ast-supplier-contract`), WP-270 (`ast-contract-expiry`), WP-440 (`ui-contracts-suppliers`)

## E-76

**Thema:** AST-01/LCY-02: Zustand und Altfelder am Asset (Backfill `lifecycle_state`, Ablösung von `status`, Freitext `location` und Spiegelspalten)

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1. Befund: `asset.lifecycle_state` (000055) ist NULL, hat keine Vorgabe und wird vom Asset-Repository nie gesetzt. `asset.status` hat seit 000055 keinen CHECK mehr. Gelesen wird er vom Filter `AvailableOnly` (`backend/internal/savedview/filter.go`, `asset.status IN ('in_stock')`), von `AssetFacts.Status` (`backend/internal/ci/model.go`) und von der Inventur (`backend/internal/stocktake/pg_repository.go`). Die Inventur schreibt heute `lost`, `in_stock` und `maintenance`; diese Schreibzugriffe entfernt WP-287. Neben `location_id` besteht der Freitext `asset.location`. Ohne Backfill lehnte WP-278 die Entsorgung eines Bestandsassets mit 422 ab, weil `disposed` im Seed `physical_asset` nur aus `retired` und `repair` erreichbar ist. Es gilt:

- **(a) Backfill und Initialzustand (WP-271):** `status` → `lifecycle_state`: `in_stock` → `in_stock`, `assigned` → `deployed`, `maintenance` → `repair`, `retired` → `retired`, `disposed` → `disposed`. `lost` hat kein Ziel; die Zeile behält `lifecycle_state` NULL und geht nach `migration_quarantine` (Muster E-26). Neue Assets erhalten beim Anlegen den Initialzustand des Modells (`is_initial`, im Seed `ordered`).
- **(b) Ablösung von `status` und `location` (WP-370, WP-371):** WP-370 stellt die SQL-Leser auf `lifecycle_state` und `location_id` um (Filter `AvailableOnly`, Inventur-Leser, `loadAssetFacts`). Es leitet `status` per Trigger aus `lifecycle_state` ab: `deployed` → `assigned`, `repair` → `maintenance`, `retired` → `retired`, `disposed` → `disposed`, alle übrigen Zustände → `in_stock`; bei `lifecycle_state` NULL bleibt `status` unverändert. `status` und `location` bleiben bis zum Sunset (mindestens 12 Monate, API-01) als deprecated Felder lesbar (WP-271 markiert sie in der OpenAPI); den Sunset-Termin setzt WP-371.
- **(c) Spiegelspalten:** `asset_tag`, `purchase_cost`, `warranty_end` und `supplier` (WP-370) sowie die deprecated Felder in `CIAssetFacts` entfallen nach Ablauf der API-01-Frist in einer späteren Phase; den Sunset-Termin setzt WP-371.

**Aufgabe:** Keine. A-41 ist erledigt (Beschluss des Auftraggebers vom 2026-10-28: Option 1 mit Mapping, Behandlung von `lost`, Ableitung von `status` und Leserumstellung in WP-370).

**Frage:** Wie erhalten Bestandsassets einen Lifecycle-Zustand, und wann entfallen `status`, Freitext `location` und die Spiegelspalten?

**Optionen:**

1. Backfill, Leserumstellung und deprecated Felder mit Sunset; Entfernen in einer späteren Phase (Übergangsregel).
2. `status` bleibt dauerhaft parallel zu `lifecycle_state`; nur Backfill.
3. Kein Backfill: Bestandsassets ohne Zustand müssen manuell gesetzt werden, bevor sie entsorgt werden können.

**Empfehlung:** Option 1. Nur sie macht Bestandsassets entsorgbar und beseitigt die zweite Zustandsquelle, ohne API-01 zu verletzen. Die Leserumstellung (b) liegt bei WP-370, das dieselben Dateien für die Spaltenumbenennung ändert.

**Blockiert / Auswirkung:** WP-271: Backfill, Quarantäne für `lost`, Initialzustand und Deprecation von `status` und `location`. WP-370: Leserumstellung und Trigger für `status`. WP-371: Sunset-Termine. WP-278 kann Bestandsassets mit Backfill entsorgen. WP-263 liefert das Modell `physical_asset`; WP-287 schreibt `status` nicht mehr.

**Quelle:** backend/migrations/000009_phase2_assets.up.sql (`asset_status_check`); backend/migrations/000055_cmdb_extensions.up.sql (Seed `physical_asset`, `asset.lifecycle_state`, DROP `asset_status_check`); backend/internal/savedview/filter.go (`AvailableOnly`); backend/internal/stocktake/pg_repository.go; backend/internal/ci/model.go (`AssetFacts`); Neufassungen von WP-271, WP-278 und WP-287

**Betroffene IDs:** API-06, AST-01, DB-05, INV-01, LCY-02, LCY-03, MET-01, MET-06, MGT-02, UI-12, UI-14

**Betroffene WPs:** WP-263 (`met-lifecycle-models`), WP-271 (`ast-core`), WP-370 (`ast-field-names-db`), WP-371 (`ast-field-names-api`), WP-278 (`ast-disposal`), WP-279 (`ast-lifecycle-guards`), WP-287 (`inv-stocktake`), WP-319 (`ui-assets-contracts`)

## E-77

**Thema:** AST-01/NFR-06: Lösch- und Archivsemantik für Assets

**Status:** entschieden (2026-10-28, Option 2)

**Festlegung:** Option 2: Assets haben kein Soft-Delete. Das Lebensende eines Assets bildet der Lifecycle ab (`retired`/`disposed`). `asset` hat kein `deleted_at` (000009); `DELETE /api/v1/assets/<id>` löscht hart (`PGRepository.Delete` in `backend/internal/asset/pg_repository.go`). Das Löschen scheitert schon heute bei Historie: `asset_movement.asset_id` ist ON DELETE CASCADE, der Trigger `reject_asset_movement_mutation` (000055) verbietet das Löschen; `disposal_record.asset_id` ist ON DELETE SET NULL, und der Datensatz ist append-only (000049). Das kanonische Ledger führt `stock_movement.asset_id` ON DELETE RESTRICT (WP-376). WP-372 antwortet mit 409, wenn Bewegungen oder Entsorgungsnachweise bestehen. Assets ohne Historie bleiben hart löschbar.

**Aufgabe:** Keine. A-42 ist erledigt (Beschluss des Auftraggebers vom 2026-10-28: Option 2).

**Frage:** Gibt es für Assets ein Soft-Delete oder Archiv?

**Optionen:**

1. Soft-Delete mit `asset.deleted_at`, Ereignis `asset.deleted`, Restore und Purge nach 90 Tagen (NFR-06) nur ohne Historie.
2. Archiv über den Lifecycle (`retired`/`disposed`); DELETE nur ohne Historie, sonst 409 (Übergangsregel).

**Empfehlung:** Option 2. Bewegungen und Entsorgungsnachweise sind revisionssicher und dürfen nicht verwaisen; das Lebensende eines Assets bildet der Lifecycle ab. Ein Soft-Delete brächte Filter in allen Lesepfaden ohne Katalogauftrag.

**Blockiert / Auswirkung:** WP-372: DELETE mit 409 bei Bewegungen oder Entsorgungsnachweisen, sonst hartes Löschen; keine Spalte `asset.deleted_at`. WP-376 und WP-274: Bewegungen bleiben unlöschbar. WP-413: Massenlöschen folgt derselben Regel.

**Quelle:** backend/migrations/000009_phase2_assets.up.sql; backend/migrations/000049_disposal.up.sql; backend/migrations/000055_cmdb_extensions.up.sql:329–370; backend/internal/asset/pg_repository.go (`Delete`); Neufassungen von WP-271 und WP-274

**Betroffene IDs:** AST-01, AST-07, BLK-01, BLK-02, EVT-03, STK-04

**Betroffene WPs:** WP-372 (`ast-version-events`), WP-274 (`ast-movement`), WP-376 (`ast-movement-ledger`), WP-413 (`blk-ops-assets`)

## E-78

**Thema:** AST-01/AST-04: Benennung des Primär-CI (`primary_ci_id`)

**Status:** entschieden (2026-10-28, Option 2)

**Festlegung:** Option 2: `primary_ci_id` ist nur der API-Name über der Spalte `asset.ci_id` (WP-272). Die Spalte bleibt; WP-370 und WP-371 benennen sie nicht um. Eine Umbenennung beträfe `ci/merge.go`, `ci/pg_repository.go`, `jobs/harddelete.go`, `lifecycle/states.go`, `asset/*`, `assignment/*`, `disposal/*`, `stocktake/pg_repository.go`, die Trigger aus 000088 und mehrere Integrationstests; sie ist nicht vorgesehen.

**Frage:** Wird die Spalte `asset.ci_id` in `primary_ci_id` umbenannt oder nur der API-Name eingeführt?

**Optionen:**

1. Spalte umbenennen (eigenes WP).
2. Nur API-Name über der Spalte (Übergangsregel in WP-272).

**Empfehlung:** Option 2. AST-01 bindet den API-Vertrag, nicht den Spaltennamen; die Umbenennung kostet ein WP ohne fachlichen Nutzen.

**Blockiert / Auswirkung:** WP-272 führt `primary_ci_id` als API-Namen über `asset.ci_id`. WP-370 und WP-371 lassen die Spalte unverändert. Kein eigenes WP für eine Umbenennung.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md (AST-01, AST-04); backend/internal/ci/merge.go; backend/internal/jobs/harddelete.go; backend/internal/lifecycle/states.go; backend/migrations/000088_ci_asset_canonical.up.sql; Neufassungen von WP-271 und WP-272

**Betroffene IDs:** AST-01, AST-03, AST-04

**Betroffene WPs:** WP-370 (`ast-field-names-db`), WP-371 (`ast-field-names-api`), WP-272 (`ast-ci-link`)

## E-79

**Thema:** AST-05: Vorgabe für `child_mode` und Löschen mit Konfigurationskindern

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1 (WP-273):

- **(a) Vorgabe:** Wird `parent_ci_id` ohne `child_mode` gesetzt, gilt `standalone`. Die Kinder aus 000089 erhalten im Backfill `standalone`. API-01 verlangt additive Änderungen; `configuration` als Vorgabe würde Standort und Serial bestehender Kinder sperren.
- **(b) Löschen und Restore:** Das Soft-Delete eines Elternteils mit nicht gelöschten Konfigurationskindern ergibt 409 statt einer Kaskade. Das Restore eines Konfigurationskinds mit gelöschtem Elternteil ergibt 409. Es gibt keine Lösch- oder Restore-Kaskade; sie bräuchte Indexpflege je Kind im Indexing-Decorator (`backend/internal/ci/indexing_repository.go`).

**Frage:** Welcher `child_mode` gilt ohne Angabe, und was geschieht beim Löschen eines Elternteils mit Konfigurationskindern?

**Optionen:**

1. Vorgabe `standalone`; 409 beim Löschen bzw. Restore (Übergangsregel).
2. Vorgabe `configuration`; Kaskade beim Soft-Delete und Restore.

**Empfehlung:** Option 1. Sie ist additiv nach API-01 und vermeidet eine Kaskade ohne Indexpflege.

**Blockiert / Auswirkung:** WP-273: Vorgabe und Backfill `standalone`, 409 beim Soft-Delete eines Elternteils mit Konfigurationskindern und beim Restore eines Konfigurationskinds mit gelöschtem Elternteil.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:12 (AST-05); backend/migrations/000089_ci_parent_projection.up.sql; Neufassung von WP-273

**Betroffene IDs:** AST-04, AST-05

**Betroffene WPs:** WP-273 (`ast-composition`)

## E-80

**Thema:** SEC-05/Konvention 16: Nutzerverweise in Tabellen der Phase 2

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1. `app_user` hat kein Vault-Surrogat und bisher keinen Schlüssel `(id, organization_id)`. Protokollfelder verweisen heute ohne Fremdschlüssel auf Nutzer (`job.created_by`, 000115). Für alle neuen Nutzerverweise der Phase 2 gilt:

- **Offene Zuständigkeiten** (Owner, Halter) verweisen mit `(<x>_user_id, organization_id) REFERENCES app_user (id, organization_id)` ohne Löschaktion (`NO ACTION`). Ein Nutzer, auf den eine Zuständigkeit verweist, lässt sich nicht löschen (409, `ErrUserReferenced`, TLC-04). Deaktivieren oder Anonymisieren ersetzt das Löschen. Das gilt für `ci.owner_user_id` (WP-280), `asset.assigned_user_id` (WP-377) und `asset.owner_user_id` (WP-271).
- **Protokollfelder** (Akteur, Unterzeichner, Ersteller) führen die Nutzer-ID ohne Fremdschlüssel. Sie bleiben nach dem Löschen des Nutzers stehen. Das gilt etwa für `stock_movement.signer_user_id` (WP-276).
- `app_user.id` gilt als pseudonymer Schlüssel im Sinne von SEC-05. Name und E-Mail kommen nur über die Nutzerauflösung.
- Den Schlüssel `app_user_id_organization_key UNIQUE (id, organization_id)` legt WP-271 an. WP-280 und WP-377 setzen ihn voraus (beide haben WP-271 als Voraussetzung). `team_id_organization_key` liefert WP-264.

**Frage:** Wie verweisen neue Spalten auf Nutzer, und gilt `app_user.id` als Surrogat nach SEC-05?

**Optionen:**

1. Offene Zuständigkeiten mit zusammengesetztem Fremdschlüssel, Protokollfelder ohne Fremdschlüssel (Übergangsregel).
2. Alle Nutzerverweise ohne Fremdschlüssel (Muster `job.created_by`).
3. Alle Nutzerverweise mit zusammengesetztem Fremdschlüssel.

**Empfehlung:** Option 1. Zuständigkeiten müssen auf existierende Nutzer derselben Organisation zeigen (K19, SR-03); Protokolle müssen das Löschen von Nutzern überstehen.

**Blockiert / Auswirkung:** WP-271 legt `app_user_id_organization_key` an (das Down entfernt ihn) und `asset.owner_user_id` mit zusammengesetztem Fremdschlüssel ohne Löschaktion. Die Angabe „Nutzer-Surrogat ohne Fremdschlüssel (Muster `job.created_by`)“ in Migration und Akzeptanzkriterium von WP-271 entfällt. WP-280 legt den Schlüssel nicht mehr selbst an und entfernt ihn im Down nicht. WP-377 nennt WP-271 als Herkunft des Schlüssels. WP-276 bleibt ohne Fremdschlüssel. Verträge erhalten nach E-75 keine Owner-Spalten.

**Quelle:** docs/spec/katalog-v3/03-datenmodell-standorte-metamodell.md:34; docs/spec/katalog-v3/00-grundlagen.md:31 (CH16, SEC-05); backend/migrations/000005_users_roles.up.sql; backend/migrations/000115_job.up.sql; Neufassungen von WP-271, WP-275, WP-276 und WP-280

**Betroffene IDs:** AST-01, AST-02, CI-01, CI-07, DB-05, SEC-05, TEN-05, TLC-04

**Betroffene WPs:** WP-271 (`ast-core`), WP-377 (`ast-assignment-holder`), WP-276 (`ast-signature`), WP-280 (`ci-owner-tags-fields`)

## E-81

**Thema:** API-01: Verschärfungen an G1-Routen (`attributes.tags`, Beziehungstypen)

**Status:** entschieden (2026-10-28, Option 3)

**Festlegung:** Option 3. Zwei Neufassungen ändern G1-Verhalten nicht additiv. Bisher sind Tags in `attributes.tags` frei schreibbar (POST/PATCH `/cis`, Ingest, `PUT /cis/<id>/fields/tags/override`). `POST /api/v1/relationship-types` mit dem Schlüssel eines Systemtyps legt heute einen Org-Typ an, der den Systemtyp überschattet (`GetByKey` sortiert `organization_id NULLS LAST`). Es gilt:

- **Beziehungstypen (WP-294):** Die Prüfungen gelten als Korrektur mit Release-Hinweis. 409 für den Schlüssel eines Systemtyps, 400 bei ungültiger `cardinality` und bei `undirected` außerhalb der Kategorie `network`, 409 beim Löschen eines benutzten Typs, 422 an `POST /api/v1/relationships` bei konfigurierten `allowed_*_types` bzw. Kardinalitäten.
- **Tags (WP-281):** `attributes.tags` an POST/PATCH `/cis` und im Ingest wird bis zum Sunset (mindestens 12 Monate) in `ci_tag` übersetzt. Die Antwort trägt `Deprecation` und `Sunset` wie bei E-25. Erst nach dem Sunset ergibt `attributes.tags` 400.
- **Override-Weg:** `PUT /cis/<id>/fields/tags/override` ergibt sofort 422.

**Aufgabe:** Keine. A-43 ist mit dem Beschluss vom 2026-10-28 erledigt.

**Frage:** Gelten die Verschärfungen als zulässige Korrektur mit Release-Hinweis, oder brauchen sie eine Deprecation-Frist nach API-01?

**Optionen:**

1. Alle als Korrektur mit Release-Hinweis (Empfehlung der Prüfung zu WP-281: sofortige Sperre).
2. Alle mit Deprecation-Frist: altes Verhalten bis zum Sunset, danach die Prüfung.
3. Gemischt: Prüfungen gegen Fehlzustände (Überschatten, ungültige Werte, Löschen benutzter Typen) als Korrektur; die Tags-Sperre mit Übersetzung bis zum Sunset (Übergangsregel).

**Empfehlung:** Option 3. Die Beziehungstyp-Prüfungen verhindern Zustände, die schon in G1 fehlerhaft waren. Die Tags-Sperre trifft dagegen gültige Clients und Collector-Daten; E-57 legt API-01 streng aus.

**Blockiert / Auswirkung:** WP-294 bleibt wie geplant. Der PR nennt die Verhaltensänderung als Release-Hinweis. WP-281 trägt Mehraufwand. Bisher lehnt `CheckReservedKeys` `tags` sofort mit 400 ab. Stattdessen übersetzen POST/PATCH `/cis` und der Ingest `attributes.tags` bis zum Sunset in `ci_tag` und setzen `Deprecation` und `Sunset`. Das Akzeptanzkriterium „Schreibsperre“ und der Testfall „POST/PATCH mit `attributes.tags` → 400“ in WP-281 werden darauf umgestellt. Die 400 greift erst nach dem Sunset. Der Override-Weg bleibt bei 422. Neue Phase-2-Pfade wie `set_fields` der Bulk-Aktionen (WP-304) lehnen `tags` weiter als reservierten Schlüssel ab.

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:4 (API-01); backend/internal/fieldmeta/fieldmeta.go (`ReservedNames`); backend/internal/relationshiptype/pg_repository.go (`GetByKey`, `Create`, `Delete`); E-25; E-57; Neufassungen von WP-281 und WP-294

**Betroffene IDs:** API-01, API-06, CI-07, CI-08, CI-12, IMP-10, REL-06

**Betroffene WPs:** WP-281 (`ci-tags`), WP-385 (`ci-tags-api`), WP-294 (`rel-custom-types`)

## E-82

**Thema:** API-02: Operator `all` für Mengenfelder

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1. `filter[tags][all]` (WP-386) ergänzt API-02 additiv. API-02 [B] nennt `exact`, `gte`, `lte`, `like` und `in`. Bei Mengenfeldern bedeutet `in` „mindestens einer“, `all` „alle“. `all` ist nur für Mengenfelder zugelassen. Die Erweiterung steht in der OpenAPI und in den Konformanzfällen des Filters.

**Frage:** Darf API-02 um `all` für Mengenfelder erweitert werden?

**Optionen:**

1. `all` zusätzlich zu `in` (additiv nach API-01).
2. Nur `in` (API-02 wörtlich).

**Empfehlung:** Option 1. „Alle Tags“ ist die übliche Tag-Suche; die Erweiterung bricht nichts.

**Blockiert / Auswirkung:** WP-386 liefert `filter[tags][all]` wie in seinen Akzeptanzkriterien (`api.OpAll` nur für `api.KindSet`). Keine weiteren WPs betroffen.

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:6 (API-02); Neufassung von WP-281

**Betroffene IDs:** API-02, CI-08

**Betroffene WPs:** WP-386 (`ci-tags-filter`)

## E-83

**Thema:** TKT-01: Ticket-Tags und Tag-Katalog

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1. Tickets führen Tags heute als `ticket.tags TEXT[]` (000013). WP-396 legt die Zuordnungstabelle `ticket_tag(ticket_id, tag_id, organization_id)` mit zusammengesetzten Fremdschlüsseln an (K19, Muster `ci_tag`) und übernimmt die Daten aus `ticket.tags`. `ticket.tags` bleibt als abgeleitetes API-Feld mit Katalogschlüsseln (`tag.key`). Zusammenführen und Löschschutz aus WP-385 erweitert WP-396 um `ticket_tag`, wie WP-387 es für `asset_tag_link` tut.

**Aufgabe:** Keine. A-44 ist mit dem Beschluss vom 2026-10-28 erledigt.

**Frage:** Bekommen Ticket-Tags eine Zuordnungstabelle, oder ändern die Katalogoperationen `ticket.tags` mit?

**Optionen:**

1. Tabelle `ticket_tag` wie bei CIs und Assets (Übergangsregel).
2. Die Katalogoperationen ändern `ticket.tags` mit (`array_replace`, ohne referenzielle Integrität).
3. Ticket-Tags bleiben Freitext ohne Katalogbezug.

**Empfehlung:** Option 1. CIs (`ci_tag`) und Assets (`asset_tag_link`) nutzen Zuordnungstabellen; dieselbe Form hält Katalogoperationen und Filter einheitlich.

**Blockiert / Auswirkung:** WP-396 legt `ticket_tag` mit Datenübernahme an. Es stellt den Filter `tags` auf die Tabelle um und erweitert Zusammenführen und Löschschutz von WP-385 um `ticket_tag`. Dafür braucht WP-396 WP-385 als Voraussetzung. Die Aussage „Keine Tabelle `ticket_tag`“ und der Punkt „Entscheidungsbedarf“ unter „Nicht Bestandteil“ entfallen. Der RLS-Matrix-Test erfasst die neue Tabelle.

**Quelle:** backend/migrations/000013_phase2_tickets.up.sql:21; backend/internal/ticket/model.go; Neufassung von WP-290 (Teil tkt-links)

**Betroffene IDs:** TKT-01

**Betroffene WPs:** WP-396 (`tkt-links`)

## E-84

**Thema:** STK-01/TEN-05: Gesamtbestand von Mengenartikeln über Scopes

**Status:** entschieden (2026-10-28, Option 1: (a) ja, (b) nein)

**Festlegung:** Option 1. Σ `stock_level.on_hand` hängt unter `stock_level_isolation` (Client- und Site-Prädikat, WP-283) vom Scope ab. WP-285 führt den Gesamtbestand deshalb als `consumable.on_hand_total`.

- **(a)** `on_hand_total` eines sichtbaren Artikels zeigt auch site- oder client-gescopten Principals den Gesamtbestand über alle Bins, also auch Mengen außerhalb ihres Scopes. Der Mindestbestand ist eine Eigenschaft des Artikels. Je Bin bleibt `GET /api/v1/consumables/<id>/stock` an den Scope gebunden.
- **(b)** Client-gescopte Principals buchen keine Artikel ohne Client, weil sie die `consumable`-Zeile nicht sperren dürfen (TEN-05, `consumable_scoped_update` aus 000079), wie `AddMovement` in Phase 1.

**Frage:** Darf ein gescopter Principal den Gesamtbestand sehen, und dürfen client-gescopte Principals Artikel ohne Client buchen?

**Optionen:**

1. (a) ja, (b) nein (Übergangsregel).
2. (a) nein: `on_hand_total` nur für ungescopte Principals, sonst Σ der sichtbaren Bins; (b) nein.

**Empfehlung:** Option 1. Der Mindestbestand gilt für den Artikel; eine Scope-abhängige Summe würde Warnungen je Betrachter unterschiedlich auslösen.

**Blockiert / Auswirkung:** WP-285 führt `on_hand_total` scope-unabhängig. WP-283 hält die Bin-Sicht scope-gebunden. WP-388 lehnt Buchungen client-gescopter Principals auf Artikel ohne Client ab. Keine Änderung an den WP-Texten nötig.

**Quelle:** backend/migrations/000079_rls_scoped_write_using.up.sql; backend/internal/consumable/pg_repository.go (`AddMovement`); docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:12 (TEN-05); Neufassung von WP-285

**Betroffene IDs:** API-06, AST-07, NTF-05, STK-01, STK-02, STK-03, TEN-05

**Betroffene WPs:** WP-283 (`stk-consumable-model`), WP-388 (`stk-stock-bookings`), WP-285 (`stk-min-level`)

## E-85

**Thema:** NTF-04 [B]: Digest bei Benachrichtigungen

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1. NTF-04 [B] verlangt „Deduplizierung und Digest (z. B. Reviews stündlich gebündelt)“. Der Notifier (`backend/internal/notify/notifier.go`, `notification_delivery` aus 000122) kennt Deduplizierung, Ruhezeiten und stummgeschaltete Kategorien, aber keinen Digest. Ein Folge-WP in M2.0b liefert die stündliche Bündelung je Nutzer und Kategorie über `notification_delivery`. WP-463 korrigiert die Zeile NTF-04 in `docs/acceptance/g1-gate.md`: Der Digest fehlte in G1, WP-139 hatte ihn nicht als Kriterium. Ein PASS für NTF-04 in G2 setzt den Digest voraus.

**Aufgabe:** Keine. A-45 ist mit dem Beschluss vom 2026-10-28 erledigt.

**Frage:** Liefert ein Folge-WP den Digest nach, oder gilt die Lücke bis G2 als bekannte Einschränkung?

**Optionen:**

1. Folge-WP in M2.0b: stündliche Bündelung je Nutzer und Kategorie über `notification_delivery`; WP-463 korrigiert die Zeile NTF-04 in `docs/acceptance/g1-gate.md`.
2. Bekannte Einschränkung bis G2; WP-463 setzt NTF-04 in `docs/acceptance/g1-gate.md` auf „teilweise“ mit Verweis auf diese Entscheidung.

**Empfehlung:** Option 1. NTF-04 ist [B] und gilt für jedes Gate; ein falsches PASS darf nicht in G2 übernommen werden.

**Blockiert / Auswirkung:** Der Plan braucht ein neues WP in M2.0b für den Digest (NTF-04); bisher gibt es keines. WP-463 übernimmt die Korrektur der Zeile NTF-04 in `docs/acceptance/g1-gate.md`; sein Text nennt sie bisher nicht. WP-331 prüft NTF-04 mit Digest. WP-285 bleibt ohne eigenen Digest. Die dort „gesondert geplante“ Lücke schließt das neue WP.

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:78 (NTF-04); backend/internal/notify/notifier.go; backend/migrations/000122_notification.up.sql; docs/acceptance/g1-gate.md:431; Neufassung von WP-285

**Betroffene IDs:** DIS-08, DOD-01, GATE-01, GATE-02, GATE-05, GLO-11, INS-01, INS-04, NTF-04, OPS-06, SEC-11, SEQ-01, TEN-09, TST-04

**Betroffene WPs:** WP-463 (`g1-nachweise`), WP-331 (`g2-gate`)

## E-86

**Thema:** STK-05: Benachrichtigung des Antragstellers bei Statuswechsel einer Bestellung

**Status:** entschieden (2026-10-28, Option 2)

**Festlegung:** Option 2. Phase 2 liefert keine Benachrichtigung des Antragstellers bei Statuswechseln interner Bestellungen. Der Katalog verlangt sie nicht (STK-05, NTF-05). Sie bräuchte einen Eventtyp `order.*` im öffentlichen Ereignisvertrag über EVT-03 hinaus. Ein Folge-WP ist nicht vorgesehen. WP-286 führt sie unter „Nicht Bestandteil“.

**Frage:** Liefert ein Folge-WP eine Benachrichtigung bei Statuswechseln interner Bestellungen?

**Optionen:**

1. Folge-WP mit Eventtyp `order.status_changed` und Empfänger Antragsteller.
2. Keine Benachrichtigung in Phase 2.

**Empfehlung:** Option 2. Ohne Katalogauftrag erweitert sie den öffentlichen Ereignisvertrag (EVT-03) dauerhaft.

**Blockiert / Auswirkung:** WP-286 liefert keine Events `order.*`. Unter „Nicht Bestandteil“ steht bisher, die Benachrichtigung werde „ein eigenes Folge-WP“. Dieser Satz verweist künftig auf diese Entscheidung: kein Folge-WP in Phase 2.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:28 (STK-05); docs/spec/katalog-v3/06-audit-sicherheit-events.md:52 (EVT-03); Neufassung von WP-286

**Betroffene IDs:** STK-05

**Betroffene WPs:** WP-286 (`stk-orders`)

## E-87

**Thema:** IMP: Transit über Subnetze und VLANs im Impact

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1. WP-298 hängt Subnetz- und VLAN-Knoten nur als Blätter an. Ein Ausfall erreicht über ein gemeinsames Subnetz oder VLAN keine weiteren CIs. `count` und `by_category` zählen die projizierten Knoten nicht als betroffene CIs.

**Frage:** Soll ein Ausfall über ein gemeinsames Subnetz oder VLAN weitere CIs heuristisch erreichen?

**Optionen:**

1. Nein, Subnetze und VLANs nur als Blätter.
2. Ja: wiederholte Traversierung über Netzknoten; braucht ein neues Statementbudget (NFR-02, WP-335) und eine Regel für `count`/`by_category`.

**Empfehlung:** Option 1. Option 2 hat kein konstantes Statementbudget und würde in großen Subnetzen fast jedes CI als betroffen melden.

**Blockiert / Auswirkung:** WP-298 bleibt wie geplant (Blätter, konstantes Statementbudget). Der Punkt „Transit über Subnetze und VLANs zu weiteren CIs (Entscheidungsbedarf)“ unter „Nicht Bestandteil“ verweist auf diese Entscheidung. Kein neues Statementbudget nach WP-335.

**Quelle:** Neufassung von WP-298; WP-335 (Statementbudget); E-57

**Betroffene IDs:** IMP-11

**Betroffene WPs:** WP-298 (`imp-projection`)

## E-88

**Thema:** BLK-02: Semantik der Bulk-Aktionen Archivierung, Template-Wechsel und Zuweisung

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1, wie in den Akzeptanzkriterien von WP-304, WP-412 und WP-413:

- **(a) `archive`:** Bei CIs nach BLK-02 Status `inactive` und Tag `archived`. Assets haben kein `inactive`: Tag `archived` und Lifecycle-Übergang nach `retired`. `retired` ist im Seed `physical_asset` nur aus `in_stock` und `deployed` erreichbar; andere Assets erscheinen im Partial-Failure-Report.
- **(b) `change_template`:** Versionsmigration je CI über `MigrateTemplate` bzw. Austausch per `replace_template_id` im Job `ci.bulk` mit `ci:write`; kein Job `template.migrate`.
- **(c) Zuweisung:** Bei CIs der Owner (`set_owner`, WP-280); `assign` nur für Assets.

**Frage:** BLK-02 nennt Archivierung, Template-Wechsel und Zuweisung, legt sie aber für Assets und CIs nicht fest.

**Optionen:**

1. Semantik wie oben.
2. Andere Festlegung durch den Auftraggeber.

**Empfehlung:** Option 1. Sie folgt BLK-02 für CIs und bildet die Begriffe für Assets auf den Lifecycle ab.

**Blockiert / Auswirkung:** WP-304, WP-412 und WP-413 setzen die Semantik wie in ihren Akzeptanzkriterien um. Der Vorbehalt „vorläufige Regel, siehe Entscheidungsbedarf“ an `archive` für Assets in WP-413 entfällt.

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:48 (BLK-02); backend/migrations/000055_cmdb_extensions.up.sql (Übergänge `physical_asset`); Neufassung von WP-304 (F7, F11, M4)

**Betroffene IDs:** BLK-01, BLK-02, CI-08, OVR-03

**Betroffene WPs:** WP-304 (`blk-ops`), WP-412 (`blk-ops-delegate`), WP-413 (`blk-ops-assets`)

## E-89

**Thema:** SR-04/NFR-02: Indexnutzung nicht-leakproof Operatoren unter RLS (Volltext, Attributfilter, Eindeutigkeit)

**Status:** entschieden (2026-10-28, Option 2 für Bezeichner und Attributgleichheit, Option 3 für Volltext)

**Festlegung:** Option 2 für Bezeichner und Attributgleichheit, Option 3 für Volltext. Unter FORCE RLS nutzt der Planer für die App-Rolle keinen Index, dessen Operator nicht leakproof ist (Muster `lower()` in 000134). Nach der Messung mit PostgreSQL 16 betrifft das auch `@@` (`ts_match_vq`), `->>`, `@>`, Array-Operatoren, `<<=` (`network_subeq`) und `numeric`-Vergleiche. Tags laufen über `ci_tag` mit leakproof Gleichheit und sind nicht betroffen. Es gilt:

- **Bezeichner und Attributgleichheit:** Seitentabellen bzw. gespeicherte Spalten mit leakproof Operatoren (`texteq`, Vergleiche auf `date`/`timestamptz`) für Gleichheit, Präfix und Suchwörter. WP-308 nutzt für Bezeichner eine Token-Tabelle mit B-Baum.
- **Volltext:** bleibt in Phase 2 linear im Budget bis 10.000 Objekte (E-17, WP-327).
- **Keine Freigabe von Funktionen:** Phase 2 setzt keine Systemfunktion per Migration `LEAKPROOF`. Das ist nur nach einer eigenen Sicherheitsbewertung zulässig.
- WP-468 dokumentiert Plan und Messwerte.

**Aufgabe:** Keine. A-46 ist mit dem Beschluss vom 2026-10-28 erledigt.

**Frage:** Wie werden Volltext, Attributfilter und Eindeutigkeitsprüfung unter RLS indexierbar?

**Optionen:**

1. Gewählte Funktionen per Migration `LEAKPROOF` setzen (Superuser; Sicherheitsbewertung der Fehlermeldungen; Systemfunktionen sind nicht Teil von `pg_dump`, das Setzen muss nach Restore und Upgrade erneut greifen).
2. Seitentabellen bzw. gespeicherte Spalten mit leakproof Operatoren (`texteq`, Vergleiche auf `date`/`timestamptz`) für Gleichheit, Präfix und Suchwörter.
3. Linear bis zur E-17-Grenze von 10.000 Objekten (Übergangsregel für Volltext).

**Empfehlung:** Option 2 für Bezeichner und Attributgleichheit, Option 3 für Volltext in Phase 2. Option 1 nur nach einer Sicherheitsbewertung, weil sie eine Superuser-Änderung an Systemfunktionen in jede Installation bringt. Konvention K22 nennt neben Ausdrucksindizes auch Operatorklassen.

**Blockiert / Auswirkung:** WP-308 baut die Bezeichnertabelle mit B-Baum wie geplant. Für Attributgleichheit (`filter[attributes.<key>][eq]`, `UniqueConflicts`) sieht der Plan noch kein WP mit Seitentabelle vor. Dort dokumentieren WP-468, WP-309 und WP-350 bisher den Scan. WP-266 führt Volltext über `search_document_field` und bleibt linear. WP-468 dokumentiert Plan und Messwerte; seine Variante „bei Freigabe“ mit `ALTER FUNCTION … LEAKPROOF` entfällt. WP-327 misst Volltext linear bis 10.000 Objekte. Keine Migration setzt `LEAKPROOF`.

**Quelle:** backend/migrations/000134_ci_identity_lowercase.up.sql; backend/migrations/000030_search_ai.up.sql:24; backend/migrations/000021_spec_alignment.up.sql:39; backend/internal/ci/pg_repository.go (`UniqueConflicts`); docs/security/review-g1.md (SR-04); Neufassungen von WP-308 und der Folge-WPs

**Betroffene IDs:** ABN-02, API-01, JOB-02, NFR-01, NFR-02, NFR-03, RBA-07, SRC-01, SRC-02, SRC-03

**Betroffene WPs:** WP-468 (`g1-rls-indexes`), WP-471 (`g1-scale`), WP-350 (`met-computed-refresh`), WP-266 (`fr-search-export-webhooks`), WP-308 (`src-fields`), WP-309 (`src-ast-assets`), WP-327 (`g2-perf`)

## E-90

**Thema:** SRC-02: Durchsuchbarkeit von `asset.custom_fields` und Wirkung von `searchable`

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1 (WP-308, WP-418):

- **(a) `asset.custom_fields`:** Alle skalaren Werte werden als `attributes.<key>` indiziert. Feldrechte gelten je Schlüssel nach WP-264. Asset-Attributdefinitionen gibt es in Phase 2 nicht.
- **(b) Änderung von `attribute_definition.searchable`:** Sie wirkt auf Bestandsobjekte nach dem nächsten Reindex-Job (WP-418). Der Indexjob `attribute.index` (`backend/internal/citype/ddl_job.go`) stößt keinen Reindex an.

**Frage:** (a) Sind Custom-Felder von Assets ohne Attributdefinition durchsuchbar? (b) Wann wirkt eine Änderung von `searchable` auf Bestandsobjekte?

**Optionen:**

1. (a) alle skalaren Werte; (b) nach dem nächsten Reindex-Job.
2. (a) erst mit Asset-Attributdefinitionen (ohne Phase-2-WP); (b) der Job `attribute.index` stößt den Reindex an.

**Empfehlung:** Option 1. SRC-02 nennt Custom-Felder ausdrücklich; der Reindex-Job hält den Schreibpfad der Attributdefinitionen frei von Massenarbeit.

**Blockiert / Auswirkung:** WP-308 indiziert alle skalaren Werte von `asset.custom_fields`. WP-418 liefert den Reindex-Job, über den eine Änderung von `searchable` auf Bestandsobjekte wirkt. Ein automatischer Reindex nach Änderung von `searchable` ist nicht Teil von Phase 2.

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:36 (SRC-02); backend/internal/citype/ddl_job.go (`IndexJobKind`); Neufassung von WP-308

**Betroffene IDs:** SRC-02

**Betroffene WPs:** WP-308 (`src-fields`), WP-418 (`src-fields-hits`)

## E-91

**Thema:** INV-03: Auslieferung des Batch-Etiketten-PDFs an den Browser

**Status:** entschieden (2026-10-28, Option 2)

**Festlegung:** Option 2: `GET /api/v1/labels/batch/<job_id>/download` (WP-395) antwortet dem Ersteller des Jobs mit JSON und dem Feld `download_url`, einer kurzlebigen, je Abruf frisch signierten URL. Alle anderen Principals erhalten 404. Eine Antwort 302 gibt es an diesem Pfad nicht. Das folgt dem Muster von Export (`DownloadURL` in `backend/internal/export/job_handler.go`, `ExportPage`) und Dokumenten (`backend/internal/document/handler.go`). Die SPA (WP-445) öffnet die URL direkt. Eine CORS-Freigabe des Blob-Speichers für die App-Origin ist nicht nötig.

**Frage:** Ein `fetch` aus der SPA kann ein 302-Ziel im Blob-Speicher nur mit CORS lesen; ein einfacher Link scheitert an der Bearer-Authentifizierung.

**Optionen:**

1. CORS-Freigabe des Blob-Speichers für die App-Origin als Betriebsvorgabe; Antwort 302.
2. JSON-Antwort mit kurzlebiger `download_url` nur für den Ersteller.

**Empfehlung:** Option 2. Sie nutzt ein bestehendes Muster und braucht keine Betriebsvorgabe je Installation.

**Blockiert / Auswirkung:** WP-395 liefert am Download-Pfad JSON mit `download_url` statt 302. Der Integrationstest prüft für den Ersteller 200 mit `download_url` und für andere Principals 404. WP-445 lädt das JSON über den API-Client und öffnet `download_url` direkt. Der Hinweis auf CORS entfällt.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:36 (INV-03); backend/internal/export/job_handler.go; backend/internal/document/handler.go; E-52; Neufassung von WP-320

**Betroffene IDs:** INV-03, UI-14

**Betroffene WPs:** WP-395 (`inv-labels`), WP-445 (`ui-stocktake-labels`)

## E-92

**Thema:** ABN-01 (c) [Q]: Ausführungsform der Metamodell-Abnahme

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1 nach dem Muster von (e) (`docs/acceptance/abn-01.md`): ABN-01 (c) läuft als Playwright-Ablauf gegen das gemockte Backend im CI-Job `e2e` (WP-324). Die Wirkung im Backend belegen die Integrationstests der umsetzenden WPs. WP-330 führt beide Nachweise im Protokoll. Der Beschluss des Auftraggebers vom 2026-10-28 bestätigt diese Form für G2 ([Q]). Ein Lauf gegen eine echte Installation mit dem Seed aus WP-325 ist für (c) nicht gefordert.

**Aufgabe:** Erledigt am 2026-10-28 (A-47): Der Beschluss des Auftraggebers bestätigt Option 1.

**Frage:** Läuft ABN-01 (c) gegen ein gemocktes Backend oder gegen eine echte Installation?

**Optionen:**

1. Gemocktes Backend im CI-Job `e2e` plus Integrationstests (Muster (e)).
2. Lauf gegen eine echte Installation mit dem Phase-2-Seed in `install-smoke.yml` wie bei (d) (+0,5 PT, Module nach E-61).

**Empfehlung:** Option 1. Sie folgt dem für G1 abgenommenen Muster (e) und hält die Abnahme im normalen CI-Lauf.

**Blockiert / Auswirkung:** WP-330 und WP-324 setzen die Abnahme im CI-Job `e2e` mit den Backend-Nachweisen der umsetzenden WPs um. WP-325 und `.github/workflows/install-smoke.yml` bleiben für (c) unverändert.

**Quelle:** docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:97 (ABN-01); docs/acceptance/abn-01.md; .github/workflows/ci.yml (Job `e2e`); .github/workflows/install-smoke.yml; Neufassung von WP-330

**Betroffene IDs:** ABN-01, TST-03, UI-14

**Betroffene WPs:** WP-324 (`ui-e2e-p2`), WP-330 (`g2-abnahme`)

## E-93

**Thema:** Termin der G1-Restbefunde ohne Phase-2-WP (Rest von SR-03, SR-10)

**Status:** entschieden (2026-10-28, Option 2)

**Festlegung:** Option 2: Der Rest von SR-03 und SR-10 wird vor GA behoben, mit WPs einer späteren Phase. `docs/security/review-g1.md` führt SR-03 (Medium, 178 von 194 Fremdschlüsseln zwischen Mandantentabellen ohne Organisation) und SR-10 (Low, Identitätssperre exklusiv bei mehr als 256 Schlüsseln je Record) als offen. WP-470 deckt von SR-03 die Verweise, die Phase 2 nutzt. Der Rest von SR-03 (Restliste in `rls.ReferencesWithoutOrganization`) und SR-10 haben den Termin vor GA. WP-329 führt beide in der Nachverfolgung der G1-Befunde mit diesem Termin. Sie blockieren G2 nicht.

**Aufgabe:** Erledigt am 2026-10-28 (A-48): Der Beschluss des Auftraggebers bestätigt den Termin vor GA.

**Frage:** Bis wann werden die G1-Restbefunde ohne Phase-2-WP behoben?

**Optionen:**

1. Vor G2, mit neuen WPs in M2.0b.
2. Vor GA, mit WPs einer späteren Phase (Übergangsregel).
3. Bekannte Einschränkung ohne Termin.

**Empfehlung:** Option 2. Beide Befunde sind Defense in Depth bzw. Verfügbarkeit (Medium/Low) und blockieren G2 nicht; ein Termin vor GA hält sie in der Nachverfolgung.

**Blockiert / Auswirkung:** WP-470 bleibt auf die Verweise der Phase 2 beschränkt. WP-329 führt den Rest von SR-03 und SR-10 als offen mit Termin vor GA. Phase 2 erhält dafür kein neues WP.

**Quelle:** docs/security/review-g1.md:140, :144 (SR-03, SR-10); Folge-WPs (Teil folgewps)

**Betroffene IDs:** ABN-03, SEC-11, TEN-09

**Betroffene WPs:** WP-470 (`g1-tenant-fk`), WP-329 (`g2-security`)

## E-94

**Thema:** RBA-05/STK-03: Recht für die Umwandlung von Mengenreservierungen (`reservations/<id>/convert`)

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1: `POST /api/v1/reservations/<id>/convert` behält das Routenrecht `assignment:write` (`routeRules` in `backend/internal/server/authz.go`, WP-277). `routeRules` unterscheiden nicht nach der Art der Reservierung. Bei Mengenreservierungen bucht die Umwandlung eine Entnahme `retrieval` (WP-391). Dafür prüft der Handler zusätzlich `stock:write` (`Principal.Has`, sonst 403 wie in `authz.go`), neben dem Feature `stock`. Damit verlangt jede Entnahme dasselbe Recht wie `POST /api/v1/stock-movements` (WP-388). Nach E-62 halten org_admin und engineer beide Rechte, client_technician keines der beiden.

**Frage:** Nach WP-277 verlangt `…/convert` `assignment:write`. Bei Mengenreservierungen ist die Umwandlung fachlich eine Entnahme (`stock:write`). Genügt `assignment:write`?

**Optionen:**

1. Routenrecht `assignment:write`; bei Mengenreservierungen zusätzlich `stock:write` im Handler.
2. Nur `assignment:write` für alle Reservierungen.

**Empfehlung:** Option 1. Sonst entnimmt eine Rolle mit `assignment:write`, aber ohne `stock:write` Bestand. Die Route ist neu (E-38); API-01 ist nicht berührt.

**Blockiert / Auswirkung:** WP-277 registriert die Route mit `assignment:write`. WP-391 prüft bei Mengenreservierungen im Handler zusätzlich `stock:write` und das Feature `stock`.

**Quelle:** backend/internal/server/authz.go (`routeRules`); backend/internal/identity/principal.go (`Has`); E-62; Neufassungen von WP-277 und WP-284

**Betroffene IDs:** API-06, AST-06, EVT-03, RBA-05, STK-03

**Betroffene WPs:** WP-277 (`ast-reservation`), WP-391 (`stk-availability-api`)

## E-95

**Thema:** ENT-06/INV-03: Entitlement `barcode` an den Barcode-Feldern von Assets und Standorten

**Status:** entschieden (2026-10-28, Option 2)

**Festlegung:** Option 2: Der Handler prüft beim Setzen von `asset.barcode` und `location.barcode` das Feature `barcode` nicht. `asset.barcode` (000051) bleibt über `PATCH /api/v1/assets/<id>` mit `assets_basic` schreibbar. `location.barcode` setzt WP-289 über den G1-Pfad `PATCH /api/v1/locations/<id>`. Das Feature `barcode` sperrt nach E-44 die Pfade `labels` und `assets/<id>/label`, also Lookup und Etiketten (WP-394, WP-395). Der Kamera-Scan der Oberfläche nutzt den Lookup. Der Dublettenschutz über `barcode_registry` (WP-289) gilt in allen Paketen.

**Aufgabe:** Erledigt am 2026-10-28 (A-49): Der Beschluss des Auftraggebers bestätigt Option 2.

**Frage:** Prüft der Handler beim Setzen von `asset.barcode` und `location.barcode` das Feature `barcode` (ENT-06: ab Standard)?

**Optionen:**

1. Handlerprüfung mit `entitlement.Service.Check`: Setzen oder Ändern eines Barcodes ohne `barcode` ergibt 403 `feature-not-entitled`; Lesen bleibt frei.
2. Keine Prüfung an den Feldern; das Feature sperrt nur Lookup, Scan und Etiketten (Übergangsregel).

**Empfehlung:** Option 2. AST-01 führt `barcode` als Feld des Assets, und Assets gehören mit `assets_basic` zu allen Paketen. Den Nutzen des Pakets (Lookup, Kamera-Scan, Etiketten nach INV-03) sperrt das Feature bereits.

**Blockiert / Auswirkung:** WP-289 setzt `location.barcode` und pflegt `barcode_registry` ohne Feature-Prüfung. WP-394 und WP-395 bleiben an `barcode` gebunden. WP-271 erhält keine Handlerprüfung.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:4, :36 (AST-01, INV-03); docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:104 (ENT-06); backend/internal/entitlement/middleware.go (`gatedRoutes`); backend/migrations/000051_rfid_gps.up.sql; E-44; Neufassung von WP-289

**Betroffene IDs:** ENT-06, INV-03

**Betroffene WPs:** WP-289 (`inv-barcode`), WP-394 (`inv-lookup`)

## E-96

**Thema:** RBA-07: Feldrechte an Kanten (Scope beider Enden, Objektart)

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1, so in WP-402 umgesetzt:

- **Scope:** WP-264 wertet Grants je Objekt mit dessen `client_id`/`site_id` aus. Eine Kante hat zwei Enden (`source_client_id`/`source_site_id`, `target_client_id`/`target_site_id`, 000061). Ausgewertet wird für beide Enden; das niedrigere Level gilt. Das entspricht der RLS-Policy `ci_relationship_isolation` (000063), die beide Enden verlangt.
- **Objektart:** Auf Beziehungs-DTOs wirken nur Grants der Objektart `relationship` (Typ-Grants über `relationship_type_id`, Feld-Grants `attributes.<key>` und `core.`-Felder). Grants der Objektart `ci` mit `relationship_type_id` wirken nicht auf Kanten.

**Frage:** Welcher Scope gilt für Feldrechte an einer Kante mit zwei Enden, und wirken Grants der Objektart `ci` mit `relationship_type_id` auf Beziehungs-DTOs?

**Optionen:**

1. Beide Enden, das niedrigere Level gilt; auf Kanten nur Grants der Objektart `relationship` (Übergangsregel).
2. Nur das Quell-Ende; Grants der Objektart `ci` mit `relationship_type_id` wirken zusätzlich auf die Kante.

**Empfehlung:** Option 1. Sie folgt der RLS-Policy, die beide Enden verlangt, und hält je Objektart eine Quelle für Grants.

**Blockiert / Auswirkung:** WP-402 wertet Feldrechte an Kanten für beide Enden aus und wendet nur Grants der Objektart `relationship` an. WP-264 liefert die Auswertung je Objekt.

**Quelle:** backend/migrations/000061_denormalize_ci_scope.up.sql; backend/migrations/000063_site_scope_policies.up.sql (`ci_relationship_isolation`); E-43; Neufassung von WP-295

**Betroffene IDs:** RBA-07

**Betroffene WPs:** WP-402 (`rel-attributes-grants`)

## E-97

**Thema:** API-01/EXP-01: Formatwert `datev` des Exports und seine Sperre

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1: `datev` bleibt der Formatwert. Die Umstellung auf Asset-Daten (WP-310) und die Sperre hinter `export_datev`, `assets_basic`, `asset:read` und dem Modul `inventory` (WP-422) gelten als Korrektur, weil DATEV [P2] nie Teil des G1-Vertrags war (`docs/acceptance/g1-gate.md`, Zeile EXP-01). Neue Spalten und Windows-1252 an `POST /api/v1/exports` und am veralteten `POST /api/v1/export/jobs` sowie neue 403 an diesen beiden Pfaden und an `GET /api/v1/export/cis` sind damit zulässig. `GET /api/v1/export/cis?format=datev` (`x-sunset: 2027-10-15`) liefert bis zum Sunset das CI-Layout, aber nur hinter dem Gate. Ein neuer Formatwert und eine Änderung von `chk_export_format` (000035) entfallen. Diese Festlegung beantwortet die Frage aus dem letzten Absatz der Festlegung von E-50 (CI-Layout von `GET /api/v1/export/cis?format=datev`).

**Aufgabe:** Erledigt am 2026-10-28 (A-50): Der Beschluss des Auftraggebers wählt Option 1.

**Frage:** Darf der veröffentlichte Formatwert `datev` auf Asset-Daten umgestellt und gesperrt werden, oder braucht die Umstellung einen neuen Formatwert mit Deprecation?

**Optionen:**

1. `datev` bleibt Formatwert; Umstellung und Sperre gelten als Korrektur, weil DATEV [P2] nie Teil des G1-Vertrags war (`docs/acceptance/g1-gate.md`, Zeile EXP-01). `GET /api/v1/export/cis?format=datev` liefert bis zum Sunset 2027-10-15 das CI-Layout, aber nur hinter dem Gate.
2. Neuer Formatwert (z. B. `datev_assets`) mit Gate und Erweiterung von `chk_export_format` (000035); `datev` bleibt ungesperrt im CI-Layout und erhält `Deprecation` und `Sunset` (mindestens 12 Monate).

**Empfehlung:** Option 1. DATEV ist [P2]; G1 hat EXP-01 nur für den [B]-Anteil abgenommen. Ein zweiter Formatwert hielte ein CI-Layout ohne Katalogauftrag bis zum Sunset offen. Bei Option 1 passt die Konsolidierung den letzten Satz von E-50 an.

**Blockiert / Auswirkung:** WP-422 kann beginnen und setzt die Sperre auf allen drei Exportpfaden um. WP-310 rendert für `datev` nur Assets und braucht keine Migration. WP-423 unterscheidet CI- und Anlagen-Export. Der letzte Absatz der Festlegung von E-50 und der DATEV-Punkt von A-18 (E-44) folgen dieser Festlegung.

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md:4, :50 (API-01, EXP-01); docs/acceptance/g1-gate.md:359; backend/internal/export/export.go; backend/migrations/000035_export_job_formats.up.sql (`chk_export_format`); api/openapi.yaml (`/api/v1/export/cis`); E-50; E-57; Neufassung von WP-310

**Betroffene IDs:** API-01, ENT-02, ENT-05, EXP-01, MET-06, MET-45, TST-01

**Betroffene WPs:** WP-310 (`exp-datev`), WP-422 (`exp-datev-gate`), WP-423 (`exp-datev-ui`), WP-326 (`g2-tst01`)

## E-98

**Thema:** AST-07: Schreibpfad für den Standortwechsel serieller Assets

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1 (Fortschreibung von E-38): Der Standortwechsel eines seriellen Assets wird als Bewegung `transfer` mit `asset_id` über `POST /api/v1/stock-movements` gebucht. Gebucht wird über den Bewegungsdienst aus WP-274 in `stock_movement`; das ist die eine Bewegungshistorie nach AST-07. Die Route verlangt nach WP-388 das Recht `stock:write` und das Feature `stock` (E-44). Ihr Handler nimmt `asset_id` nur mit `movement_type` `transfer` an und prüft dafür zusätzlich `asset:move`. Andere Bewegungsarten mit `asset_id` ergeben 422. `PATCH /api/v1/assets/<id>` mit `location_id` bleibt 422 (WP-271). `POST /api/v1/assets/<id>/transfer` (WP-275) bleibt der Halterwechsel. WP-438 bietet „Verschieben“ auch für serielle Assets an.

**Aufgabe:** Erledigt am 2026-10-28 (A-51): Der Beschluss des Auftraggebers wählt Option 1.

**Frage:** Über welchen Pfad wird der Standortwechsel eines seriellen Assets gebucht?

**Optionen:**

1. Bewegung `transfer` mit `asset_id` über `POST /api/v1/stock-movements` (Fortschreibung von E-38; Bewegungsdienst aus WP-274, im Handler von WP-388 zusätzlich `asset:move`).
2. Eigener Pfad `POST /api/v1/assets/<id>/move` über den Bewegungsdienst.
3. Kein Umzug in Phase 2 außer der Rack-Platzierung (Übergangsregel).

**Empfehlung:** Option 1. AST-07 verlangt genau eine Bewegungshistorie in `stock_movement`. E-38 nennt für `POST stock-movements` bereits physische Bewegungen nach WP-274, und WP-388 sieht die Prüfung `asset:move` für diesen Fall vor.

**Blockiert / Auswirkung:** WP-274 liefert die Asset-Buchung `transfer` im Bewegungsdienst. WP-388 lässt `asset_id` mit `transfer` zu, prüft `asset:move` und testet das (statt 422). WP-438 nimmt den Standortwechsel serieller Assets in „Verschieben“ auf. E-38 (Pfadinventar) nennt diesen Schreibweg.

**Quelle:** docs/spec/katalog-v3/09-module-phase2plus.md:16 (AST-07); E-38; Neufassungen von WP-274, WP-283 und WP-318

**Betroffene IDs:** API-06, AST-04, AST-07, EVT-03, STK-01, STK-03, STK-04, UI-14, UI-15

**Betroffene WPs:** WP-274 (`ast-movement`), WP-388 (`stk-stock-bookings`), WP-438 (`ui-flows-stock`), WP-441 (`ui-asset-detail`)

## E-99

**Thema:** TKT-01/E-39: Datenquelle der Queue-Ansicht „meine Teams“

**Status:** entschieden (2026-10-28, Option 1)

**Festlegung:** Option 1: `GET /api/v1/tickets` erhält den Filter `team_id=mine`. Er liefert die Tickets der Teams, in denen der Principal Mitglied ist (`team_member`, 000014), mit Cursor und `total`. Der Filter ist additiv nach API-01 und gehört zu WP-290 (`FilterParams` in `backend/internal/ticket/model.go`). Die Queue-Ansicht „meine Teams“ liefert WP-446 hinter dem Feature `ticketing_standard`. Sie gehört nach E-39 zu Ticketing Standard und ist Teil der Abnahme. Die Eskalation bleibt bei WP-401.

**Aufgabe:** Erledigt am 2026-10-28 (A-52): Der Beschluss des Auftraggebers wählt Option 1 und ordnet Filter und Ansicht WP-290 und WP-446 zu.

**Frage:** Wer liefert Datenquelle und Ansicht „meine Teams“, oder entfällt die Ansicht aus der Standard-Matrix?

**Optionen:**

1. Filter `team_id=mine` an `GET /api/v1/tickets` über `team_member` des Principals (additiv nach API-01, WP-290); Ansicht hinter `ticketing_standard` in WP-446.
2. `GET /api/v1/me` liefert zusätzlich `team_ids`; die Oberfläche fragt je Team mit `team_id` ab.
3. Die Ansicht entfällt aus Standard; Team-Queues sind dann nur die Eskalation (WP-401).

**Empfehlung:** Option 1. Ein Filter liefert eine Liste mit Cursor und `total`. Option 2 bräuchte eine Abfrage je Team und könnte nicht über Teams hinweg sortieren.

**Blockiert / Auswirkung:** WP-290 liefert den Filter `team_id=mine` samt OpenAPI und Test. WP-446 liefert die Ansicht „meine Teams“ hinter `ticketing_standard`. WP-401 verweist für die Ansicht auf WP-446. E-39 ordnet die Ansicht Ticketing Standard zu.

**Quelle:** api/openapi.yaml (`/api/v1/me`, `/api/v1/teams`); backend/internal/ticket/model.go (`FilterParams`); backend/migrations/000014_phase2_users_teams_roles.up.sql (`team_member`); E-39; Neufassung von WP-321

**Betroffene IDs:** EVT-03, TKT-01, UI-14

**Betroffene WPs:** WP-290 (`tkt-core`), WP-401 (`tkt-sla-breach`), WP-446 (`ui-tickets`)

## Offene Aufgaben

Diese Aufgaben folgen aus den Empfehlungen. Mit dem Beschluss des Auftraggebers vom 2026-10-28 sind alle Aufgaben dieser Datei erledigt; kein WP wartet auf eine Aufgabe. Die Nummern sind über beide Entscheidungsdateien eindeutig: [`../entscheidungen.md`](../entscheidungen.md) belegt A-01 bis A-14, diese Datei zählt ab A-15. Die bis 2026-10-18 geführten Nummern A-12 bis A-19 dieser Datei heißen jetzt A-15 bis A-22. Die Phase-1-Aufgaben A-02, A-07 und A-11 sind ohne Nachreichung erledigt: Die Erklärung des Auftraggebers vom 2026-10-15 ersetzt die Lieferungen (E-59). Die überfällige A-03 führt A-28 fort.

| Nr. | Entscheidung | Aufgabe | Zuständig | Termin |
|---|---|---|---|---|
| A-15 | [E-37](#e-37) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers ersetzt die Lieferung der Texte MET-20, MET-30 bis MET-34 und MET-40 bis MET-44 (verlängerte A-03 aus Phase 1). Die Texte werden nicht nachgeliefert. Nach E-37 gilt die Umsetzung nach den Querverweisen (Option 2) als Regel; die IDs bleiben „nicht geliefert“ und sind von GATE-02 ausgenommen (E-48). | Auftraggeber/Product Owner | erledigt 2026-10-28 |
| A-16 | [E-38](#e-38) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers bestätigt das Pfadinventar der Phase 2 (E-38, Option 1). `inventory/items/<id>/convert` entfällt mit dem Sunset von `inventory/items*`. `cis/<id>/field-definitions` bleibt ohne Umbenennung neben `cis/<id>/instance-attributes`. `cis/<id>/state` und `assets/<id>/state` werden deprecated Aliase von `…/as-of`. Der Tab „Verfügbarkeit“ entfällt mit dem Sunset von `inventory/availability`; einen Nachfolger gibt es nicht. | Product Owner | erledigt 2026-10-28 |
| A-17 | [E-39](#e-39) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers bestätigt die Funktionsmatrix Ticketing Essential/Standard nach E-39, Option 1 (Teamzuweisung und Filter `team_id` Essential; SLA, Kalender, Eskalation und Queue-Ansicht „meine Teams“ Standard) und die Prioritätswerte `low`, `medium`, `high`, `critical`. | Product Owner | erledigt 2026-10-28 |
| A-18 | [E-44](#e-44) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers bestätigt nach E-44, Option 1 die Paketmatrix der Phase 2, die Zuordnung der Routen, das Speicherlimit (Staffel ohne Plandefault, E-34), das DATEV-Gate (E-97, Option 1) und die Wirkung von `valid_until` (stoppt nur `discovery`, kein Trial-Downgrade). WP-244 startet ohne weitere Freigabe. | Product Owner/Vertrieb | erledigt 2026-10-28 |
| A-19 | [E-47](#e-47) | **erledigt 2026-10-28** Beschluss des Auftraggebers zu E-47: WP-293 (Ticket-Provider-Schnittstelle, [O]) wird in Phase 2 ausgeführt, ohne Gate-Wirkung (GATE-05). `external_ref` gilt nur für Ticket und Ticketkommentar. | Product Owner | 2026-10-28 |
| A-20 | [E-50](#e-50) | **erledigt 2026-10-28** Beschluss des Auftraggebers zu E-50: Das Default-Mapping (`export/datev_mapping.json`) ist das bestätigte DATEV-Zielformat für die Anlagenbuchhaltung. Schnittstellenbeschreibung, Pflichtspalten und Beispiel werden nicht geliefert. Die Formatfälle in WP-326 prüfen das Default-Mapping; eine Nachprüfung entfällt. | Auftraggeber/Buchhaltung | 2026-10-28 |
| A-21 | [E-55](#e-55) | **erledigt 2026-10-28** Beschluss des Auftraggebers zu E-55: DIN 66399 und BSI-Datenträgerempfehlung mit den Pflichtfeldern je Methode sind bestätigt. WP-383 setzt die Tabelle ohne weitere Anpassung um. | Auftraggeber/Datenschutz | 2026-10-28 |
| A-22 | [E-02](../entscheidungen.md#e-02) | **erledigt 2026-10-28** für ABN-01 (a)/(b): Die Erklärung des Auftraggebers vom 2026-10-15 gilt als Nachweis für G1 und G2 (E-59). Die v2-Texte (B4, A-02) werden nicht nachgereicht. Offen bleibt der UI-14 [A]-Anteil (Self-Service, Access-Reviews): v2-Texte nachliefern; B4 umfasst ihn nicht. | Auftraggeber/Product Owner | ABN-01 (a)/(b): 2026-10-28; UI-14 [A] vor dem Add-on-Gate (GA) |
| A-23 | [E-59](#e-59) | **erledigt 2026-10-28** Beschluss des Auftraggebers zu E-59: Die Erklärung vom 2026-10-15 gilt als Nachweis für G1 und G2. Die Nachweise B3 (Pentest-Befundliste, A-07), B4 (v2-Texte, A-02) und B6 (Subprozessorenliste, A-11) sind gestrichen und werden nicht geliefert. E-02 und E-35 sind entschieden. | Auftraggeber; Subprozessorenliste: Betreiber | 2026-10-28 |
| A-24 | [E-60](#e-60) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers bestätigt Option 1, die gedeckelte Zählung mit Schwelle 10.000 und `total_capped`. Sie gilt für `GET /api/v1/cis` und `GET /api/v1/search`; die übrigen Listen behalten die exakte Zählung. | Product Owner/API-Verantwortlicher | vor WP-471 |
| A-25 | [E-61](#e-61) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers bestätigt Option 1: Module `metamodel` und `import`; Verträge, Lieferanten und Etiketten im Modul `inventory`; Kalender und Ticket aus Review im Modul `ticketing`. `subnets/<id>/reservations` und `relationships/<id>/changes` sind ohne Modul freigegeben (Kern). | Product Owner | vor WP-338 |
| A-26 | [E-62](#e-62) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers bestätigt die Rollenmatrix nach Option 1. engineer erhält weder `order:approve` noch `sla:write` (auch nicht für `business-calendars`), `lifecycle:manage` oder `relationship_type:manage`. client_technician erhält `ci_instance_attribute:manage` nicht. `saved_view:manage` liegt nur bei org_admin. | Product Owner | vor WP-245 |
| A-27 | [E-63](#e-63) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers wählt Option 2. Phase 2 liefert keine Admin-Oberfläche für Feldrechte und Lifecycle-Modelle, nur REST. | Product Owner | vor Beginn von M2.9 |
| A-28 | [E-04](../entscheidungen.md#e-04) | Fortsetzung von A-03 (Phase 1, überfällig seit G1): Teil 03 in `docs/audit/befunde.csv` und `docs/audit/99-gesamtbericht.md` konsolidieren, mit den Critical-Befunden LOC-10, LOC-11, MET-14, X-SCOPE und X-MANUAL für die GATE-01-Tabelle von G2 (WP-464). Die Anforderung der Texte DB-01, MET-20, MET-30 bis MET-34 und MET-40 bis MET-44 entfällt nach dem Beschluss vom 2026-10-28 (E-37). | Audit | vor WP-329, spätestens vor WP-331 |
| A-29 | [E-38](#e-38) | **erledigt 2026-10-28** Entfällt: Eine Route `assets/<id>/move` gibt es nicht und wird nicht angelegt. Den Standortwechsel serieller Assets bucht `POST /api/v1/stock-movements` als `transfer` mit `asset_id`, mit zusätzlicher Prüfung `asset:move` (E-38, E-98). | Product Owner | erledigt 2026-10-28 |
| A-30 | [E-43](#e-43) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers bestätigt die Präzisierungen von E-43: Team nur über `object_grant`, Sensitivität in `field_sensitivity`, konservative Listenfilter-Regel, `object_grant` ohne Wirkung auf die Sichtbarkeit, Webhooks ohne Service-Account nach der Standardregel ohne Rolle. Teilfrage Audit-Log nach der Empfehlung entschieden: `audit:read` legt alle Feldwerte offen, das Audit-Log wird nicht projiziert. Die Projektion von `/history/*` aus WP-359 bleibt; für Inhaber von `audit:read` schützt sie keine Werte, weil das Audit-Log dieselben Werte offenlegt. | Product Owner | erledigt 2026-10-28 |
| A-31 | [E-11](../entscheidungen.md#e-11) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers bestätigt E-11 (Option 1, Schnittmenge nach TEN-05) mit den Phase-2-Zusätzen: (a) `ci.owner_team_id` und `asset.owner_team_id` sind die Team-Spalte nach TEN-05. (b) Zeilen ohne Team sind für team-gescopte Rollen sichtbar, aber nicht schreibbar; beim Anlegen durch einen team-gescopten Principal ist `owner_team_id` aus dem Scope Pflicht, und Dokument-Links auf Ziele ohne Team (CI, Asset, Location) sind ausgeschlossen. (c) CI-Kindtabellen erben den Team-Scope nicht. RBA-03 `scope_kind team` in Rollenzuweisungen ist nicht Bestandteil der Phase 2 und wird mit einer späteren Phase geplant. | Product Owner | vor WP-280 und WP-267 |
| A-32 | [E-64](#e-64) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers bestätigt Option 1. Materialisiert werden nur System-Basis, Typkette und Sets; Sets gibt es nur an Org-Typen. | Product Owner/Architektur | vor WP-341 |
| A-33 | [E-65](#e-65) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers wählt die Empfehlung: keine Auswertung von `attributes.system_template` bis zur Lieferung von MET-30 bis MET-34, danach automatische Zuordnung mit Override-Schutz über ein Folge-WP. In Phase 2 gilt damit Option 3. | Product Owner | vor WP-342 |
| A-34 | [E-66](#e-66) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers wählt die Empfehlung: Layouts und geteilte Views berücksichtigen IdP-gemappte Standardrollen nach WP-357; WP-307 setzt beides um. Bis dahin gelten nur gespeicherte Zuweisungen. | Product Owner | vor WP-307 |
| A-35 | [E-68](#e-68) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers bestätigt Option 1: Item-Verweise mit dem Schlüssel `$item` im Payload, aufgelöst in WP-261. | Architektur/Product Owner | vor WP-260, spätestens vor WP-354 |
| A-36 | [E-69](#e-69) | **erledigt 2026-10-28** Der Beschluss des Auftraggebers wählt Option 3: Overlay in den Editoren, Vorschau über `ci-types/<id>/form?draft=<draft_id>` im Backend. | Product Owner | vor WP-430 |
| A-37 | [E-70](#e-70) | **erledigt 2026-10-28** Verhältnis von `validation`/`conditional` zum Regelkatalog entschieden: Option 3, parallel in Phase 2, neue Regeln nur im Katalog, Überführung in einer späteren Phase (Beschluss des Auftraggebers, E-70). | Product Owner | erledigt 2026-10-28 |
| A-38 | [E-73](#e-73) | **erledigt 2026-10-28** Kategorie und Icon der Systemtypen entschieden: Option 1, Datenmigration auf `ci_type.category` und `ci_type.icon` in WP-248 (Beschluss des Auftraggebers, E-73). | Product Owner | erledigt 2026-10-28 |
| A-39 | [E-74](#e-74) | **erledigt 2026-10-28** Frist für das Löschen nicht aktueller Dokumentversionen bestätigt: 35 Tage wie die Backup-Frist (`backup_retention_days`, Beschluss des Auftraggebers, E-74). | Auftraggeber/Datenschutz | erledigt 2026-10-28 |
| A-40 | [E-75](#e-75) | **erledigt 2026-10-28** Owner am Vertrag und Werteliste von `contract.kind` bestätigt: Option 2, kein Owner, Werteliste mit CHECK (Beschluss des Auftraggebers, E-75). | Product Owner | erledigt 2026-10-28 |
| A-41 | [E-76](#e-76) | **erledigt 2026-10-28** Mapping `status` → `lifecycle_state`, Behandlung von `lost`, Ableitung von `status` und die Leserumstellung in WP-370 bestätigt: Option 1 (Beschluss des Auftraggebers, E-76). | Product Owner | erledigt 2026-10-28 |
| A-42 | [E-77](#e-77) | **erledigt 2026-10-28** Lösch- und Archivsemantik für Assets entschieden: Option 2, Archiv über den Lifecycle, DELETE nur ohne Historie, sonst 409 (Beschluss des Auftraggebers, E-77). | Product Owner | erledigt 2026-10-28 |
| A-43 | [E-81](#e-81) | **erledigt 2026-10-28** Verschärfungen an G1-Routen eingeordnet (E-81, Option 3): Beziehungstyp-Prüfungen als Korrektur mit Release-Hinweis, `attributes.tags` mit Übersetzung bis zum Sunset, Override-Weg sofort 422. | Product Owner/API-Verantwortlicher | vor WP-281 und WP-294 |
| A-44 | [E-83](#e-83) | **erledigt 2026-10-28** Form der Ticket-Tags bestätigt (E-83, Option 1): Tabelle `ticket_tag` in WP-396. | Product Owner | vor WP-396 |
| A-45 | [E-85](#e-85) | **erledigt 2026-10-28** Digest nach NTF-04 wird nachgeliefert (E-85, Option 1): Folge-WP in M2.0b, Korrektur der Zeile NTF-04 in WP-463. | Product Owner | vor WP-463 |
| A-46 | [E-89](#e-89) | **erledigt 2026-10-28** Indexierung unter RLS entschieden (E-89): Seitentabellen für Bezeichner und Attributgleichheit, Volltext linear bis 10.000 Objekte, kein `LEAKPROOF`. | Architektur/Sicherheit | vor WP-308 |
| A-47 | [E-92](#e-92) | **erledigt 2026-10-28** Beschluss des Auftraggebers: ABN-01 (c) läuft gegen das gemockte Backend im CI-Job `e2e` plus Integrationstests (Option 1 von E-92). | Auftraggeber | vor WP-330 |
| A-48 | [E-93](#e-93) | **erledigt 2026-10-28** Beschluss des Auftraggebers: Rest von SR-03 und SR-10 vor GA, mit WPs einer späteren Phase (Option 2 von E-93). | Product Owner/Sicherheit | vor WP-329 |
| A-49 | [E-95](#e-95) | **erledigt 2026-10-28** Beschluss des Auftraggebers: Barcode-Felder bleiben ohne Gate `barcode` schreibbar (Option 2 von E-95). | Product Owner/Vertrieb | vor WP-289 |
| A-50 | [E-97](#e-97) | **erledigt 2026-10-28** Beschluss des Auftraggebers: `datev` bleibt Formatwert, Umstellung und Sperre gelten als Korrektur, Altpfad bis zum Sunset im CI-Layout hinter dem Gate (Option 1 von E-97). | Product Owner/API-Verantwortlicher | vor WP-422 |
| A-51 | [E-98](#e-98) | **erledigt 2026-10-28** Beschluss des Auftraggebers: Standortwechsel serieller Assets als `transfer` mit `asset_id` über `POST /api/v1/stock-movements` (Option 1 von E-98). | Product Owner | vor WP-274 |
| A-52 | [E-99](#e-99) | **erledigt 2026-10-28** Beschluss des Auftraggebers: Filter `team_id=mine` in WP-290, Ansicht „meine Teams“ in WP-446 (Option 1 von E-99). | Product Owner | vor WP-290 |
| A-53 | [E-51](#e-51) | **erledigt 2026-10-28** Beschluss des Auftraggebers zu E-51: Die Übernahme der Altsignaturen aus Migration 000047 (`assignment.checkout_signature`/`return_signature`) ist bestätigt (a). WP-381 wird ausgeführt; WP-379 legt die Sicherungstabelle `assignment_signature_legacy` an. Ein Verzicht (b) entfällt. | Product Owner | 2026-10-28 |
