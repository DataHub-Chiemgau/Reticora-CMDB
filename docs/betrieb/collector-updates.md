# Collector-Auto-Updates

Anforderung: COL-06 ([B]; Air-Gapped-Betrieb [O]). Umsetzung: WP-148.

## Ablauf

1. Der Release-Workflow (`.github/workflows/release.yml`) baut den Collector
   mit eingebetteter Version (`collectorcmd.Version`), signiert jedes Binary
   keyless mit cosign und schreibt je Plattform ein Manifest
   `reticora-collector-linux-<arch>.release.json` mit Version, Kanal,
   Download-URL, SHA-256 und Signatur-Bundle. Ein Tag mit Vorabversion
   (`v1.4.0-beta.1`) ist Kanal `beta`, jedes andere Tag `stable`.
2. Der Betreiber stellt die Manifeste unter
   `<RETICORA_UPDATE_URL>/<kanal>/` bereit (z. B. Spiegel der GitHub-Releases).
3. Der Collector prüft nach dem ersten erfolgreichen Heartbeat und danach alle
   `RETICORA_UPDATE_INTERVAL` (Standard 6 h) das Manifest seines Kanals.
   Installiert wird nur eine **neuere** Version (SemVer, kein Downgrade) des
   abonnierten Kanals (`beta` erhält auch `stable`).
4. Vor dem Austausch: Prüfsumme, **Signaturpflicht** (`cosign verify-blob`
   gegen die Identität des Release-Workflows; ohne oder mit falscher Signatur
   wird abgelehnt) und Startprobe (`collector version` muss die neue Version
   melden). Danach wird das alte Binary als `.old` behalten und der Collector
   beendet sich; systemd (`Restart=always`) startet das neue Binary.

## Rollback bei Fehlstart

Das neue Binary gilt erst als installiert, wenn es einen Heartbeat erfolgreich
gesendet hat (Marker `.update-pending` wird entfernt). Jeder Start ohne diese
Bestätigung wird gezählt; nach drei Starts stellt der Collector das alte Binary
wieder her (das fehlerhafte bleibt als `.failed` liegen), merkt sich die
Version in `.update-rolledback` und beendet sich, damit systemd das alte Binary
startet. Die zurückgerollte Version wird nicht erneut installiert, die nächste
neuere schon.

## Konfiguration

| Variable | Standard | Bedeutung |
|---|---|---|
| `RETICORA_UPDATE_URL` | leer (aus) | Basis-URL der Manifeste; leer schaltet Auto-Updates ab |
| `RETICORA_UPDATE_CHANNEL` | `stable` | `stable` oder `beta`; ein unbekannter Kanal schaltet Updates ab |
| `RETICORA_UPDATE_INTERVAL` | `6h` | Prüfintervall |

Der Collector braucht dafür `cosign` im `PATH` und Schreibrecht auf sein
Installationsverzeichnis. Docker-Installationen aktualisieren über das
signierte Image, nicht über den Updater.

## Außerhalb des Umfangs

OVA- und Windows-Paketierung sowie das Air-Gap-Bundle (Updates ohne
Internetzugang) bleiben außerhalb (O/E-03).
