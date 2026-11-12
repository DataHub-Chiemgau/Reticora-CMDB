# Design-System – Website für Reticora CMDB

| | |
|---|---|
| Stand | 2026-11-12 |
| Status | **Vorschlag für drei Richtungen – keine ist freigegeben** |
| Nach Freigabe | Die gewählte Richtung wird ausgearbeitet und ist dann die verbindliche Quelle für die Umsetzung. Die anderen beiden wandern in den Anhang. |

Dieses Dokument legt Typografie, Farbe, Layout, Abstände, Bildsprache und
Interaktion fest, bevor Code entsteht. Die Idee hinter jeder Richtung steht in
`brand-strategy.md` §6.

## 1. Gilt für alle Richtungen

### 1.1 Markenkonstanten

- **Bildmarke:** das Fadenkreuz aus `frontend/src/components/shell/Brand.tsx`
  (Ring r 6,25, vier Striche, Mittelpunkt, Strichstärke 1,75 im 24er-Raster),
  unverändert.
- **Wortmarke:** „Reticora“ in Gewicht 600, eng gesetzt.
- **Petrol-Familie** der App: `#087680` (hell), `#4CC4CE` (dunkel).
- **Graphit-Neutral** der App: `#F7F9FA` bis `#0D1215`.

### 1.2 Token-Aufbau

Tokens sind CSS-Variablen in `src/styles/tokens.css`. Tailwind liest sie über
das Theme. Die Namen folgen der App, damit beide Systeme dieselbe Sprache
sprechen:

| Token | Zweck |
|---|---|
| `canvas`, `panel`, `raised` | Seitengrund, Fläche, schwebende Ebene |
| `line`, `line-strong` | Haarlinien und Trenner |
| `fg`, `fg-muted`, `fg-subtle` | Text: Standard, sekundär, Hinweis |
| `accent`, `accent-strong`, `accent-soft`, `accent-fg` | Akzent gefüllt, Text, Fläche, Schrift darauf |
| `signal` | Zustand „braucht Entscheidung“ (Bernstein) |
| `space-*`, `radius-*`, `measure` | Abstände, Radien, Zeilenlänge |

Komponenten verwenden nur semantische Tokens, nie Hex-Werte.

### 1.3 Barrierefreiheit

- Ziel WCAG 2.2 AA. Text mindestens 4,5:1, Bedienelemente und Grafikteile
  mindestens 3:1.
- Fokus: 2 px Umriss in `accent`, 2 px Abstand, auf jedem Bedienelement.
- Zielgröße mindestens 44 × 44 px auf Touch-Geräten.
- Ein `h1` je Seite, Landmarken, Sprunglink zum Inhalt.
- Farbe trägt nie allein Bedeutung.
- Zoom bis 400 % ohne waagerechtes Scrollen des Seitenkörpers.

### 1.4 Bewegung

Bewegung antwortet auf eine Handlung und dauert 120 bis 200 ms, wie in der
App. Kein Einblenden beim Scrollen, kein Parallax, kein Scroll-Hijacking.
`prefers-reduced-motion` schaltet alles ab.

### 1.5 Screenshot-Platzhalter

Eine Komponente `ScreenshotSlot` für alle Produktbilder:

- Eingaben: `id` (z. B. `S-02`), Seitenverhältnis, Bildunterschrift, Thema
  (hell/dunkel).
- Solange kein Bild hinterlegt ist, zeigt sie einen Rahmen im Stil der
  Richtung mit ID, Motiv und Zielmaß. Der Rahmen reserviert die endgültige
  Fläche, damit beim Austausch nichts springt.
- Ein Manifest (`src/data/screenshots.ts`) führt je ID Motiv, Quelle in der
  App, Viewport und Status. Die Liste steht in `content-plan.md` §8.
- Ein Test meldet vor dem Go-live jeden noch offenen Platzhalter.

### 1.6 Verbote

