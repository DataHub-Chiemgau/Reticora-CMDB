package importer

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// readAll reads every data row of r; it stops at the first fatal error.
func readAll(t *testing.T, r Reader) ([]Row, []*ParseError, error) {
	t.Helper()
	var rows []Row
	var rowErrs []*ParseError
	for {
		row, err := r.Next()
		if errors.Is(err, io.EOF) {
			return rows, rowErrs, nil
		}
		if err != nil {
			var pe *ParseError
			if errors.As(err, &pe) && !pe.Fatal {
				rowErrs = append(rowErrs, pe)
				continue
			}
			return rows, rowErrs, err
		}
		rows = append(rows, Row{Line: row.Line, Cells: slices.Clone(row.Cells)})
	}
}

// wantCode asserts a fatal parser error with code.
func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var pe *ParseError
	if !errors.As(err, &pe) || pe.Code != code || !pe.Fatal {
		t.Fatalf("error = %v, want fatal %s", err, code)
	}
}

func TestCSVSeparatorsAndBOM(t *testing.T) {
	for name, content := range map[string]string{
		"semicolon":     "name;serial\nweb-01;S1\n",
		"comma":         "name,serial\nweb-01,S1\n",
		"tab":           "name\tserial\nweb-01\tS1\n",
		"bom semicolon": "\xEF\xBB\xBFname;serial\nweb-01;S1\n",
		"bom comma":     "\xEF\xBB\xBFname,serial\r\nweb-01,S1\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			r, err := OpenCSV(strings.NewReader(content))
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if got := r.Header(); !slices.Equal(got, []string{"name", "serial"}) {
				t.Fatalf("header = %q", got)
			}
			rows, _, err := readAll(t, r)
			if err != nil || len(rows) != 1 || !slices.Equal(rows[0].Cells, []string{"web-01", "S1"}) || rows[0].Line != 2 {
				t.Fatalf("rows = %+v, err = %v", rows, err)
			}
			if r.Format() != FormatCSV || r.Sheets() != nil {
				t.Fatalf("format %s sheets %v", r.Format(), r.Sheets())
			}
		})
	}
}

func TestCSVQuotesLineBreaksAndEmptyRows(t *testing.T) {
	content := "name;note\n\n\"web;01\";\"line one\nline two\"\n;;\n\"say \"\"hi\"\"\";x\n"
	r, err := OpenCSV(strings.NewReader(content))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rows, _, err := readAll(t, r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2 (empty rows skipped)", rows)
	}
	if !slices.Equal(rows[0].Cells, []string{"web;01", "line one\nline two"}) || rows[0].Line != 3 {
		t.Fatalf("row 1 = %+v", rows[0])
	}
	// The line is the one of the file: the quoted line break and the
	// empty rows count.
	if !slices.Equal(rows[1].Cells, []string{`say "hi"`, "x"}) || rows[1].Line != 6 {
		t.Fatalf("row 2 = %+v", rows[1])
	}
}

