# Design-System „Leitstand“ – Website für Reticora CMDB

| | |
|---|---|
| Stand | 2026-11-12 |
| Status | **Entwurf zur Freigabe (Phase 4).** Die Richtung ist entschieden (`project-brief.md` W-E04), dieses Dokument noch nicht freigegeben. |
| Ansicht | Tokens, Bausteine mit Vorschau und der Seitenanfang der Startseite stehen als durchsehbares Design-System unter https://claude.ai/artifact/Ajb6TSMRYLnmyhTw53PNaj (privat, nur für den Auftraggeber sichtbar). |
| Nach Freigabe | Dieses Dokument ist die verbindliche Quelle für die Umsetzung. Bei Widerspruch zwischen Dokument und Ansicht gilt das Dokument. |

Die Idee und die verworfenen Richtungen A und C stehen in
`brand-strategy.md` §6.

## 1. Grundsatz

1. Die Website zeigt das Netz selbst: dunkel, mit einem Netzschema als Bühne.
2. Sie zeigt, was die App tut, so wie die App es zeigt. Farben, Wortlaut und
   Rechenregeln des Netzschemas stammen aus der Topologie-Ansicht der App.
3. Farben, Abstände, Radien und Schriften kommen nur aus Tokens.
4. Jede Aussage mit Zahl oder Sicherheitsbezug trägt einen Beleg.

## 2. Was sich gegenüber der Entwurfstafel B ändert

Die Entwurfstafel war eine Skizze vor dem Abgleich mit der App. Der Abgleich
(`frontend/src/pages/TopologyPage.tsx`,
`frontend/src/components/graph/TopologyGraph.tsx`,
`backend/internal/topology/redundancy.go`) ergab Unterschiede. Weil der
Auftrag „passend zur tatsächlichen Software“ verlangt und eine Darstellung,
die anders rechnet als das Produkt, irreführen würde, folgt das Design-System
der App.

| Entwurfstafel B | Design-System | Grund |
|---|---|---|
| Knoten als beschriftete Kästen | gefüllte Punkte mit Namen daneben | so zeichnet die App; die Farbe steht für den Status, nicht den Gerätetyp |
| ausgefallen und betroffen in Bernstein | ausgefallen rot und 1,4-mal so groß, betroffen grau | Farben der App; ihr Ergebnissatz sagt „grau dargestellt“. Bernstein bedeutet dort „Single Point of Failure eines Services“ |
| redundant versorgte Knoten gestrichelt in Petrol | kein Zeichen am Knoten; Nennung in der Ergebniszeile | die App markiert sie nicht; ihr Wortlaut ist „Durch Redundanz geschützt“ |
| Redundanz über zwei Netz-Uplinks | Redundanz über zwei Stromwege (USV, zwei PDUs) | die App rechnet Redundanz nur über `powered_by` und `member_of_cluster`; `connected_to` gilt nur als Heuristik |
| Klick auf einen Knoten | Knöpfe unter dem Schema, zusätzlich Klick auf den Punkt | die App löst den Ausfall aus der Knotenliste aus; Punkte sind zu klein als einziges Ziel |
| Fadenkreuz um den ausgefallenen Knoten | Fadenkreuz als Fokusmarke | in der App bedeutet das Fadenkreuz „Fokussieren“ |
| Schema über der Überschrift | Überschrift und Handlungsaufrufe zuerst, Schema direkt darunter | die Aussage muss ohne Scrollen lesbar sein; das Schema ist mit Knöpfen und Ergebniszeile höher geworden |

Unverändert bleiben: dunkler Grund, Archivo in gedehnten Überschriften,
JetBrains Mono für Daten, die Betriebsleiste, helle Petrol-Akzente, keine
Verläufe und kein Leuchten.

## 3. Tokens

Die Token-Namen folgen der App (`frontend/tailwind.config.ts`,
`frontend/src/index.css`). Die Werte der Themen sind die des dunklen und des
hellen Themas der App, aus deren HSL-Angaben berechnet.

