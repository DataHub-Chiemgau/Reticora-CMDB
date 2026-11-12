# Markenstrategie – Website für Reticora CMDB

| | |
|---|---|
| Stand | 2026-11-12 |
| Status | **Entwurf – Freigabe ausstehend** |
| Grundlage | `README.md`, `frontend/DESIGN.md`, `frontend/src/components/shell/Brand.tsx` |

## 1. Positionierung

> Reticora ist die CMDB für Netzbetreiber und Netzwerk-MSPs, die ihren Bestand
> selbst ausliest und zu jedem Wert sagt, woher er stammt.

- **Für wen:** ISPs und MSPs mit Netzwerkschwerpunkt, 500 bis 50.000 CIs,
  meist mehrere Kunden und Standorte.
- **Gegen was:** Tabellen, Wikis und CMDBs, die von Hand gepflegt werden und
  nach dem ersten Quartal nicht mehr stimmen; Discovery-Werkzeuge, die
  Dubletten erzeugen und stillschweigend überschreiben.
- **Warum glaubwürdig:** Die Regeln stehen offen im Produkt. Quellränge,
  Identitätsschlüssel und Konfliktfälle sind dokumentiert und in der
  Oberfläche pro Feld einsehbar (`README.md` §5.3).

## 2. Markenidee

Die Bildmarke ist ein Fadenkreuz: das Instrument, das ein Gerät aus einem
Netz herausgreift (`Brand.tsx`). Daraus folgt die Idee für die Website:

**Ein Messinstrument für Infrastruktur.** Ein Messinstrument behauptet nichts.
Es zeigt einen Wert, die Einheit, die Quelle und die Genauigkeit. Die Website
tut dasselbe mit ihren Aussagen.

Das ist die inhaltliche Auslegung von „Technical Precision“: Präzision als
überprüfbare Angabe und nicht als Technik-Dekor.

## 3. Botschaften

Reihenfolge nach Gewicht. Jede Botschaft hat einen Beleg im Repository; die
vollständige Liste steht im Claim-Register (`content-plan.md` §7).

| Rang | Botschaft | Beleg |
|---|---|---|
| 1 | **Ausgelesen, nicht abgetippt.** Ein Collector im Netz erfasst Netzwerk, Server, Strom und Storage über SNMP, SSH, Redfish und weitere Protokolle. | `README.md` §5.1, §5.4 |
| 2 | **Jeder Wert ist belegt.** Quelle, Zeitpunkt und Auswahlgrund pro Feld. Manuelle Werte gewinnen immer. Unklares wird zur Entscheidung vorgelegt und nie still verworfen. | `README.md` §1.1, §5.3 |
| 3 | **Ein Gerät, ein CI.** Identität über Seriennummer, Hardware-UUID, MAC und Management-IP; nie automatisch zusammengeführt nur wegen des Hostnamens. | `README.md` §5.3 |
| 4 | **Für mehrere Kunden gebaut.** Organisation, Kunde, Standort; getrennt bis in die Datenbank. | `README.md` §4 |
| 5 | **Zusammenhänge sehen.** Topologie aus LLDP, Racks höheneinheitengenau, Ausfallsimulation mit Redundanz. | `README.md` §5.5, §7, §9 |
| 6 | **Nur ausgehend, in der EU.** Der Collector baut die Verbindung auf; die Cloud greift nie ins Kundennetz. Daten, Backups und Logs liegen in der EU. | `README.md` §2.1, §3 |
| 7 | **Mehr als Inventar.** Assets, Lager, Inventur, Tickets, Monitoring, Workflows, Compliance auf demselben Bestand. | `README.md` §6.5, §14 |
| 8 | **Für Maschinen lesbar.** REST-API nach OpenAPI, signierte Webhooks, Export. | `README.md` §8 |

## 4. Tonalität

| Wir schreiben | Wir schreiben nicht |
|---|---|
| Sie-Form, wie in der App | du-Form |
| konkrete Angaben mit Einheit: „Poll alle 15 Minuten“, „Puffer bis 24 Stunden“ | „in Echtzeit“, „blitzschnell“, „nahtlos“ |
| Verben im Aktiv: „Der Collector liest aus“ | „Es wird ermöglicht, dass …“ |
| Begriffe der App: Kunde, Standort, CI, Review, Collector, Scope | Synonyme, die in der App nicht vorkommen |
| was das Produkt tut | Superlative, „führend“, „revolutionär“, „KI-gestützt“ als Etikett |
| Grenzen, wo sie die Entscheidung erleichtern | Versprechen ohne Beleg |

Fachbegriffe bleiben stehen, wo die Zielgruppe sie benutzt (SNMP, LLDP, VRF,
Redfish). Eigene Begriffe werden beim ersten Vorkommen in einem Halbsatz
erklärt (CI, Reconciliation, Review-Inbox).

## 5. Was die Website nicht zeigt

- keine Stockfotos, keine Menschen an Laptops
- keine erfundenen Dashboards: Produktbilder sind Screenshots der App
  (zunächst Platzhalter) oder klar als Schema gekennzeichnete Zeichnungen
