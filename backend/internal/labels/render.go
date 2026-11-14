package labels

// Rendering of labels (INV-03, WP-849, E-52): Code128 and QR through
// github.com/boombuler/barcode as SVG and PNG, PDF labels through
// codeberg.org/go-pdf/fpdf (the maintained home of go-pdf/fpdf) with three
// fixed templates. The renderer works on
// the neutral Subject; the asset package maps an asset onto it, WP-763
// renders the batches of inventory areas with it.

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"codeberg.org/go-pdf/fpdf"
	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/code128"
	"github.com/boombuler/barcode/qr"
)

// Subject is what a label shows: the object (Kind, ID), the scanned
// content (Code: the inventory number of an asset, otherwise the URN of
// the object, E-52) and two lines of text.
type Subject struct {
	Kind     string
	ID       string
	Code     string
	Title    string
	Subtitle string
}

// URN returns the URN urn:reticora:<kind>:<uuid> of an object, the label
// content of an object without an inventory number; GET
// /api/v1/labels/lookup resolves it (WP-762).
func URN(kind, id string) string {
	return urnPrefix + kind + ":" + strings.ToLower(id)
}

// Symbology is a barcode symbology of the labels.
type Symbology string

// The symbologies of E-52.
const (
	Code128 Symbology = "code128"
	QR      Symbology = "qr"
)

// Output formats of a label.
const (
	FormatSVG = "svg"
	FormatPNG = "png"
	FormatPDF = "pdf"
)

// Templates of the PDF labels (E-52).
const (
	// TemplateAsset50x25 is the asset label, 50×25 mm, one per page.
	TemplateAsset50x25 = "asset_50x25"
	// TemplateBin100x50 is the bin label, 100×50 mm, one per page.
	TemplateBin100x50 = "bin_100x50"
	// TemplateA4x24 is an A4 sheet of 24 labels (3 columns, 8 rows).
	TemplateA4x24 = "a4_24"
)

// Errors of the renderer.
var (
	ErrUnknownFormat   = errors.New("unknown label format")
	ErrUnknownTemplate = errors.New("unknown label template")
	ErrNoContent       = errors.New("the label has no content")
)

// ContentType returns the media type of a format.
func ContentType(format string) (string, error) {
	switch format {
	case FormatSVG:
		return "image/svg+xml", nil
	case FormatPNG:
		return "image/png", nil
	case FormatPDF:
		return "application/pdf", nil
	default:
		return "", ErrUnknownFormat
	}
}

// Page is the layout of a PDF template: the page size and the label cells
// on a page, all in millimetres.
type Page struct {
	Width, Height float64
	Cells         []Cell
}

// Cell is one label on a page.
type Cell struct {
	X, Y, Width, Height float64
}

// Layout returns the page layout of a template.
func Layout(template string) (Page, error) {
	switch template {
	case TemplateAsset50x25:
		return Page{Width: 50, Height: 25, Cells: []Cell{{Width: 50, Height: 25}}}, nil
	case TemplateBin100x50:
		return Page{Width: 100, Height: 50, Cells: []Cell{{Width: 100, Height: 50}}}, nil
	case TemplateA4x24:
		const cols, rows = 3, 8
		page := Page{Width: 210, Height: 297}
		w, h := page.Width/cols, page.Height/rows
		for row := range rows {
			for col := range cols {
				page.Cells = append(page.Cells, Cell{X: float64(col) * w, Y: float64(row) * h, Width: w, Height: h})
			}
		}
		return page, nil
	default:
		return Page{}, ErrUnknownTemplate
	}
}

// Encode returns the barcode of content in a symbology. Code128 takes up
// to 80 ASCII characters; QR takes UTF-8 at error correction level M.
func Encode(sym Symbology, content string) (barcode.Barcode, error) {
	if content == "" {
		return nil, ErrNoContent
	}
	switch sym {
	case Code128:
		for _, r := range content {
			if r > unicode.MaxASCII || !unicode.IsPrint(r) {
				return nil, fmt.Errorf("code128: %q is not printable ASCII", content)
			}
		}
		return code128.Encode(content)
	case QR:
		return qr.Encode(content, qr.M, qr.Auto)
	default:
		return nil, fmt.Errorf("unknown symbology %q", sym)
	}
}