### 3.1 Farbe

| Token | Leitstand | Dokument | Verwendung |
|---|---|---|---|
| `canvas` | `#0d1215` | `#f5f7f8` | Seitengrund |
| `panel` | `#151b20` | `#ffffff` | Abbildungsrahmen, Tabellen, Felder, Code |
| `raised` | `#1b2329` | `#ffffff` | aufgeklapptes Menü, Belegnotiz |
| `line` | `#262f36` | `#e1e6e9` | Haarlinien; Kanten an betroffenen Knoten |
| `line-strong` | `#36424a` | `#cbd3d8` | Kanten im Netzschema; betroffene Knoten |
| `control` | `#65747f` | `#808f9a` | Umriss von Bedienelementen |
| `fg` | `#e8edef` | `#171e23` | Überschriften, Fließtext |
| `fg-muted` | `#a2aeb6` | `#4e5c65` | Vorspann, Beschreibungen, Tabellenköpfe |
| `fg-subtle` | `#8a98a1` | `#5d6d76` | Hinweise, Bildunterschriften ab 13 px |
| `accent` | `#4cc3ce` | `#087680` | Bildmarke, Hauptknopf, Fokus, Werte |
| `accent-strong` | `#82dae0` | `#065a62` | Links, Hover des Hauptknopfs |
| `accent-soft` | `#10292e` | `#e4f3f4` | Hinweisband |
| `accent-fg` | `#0d1215` | `#ffffff` | Schrift auf `accent` |
| `signal` | `#fbbf24` | `#b45309` | „braucht Aufmerksamkeit“, immer mit Wort |
| `danger` | `#f87171` | `#b91c1c` | Fehlertext, Fehlerrahmen |
| `success` | `#4ade80` | `#15803d` | Bestätigung, Lampe „erreichbar“ |
| `graph-active` | `#16a34a` | `#16a34a` | Knoten mit Status „Aktiv“ (feste Farbe der App) |
| `graph-failed` | `#dc2626` | `#dc2626` | ausgefallener Knoten (feste Farbe der App) |

Kontrast nach WCAG 2, selbst berechnet:

| Paar | Leitstand | Dokument | Anforderung |
|---|---|---|---|
| `fg` auf `canvas` / `panel` / `raised` | 16,0 / 14,7 / 13,5 | 15,7 / 16,9 / 16,9 | 4,5 |
| `fg-muted` auf `canvas` / `panel` / `raised` | 8,3 / 7,7 / 7,0 | 6,4 / 6,9 / 6,9 | 4,5 |
| `fg-subtle` auf `canvas` / `panel` | 6,4 / 5,9 | 5,0 / 5,4 | 4,5 |
| `accent` auf `canvas` / `panel` | 9,0 / 8,3 | 5,0 / 5,4 | 4,5 |
| `accent-strong` auf `canvas` / `accent-soft` | 11,7 / 9,5 | 7,4 / – | 4,5 |
| `accent-fg` auf `accent` | 9,0 | 5,4 | 4,5 |
| `control` auf `canvas` / `panel` / `raised` | 3,9 / 3,6 / 3,3 | 3,1 / 3,3 / 3,3 | 3,0 |
| `signal`, `danger`, `success` auf `panel` | 10,4 / 6,3 / 10,0 | 5,0 / 6,5 / 5,0 | 4,5 |
| `graph-active`, `graph-failed` auf `panel` (Fläche) | 5,3 / 3,6 | 3,3 / 4,8 | 3,0 |

Betroffene Knoten sind mit Absicht blass (`line-strong` auf `panel`: 1,7).
Ihr Name bleibt lesbar (`fg-subtle`), und die Ergebniszeile nennt jedes
betroffene CI beim Namen. Farbe trägt die Aussage also nie allein.

### 3.2 Schrift

| Familie | Datei | Einsatz |
|---|---|---|
| Archivo (variabel, Gewicht 100–900, Breite 62–125 %) | `archivo-latin-wdth-normal.woff2`, 90 KB | Überschriften und Text |
| JetBrains Mono (variabel, Gewicht 100–800) | `jetbrains-mono-latin-wght-normal.woff2`, 40 KB | Daten |

