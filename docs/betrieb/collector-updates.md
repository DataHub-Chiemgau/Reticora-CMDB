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
Installationsverzeichnis. `install-vm.sh` installiert mit `RETICORA_UPDATE_URL`
(Frage des Installers oder Umgebung) cosign `v2.4.1` nach Prüfung der
veröffentlichten Prüfsumme als `/usr/local/bin/cosign` und schreibt URL und
Kanal in `collector.env`; ohne Update-URL wird nichts installiert. Fehlt cosign
bei aktiven Updates, warnt der Collector beim Start
(`collector.update.cosign_missing`). Docker-Installationen aktualisieren über
das signierte Image, nicht über den Updater.

## Nachweise

| Nachweis | Was er belegt | Wo er läuft |
|---|---|---|
| `tests/collector-selfupdate.test.sh` (WP-546) | Zwei echte Builds (`collectorcmd.Version` `v0.0.1` und `v0.0.2`), Manifest und Artefakte über einen lokalen HTTP-Server, cosign-Stub im `PATH`, der nur das Fixture-Bundle mit der gepinnten Release-Identität annimmt: (a) `v0.0.1` aktualisiert sich nach dem ersten Heartbeat auf `v0.0.2`, das neu gestartete Binary meldet `v0.0.2` und bestätigt das Update; (b) falsche oder fehlende Signatur → kein Tausch, Log `collector update refused`; (c) beta-Release bei Kanal `stable` → kein Tausch, cosign nicht aufgerufen; (d) die neue Version kommt nicht hoch (Heartbeats abgewiesen) → nach drei unbestätigten Starts Rollback auf `v0.0.1`, die zurückgerollte Version wird nicht erneut installiert | `install-smoke.yml`, Job `installer-script-tests` |
| `collector/collectorcmd/update_test.go` (WP-546) | `newUpdateChecker`: Quelle aus `RETICORA_UPDATE_URL`, Kanal und Artefakt `reticora-collector`; `CosignVerifier` ruft `cosign verify-blob` mit der gepinnten Release-Identität und dem GitHub-Actions-Issuer auf; Startprobe `version`; ein unbekannter Kanal schaltet Updates ab | `ci.yml`, Job `go-modules` (collector) |
| `edgecore/update/cosign_real_test.go` (WP-545, Build-Tag `cosignreal`) | Echtes cosign gegen ein echtes keyless Bundle. Im Job `cosign-verify` signiert der CI-Workflow ein Test-Artefakt und ein zweites Artefakt (fremdes Bundle) und ruft den Test zweimal auf: mit der Identität des CI-Workflows (`COSIGN_EXPECT_IDENTITY`) wird das Artefakt angenommen, das veränderte Artefakt und das fremde Bundle abgewiesen; mit der gepinnten Release-Identität wird die Signatur wegen der Identität abgewiesen (Exit ≠ 0, `none of the expected identities matched`). Im Release prüft `release.yml` jedes Collector-Binary so | `ci.yml`, Job `cosign-verify`; `release.yml` |
| `edgecore/update/update_test.go` (u. a. `TestApplyRefusesUnsignedAndWronglySignedReleases`, `TestCosignVerifierPinsTheReleaseIdentity`, `TestFailedStartsRollBackTheUpdate`) | Ablehnung unsignierter und falsch signierter Releases, Identitäts-Pin, Kanal, Rollback | `ci.yml`, Job `go-modules` (edgecore) |
| `tests/install-vm-cosign.test.sh` (WP-432) | `install-vm.sh` installiert mit `RETICORA_UPDATE_URL` das gepinnte cosign | `install-smoke.yml`, Job `installer-script-tests` |

## Außerhalb des Umfangs

OVA- und Windows-Paketierung sowie das Air-Gap-Bundle (Updates ohne
Internetzugang) bleiben außerhalb (O/E-03).
