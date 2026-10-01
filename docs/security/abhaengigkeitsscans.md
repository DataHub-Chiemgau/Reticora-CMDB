# Abhängigkeits- und Container-Scans

Anforderungen: SEC-11 ([Q]: „Abhängigkeits- und Container-Scans in der CI;
CVE-Fix-SLA Critical 7 Tage, High 30 Tage“) und TST-04. Werkzeuge nach E-31:
govulncheck, npm audit, Trivy. Umsetzung: WP-004.

## Jobs

Alle Jobs stehen in `.github/workflows/ci.yml`. Sie laufen bei jedem Pull
Request und bei jedem Push auf `main`.

| Job | Gegenstand | Blockiert bei | Bericht (Artefakt, 30 Tage) |
|---|---|---|---|
| `govulncheck` (Matrix `backend`, `collector`, `edgecore`) | Go-Abhängigkeiten und Standardbibliothek jedes go.work-Moduls | Schwachstellen, deren betroffenes Symbol vom Code des Moduls erreicht wird | `govulncheck-<modul>` (JSON, auch nur importierte Funde) |
| `npm-audit` | `frontend/package-lock.json` | High/Critical in Produktionsabhängigkeiten (`--omit=dev`, ausgeliefertes Bundle) | `npm-audit` (Produktion und alle inkl. Entwicklung) |
| `image-scan` (Matrix `backend`, `collector`, `frontend`) | gebautes Container-Image: OS-Pakete und Anwendungsabhängigkeiten (Trivy) | Critical/High mit verfügbarem Fix (`ignore-unfixed`) | `trivy-<image>` (JSON) |

Begründung der Schwellen:

- **Go:** govulncheck bewertet ohne Schweregrad. Es weist aber nach, ob der
  verwundbare Code tatsächlich aufgerufen wird. Erreichbare Funde blockieren.
  Nur importierte Funde erscheinen im Bericht und werden mit dem nächsten
  Abhängigkeits-Update behoben.
- **npm:** Entwicklungswerkzeuge (Build, Tests, Lint) werden nicht
  ausgeliefert. Ihre Funde stehen im Bericht, blockieren aber nicht.
- **Container:** Funde ohne verfügbaren Fix lassen sich nicht innerhalb der SLA
  beheben. Sie stehen im Bericht und blockieren, sobald ein Fix erscheint.

## Ausnahmen und CVE-Fix-SLA

Akzeptierte Funde stehen in `security/scan-exceptions.txt`, je Zeile:

```text
<ID> exp:<JJJJ-MM-TT> # <Begründung, Ticket>
```

- IDs: `GO-…` (govulncheck), `GHSA-…` (npm audit), `CVE-…` (Trivy). Das
  Format ist das `.trivyignore`-Format; Trivy liest die Datei direkt.
- Jede Ausnahme braucht ein Ablaufdatum innerhalb der SLA, gerechnet ab der
  ersten Meldung des Funds: **Critical höchstens 7 Tage, High höchstens 30
  Tage**. Ein Eintrag ohne `exp:` gilt nicht als Ausnahme.
- Nach Ablauf greift die Ausnahme nicht mehr, der Job schlägt wieder fehl. Eine
  Verlängerung ist nur mit neuer Begründung im Review zulässig, etwa wenn kein
  Fix existiert.
- Reviewer prüfen beim Hinzufügen, ob das Datum zur SLA passt.

## Ablauf bei einem neuen Fund

1. Job-Log und Artefakt zeigen die ID, das Paket und die Fix-Version.
2. Abhängigkeit aktualisieren (`go get …@<fix>` und `go mod tidy`,
   `npm update`/`npm audit fix`, Basis-Image-Tag) und den PR erneut prüfen.
3. Ist das innerhalb des PRs nicht möglich, eine Ausnahme mit SLA-Datum und
   Ticket eintragen.