Beide stehen unter der SIL Open Font License 1.1, liegen auf dem eigenen
Webspace und laden mit `font-display: swap`. Archivo wird vorab geladen.

| Stil | Größe / Zeilenhöhe ab 1280 px | auf dem Telefon | Gewicht, Breite | Einsatz |
|---|---|---|---|---|
| `display-xl` | 72 / 72 | 36 / 40 | 600, 112 % | Titel der Startseite |
| `display-l` | 52 / 56 | 32 / 36 | 600, 112 % | Seitentitel |
| `heading-l` | 36 / 42 | 28 / 34 | 600, 112 % | Abschnittstitel |
| `heading-m` | 26 / 32 | 22 / 28 | 600, 112 % | Titel im Abschnitt |
| `heading-s` | 19 / 26 | 19 / 26 | 600, 106 % | Tarifnamen, Tabellen, Formulare |
| `lead` | 20 / 32 | 18 / 28 | 400 | Vorspann, in `fg-muted` |
| `body` | 17 / 28 | 17 / 28 | 400 | Fließtext, höchstens 68 Zeichen |
| `body-s` | 15 / 24 | 15 / 24 | 400, Knöpfe 600 | Tabellen, Navigation, Hinweise |
| `caption` | 13 / 20 | 13 / 20 | 400 | Bildunterschriften |
| `data` | 14 / 22 | 14 / 22 | 400 | Werte, Gerätenamen, Code |
| `data-label` | 12 / 16, Sperrung 0,04 em | gleich | 500 | Betriebsleiste, Tabellenköpfe |

Die Display-Stufen wachsen fließend zwischen den beiden Größen (`clamp`).
Laufweite der Display-Stufen −0,02 em. Versalien stehen nur in den
Bezeichnungen der Betriebsleiste. Zahl und Einheit trennt ein geschütztes
Leerzeichen.

### 3.3 Abstände, Radien, Layout

| Familie | Werte |
|---|---|
| Abstände | `space-1` 4, `-2` 8, `-3` 12, `-4` 16, `-5` 20, `-6` 24, `-8` 32, `-10` 40, `-12` 48, `-16` 64, `-24` 96, `-30` 120 px |
| Radien | `radius-sm` 4 px (Marken, Kennzeichen), `radius-md` 6 px (Knöpfe, Felder), `radius-lg` 10 px (Abbildungen, Tabellen, Code) |
| Layout | `container` 1200 px, `measure` 68 Zeichen, `gutter` 24 px (unter 768 px: 16 px), `header-height` 64 px, `target-min` 44 px, `hairline` 1 px |
| Umbrüche | `bp-sm` 480, `bp-md` 768, `bp-lg` 1024, `bp-nav` 1120, `bp-xl` 1280 px |

## 4. Layout

- Bänder laufen über die volle Breite und sind durch eine Haarlinie getrennt.
  Der Inhalt sitzt in `container` mit `gutter` am Rand.
- Senkrechter Innenabstand eines Bands: 120 px ab `bp-lg`, 96 px ab `bp-md`,
  64 px darunter.
- Ein Abschnitt besteht aus Titel, Vorspann und Inhalt, mit `space-8` zwischen
  Titel und Inhalt.
- Text und Abbildung stehen ab `bp-lg` nebeneinander (`space-16` Abstand),
  darunter untereinander, Text zuerst.
- Vergleiche stehen in einer Tabelle, Abläufe in einer Schrittliste, Werte in
  einer Kennwertliste. Reihen aus drei gleichen Karten gibt es nicht.
- Die Seite scrollt nie waagerecht. Tabellen, Code und das Netzschema scrollen
  in ihrem eigenen Rahmen.
- Rahmen stehen nur um Abbildungen, Tabellen, Code, Felder und Hinweise.

## 5. Bausteine