// Matrix returns the modules of a barcode, rows of dark (true) and light
// modules; a linear barcode has one row.
func Matrix(bc barcode.Barcode) [][]bool {
	b := bc.Bounds()
	out := make([][]bool, b.Dy())
	for y := range out {
		out[y] = make([]bool, b.Dx())
		for x := range out[y] {
			out[y][x] = dark(bc.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

func dark(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r+g+b < 3*0x8000
}

// quietZone returns the light margin in modules around a symbol: ten
// modules left and right of Code128, four around QR.
func quietZone(sym Symbology) int {
	if sym == Code128 {
		return 10
	}
	return 4
}

// SymbolSVG renders a barcode alone as SVG in module units, with its
// quiet zone; a Code128 symbol is barHeight modules high.
func SymbolSVG(sym Symbology, content string) ([]byte, error) {
	bc, err := Encode(sym, content)
	if err != nil {
		return nil, err
	}
	m := Matrix(bc)
	q := quietZone(sym)
	w, h := len(m[0])+2*q, len(m)
	if sym == QR {
		h += 2 * q
	} else {
		h = barHeight
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, w, h)
	buf.WriteString(`<rect width="100%" height="100%" fill="#fff"/>`)
	writeModulesSVG(&buf, sym, m, float64(q), float64(q*boolInt(sym == QR)), 1, float64(barHeight))
	buf.WriteString("</svg>\n")
	return buf.Bytes(), nil
}

// barHeight is the height in modules of a Code128 symbol rendered alone.
const barHeight = 50

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// writeModulesSVG writes the dark runs of a module matrix as rects at
// (x, y) with module size s; a linear symbol's rects are barH high.
func writeModulesSVG(buf *bytes.Buffer, sym Symbology, m [][]bool, x, y, s, barH float64) {
	buf.WriteString(`<g fill="#000">`)
	forEachRun(m, func(row, col, n int) {
		h := s
		if sym == Code128 {
			h = barH
		}
		fmt.Fprintf(buf, `<rect x="%s" y="%s" width="%s" height="%s"/>`,
			num(x+float64(col)*s), num(y+float64(row)*s), num(float64(n)*s), num(h))
	})
	buf.WriteString("</g>")
}

// forEachRun calls fn for every horizontal run of dark modules.
func forEachRun(m [][]bool, fn func(row, col, n int)) {
	for row, line := range m {
		for col := 0; col < len(line); {
			if !line[col] {
				col++
				continue
			}
			start := col
			for col < len(line) && line[col] {
				col++
			}
			fn(row, start, col-start)
		}
	}
}

// num formats a coordinate with at most three decimals.
func num(v float64) string {
	return strconv.FormatFloat(math.Round(v*1000)/1000, 'f', -1, 64)
}

// SymbolPNG renders a barcode alone as PNG with scale pixels per module
// and its quiet zone; a Code128 symbol is barHeight modules high.
func SymbolPNG(sym Symbology, content string, scale int) ([]byte, error) {
	bc, err := Encode(sym, content)
	if err != nil {
		return nil, err
	}
	img := symbolImage(sym, Matrix(bc), scale)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// symbolImage rasterizes a module matrix with its quiet zone.
func symbolImage(sym Symbology, m [][]bool, scale int) *image.Gray {
	q := quietZone(sym)
	w, h := (len(m[0])+2*q)*scale, (len(m)+2*q)*scale
	top := q * scale
	if sym == Code128 {
		h, top = barHeight*scale, 0
	}
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	forEachRun(m, func(row, col, n int) {
		x0, y0 := (q+col)*scale, top+row*scale
		y1 := y0 + scale
		if sym == Code128 {
			y1 = h
		}
		for y := y0; y < y1; y++ {
			for x := x0; x < x0+n*scale; x++ {
				img.SetGray(x, y, color.Gray{})
			}
		}
	})
	return img
}

// symbols encodes the code of a subject: the QR code always, the Code128
// symbol only where the code is printable ASCII of at most 80 characters
// (otherwise nil; the QR code carries the content).
func symbols(s *Subject) (qrCode, bars [][]bool, err error) {
	q, err := Encode(QR, s.Code)
	if err != nil {
		return nil, nil, err
	}
	if b, barErr := Encode(Code128, s.Code); barErr == nil {
		bars = Matrix(b)
	}
	return Matrix(q), bars, nil
}

// RenderPNG renders the symbols of a subject as one PNG without text: the
// Code128 symbol above the QR code (the PNG carries no font).
func RenderPNG(s *Subject) ([]byte, error) {
	qrCode, bars, err := symbols(s)
	if err != nil {
		return nil, err
	}
	const scale = 4
	parts := []*image.Gray{}
	if bars != nil {
		parts = append(parts, symbolImage(Code128, bars, scale))
	}
	parts = append(parts, symbolImage(QR, qrCode, scale))
	w, h := 0, 0
	for _, p := range parts {
		w = max(w, p.Bounds().Dx())
		h += p.Bounds().Dy()
	}
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	y := 0
	for _, p := range parts {
		x := (w - p.Bounds().Dx()) / 2
		for py := range p.Bounds().Dy() {
			copy(img.Pix[(y+py)*img.Stride+x:], p.Pix[py*p.Stride:py*p.Stride+p.Bounds().Dx()])
		}
		y += p.Bounds().Dy()
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// canvas is a drawing surface in millimetres.
type canvas interface {
	modules(sym Symbology, m [][]bool, x, y, size, barH float64)
	// text draws s with its baseline at y; size is the font height.
	text(x, y, size float64, bold bool, s string)
}

// minModule is the narrowest Code128 module (mm) the labels print; a code
// whose symbol would be narrower is carried by the QR code alone.
const minModule = 0.19

// charWidth is the advance of a monospace character per font size
// (Courier in the PDF, monospace in the SVG).
const charWidth = 0.6

// drawLabel lays out one label in the cell: the QR code left, title,
// subtitle and code right of it, the Code128 symbol across the bottom when
// it fits.
func drawLabel(c canvas, cell Cell, s *Subject, qrCode, bars [][]bool) {
	m := cell.Height * 0.08
	inner := cell.Width - 2*m
	contentH := cell.Height - 2*m
	if bars != nil {
		mw := inner / float64(len(bars[0])+2*quietZone(Code128))
		if mw >= minModule {
			barH := cell.Height * 0.22
			c.modules(Code128, bars, cell.X+m+float64(quietZone(Code128))*mw, cell.Y+cell.Height-m-barH, mw, barH)
			contentH -= barH + m/2
		}
	}
	// The QR code takes at most 40 % of the width, the text the rest.
	q := min(contentH, inner*0.4)
	ms := q / float64(len(qrCode)+2)
	c.modules(QR, qrCode, cell.X+m+ms, cell.Y+m+ms, ms, 0)

	tx := cell.X + 2*m + q
	tw := cell.X + cell.Width - m - tx
	ts := cell.Height * 0.11
	y := cell.Y + m + ts
	c.text(tx, y, ts, true, fit(s.Title, ts, tw))
	if s.Subtitle != "" {
		y += ts * 1.2
		c.text(tx, y, ts*0.75, false, fit(s.Subtitle, ts*0.75, tw))
	}
	// The code in plain text at the bottom of the text column, on up to
	// two lines (a URN is longer than a line).
	cs := ts * 0.6
	lines := wrap(s.Code, cs, tw, 2)
	for i, line := range lines {
		c.text(tx, cell.Y+m+contentH-float64(len(lines)-1-i)*cs*1.2, cs, false, line)
	}
}

// wrap breaks s into at most n lines of the width w at font size size;
// the last line is shortened when s is longer.
func wrap(s string, size, w float64, n int) []string {
	per := int(w / (size * charWidth))
	if per <= 0 {
		return nil
	}
	runes := []rune(s)
	var lines []string
	for len(runes) > 0 && len(lines) < n-1 && len(runes) > per {
		lines = append(lines, string(runes[:per]))
		runes = runes[per:]
	}
	return append(lines, fit(string(runes), size, w))
}

// fit shortens s to the width w at font size size.
func fit(s string, size, w float64) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	limit := int(w / (size * charWidth))
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	if limit <= 3 {
		return ""
	}
	return string([]rune(s)[:limit-3]) + "..."
}

// svgCanvas draws into an SVG document.
type svgCanvas struct{ buf *bytes.Buffer }

func (c svgCanvas) modules(sym Symbology, m [][]bool, x, y, size, barH float64) {
	writeModulesSVG(c.buf, sym, m, x, y, size, barH)
}

func (c svgCanvas) text(x, y, size float64, bold bool, s string) {
	if s == "" {
		return
	}
	weight := ""
	if bold {
		weight = ` font-weight="bold"`
	}
	fmt.Fprintf(c.buf, `<text x="%s" y="%s" font-size="%s"%s>%s</text>`, num(x), num(y), num(size), weight, html.EscapeString(s))
}

// RenderSVG renders the asset label of a subject (50×25 mm) as SVG.
func RenderSVG(s *Subject) ([]byte, error) {
	qrCode, bars, err := symbols(s)
	if err != nil {
		return nil, err
	}
	page, err := Layout(TemplateAsset50x25)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, `<svg xmlns="http://www.w3.org/2000/svg" width="%smm" height="%smm" viewBox="0 0 %s %s" font-family="monospace" shape-rendering="crispEdges">`,
		num(page.Width), num(page.Height), num(page.Width), num(page.Height))
	buf.WriteString(`<rect width="100%" height="100%" fill="#fff"/>`)
	drawLabel(svgCanvas{buf: &buf}, page.Cells[0], s, qrCode, bars)
	buf.WriteString("</svg>\n")
	return buf.Bytes(), nil
}

// pdfCanvas draws into an fpdf document.
type pdfCanvas struct {
	pdf       *fpdf.Fpdf
	translate func(string) string
}

// ptPerMM converts millimetres to points (font sizes).
const ptPerMM = 72 / 25.4

func (c pdfCanvas) modules(sym Symbology, m [][]bool, x, y, size, barH float64) {
	forEachRun(m, func(row, col, n int) {
		h := size
		if sym == Code128 {
			h = barH
		}
		c.pdf.Rect(x+float64(col)*size, y+float64(row)*size, float64(n)*size, h, "F")
	})
}

func (c pdfCanvas) text(x, y, size float64, bold bool, s string) {
	if s == "" {
		return
	}
	style := ""
	if bold {
		style = "B"
	}
	c.pdf.SetFont("Courier", style, size*ptPerMM)
	c.pdf.Text(x, y, c.translate(s))
}

// RenderPDF renders the labels of the subjects with a template: one label
// per page (asset_50x25, bin_100x50) or 24 per A4 page (a4_24).
func RenderPDF(template string, subjects []Subject) ([]byte, error) {
	page, err := Layout(template)
	if err != nil {
		return nil, err
	}
	if len(subjects) == 0 {
		return nil, ErrNoContent
	}
	pdf := fpdf.NewCustom(&fpdf.InitType{
		OrientationStr: "P",
		UnitStr:        "mm",
		Size:           fpdf.SizeType{Wd: page.Width, Ht: page.Height},
	})
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0)
	pdf.SetFillColor(0, 0, 0)
	c := pdfCanvas{pdf: pdf, translate: pdf.UnicodeTranslatorFromDescriptor("")}
	for i := range subjects {
		slot := i % len(page.Cells)
		if slot == 0 {
			pdf.AddPage()
		}
		qrCode, bars, symErr := symbols(&subjects[i])
		if symErr != nil {
			return nil, symErr
		}
		drawLabel(c, page.Cells[slot], &subjects[i], qrCode, bars)
	}
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Render renders the label of one subject in a format; template applies
// to pdf only (empty: asset_50x25) and must be known in any case.
func Render(s *Subject, format, template string) ([]byte, error) {
	if template == "" {
		template = TemplateAsset50x25
	}
	if _, err := Layout(template); err != nil {
		return nil, err
	}
	switch format {
	case FormatSVG:
		return RenderSVG(s)
	case FormatPNG:
		return RenderPNG(s)
	case FormatPDF:
		return RenderPDF(template, []Subject{*s})
	default:
		return nil, ErrUnknownFormat
	}
}
