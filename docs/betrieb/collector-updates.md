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

Der Collector braucht dafür `cosign` ab `v3.1.3` im `PATH` und Schreibrecht
auf sein Installationsverzeichnis. `install-vm.sh` installiert mit
`RETICORA_UPDATE_URL` (Frage des Installers oder Umgebung) cosign `v3.1.4`
nach Prüfung der veröffentlichten Prüfsumme als `/usr/local/bin/cosign` und
schreibt URL und Kanal in `collector.env`; ohne Update-URL wird nichts
installiert. Fehlt cosign bei aktiven Updates, warnt der Collector beim Start
(`collector.update.cosign_missing`). Docker-Installationen aktualisieren über
das signierte Image, nicht über den Updater.

### cosign v3 (E-258): Betriebshinweis für bestehende Collector-Hosts

Releases werden seit dem Update auf cosign v3 im Sigstore-Bundle-Format
(v0.3) signiert. Der Updater ruft cosign erst auf, wenn `cosign version`
mindestens `v3.1.3` meldet (`update.MinCosignVersion`). Ältere Versionen
nehmen ein präpariertes Legacy-Bundle trotz Identitätsprüfung an
(GHSA-fx35-mq7g-6g98, high). cosign v2 liest die neuen Bundles außerdem nur
mit einem Zusatzschalter. Bereits veröffentlichte Releases mit Legacy-Bundle
(z. B. `v1.0.0-rc.1`) bleiben mit cosign v3 prüfbar.

Hosts, die `install-vm.sh` vor diesem Update mit `RETICORA_UPDATE_URL`
eingerichtet hat, haben cosign `v2.4.1` als `/usr/local/bin/cosign`. Das
bedeutet:

- **Collector ab diesem Release:** Er warnt beim Start
  (`collector.update.cosign_outdated` mit Pfad und Version). Jedes Update
  lehnt er mit `collector update refused` ab (`cosign is older than v3.1.3`).
  Der laufende Collector bleibt unverändert.
- **Collector vor diesem Release (z. B. `v1.0.0-rc.1`):** Er prüft die
  Version nicht. Sein cosign v2.4.1 kann die neuen Bundles aber nicht lesen,
  deshalb schlägt die Signaturprüfung fehl und das Update wird ebenfalls
  abgelehnt (fail-closed, Log `collector update refused`).

Kein Host übernimmt also ein Update mit dem alten cosign. Dafür müssen
Altinstallationen einmalig nachgezogen werden:

1. `sudo ./install-vm.sh --release v<version>` mit der bisherigen
   `collector.env` erneut ausführen. Der Installer übernimmt die Update-URL
   aus `collector.env` und ersetzt `/usr/local/bin/cosign` durch das gepinnte
   cosign `v3.1.4` (Prüfsumme geprüft). Den Collector installiert er in der
   genannten Version.
2. Alternativ nur cosign tauschen: cosign `v3.1.4` für `linux-amd64` bzw.
   `linux-arm64` von `https://github.com/sigstore/cosign/releases/download/v3.1.4/`
   laden und gegen die Prüfsumme prüfen (`424e2c97…bbfa` amd64,
   `ade83b7a…dad2` arm64, vollständig in `install-preflight.sh`
   `COSIGN_SHA256_*`). Dann als `/usr/local/bin/cosign` (Modus 0755)
   ablegen und `cosign version` prüfen. Der Collector braucht keinen
   Neustart, die Warnung beim Start verschwindet aber erst nach einem
   Neustart.
3. Kontrolle: `journalctl -u reticora-collector` zeigt beim nächsten
   Prüfintervall `installing collector update` statt `collector update
   refused`.

Ein cosign aus der Distribution unter `/usr/bin` wird vom `PATH` der Unit
(`/usr/local/bin` zuerst) überdeckt. Liegt dort ein älteres cosign, ändert
das am Ergebnis nichts.

## Nachweise