Jeder Baustein hat in der Ansicht eine Vorschau und eine Anleitung (Zweck,
was man liefert, Regeln). Hier die Übersicht.

| Baustein | Zweck | Zustände und Hinweise |
|---|---|---|
| `Button` | eine Handlung | Varianten `primary` (einer je Bildschirmhöhe), `secondary`, `quiet`; Größe 44 px, im Seitenanfang 52 px; Hover, Fokus, aktiv, deaktiviert während des Sendens |
| `SiteHeader` | Marke, Navigation, Sprache, drei Handlungen | Navigation ab 1120 px in der Zeile, darunter als `details`-Menü ohne JavaScript; nicht fixiert |
| `SiteFooter` | alle Seiten, Pflichtlinks, Firmierung | „Impressum“ und „Datenschutz“ auf jeder Seite; Satz „Angebot nur für Unternehmen“ |
| `StatusStrip` | drei bis fünf feste Betriebswerte | höchstens einmal je Seite; nur Werte aus dem Claim-Register |
| `TopologyStage` | Netzschema mit Ausfallsimulation | siehe §6 |
| `ScreenshotSlot` | Platzhalter und späterer Rahmen für Screenshots | Nummer, Motiv, Zielmaß; Bildunterschrift mit Nummer; kein Geräterahmen |
| `SpecList` | Bezeichnung und Wert mit Einheit | drei bis acht Zeilen, Mono, rechtsbündige Werte |
| `DataTable` | zeilenweiser Vergleich | Zeilenüberschriften, fokussierbarer Scrollrahmen, „ja“ und „–“ als Wort |
| `PricingTable` | Tarife mit Monats-, Jahres- und Beta-Preis | Zeilen statt Karten; darunter Beta-Bedingungen, B2B-Satz, Zusatzkosten; Daten aus einer Datei |
| `Beleg` | Quelle einer Aussage | `details`; Pflicht bei Zahlen und Sicherheitsaussagen |
| `StatusTag` | Zustand als Wort mit Lampe | nur in Schemata und Tabellen; Wörter der App |
| `Steps` | echte Reihenfolge | geordnete Liste; nur wo die Reihenfolge feststeht |
| `CodeSample` | Aufruf der API | nur Aufrufe aus der OpenAPI-Beschreibung; Schlüssel gekürzt; Kopierknopf |
| `Notice` | Bedingung oder Grenzfall | zwei Varianten; höchstens eines je Abschnitt; kein Randstreifen |
| `ContactForm` | fünf Angaben | sendet ohne JavaScript; Fehlerzusammenfassung und Feldfehler; Erfolgsseite |

## 6. Netzschema und Ausfallsimulation

### 6.1 Zeichnung

- Knoten: gefüllter Punkt, Radius 8, Name in `data` und `fg-muted`.
  Aktiv ist `graph-active`.
- Ausgefallen: `graph-failed`, 1,4-fache Größe, Name in `fg`. Das einzige Rot
  auf der Bühne.
- Betroffen: Punkt in `line-strong`, Name in `fg-subtle`; Kanten daran in
  `line`, ohne Beschriftung.
- Redundant versorgt und nicht betroffen: unverändert grün.
- Kanten: gerade, 1,25 px, `line-strong`, ohne Pfeil und Strichelung;
  Beschriftung ist der Beziehungstyp in 11 px (`powered_by`, `hosted_on`).
- Fläche: `panel` auf `canvas`, Rahmen `line`, `radius-lg`.
- Bernstein, Gerätesymbole, Subnetze und VLANs kommen im Schema nicht vor.
  Knoten in Wartung oder außer Betrieb ebenfalls nicht, weil ihre Statusfarben
  in der App den Farben für SPOF und Ausfall gleichen.
- Die Zeile „Schemazeichnung, kein Screenshot“ steht in der Legende. Die App
  ordnet Knoten im Kreis an; die geordnete Zeichnung ist eine Vereinfachung.

### 6.2 Beispiel