func TestCSVMalformedRowIsReportedAndSkipped(t *testing.T) {
	r, err := OpenCSV(strings.NewReader("a,b\nx,y\"z\n1,2\n"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rows, rowErrs, err := readAll(t, r)
	if err != nil || len(rows) != 1 || len(rowErrs) != 1 || rowErrs[0].Code != CodeRowMalformed || rowErrs[0].Line != 2 {
		t.Fatalf("rows %+v, row errors %+v, err %v", rows, rowErrs, err)
	}
}

func TestCSVInvalidUTF8IsUnsupported(t *testing.T) {
	_, err := OpenCSV(strings.NewReader("name;ort\nweb;M\xfcnchen\n"))
	if err == nil {
		r, _ := OpenCSV(strings.NewReader("name;ort\nweb;M\xfcnchen\n"))
		_, _, err = readAll(t, r)
	}
	wantCode(t, err, CodeFormatUnsupported)
	_, err = OpenCSV(strings.NewReader("n\xe4me;ort\n"))
	wantCode(t, err, CodeFormatUnsupported)
}

func TestCSVRowLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("n\n")
	for i := range MaxRows + 1 {
		fmt.Fprintf(&b, "%d\n", i)
	}
	r, err := OpenCSV(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rows, _, err := readAll(t, r)
	wantCode(t, err, CodeRowLimit)
	if len(rows) != MaxRows {
		t.Fatalf("read %d rows before the limit, want %d", len(rows), MaxRows)
	}

	// Exactly MaxRows rows are fine.
	b.Reset()
	b.WriteString("n\n")
	for i := range MaxRows {
		fmt.Fprintf(&b, "%d\n", i)
	}
	r, _ = OpenCSV(strings.NewReader(b.String()))
	if rows, _, err = readAll(t, r); err != nil || len(rows) != MaxRows {
		t.Fatalf("%d rows, err %v", len(rows), err)
	}
}

func TestCSVTooComplex(t *testing.T) {
	cols := make([]string, MaxColumns+1)
	for i := range cols {
		cols[i] = "c"
	}
	_, err := OpenCSV(strings.NewReader(strings.Join(cols, ";") + "\n"))
	wantCode(t, err, CodeFileTooComplex)

	long := "name;note\nweb;" + strings.Repeat("x", MaxRowBytes+1) + "\n"
	r, err := OpenCSV(strings.NewReader(long))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _, err = readAll(t, r)
	wantCode(t, err, CodeFileTooComplex)

	// 500 columns are allowed.
	if _, err = OpenCSV(strings.NewReader(strings.Join(cols[:MaxColumns], ";") + "\n")); err != nil {
		t.Fatalf("500 columns: %v", err)
	}
}

func TestCSVWithoutHeader(t *testing.T) {
	_, err := OpenCSV(strings.NewReader("\n\n"))
	wantCode(t, err, CodeFormatUnsupported)
}

// workbook builds an XLSX file with the given sheets and rows.
func workbook(t *testing.T, sheets map[string][][]string, order ...string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	for i, name := range order {
		if i == 0 {
			if err := f.SetSheetName("Sheet1", name); err != nil {
				t.Fatal(err)
			}
		} else if _, err := f.NewSheet(name); err != nil {
			t.Fatal(err)
		}
		for r, row := range sheets[name] {
			for c, v := range row {
				cell, _ := excelize.CoordinatesToCellName(c+1, r+1)
				if err := f.SetCellStr(name, cell, v); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestXLSXFirstAndNamedSheet(t *testing.T) {
	data := workbook(t, map[string][][]string{
		"CIs":    {{"name", "serial"}, {"web-01", "S1"}, {}, {"web-02", "S2"}},
		"Assets": {{"tag"}, {"A-1"}},
	}, "CIs", "Assets")

	r, err := OpenXLSX(bytes.NewReader(data), "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !slices.Equal(r.Sheets(), []string{"CIs", "Assets"}) || !slices.Equal(r.Header(), []string{"name", "serial"}) {
		t.Fatalf("sheets %v header %v", r.Sheets(), r.Header())
	}
	rows, _, err := readAll(t, r)
	_ = r.Close()
	if err != nil || len(rows) != 2 || rows[0].Line != 2 || rows[1].Line != 4 || !slices.Equal(rows[1].Cells, []string{"web-02", "S2"}) {
		t.Fatalf("rows %+v err %v", rows, err)
	}

	r, err = Open(FormatXLSX, bytes.NewReader(data), "Assets")
	if err != nil {
		t.Fatalf("open named: %v", err)
	}
	rows, _, err = readAll(t, r)
	_ = r.Close()
	if err != nil || !slices.Equal(r.Header(), []string{"tag"}) || len(rows) != 1 || rows[0].Cells[0] != "A-1" {
		t.Fatalf("named sheet: header %v rows %+v err %v", r.Header(), rows, err)
	}

	_, err = OpenXLSX(bytes.NewReader(data), "Missing")
	wantCode(t, err, CodeSheetNotFound)
}

func TestXLSXTooComplex(t *testing.T) {
	// An entry that declares far more uncompressed bytes than the unpack
	// limit: the ratio of a zip bomb, refused before unpacking.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{Name: "xl/worksheets/sheet1.xml", Method: zip.Store,
		CompressedSize64: 4, UncompressedSize64: uint64(xlsxUnzipSizeLimit) + 1})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("<x/>"))
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = OpenXLSX(bytes.NewReader(buf.Bytes()), "")
	wantCode(t, err, CodeFileTooComplex)

	// More than 500 columns in a row.
	row := make([]string, MaxColumns+1)
	for i := range row {
		row[i] = "c"
	}
	_, err = OpenXLSX(bytes.NewReader(workbook(t, map[string][][]string{"S": {row}}, "S")), "")
	wantCode(t, err, CodeFileTooComplex)
}

func TestXLSXRowLimit(t *testing.T) {
	f := excelize.NewFile()
	sw, err := f.NewStreamWriter("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= MaxRows+2; i++ {
		cell, _ := excelize.CoordinatesToCellName(1, i)
		if err = sw.SetRow(cell, []any{fmt.Sprintf("r%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err = sw.Flush(); err != nil {
		t.Fatal(err)
	}
	buf, err := f.WriteToBuffer()
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenXLSX(bytes.NewReader(buf.Bytes()), "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = r.Close() }()
	_, _, err = readAll(t, r)
	wantCode(t, err, CodeRowLimit)
}

func TestXLSXWithoutZIPSignatureIsUnsupported(t *testing.T) {
	// OLE2 compound file (.xls).
	ole := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 512)...)
	_, err := OpenXLSX(bytes.NewReader(ole), "")
	wantCode(t, err, CodeFormatUnsupported)
	_, err = OpenXLSX(strings.NewReader("name;serial\n"), "")
	wantCode(t, err, CodeFormatUnsupported)
	// A ZIP archive that is no workbook.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("readme.txt")
	_, _ = w.Write([]byte("hi"))
	_ = zw.Close()
	_, err = OpenXLSX(bytes.NewReader(buf.Bytes()), "")
	if err == nil {
		t.Fatal("a ZIP without workbook was opened")
	}
}

func TestFormatOfMime(t *testing.T) {
	for mime, want := range map[string]string{
		"text/csv": FormatCSV, "text/plain": FormatCSV, mimeXLSX: FormatXLSX,
		"application/vnd.ms-excel": "", "application/vnd.oasis.opendocument.spreadsheet": "",
		"application/json": "", "application/pdf": "",
	} {
		got, ok := FormatOfMime(mime)
		if got != want || ok != (want != "") {
			t.Errorf("FormatOfMime(%q) = %q, %v", mime, got, ok)
		}
	}
	if _, err := Open("json", strings.NewReader("{}"), ""); err == nil {
		t.Fatal("json opened")
	}
}
