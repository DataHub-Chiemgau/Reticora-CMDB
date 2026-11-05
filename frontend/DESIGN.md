# Reticora – Designsystem

Dieses Dokument beschreibt die Oberfläche von Reticora CMDB: Produktannahme, Informationsarchitektur, Tokens, Komponenten und Seitenmuster. Wer eine Seite baut oder ändert, hält sich daran; wer einen neuen Baustein braucht, ergänzt ihn hier und in `src/components/ui`.

## 1. Produkt und Annahme

Reticora ist eine mandantenfähige CMDB für ISPs und netzwerkorientierte MSPs (siehe Repository-README). Die Oberfläche bedient vier Rollen: Techniker (Infrastruktur verstehen, Daten richtig halten), Service-Desk (Tickets, SLAs), Bestandsverwaltung (Assets, Lager, Inventur) und Compliance/Administration.

Annahmen, die das Design trägt:

- Der Arbeitsbereich (Organisation → Kunde → Standort) ist der wichtigste Kontext. Er steht dauerhaft im Header und färbt sich, sobald er Listen einschränkt.
- Die Arbeit besteht überwiegend aus Suchen, Filtern, Vergleichen und Prüfen. Tabellen, Detailansichten und Review-Entscheidungen haben Vorrang vor Dekoration.
- Nicht jeder Mandant hat jedes Modul. Die Navigation blendet nicht lizenzierte oder nicht erlaubte Seiten aus, statt sie anzuklicken zu lassen.

## 2. Informationsarchitektur

Die Navigation folgt der Arbeit, nicht den Modulen (`src/components/shell/navigation.ts`, die einzige Quelle):

| Gruppe              | Seiten                                                         |
| ------------------- | -------------------------------------------------------------- |
| (ohne Titel)        | Dashboard, Assistent                                           |
| Infrastruktur       | CMDB, Topologie, Racks, Raumplan, Karte, Standorte, Ansichten  |
| Erfassung           | Reviews (mit Zähler offener Einträge), Discovery, Agents, Jobs |
| Betrieb             | Tickets, SLAs, Wartung, Monitoring, Workflows, Formulare       |
| Assets              | Inventar, Zuweisungen, Dokumente, Entsorgung                   |
| Lager & Beschaffung | Verbrauchsmaterial, Lagerbestand, Inventur, Bestellungen       |
| Arbeitsplatz        | Arbeitsplätze, Schlüssel, Schulungen                           |
| Governance          | Compliance, Befunde, IGA, Audit, Sicherheit                    |
| Administration      | Benutzer, Berechtigungen, CI-Typen, Webhooks, Export           |

Gruppen lassen sich ein- und ausklappen (gemerkt pro Browser), die Gruppe der aktuellen Seite öffnet sich selbst. Jede Seite ist zusätzlich über die Befehlspalette (`Strg/⌘ K`) erreichbar.

## 3. App-Shell

- **Sidebar** 240 px (`--sidebar-width`), einklappbar zur Icon-Leiste 64 px, unter 1024 px ein Drawer. Aktive Seite: weißes Feld mit Haarlinie und Akzent-Icon, `aria-current="page"`.
- **Header** 56 px (`--header-height`): links der Arbeitsbereich als Pfad, rechts Suche/Befehle, Benachrichtigungen, Darstellung, Konto. Unter 1024 px wandert der Arbeitsbereich in den Drawer.
- **Hauptbereich** `#main-content` füllt die Breite, Innenabstand 16/24/32 px je Breakpoint. Hinweise (ablaufende Lizenzen, Metamodell-Entwurf) stehen als Banner darüber.
- Seiten vor der Anmeldung nutzen `PublicShell`: Marke, Sprache, Darstellung.

## 4. Tokens

Alle Werte stehen in `tailwind.config.ts` (Skalen) und `src/index.css` (Themenvariablen).

### Farben

Eine Akzentfarbe (Petrol) und ein kühles Graphit als Neutral. Semantische Token wechseln mit dem Theme und brauchen kein `dark:`:

| Token                                                                 | Zweck                                                           |
| --------------------------------------------------------------------- | --------------------------------------------------------------- |
| `bg-canvas`                                                           | Seitengrund (hinter Panels)                                     |
| `bg-panel`                                                            | Karten, Tabellen, Eingabefelder                                 |
| `bg-raised`                                                           | schwebende Ebenen (Menüs, Palette)                              |
| `border-line` / `border-line-strong`                                  | Struktur: Panels, Tabellenzeilen / Trenner mit mehr Gewicht     |
| `border-control`                                                      | Umriss von Formularelementen (3:1 gegen das Panel, WCAG 1.4.11) |
| `text-fg` / `text-fg-muted` / `text-fg-subtle`                        | Text: Standard / sekundär / Hinweis (alle ≥ 4,5:1)              |
| `bg-accent`, `text-accent-fg`, `bg-accent-soft`, `text-accent-strong` | Akzent: gefüllt, Beschriftung darauf, Fläche, Text auf Fläche   |

Ältere Seiten nutzen die Tailwind-Skalen `gray-*` und `blue-*`. Beide sind im Config auf dieselbe Identität umgebogen (`gray` = Graphit, `blue` = Petrol-Familie). Neuer Code nimmt die semantischen Token.

Zustandsfarben: Erfolg grün, Warnung bernstein, Fehler rot, Information Petrol. Farbe verstärkt nur, der Text trägt immer die Bedeutung (Badge mit Statuslampe plus Label).

### Typografie

Geist (Variable, selbst gehostet über `@fontsource-variable`) und Geist Mono für Bezeichner wie IPs und Seriennummern. Skala: 12 / 14 / 15 / 17 / 19 / 24 / 30 px. Zwei Gewichte tragen die Hierarchie: 500 für Bedienelemente, 600 für Titel und Zahlen (`font-bold` ist auf 600 abgebildet). Seitentitel `text-2xl`, Kartentitel `text-base`, Tabellenkopf `text-xs` in `font-medium`. Zahlen in Tabellen und Kennzahlen sind tabellarisch (`tabular-nums`). Kein Versalsatz, keine Eyebrow-Labels.

### Form und Tiefe

Radien: 6 px Chips und Badges, 8 px Bedienelemente, 10 px Menüs, 12 px Panels und Dialoge. Panels trennt eine Haarlinie, keine Schatten. Schatten (`shadow-md` bis `shadow-2xl`) tragen nur schwebende Ebenen: Menüs, Dialoge, Toasts, Palette.

### Raster und Bewegung

4/8-px-Raster (Tailwind-Standard). Bewegung antwortet auf eine Handlung (Dialog öffnet, Menü klappt auf, Drawer fährt ein, Toast erscheint) und dauert 120–200 ms. Es gibt keine Einblend-Animationen beim Laden. `prefers-reduced-motion` schaltet alle Animationen ab.

### Zustände

Jedes Bedienelement kennt Hover, Fokus (`:focus-visible`, 2 px Akzent-Outline), Disabled (50 %), Loading (Spinner in `Button`, Skeleton in Daten) und Fehler (`aria-invalid`, Fehlertext unter dem Feld).

## 5. Komponenten (`src/components/ui`)

| Baustein                                               | Einsatz                                                                                                                                                |
| ------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `Button`, `IconButton`                                 | Aktionen. `variant`: primary (eine pro Seite, rechts), secondary, ghost, danger. `icon`/`iconAfter` für Lucide-Icons. Ein Icon-Button braucht `label`. |
| `Input`, `Select`, `Textarea`                          | Formularfelder mit Label, Fehlertext und `aria-describedby`. `Input` nimmt `icon`.                                                                     |
| `Badge`                                                | Status. Varianten success, warning, danger, info, neutral.                                                                                             |
| `Card`                                                 | das eine Panel. Tabellen: `className="p-0 overflow-hidden"`.                                                                                           |
| `MetricCard`                                           | eine Kennzahl, optional als Link auf die Seite, wo man handelt.                                                                                        |
| `PageHeader`                                           | h1, ein Satz Kontext, Seitenaktionen, optional Breadcrumbs.                                                                                            |
| `Toolbar`, `ToolbarEnd`                                | Zeile über einer Liste: Suche, Filter, Zähler. Direkt auf der Seite, nicht in einer Karte.                                                             |
| `table.ts`                                             | Klassen für handgeschriebene Tabellen (Kopf, Zellen, klickbare Zeile).                                                                                 |
| `EmptyState`, `ErrorState`, `Skeleton`, `SkeletonList` | Leer, Fehler, Laden.                                                                                                                                   |
| `Modal`                                                | Dialog mit Größen `sm`–`xl`, stellt den Fokus wieder her.                                                                                              |
| `Tooltip`, `Kbd`                                       | Beschriftung für reine Icon-Bedienung, Tastenkürzel.                                                                                                   |
| `cn`                                                   | Klassen verbinden; spätere Klassen überschreiben frühere (`tailwind-merge`).                                                                           |