Neun CIs einer Stromkette: `usv-01` versorgt `pdu-a` und `pdu-b`. `srv-01`
und `srv-02` hängen an beiden PDUs, `sw-access-01` nur an `pdu-a`, `nas-01`
nur an `pdu-b`. `vm-app-01` läuft auf `srv-01`, `vm-db-01` auf `srv-02`.

Das Beispiel entspricht dem Testfall des Backends
(`backend/internal/topology/redundancy_test.go`) und dem Aufbau des
Simulationsmodus (eine USV, zwei PDUs, Server mit zwei Netzteilen).

| Ausfall | betroffen | durch Redundanz geschützt |
|---|---|---|
| `pdu-a` | `sw-access-01` | `srv-01`, `srv-02` |
| `pdu-b` | `nas-01` | `srv-01`, `srv-02` |
| `pdu-a` und `pdu-b` | beide Server, Switch, NAS, beide VMs (6) | – |
| `usv-01` | beide PDUs und alles dahinter (8) | – |
| `srv-01` | `vm-app-01` | – |
| eine VM | nichts | – |

### 6.3 Rechenregeln

- `powered_by` und `member_of_cluster` sind redundant: Ein CI fällt erst aus,
  wenn alle seine Versorger dieses Typs ausgefallen sind.
- `hosted_on`, `runs_on`, `depends_on`: Ein ausgefallener Versorger genügt.
- `connected_to` löst nie einen sicheren Ausfall aus und wird im Schema nicht
  gezeichnet.
- Mehrere CIs können gemeinsam ausfallen. Ausgefallene zählen nicht zu den
  betroffenen.

### 6.4 Bedienung und Text

- Unter dem Schema steht je CI ein Umschaltknopf (`aria-pressed`), mindestens
  44 px hoch, mit der Gruppenbezeichnung „Ausfall simulieren“. Das ist die
  Bedienung für Tastatur und Screenreader. Ein Klick auf einen Punkt tut
  dasselbe.
- Die Ergebniszeile ist eine Live-Region (`role="status"`).

| Fall | Deutsch | Englisch |
|---|---|---|
| ein CI, mehrere betroffen | Fällt „{name}“ aus, sind {n} weitere CIs betroffen (grau dargestellt). | If "{name}" fails, {n} more CIs are affected (shown in grey). |
| ein CI, eines betroffen | Fällt „{name}“ aus, ist 1 weiteres CI betroffen (grau dargestellt). | If "{name}" fails, 1 more CI is affected (shown in grey). |
| ein CI, keines betroffen | Fällt „{name}“ aus, ist kein weiteres CI betroffen. | If "{name}" fails, no other CI is affected. |
| mehrere CIs | Fallen {k} CIs ({namen}) gemeinsam aus, sind {n} weitere CIs betroffen (grau dargestellt). | If {k} CIs ({names}) fail together, {n} more CIs are affected (shown in grey). |
| Zusatz | Durch Redundanz geschützt: {namen}. | Protected by redundancy: {names}. |
| nichts gewählt | Wählen Sie ein CI, um seinen Ausfall zu simulieren. | Choose a CI to simulate its outage. |

  Der deutsche Wortlaut folgt der App. Die englische Oberfläche der App
  schreibt im selben Satz einmal „impacted (shown greyed out)“ und einmal
  „affected (shown in grey)“; die Website nimmt durchgehend die zweite Form.
- Ohne JavaScript steht das Schema im Ausgangszustand (`pdu-a` ausgefallen)
  mit der passenden Ergebniszeile. Die Knöpfe entfallen.
- Bewegung: nur der Farbwechsel, 150 ms; mit `prefers-reduced-motion` keiner.
- Budget: 8 KB JavaScript, kein Framework. Es ist die einzige Insel der
  Startseite neben dem Kopierknopf.

## 7. Bildsprache

- Screenshots: dunkles Thema der App, Demo-Organisation, 1440 × 900 px in
  doppelter Auflösung, Oberfläche in der Sprache der Seite. Kein Geräterahmen,
  keine Schrägstellung, kein Schatten.
