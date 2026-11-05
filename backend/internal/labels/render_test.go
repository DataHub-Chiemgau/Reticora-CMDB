package labels_test

import (
	"bytes"
	"compress/zlib"
	"encoding/xml"
	"errors"
	"image"
	"image/png"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/oned"
	"github.com/makiuchi-d/gozxing/qrcode"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/labels"
)

const subjectID = "0b9f7f3e-6a51-4c2e-9d0e-3f5b8a1c2d40"

// decode reads a PNG with gozxing in the symbology.
func decode(t *testing.T, sym labels.Symbology, raw []byte) string {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatalf("bitmap: %v", err)
	}
	hints := map[gozxing.DecodeHintType]any{gozxing.DecodeHintType_TRY_HARDER: true}
	var res *gozxing.Result
	if sym == labels.Code128 {
		res, err = oned.NewCode128Reader().Decode(bmp, hints)
	} else {
		res, err = qrcode.NewQRCodeReader().Decode(bmp, hints)
	}
	if err != nil {
		t.Fatalf("decode %s: %v", sym, err)
	}
	return res.GetText()
}

// TestSymbolPNGDecodes covers INV-03 (WP-849, E-52): the PNG output of
// Code128 and QR decodes to its content (inventory number and URN).
func TestSymbolPNGDecodes(t *testing.T) {
	for _, content := range []string{"INV-000123", labels.URN(labels.KindLocation, subjectID)} {
		for _, sym := range []labels.Symbology{labels.Code128, labels.QR} {
			raw, err := labels.SymbolPNG(sym, content, 3)
			if err != nil {
				t.Fatalf("%s %q: %v", sym, content, err)
			}
			if got := decode(t, sym, raw); got != content {
				t.Errorf("%s: decoded %q, want %q", sym, got, content)
			}
		}
	}
}

// TestLabelPNGDecodes: the PNG label carries the code in both symbols.
func TestLabelPNGDecodes(t *testing.T) {
	s := &labels.Subject{Kind: labels.KindAsset, ID: subjectID, Code: "INV-42", Title: "Laptop"}
	raw, err := labels.Render(s, labels.FormatPNG, "")
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	// The Code128 symbol is the upper part of the image.
	b := img.Bounds()
	sub, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		t.Fatalf("image %T has no SubImage", img)
	}
	var top bytes.Buffer
	if err := png.Encode(&top, sub.SubImage(image.Rect(0, 0, b.Dx(), 50*4))); err != nil {
		t.Fatal(err)
	}
	if got := decode(t, labels.Code128, top.Bytes()); got != s.Code {
		t.Errorf("code128 in label: %q", got)
	}
	if got := decode(t, labels.QR, raw); got != s.Code {
		t.Errorf("qr in label: %q", got)
	}
}

// svgRect is a rect of an SVG symbol.
type svgRect struct {
	X      float64 `xml:"x,attr"`
	Y      float64 `xml:"y,attr"`
	Width  float64 `xml:"width,attr"`
	Height float64 `xml:"height,attr"`
}

type svgDoc struct {
	Rects []svgRect `xml:"g>rect"`
}

// TestSymbolSVGMatchesModules: the SVG rects of Code128 and QR are the
// dark modules of the symbol's module matrix.
func TestSymbolSVGMatchesModules(t *testing.T) {
	content := labels.URN(labels.KindAsset, subjectID)
	for _, sym := range []labels.Symbology{labels.Code128, labels.QR} {
		raw, err := labels.SymbolSVG(sym, content)
		if err != nil {
			t.Fatal(err)
		}
		var doc svgDoc
		if xmlErr := xml.Unmarshal(raw, &doc); xmlErr != nil {
			t.Fatalf("%s: %v", sym, xmlErr)
		}
		bc, err := labels.Encode(sym, content)
		if err != nil {
			t.Fatal(err)
		}
		want := labels.Matrix(bc)
		quiet := 4
		if sym == labels.Code128 {
			quiet = 10
		}
		got := make([][]bool, len(want))
		for i := range got {
			got[i] = make([]bool, len(want[0]))
		}
		for _, r := range doc.Rects {
			row := 0
			if sym == labels.QR {
				row = int(r.Y) - quiet
			}
			for x := int(r.X); x < int(r.X+r.Width); x++ {
				got[row][x-quiet] = true
			}
		}
		for y := range want {
			for x := range want[y] {
				if got[y][x] != want[y][x] {
					t.Fatalf("%s: module (%d,%d) = %v, want %v", sym, x, y, got[y][x], want[y][x])
				}
			}
		}
	}
}

