# Architekturentscheidungen (ADR)

Dieses Verzeichnis gehört zur Repository-Struktur nach REP-01. Es enthält die
Architekturentscheidungen, die aus der Umsetzung entstehen. Jede Entscheidung
liegt in einer eigenen Datei `NNNN-kurzname.md` mit Status, Kontext,
Entscheidung und Folgen. `tests/rep01-structure.test.sh` prüft, dass jede
Datei hier verzeichnet ist.

Die Planungsentscheidungen E-01 bis E-36 (Phase 1) und E-37 bis E-55
(Phase 2) stehen nicht hier. Sie gehören zum Implementierungsplan und liegen
in `docs/plan/entscheidungen.md` bzw. `docs/plan/phase2/`. Ein ADR verweist auf
die E-Nummer, aus der es folgt.

| ADR | Titel | Status | Anforderungen | Bezug |
|---|---|---|---|---|
| [0001](0001-timescale-rls.md) | Mandantentrennung für Metriken (TimescaleDB) über Security-Barrier-Views | angenommen | TEC-06, MON-01 | Spike WP-039, Umsetzung WP-040 |

## Neue Entscheidung anlegen

1. Nächste freie Nummer wählen und `NNNN-kurzname.md` anlegen. Kopf: Status,
   Anforderungen, Umsetzung (WP), Nachweis (Test).
2. Zeile in der Tabelle oben ergänzen.
3. Ersetzt die Entscheidung eine ältere, bekommt die ältere den Status
   „ersetzt durch NNNN“.