| Nachweis | Was er belegt | Wo er läuft |
|---|---|---|
| `tests/collector-selfupdate.test.sh` (WP-546) | Zwei echte Builds (`collectorcmd.Version` `v0.0.1` und `v0.0.2`), Manifest und Artefakte über einen lokalen HTTP-Server, cosign-Stub im `PATH`, der nur das Fixture-Bundle mit der gepinnten Release-Identität annimmt: (a) `v0.0.1` aktualisiert sich nach dem ersten Heartbeat auf `v0.0.2`, das neu gestartete Binary meldet `v0.0.2` und bestätigt das Update; (b) falsche oder fehlende Signatur → kein Tausch, Log `collector update refused`; (c) beta-Release bei Kanal `stable` → kein Tausch, cosign nicht aufgerufen; (d) die neue Version kommt nicht hoch (Heartbeats abgewiesen) → nach drei unbestätigten Starts Rollback auf `v0.0.1`, die zurückgerollte Version wird nicht erneut installiert; (e) cosign `v2.4.1` im `PATH` → Warnung `collector.update.cosign_outdated`, Update abgelehnt, `verify-blob` nicht aufgerufen (E-258) | `install-smoke.yml`, Job `installer-script-tests` |
| `collector/collectorcmd/update_test.go` (WP-546) | `newUpdateChecker`: Quelle aus `RETICORA_UPDATE_URL`, Kanal und Artefakt `reticora-collector`; `CosignVerifier` ruft `cosign verify-blob` mit der gepinnten Release-Identität und dem GitHub-Actions-Issuer auf; Startprobe `version`; ein unbekannter Kanal schaltet Updates ab | `ci.yml`, Job `go-modules` (collector) |
| `edgecore/update/cosign_real_test.go` (WP-545, Build-Tag `cosignreal`) | Echtes cosign gegen ein echtes keyless Bundle. Im Job `cosign-verify` signiert der CI-Workflow ein Test-Artefakt und ein zweites Artefakt (fremdes Bundle) und ruft den Test zweimal auf: mit der Identität des CI-Workflows (`COSIGN_EXPECT_IDENTITY`) wird das Artefakt angenommen, das veränderte Artefakt und das fremde Bundle abgewiesen; mit der gepinnten Release-Identität wird die Signatur wegen der Identität abgewiesen (Exit ≠ 0; cosign v3 meldet das beim Sigstore-Bundle v0.3 als `no matching CertificateIdentity found`, beim Legacy-Bundle als `none of the expected identities matched`). Die neuen Bundles müssen Sigstore-Bundles v0.3 sein. `TestCosignVerifierWithLegacyReleaseBundle` prüft mit demselben cosign v3 das Legacy-Bundle (cosign v2.4.1) des veröffentlichten Releases `v1.0.0-rc.1` (Fixture `edgecore/update/testdata/legacy-release-v1.0.0-rc.1`): angenommen für die Release-Identität und genau seinen Tag, abgewiesen bei verändertem Manifest, fremdem Bundle und anderem Tag (E-258). Im Release prüft `release.yml` jedes Collector-Binary so | `ci.yml`, Job `cosign-verify`; `release.yml` |
| `edgecore/update/update_test.go` (u. a. `TestApplyRefusesUnsignedAndWronglySignedReleases`, `TestCosignVerifierPinsTheReleaseIdentity`, `TestCosignVerifierRefusesAnOutdatedCosign`, `TestFailedStartsRollBackTheUpdate`) | Ablehnung unsignierter und falsch signierter Releases, Identitäts-Pin, Mindestversion von cosign (`v3.1.3`, gleich `COSIGN_MIN_VERSION` der Installer), Kanal, Rollback | `ci.yml`, Job `go-modules` (edgecore) |
| `tests/install-vm-cosign.test.sh` (WP-432) | `install-vm.sh` installiert mit `RETICORA_UPDATE_URL` das gepinnte cosign | `install-smoke.yml`, Job `installer-script-tests` |

## Außerhalb des Umfangs

OVA- und Windows-Paketierung sowie das Air-Gap-Bundle (Updates ohne
Internetzugang) bleiben außerhalb (O/E-03).