// pdfPages returns the page sizes (pt) and the inflated content streams.
func pdfPages(t *testing.T, raw []byte) (sizes []string, contents []string) {
	t.Helper()
	for _, m := range regexp.MustCompile(`/MediaBox \[0 0 ([\d.]+) ([\d.]+)\]`).FindAllSubmatch(raw, -1) {
		sizes = append(sizes, string(m[1])+"x"+string(m[2]))
	}
	for _, m := range regexp.MustCompile(`(?s)/Filter /FlateDecode /Length \d+>>\nstream\n(.*?)\nendstream`).FindAllSubmatch(raw, -1) {
		r, err := zlib.NewReader(bytes.NewReader(m[1]))
		if err != nil {
			continue
		}
		body, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("inflate: %v", err)
		}
		if strings.Contains(string(body), " Tj") {
			contents = append(contents, string(body))
		}
	}
	return sizes, contents
}

func mmToPt(mm float64) string {
	return strconv.FormatFloat(mm*72/25.4, 'f', 2, 64)
}

// TestPDFTemplates: three templates with their page format and labels per
// page (asset_50x25 and bin_100x50 one, a4_24 twenty-four).
func TestPDFTemplates(t *testing.T) {
	subjects := make([]labels.Subject, 25)
	for i := range subjects {
		subjects[i] = labels.Subject{Kind: labels.KindLocation, ID: subjectID,
			Code: "BIN-" + strconv.Itoa(100+i), Title: "Regal " + strconv.Itoa(i), Subtitle: "Lager Süd"}
	}
	for _, tc := range []struct {
		template      string
		width, height float64
		perPage       int
	}{
		{labels.TemplateAsset50x25, 50, 25, 1},
		{labels.TemplateBin100x50, 100, 50, 1},
		{labels.TemplateA4x24, 210, 297, 24},
	} {
		page, err := labels.Layout(tc.template)
		if err != nil || len(page.Cells) != tc.perPage {
			t.Fatalf("%s: layout %+v, %v", tc.template, page, err)
		}
		raw, err := labels.RenderPDF(tc.template, subjects)
		if err != nil {
			t.Fatalf("%s: %v", tc.template, err)
		}
		if !bytes.HasPrefix(raw, []byte("%PDF-")) {
			t.Fatalf("%s: not a PDF", tc.template)
		}
		sizes, contents := pdfPages(t, raw)
		wantPages := (len(subjects) + tc.perPage - 1) / tc.perPage
		if len(contents) != wantPages {
			t.Fatalf("%s: %d pages with text, want %d", tc.template, len(contents), wantPages)
		}
		wantSize := mmToPt(tc.width) + "x" + mmToPt(tc.height)
		for _, size := range sizes {
			if size != wantSize {
				t.Errorf("%s: page size %s, want %s", tc.template, size, wantSize)
			}
		}
		// Every label prints its code once; the first page holds perPage.
		if n := strings.Count(contents[0], "(BIN-"); n != tc.perPage {
			t.Errorf("%s: %d labels on the first page, want %d", tc.template, n, tc.perPage)
		}
	}
}

// TestRenderRejectsUnknownFormatAndTemplate.
func TestRenderRejectsUnknownFormatAndTemplate(t *testing.T) {
	s := &labels.Subject{Kind: labels.KindAsset, ID: subjectID, Code: "INV-1", Title: "x"}
	if _, err := labels.Render(s, "gif", ""); !errors.Is(err, labels.ErrUnknownFormat) {
		t.Errorf("format gif: %v", err)
	}
	if _, err := labels.Render(s, labels.FormatPDF, "a5"); !errors.Is(err, labels.ErrUnknownTemplate) {
		t.Errorf("template a5: %v", err)
	}
	if _, err := labels.Render(s, labels.FormatSVG, "a5"); !errors.Is(err, labels.ErrUnknownTemplate) {
		t.Errorf("svg with template a5: %v", err)
	}
	if _, err := labels.RenderPDF(labels.TemplateA4x24, nil); !errors.Is(err, labels.ErrNoContent) {
		t.Errorf("no subjects: %v", err)
	}
}

// TestCode128FallsBackToQR: a code Code128 cannot carry (non-ASCII) is on
// the label in the QR code alone.
func TestCode128FallsBackToQR(t *testing.T) {
	s := &labels.Subject{Kind: labels.KindAsset, ID: subjectID, Code: "INV-Ä1", Title: "Gerät"}
	if _, err := labels.Encode(labels.Code128, s.Code); err == nil {
		t.Fatal("code128 accepted non-ASCII")
	}
	raw, err := labels.RenderPNG(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, labels.QR, raw); got != s.Code {
		t.Errorf("qr: %q", got)
	}
	svg, err := labels.RenderSVG(s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(svg, []byte("Ger&auml;t")) && !bytes.Contains(svg, []byte("Gerät")) {
		t.Errorf("svg lacks the title: %s", svg)
	}
}
