# Archivierte Lastläufe (NFR-10, E-58)

Je `load.yml`-Lauf ein Verzeichnis `<Lauf-ID>/`, unverändert aus dem
Artefakt `load-archive-<Lauf-ID>` des Laufs übernommen (WP-439). Erzeugt
wird es von `tests/load/archive-run.sh` im Job `archive`; Inhalt und
Übernahmebefehl stehen in `tests/load/README.md`, Abschnitt „Archiv eines
Laufs“.

| Datei | Inhalt |
|---|---|
| `<Lauf-ID>/ergebnis.md` | Lauf, Commits, Eingaben, Vollständigkeit, je Job Status, CPU, RAM, Konfiguration, Datenbestand und jede k6-Schwelle mit Messwert |
| `<Lauf-ID>/<job>/<job>.json`, `<job>.txt` | k6-Zusammenfassung (`--summary-export`) und k6-Ausgabe ohne Fortschrittszeilen |
| `<Lauf-ID>/<job>/run-<job>.txt` | Lauf- und Server-Commit, Konfiguration, Versionen, `lscpu`, `/proc/meminfo`, `free -h` |
| `<Lauf-ID>/<job>/result-<job>.txt` | Ergebnis des Jobs und seiner Schritte |
| `<Lauf-ID>/<job>/seed-<job>.txt`, `volumes-<job>.txt`, `integrity-<job>.txt`, `statements-<job>.txt` | Seed-Dauer, Datenbestand nach dem Seed, Integritätsprüfung, Datenbankkosten |
| `<Lauf-ID>/SHA256SUMS` | Prüfsummen; `cd <Lauf-ID> && sha256sum -c SHA256SUMS` |

Bewertung der drei Archive eines Nachweises nach E-58 (Median über drei
Läufe; `tests/load/README.md`, Abschnitt „Bewertung über drei Läufe“):
`tests/load/evaluate-runs.sh <Lauf-ID-1> <Lauf-ID-2> <Lauf-ID-3>` mit den
Pfaden der drei Verzeichnisse hier.

Die Läufe und ihre Werte stehen in `docs/acceptance/lasttest-g1.md`
(Abschnitt „Läufe“). Noch ist kein Lauf archiviert.
