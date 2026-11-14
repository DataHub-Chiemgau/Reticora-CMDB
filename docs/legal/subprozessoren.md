# Subprozessoren (NFR-09)

> **Status (2026-11-09, WP-831):** Die Liste hat der Betreiber nicht
> geliefert ([E-35](../plan/entscheidungen.md#e-35), Aufgabe A-11). Nach dem
> Beschluss des Auftraggebers vom 2026-10-28 gilt seine Erklärung vom
> 2026-10-15 für NFR-09 („Subprozessoren dokumentiert und AVV-fähig“) als
> Nachweis für G1 und G2 ([E-103](../plan/phase2/entscheidungen.md#e-103));
> die Liste wird nicht nachgereicht, A-11 ist erledigt. Diese Datei enthält
> deshalb keine Einträge und erfindet keine Subprozessoren. Die Tabelle unten
> bleibt die verbindliche Struktur für spätere Einträge.

> **Phase 3 (2026-11-14, WP-1159, [E-242](../plan/phase3/entscheidungen.md#e-242)):**
> SMS-, Pager- und ADMP-Provider sind kundeneigene Konten: Die Organisation
> schließt den Vertrag selbst und hinterlegt die Zugangsdaten im Secret-Store
> ihrer Organisation. Der Betreiber setzt dafür keinen Dienstleister ein; die
> Provider sind deshalb keine Unterauftragsverarbeiter des Betreibers und
> stehen nicht in dieser Liste. Für Provider mit Sitz außerhalb der EU ist
> ein Drittlandhinweis in der Kanalkonfiguration geplant (kommt mit WP-1142
> und WP-1148; bis dahin ohne Hinweis). Die
> technischen und organisatorischen Maßnahmen stehen in der AVV-Anlage
> [`docs/security/avv-tom.md`](../security/avv-tom.md).

## Anforderungen an jeden Eintrag

- Verarbeitung personenbezogener Daten im Auftrag (Art. 28 DSGVO) mit
  abgeschlossenem oder abschließbarem AV-Vertrag (AVV-fähig).
- Verarbeitungsort in der EU für alle SaaS-Daten, also Datenbank, Backups, Logs
  und Suchindex (siehe [`docs/betrieb/referenzmodell.md`](../betrieb/referenzmodell.md)).
  Ein Zugriff aus einem Drittland braucht eine eigene Rechtsgrundlage und ist
  hier zu vermerken.
- Änderungen an der Liste werden den Kunden vor dem Wirksamwerden mitgeteilt.

## Liste

| Subprozessor | Leistung | Verarbeitete Daten | Verarbeitungsort (Region) | Rechtsgrundlage / AVV | Seit |
|---|---|---|---|---|---|
| – | *keine Einträge; Nachweis für NFR-09 ist die Erklärung des Auftraggebers vom 2026-10-15 (E-103)* | – | – | – | – |
