# Kapazitätsprognose: Rechenverfahren

Diese Seite legt das Verfahren der Kapazitätsprognosen offen (ALR-08, [E-208](../plan/phase3/entscheidungen.md#e-208), WP-1059). Die Rechnung steckt in der Bibliothek `backend/internal/alarm/forecast` (Funktion `Fit`). Den täglichen Lauf, die Speicherung und den Alarm im Horizont liefert WP-1060, API und Oberfläche WP-1061.

## Datenbasis

- Eine Prognose arbeitet auf **einer Reihe von Tageswerten**, z. B. der Auslastung eines Interfaces. Der Tageswert ist das Maximum der Stundenmittel eines UTC-Tages. Die Reihe je Serie (CI, Metrik, Labels wie `ifIndex`) bildet WP-1060.
- Jeder Tag wird auf 00:00 UTC gekürzt. Mehrere Werte desselben Tages werden gemittelt. Nicht endliche Werte (NaN, ±∞) werden verworfen.
- **Fenster:** 30 oder 90 Tage (Standard 90). Jeder andere Wert (auch 0, 7 oder 45) wird als 90 Tage gerechnet. Das Fenster endet am jüngsten Tag mit Wert und beginnt `Fenster − 1` Tage davor. Fehlende Tage sind zulässig.
- **Mindestdaten:** mindestens 14 Tage mit Wert im Fenster. Sonst ist der Status `insufficient_data` und es gibt keine Prognose.

## Regression

Kleinste Quadrate y = a + b·x, wobei x die Zahl der Tage seit Fensterbeginn ist (fehlende Tage zählen mit, x ist ein Datumsabstand):

- x̄ = Σx / n, ȳ = Σy / n
- Sxx = Σ(x − x̄)², Sxy = Σ(x − x̄)(y − ȳ)
- **Steigung** b = Sxy / Sxx (je Tag), **Achsenabschnitt** a = ȳ − b·x̄
- **Bestimmtheitsmaß** R² = 1 − SSE / SST mit SSE = Σ(y − ŷ)², SST = Σ(y − ȳ)²; bei SST = 0 und SSE = 0 ist R² = 1
- **Residuen-Standardabweichung** σ = √(SSE / (n − 2))

Alle Summen laufen in fester Reihenfolge über die nach Datum sortierten Tage. Es gibt keine Zufallszahlen und keine Map-Iteration; gleiche Eingabe ergibt bitgleiche Ausgabe.

## Saisonbereinigung (Wochenprofil)

Optional (`Seasonal`), nur wenn **jeder Wochentag mindestens zweimal** im Fenster vorkommt; sonst wird ohne Saisonalität gerechnet und `SeasonalApplied` ist `false`.

1. Erste Anpassung wie oben.
2. Je Wochentag w das Mittel der Residuen: p(w) = Mittel(y − (a + b·x)) über die Tage mit Wochentag w. Die sieben Werte sind das **Wochenprofil**.
3. Von jedem Wert wird p(Wochentag) abgezogen; danach zweite Anpassung → a, b.
4. Modell: ŷ(x) = a + b·x + p(Wochentag von x). R² und σ beziehen sich auf dieses Modell.

## Prognose und Prognoseintervall

Für einen Tag x₀ nach dem Fenster:

ŷ(x₀) = a + b·x₀ + p(Wochentag von x₀) (ohne Saisonalität p = 0)

Intervall: ŷ(x₀) ± t(n − 2) · σ · √(1 + 1/n + (x₀ − x̄)² / Sxx)

- 80 %: t = 0,90-Quantil der Student-t-Verteilung, 95 %: t = 0,975-Quantil.
- Die Quantile stehen als Tabelle für 1 bis 120 Freiheitsgrade im Code (sechs Nachkommastellen); darüber gilt die Normalverteilung (1,281552 bzw. 1,959964).

## Schwellwertüberschreitung und Status

Gesucht wird tageweise vom Tag nach dem jüngsten Wert bis `SearchDays` (Standard 365) Tage danach. Eine Schwelle gilt als erreicht, wenn der Wert ≥ Schwelle ist (mit einer Rundungstoleranz von 10⁻⁹ relativ zur Schwelle).

- `CrossingAt`: erster Tag mit ŷ ≥ Schwelle.
- `CrossingEarliest`: erster Tag, an dem die obere 95-%-Grenze die Schwelle erreicht.
- `CrossingLatest`: erster Tag, an dem die untere 95-%-Grenze die Schwelle erreicht (fehlt, wenn das im Suchraum nicht geschieht).

| Status | Bedeutung |
|---|---|
| `insufficient_data` | weniger als 14 Tage mit Wert im Fenster |
| `exceeded` | der jüngste Wert liegt bereits auf oder über der Schwelle |
| `stable` | Steigung ≤ 0 oder keine Überschreitung im Suchraum |
| `crossing` | Überschreitung im Suchraum gefunden (`CrossingAt` gesetzt) |

Ob daraus ein Alarm wird (Horizont, Standard 30 Tage), entscheidet WP-1060.

## Rechenbeispiel

Reihe über 90 Tage (x = 0 … 89) mit y = 0,2 + 0,005·x, Schwelle 0,8, ohne Saisonalität:

- x̄ = 44,5, ȳ = 0,4225, Sxx = 60.742,5, Sxy = 303,7125 → b = 0,005, a = 0,4225 − 0,005·44,5 = 0,2
- SSE = 0 → R² = 1, σ = 0, das Intervall fällt auf ŷ zusammen.
- ŷ(x) ≥ 0,8 ⇔ x ≥ 120. Der jüngste Wert liegt bei x = 89 (0,645), also ist `CrossingAt` = Tag 120, 31 Tage nach dem jüngsten Wert; `CrossingEarliest` = `CrossingLatest` = `CrossingAt`, Status `crossing`.

Mit Rauschen (σ > 0) öffnet sich das Intervall: `CrossingEarliest` liegt vor, `CrossingLatest` nach `CrossingAt`. Die Testreihen in `backend/internal/alarm/forecast/testdata/series.json` zeigen das für eine Interface-Auslastung mit Trend und Wochenenddelle (Test `TestFitGolden`).
