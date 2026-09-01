package asset

import (
	"fmt"
	"html"
	"strings"
)

// RenderLabelSVG renders a direct-access asset label (spec §13: Direct-Access-
// Etiketten): asset tag, name, optional barcode/RFID value as a scannable
// pseudo-barcode strip, and the deep link into the CMDB.
//
// The barcode strip is a deterministic bar pattern derived from the value's
// bytes — a visual label marker, not a scanner-grade symbology.
func RenderLabelSVG(a *Asset, baseURL string) string {
	tag := a.AssetTag
	if tag == "" {
		tag = a.ID
	}
	code := a.Barcode
	if code == "" {
		code = a.RFIDTag
	}
	if code == "" {
		code = tag
	}
	link := strings.TrimRight(baseURL, "/") + "/assets/" + a.ID

	var bars strings.Builder
	x := 20
	for _, b := range []byte(code) {
		w := 1 + int(b%3)
		if b%2 == 0 {
			bars.WriteString(fmt.Sprintf(`<rect x="%d" y="78" width="%d" height="30" fill="#111"/>`, x, w))
		}
		x += w + 1
		if x > 268 {
			break
		}
	}

	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="300" height="140" viewBox="0 0 300 140" font-family="monospace">
  <rect width="300" height="140" fill="#fff" stroke="#111" stroke-width="2" rx="8"/>
  <text x="16" y="30" font-size="18" font-weight="bold">%s</text>
  <text x="16" y="52" font-size="11" fill="#333">%s</text>
  <text x="16" y="68" font-size="9" fill="#666">%s</text>
  %s
  <text x="16" y="126" font-size="8" fill="#888">%s</text>
</svg>`, html.EscapeString(tag), html.EscapeString(a.Name), html.EscapeString(code), bars.String(), html.EscapeString(link))
}