- Bis ein Screenshot vorliegt, steht ein `ScreenshotSlot` mit Nummer, Motiv
  und Zielmaß (`content-plan.md` §8). Er hat das endgültige Seitenverhältnis.
- Jede Abbildung hat eine Nummer und eine Unterschrift.
- Keine Stockfotos, keine Illustrationen von Menschen, keine nachgebauten
  Oberflächen, keine Herstellerlogos.

## 8. Zustände und Bewegung

- Fokus: 2 px Umriss in `accent` mit 2 px Abstand, auf jedem Bedienelement.
- Hover hellt die Fläche auf `raised` auf oder verstärkt den Umriss. Aktiv
  verschiebt den Knopf um 1 px.
- Bewegung antwortet auf eine Handlung und dauert höchstens 150 ms. Nichts
  blendet beim Scrollen ein. `prefers-reduced-motion` schaltet alles ab.

## 9. Barrierefreiheit

- Ziel WCAG 2.2 AA; die Kontraste stehen in §3.1.
- Jedes Bedienelement mindestens 44 × 44 px.
- Eine `h1` je Seite, Landmarken, Sprunglink zum Inhalt, `lang` je Seite und
  an fremdsprachigen Links.
- Farbe trägt nie allein Bedeutung: Zustände haben ein Wort, das Netzschema
  eine Ergebniszeile.
- Alles ist per Tastatur erreichbar; das Menü und der Beleg sind
  `details`-Elemente.
- Zoom bis 400 % ohne waagerechtes Scrollen der Seite.

## 10. Zwei Themen

- **Leitstand** gilt für alle Marketingseiten. Es folgt nicht der
  Systemeinstellung und hat keinen Umschalter. So wird auch nichts im Browser
  gespeichert (`compliance-matrix.md` T-02).
- **Dokument** gilt für Impressum, Datenschutz, AGB, Subprozessoren,
  Barrierefreiheit und für jede Druckansicht. Es nutzt dieselben Tokens mit
  den hellen Werten der App.
- Bausteine kennen kein Thema und lesen nur Tokens.

## 11. Seitenvorlagen

| Vorlage | Aufbau |
|---|---|
| Startseite | Kopfzeile · Titel, Vorspann, zwei Knöpfe · Netzschema · Betriebsleiste · Abschnitte nach `content-plan.md` §5 · Abschlussband · Fußzeile |
| Produktseite | Kopfzeile · Seitentitel, Vorspann · Betriebsleiste (wenn passend) · Abschnitte aus Text und Abbildung im Wechsel · Kennwerte oder Tabelle · nächster Schritt · Fußzeile |
| Preise | Seitentitel · Preistabelle mit Beta-Bedingungen und B2B-Satz · Funktionen je Tarif · Fragen · Kontakt für Enterprise |
| Geräte | Seitentitel · Tabelle der Geräteklassen mit Filter nach Kategorie · Hinweis zu unbekannten Geräten |
| Sicherheit | Seitentitel · Abschnitte mit Beleg an jeder Aussage · Schema der Verbindungsrichtung · Kontakt |
| Kontakt | Seitentitel · Formular · direkte Kontaktdaten |
| Rechtsseite | Thema Dokument · Seitentitel · Text in `measure` · Fußzeile |

## 12. Offene Punkte

| Nr. | Punkt | Vorschlag |
|---|---|---|
| DS-1 | Die beiden Schriftdateien wiegen zusammen 130 KB. | bei der Umsetzung auf die genutzten Zeichen kürzen; Ziel unter 90 KB |
| DS-2 | Die Entwurfstafel zeigte das Schema vor der Überschrift. | Überschrift zuerst (§2); auf Wunsch umkehrbar |
| DS-3 | Englische Einheit im Rack: Die App schreibt auch englisch „HE“. | auf der englischen Website „U“, weil „HE“ dort unverständlich ist |
| DS-4 | Untertitel der Enterprise-Tarife („gemeinsame Plattform“, „eigene Instanz“) sind eine Annahme. | Bestätigung über F-04 |