- keine Kundenlogos, Zitate oder Kennzahlen ohne Nachweis (F-16)
- keine Logos von Geräteherstellern; Herstellernamen nur als Text
- keine Vergleiche mit namentlich genannten Wettbewerbern zum Start

## 6. Drei Art Directions

Alle drei legen „Technical Precision“ aus und nutzen die Bildmarke und das
Petrol der App. Sie unterscheiden sich in Schrift, Farbe, Raster, Bildsprache
und Interaktion. Die Kennwerte stehen in `design-system.md`.

### A – „Messblatt“

**Idee:** Die Website liest sich wie das Datenblatt eines Messgeräts. Hell,
ruhig, mit sichtbarem Raster. Jeder Abschnitt hat links eine schmale Spalte
mit Kennwerten und rechts Erklärung und Abbildung.

- **Schrift:** Geist und Geist Mono, dieselben Schriften wie in der App.
- **Farbe:** kühles Weiß, Graphit, Petrol als einziger Akzent; Bernstein nur
  für den Zustand „braucht Entscheidung“, wie in der App.
- **Layout:** 12 Spalten, Haarlinien, Verhältnis 4 zu 8; Kennwerttabellen
  statt Kartenreihen.
- **Bildsprache:** Screenshots als nummerierte Abbildungen mit Maßstrichen und
  Hinweislinien auf Details.
- **Interaktion:** sparsam. Ein Kennwert zeigt bei Fokus seine Herkunft, so
  wie ein Feld in der App.
- **Stärke:** nächste Verwandtschaft zur App; wer von der Website in den Test
  wechselt, erkennt das Produkt wieder. Schnellste Seiten, einfachste Pflege.
- **Risiko:** kann nüchtern wirken; braucht eine starke Hero-Abbildung.

### B – „Leitstand“

**Idee:** Die Website zeigt das Netz selbst. Dunkel, mit einer
Topologie-Zeichnung als Bühne: Knoten, Kanten, ein Fadenkreuz auf einem Gerät.
Der Besucher löst einen Ausfall aus und sieht, was betroffen ist.

- **Schrift:** Archivo, in Überschriften leicht gedehnt; JetBrains Mono für
  Daten.
- **Farbe:** Graphit-Schwarz, helles Petrol als Linienfarbe, Bernstein für
  betroffene Knoten. Keine Leuchteffekte, keine Verläufe.
- **Layout:** breite Bänder über die volle Breite, Text in einer schmalen
  Mittelspalte, Statusleiste mit festen Betriebswerten.
- **Bildsprache:** dunkle Screenshots, Linienzeichnungen von Topologie und
  Rack.
- **Interaktion:** eine Ausfallsimulation in der Hero als einziges
  Schaustück, per Tastatur bedienbar, ohne JavaScript als Standbild.
- **Stärke:** einprägsam, zeigt den Nutzen in den ersten Sekunden.
- **Risiko:** dunkle SaaS-Seiten sind verbreitet; lange Texte, Tabellen und
  Rechtstexte lesen sich dunkel schlechter; mehr JavaScript und mehr
  Pflegeaufwand; weiter weg vom hellen Standard der App.

### C – „Prüfbericht“

**Idee:** Die Website ist gesetzt wie ein technischer Prüfbericht. Eine
Textspalte, daneben eine Randspalte mit Belegen: Zu jeder Aussage steht dort,
worauf sie sich stützt. Das Prinzip der App, Provenienz je Feld, wird zum
Layout.

- **Schrift:** IBM Plex Serif für Überschriften, Plex Sans für Text, Plex
  Mono für Daten.
- **Farbe:** Papierweiß, Tinte, Petrol für Links und Marke, Rostrot nur für
  Belegmarken.
- **Layout:** Textspalte mit etwa 66 Zeichen, Randspalte, nummerierte
  Abschnitte und Abbildungen; Tabellen und Abbildungen brechen breit aus.
- **Bildsprache:** Abbildungen mit Nummer und Unterschrift wie in einem
  Bericht.
- **Interaktion:** fast keine. Randnoten klappen auf schmalen Bildschirmen
  unter den Absatz. Die Seite druckt sauber.
- **Stärke:** eigenständig, sehr glaubwürdig für Prüfer und Einkauf.
- **Risiko:** liest sich langsamer; Serifenschrift steht der App ferner;
  Handlungsaufrufe treten zurück.

### Empfehlung

**A „Messblatt“ als Grundlage**, mit zwei Übernahmen:

1. aus C das Belegprinzip als Inhaltsregel: Aussagen mit Zahl oder
   Sicherheitsbezug tragen einen sichtbaren Hinweis auf ihre Quelle;
2. aus B die Ausfallsimulation als eine kleine, helle Schemazeichnung auf der
   Seite „Topologie und Impact“, sofern das Budget (`project-brief.md` §7)
   hält.

Begründung: Der Auftrag verlangt „passend zur tatsächlichen Software“. A teilt
Schrift, Farbe und Bildmarke mit der App, hält die Seiten am leichtesten und
ist auf einem Shared Hosting am einfachsten zu pflegen. Die Entscheidung
trifft der Auftraggeber (Phase 4).