Shell-Bausteine liegen in `src/components/shell` (`AppShell`, `SidebarNav`, `Brand`, `UserMenu`, `ThemeToggle`, `AuthCard`). Weitere gemeinsame Muster: `components/spatial/Panel` (ruhiges Seitenpanel neben Graph, Karte, Raumplan und Rack), `components/admin/AdminSection` (Abschnitt mit Überschrift und einem Satz Erklärung) und `pages/ListParts` (leere Tabellenzeile, bleibt eine `td[colSpan]`).

## 6. Seitenmuster

Eine Listenseite besteht in dieser Reihenfolge aus:

1. `PageHeader`: Titel, ein Satz, Aktionen (sekundäre zuerst, die primäre zuletzt).
2. `Toolbar`: Suche mit Icon, Filter als `Select`, Zähler rechts.
3. Inhalt: Tabelle in `Card p-0 overflow-hidden` mit `ScrollRegion`, oder `EmptyState`, `ErrorState`, `SkeletonList`.
4. Paginierung: Bereich „1–50 / 312“, Zurück und Weiter als `Button size="sm"` mit Icons.

Regeln:

- Eine primäre Aktion je Seite und Dialog.
- Leere Ansichten laden zur nächsten Handlung ein („CI anlegen“), Fehler nennen Ursache und Ausweg und bieten „Erneut versuchen“.
- Ein Vorgang heißt im ganzen Ablauf gleich. Anlegen heißt im Kopf „<Objekt> anlegen“ und im Dialog „Anlegen“, Ändern heißt „Speichern“; „erstellen“ kommt nicht vor.
- Unwiderrufliche Löschungen (Webhook, Benutzer) fragen in einem Dialog nach; der rote Button steht nur dort.
- Datumsangaben folgen der Oberflächensprache (`i18n.language`), Aufzählungswerte kommen aus dem Katalog, nie als Rohschlüssel.
- Zeilen, die ein Detail öffnen, sind per Tastatur bedienbar (`useRowNavigation`): Pfeile bewegen, Enter öffnet, Leertaste wählt.
- Tabellen auf schmalen Bildschirmen scrollen waagerecht in einer fokussierbaren `ScrollRegion`.

## 7. Barrierefreiheit

Ziel ist WCAG 2.2 AA. Geprüft wird automatisiert mit axe auf jeder Route, in Hell und Dunkel, in Deutsch und Englisch (`e2e/a11y.spec.ts`), dazu die Tastaturtests (`e2e/keyboard.spec.ts`, `e2e/palette.spec.ts`).

- Skip-Link zum Inhalt, genau ein `<h1>` je Seite (`PageHeader`), Landmarken `nav`, `main`, `header`.
- Fokus bleibt sichtbar; Dialoge fangen den Fokus und geben ihn zurück.
- Zielgrößen mindestens 32 px in Tabellen, 36 px sonst.
- Textfarben nur aus den Token oder `text-muted` / `text-danger`; `src/test/darkVariant.test.ts` und `src/test/lightTheme.test.ts` prüfen das im Quelltext.

## 8. Einschränkungen und bewusste Entscheidungen

- Das Backend liefert keinen Zähler für alle Seiten. Der Zähler „offene Reviews“ in der Navigation nutzt `GET /review-items?status=open&limit=1` und erscheint nur mit `discovery:read`.
- Die Seiten bleiben pro Route Code-Splitting-Chunks; das Designsystem fügt dem Hauptbündel nur Geist (Latin, ca. 30 KB je Schnitt), Lucide-Icons einzeln und Radix-Tooltip hinzu.
- Schriften sind selbst gehostet, es gibt keinen Aufruf an Dritte (DSGVO).
- Die Darstellung (hell, dunkel, System) schaltet ein Knopf im Header zyklisch um. Die Sprache liegt im Kontomenü, vor der Anmeldung im Kopf der Seite.