Keine Verläufe als Flächenfüllung, kein Glas-Effekt, keine Leuchtkanten, keine
Stockfotos, keine erfundenen Oberflächen, keine Reihen aus drei gleichen
Karten als Standardmuster, keine Emojis als Symbole.

## 2. Richtung A – „Messblatt“

| Bereich | Festlegung |
|---|---|
| **Typografie** | Geist Variable (400, 500, 600) und Geist Mono Variable, selbst gehostet. Display 40→64 px (fließend), Zeilenhöhe 1,05, Laufweite −0,03 em. H2 28→40 px. H3 20→24 px. Lauftext 17/28 px. Daten und Kennwerte in Mono 13→14 px, tabellarische Ziffern. Kein Versalsatz. |
| **Farbe** | `canvas #F7F9FA`, `panel #FFFFFF`, `line #E1E6E9`, `line-strong #CBD3D8`, `fg #151B20`, `fg-muted #4E5C65`, `accent #087680` (5,4:1 auf Weiß), `accent-soft #E8F5F6`, `signal #B45309` (5,0:1 auf Weiß). Ein dunkles Thema ist über dieselben Tokens möglich, zum Start aber nicht vorgesehen. |
| **Layout** | 12 Spalten, maximale Breite 1200 px, Spaltenabstand 24 px. Abschnitte im Verhältnis 4 zu 8: links Titel und Kennwertliste, rechts Erklärung und Abbildung. Haarlinien zwischen Abschnitten statt Flächenwechsel. Unter 900 px eine Spalte, Kennwerte über dem Text. |
| **Abstände** | 8-px-Raster. Abschnitte 96 px (mobil 64 px). Innenabstand von Tafeln 24 px. |
| **Form** | Radius 2 px für Abbildungstafeln, 4 px für Bedienelemente. Keine Schatten. |
| **Bildsprache** | Abbildungstafeln mit Nummer („Abb. 3“), Unterschrift und Maßstrichen am Rand. Hinweislinien mit Ziffern verbinden Bilddetails mit einer Legende. Schemazeichnungen in 1,5 px Strich, Petrol für das hervorgehobene Element. |
| **Interaktion** | Kennwerte mit Quelle: Fokus oder Hover öffnet einen kleinen Beleg (Quelle, Stand), als `details`-Element ohne JavaScript nutzbar. Umschalter Vorder-/Rückseite bei der Rack-Abbildung. Kopierknopf an API-Beispielen. |
| **Signatur** | Hero mit geteilter Abbildung: links ein Netzschema mit dem Fadenkreuz auf einem Gerät, rechts dessen Felder mit Quelle und Zeitpunkt. |
| **Aufwand** | niedrig. JavaScript nahe null. |

## 3. Richtung B – „Leitstand“

| Bereich | Festlegung |
|---|---|
| **Typografie** | Archivo Variable: Überschriften Breite 112 %, Gewicht 600; Lauftext Breite 100 %, Gewicht 400, 17/28 px. JetBrains Mono für Daten. Display 44→72 px, Zeilenhöhe 1,0. Kurze Statuszeilen in Mono mit 0,04 em Sperrung. |
| **Farbe** | `canvas #0D1215`, `panel #151B20`, `raised #1C242A`, `line #262F36`, `line-strong #3B474F`, `fg #E6ECEF`, `fg-muted #A3B0B8`, `accent #4CC4CE`, `accent-strong #7FD8DF`, `signal #F0B429`. Alle Textfarben mindestens 7:1 auf `canvas`. Rechtstexte und Druck in hellem Thema. |
| **Layout** | Bänder über die volle Breite. Hero mit Topologie-Zeichnung über die ganze Breite, Text links unten. Lesespalte 8 von 12. Statusleiste mit festen Betriebswerten unter der Hero. |
| **Abstände** | 8-px-Raster. Bänder 120 px (mobil 72 px). |
| **Form** | Radius 6 px Bedienelemente, 10 px Tafeln, wie in der App. Keine Schatten, keine Leuchteffekte; Tiefe über Flächenhelligkeit. |
| **Bildsprache** | Screenshots im dunklen Thema. Linienzeichnungen von Topologie und Rack in 1 px, Knoten als Symbole nach Gerätetyp. |
| **Interaktion** | Ausfallsimulation in der Hero: Knoten wählen, abhängige Knoten wechseln auf `signal`, redundante bleiben. Tastaturbedienung, Live-Region für das Ergebnis, Standbild ohne JavaScript und bei reduzierter Bewegung. Budget für die Insel 8 KB. |
| **Signatur** | Die Topologie mit dem Fadenkreuz als Bühne. |
| **Aufwand** | hoch. Zwei Themen (dunkel für Marketing, hell für Recht und Druck), eine interaktive Insel, dunkle Screenshots zusätzlich. |

## 4. Richtung C – „Prüfbericht“

| Bereich | Festlegung |
|---|---|
| **Typografie** | IBM Plex Serif (500) für Überschriften, IBM Plex Sans (400, 600) für Text und Bedienung, IBM Plex Mono für Daten und Belegmarken. Display 40→60 px, Zeilenhöhe 1,1. Lauftext 18/30 px. Randnoten 14/20 px. |
| **Farbe** | `canvas #FAF8F3`, `panel #FFFFFF`, `line #D9D4C7`, `fg #1B1F23`, `fg-muted #575E66`, `accent #087680`, `signal #B4402A` (Rostrot, 5,3:1 auf `canvas`) ausschließlich für Belegmarken. |
| **Layout** | Textspalte mit `measure` 66 Zeichen, rechts eine Randspalte von 240 px für Belege. Abschnitte nummeriert (1, 1.1). Tabellen und Abbildungen brechen über beide Spalten aus. Lange Seiten mit Inhaltsverzeichnis. Unter 900 px klappen Randnoten unter den Absatz. |
| **Abstände** | Grundlinienraster 30 px. Abschnitte 90 px. |
| **Form** | Radius 0. Linien 1 px. Keine Flächenkarten. |
| **Bildsprache** | Abbildungen mit Nummer und Unterschrift, dünner Rahmen. Schemata als Strichzeichnung in Tinte, ein Element in Petrol. |
| **Interaktion** | Belegmarke springt zur Randnote und zurück. Anker an jedem Abschnitt. Druckansicht als vollwertige Ausgabe. Kein weiteres JavaScript. |
| **Signatur** | Die Randspalte mit Belegen. |
| **Aufwand** | mittel. Viel redaktionelle Arbeit an Belegen, wenig Technik. |

## 5. Vergleich

| Kriterium | A Messblatt | B Leitstand | C Prüfbericht |
|---|---|---|---|
| Nähe zur App | sehr hoch | mittel (dunkles Thema der App) | gering |
| Eigenständigkeit | mittel | mittel | hoch |
| Lesbarkeit langer Inhalte | hoch | mittel | sehr hoch |
| Wirkung in den ersten Sekunden | mittel | hoch | mittel |
| JavaScript-Bedarf | nahe null | am höchsten | nahe null |
| Pflegeaufwand | niedrig | hoch | mittel |
| Risiko „sieht nach Vorlage aus“ | gering bei guter Hero-Abbildung | am höchsten | gering |

Empfehlung: A mit dem Belegprinzip aus C (`brand-strategy.md` §6).

## 6. Geplante Bausteine (nach Freigabe auszuarbeiten)

Kopfzeile mit Navigation und zwei Handlungsaufrufen, Fußzeile mit
Pflichtlinks, Abschnitt mit Kennwertspalte, Kennwerttabelle, `ScreenshotSlot`,
Schemazeichnung, Beleg, Ablauf in Schritten, Paketmatrix, Geräteliste mit
Filter, Code-Beispiel, Formular mit Fehlerzuständen, Hinweisband, 404-Seite.

Jeder Baustein bekommt bei der Ausarbeitung: Zweck, Varianten, Zustände
(Hover, Fokus, Fehler, deaktiviert), Verhalten bei 360, 768 und 1280 px sowie
Hinweise zur Barrierefreiheit.
