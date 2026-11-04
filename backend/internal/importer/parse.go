package importer

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

// Limits of an import file (E-49, IMP-IO-01).
const (
	// MaxFileBytes is the largest document version an import reads, 50 MB.
	MaxFileBytes = 52_428_800
	// MaxRows is the largest number of data rows (header excluded).
	MaxRows = 50_000
	// MaxColumns is the largest number of cells in a row.
	MaxColumns = 500
	// MaxRowBytes is the largest size of one row: the bytes of the record
	// in a CSV file, the sum of the cell values in an XLSX file.
	MaxRowBytes = 1 << 20
	// PreviewRows is the number of data rows the preview reads.
	PreviewRows = 20
)

// Unpack limits of an XLSX file (E-49): the sum of the uncompressed entries
// and the size up to which a worksheet is unpacked in memory (larger ones go
// to a temporary file). Variables so a test can lower them.
var (
	xlsxUnzipSizeLimit    int64 = 200 << 20
	xlsxUnzipXMLSizeLimit int64 = 16 << 20
)

// Formats of the parser: the profile formats an import file can have.
const (
	FormatCSV  = "csv"
	FormatXLSX = "xlsx"
)

// Verified media types of a document version (sniff of WP-268) that an
// import reads.
const (
	mimeCSV       = "text/csv"
	mimeText      = "text/plain"
	mimeXLSX      = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	zipSignature  = "PK\x03\x04"
	csvPeekWindow = 64 << 10
)

// Error codes of an import (job.error, report, problem types). A code is
// the last path segment of its problem type https://reticora.io/problems/.
const (
	CodeFormatUnsupported = "import-format-unsupported"
	CodeSheetNotFound     = "import-sheet-not-found"
	CodeRowLimit          = "import-row-limit"
	CodeFileTooComplex    = "import-file-too-complex"
	CodeFileTooLarge      = "import-file-too-large"
	// CodeRowMalformed is a row the CSV reader cannot split (a stray
	// quote); the run reports it and goes on with the next row.
	CodeRowMalformed = "import-row-malformed"
)

// ProblemType returns the problem type URI of an error code.
func ProblemType(code string) string { return "https://reticora.io/problems/" + code }

// ParseError is an error of the parser with its code, the line of the file
// (0: the file as a whole) and the column (1-based, 0: the row as a
// whole). A Fatal error ends the reading; any other affects only its row.
type ParseError struct {
	Code    string
	Line    int
	Column  int
	Message string
	Fatal   bool
}

// Error returns the code and the message.
func (e *ParseError) Error() string { return e.Code + ": " + e.Message }

func fatal(code string, line int, format string, args ...any) *ParseError {
	return &ParseError{Code: code, Line: line, Message: fmt.Sprintf(format, args...), Fatal: true}
}

// Row is a data row: its line in the file (the row number of the sheet for
// XLSX) and its cells.
type Row struct {
	Line  int
	Cells []string
}

// Reader reads an import file row by row (streamed): Header is the first
// non-empty row, Next returns the following non-empty rows and io.EOF at
// the end. Next counts the data rows: beyond MaxRows it fails with
// CodeRowLimit. The cells of a returned row are valid until the next call.
type Reader interface {
	Format() string
	// Sheets lists the sheets of an XLSX file; nil for CSV.
	Sheets() []string
	Header() []string
	Next() (Row, error)
	Close() error
}

// FormatOfMime returns the parser format of a verified media type, false
// for a type an import does not read (.xls, .ods, JSON and any other).
func FormatOfMime(mimeType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case mimeCSV, mimeText:
		return FormatCSV, true
	case mimeXLSX:
		return FormatXLSX, true
	}
	return "", false
}

// Open returns the reader of format over r: for XLSX of the named sheet
// (empty: the first). r is read at most up to MaxFileBytes; the caller
// closes the reader.
func Open(format string, r io.Reader, sheet string) (Reader, error) {
	switch format {
	case FormatCSV:
		return OpenCSV(r)
	case FormatXLSX:
		return OpenXLSX(r, sheet)
	}
	return nil, fatal(CodeFormatUnsupported, 0, "the import format %q is not supported", format)
}

// rowCounter counts the data rows of a reader and enforces MaxRows.
type rowCounter struct{ rows int }

func (c *rowCounter) count(line int) error {
	c.rows++
	if c.rows > MaxRows {
		return fatal(CodeRowLimit, line, "the file has more than %d data rows", MaxRows)
	}
	return nil
}

// blank reports whether every cell of a row is empty or white space.
func blank(cells []string) bool {
	for _, c := range cells {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// csvReader reads a CSV file (UTF-8 with or without BOM; separator
// semicolon, comma or tab, detected from the header line).
type csvReader struct {
	r       *csv.Reader
	header  []string
	counter rowCounter
	offset  int64
}

// OpenCSV starts reading a CSV file: it skips a UTF-8 BOM, detects the
// separator from the header line and reads the header.
func OpenCSV(r io.Reader) (Reader, error) {
	br := bufio.NewReaderSize(io.LimitReader(r, MaxFileBytes), csvPeekWindow)
	if bom, _ := br.Peek(3); bytes.Equal(bom, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	}
	head, err := br.Peek(csvPeekWindow)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, fmt.Errorf("read import file: %w", err)
	}
	cr := csv.NewReader(br)
	cr.Comma = detectSeparator(head)
	cr.ReuseRecord = true
	cr.FieldsPerRecord = -1
	c := &csvReader{r: cr}
	for {
		row, rerr := c.read()
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil, fatal(CodeFormatUnsupported, 0, "the file has no header row")
			}
			var perr *ParseError
			if errors.As(rerr, &perr) && !perr.Fatal {
				// A header that cannot be split is no table.
				perr.Fatal, perr.Code = true, CodeFormatUnsupported
			}
			return nil, rerr
		}
		if !blank(row.Cells) {
			c.header = slices.Clone(row.Cells)
			return c, nil
		}
	}
}

// detectSeparator picks the separator that occurs most often in the first
// line outside quotes: semicolon, comma or tab; comma when none occurs.
func detectSeparator(head []byte) rune {
	counts := map[rune]int{}
	quoted := false
	for _, b := range head {
		if b == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		if b == '\n' || b == '\r' {
			break
		}
		switch b {
		case ';', ',', '\t':
			counts[rune(b)]++
		}
	}
	best, n := ',', 0
	for _, sep := range []rune{';', ',', '\t'} {
		if counts[sep] > n {
			best, n = sep, counts[sep]
		}
	}
	return best
}

// read returns the next record with its limits checked: valid UTF-8, at
// most MaxColumns cells and MaxRowBytes bytes.
func (c *csvReader) read() (Row, error) {
	rec, err := c.r.Read()
	start := c.offset
	c.offset = c.r.InputOffset()
	if err != nil {
		var perr *csv.ParseError
		if errors.As(err, &perr) {
			if c.offset-start > MaxRowBytes {
				return Row{}, fatal(CodeFileTooComplex, perr.StartLine, "row %d is larger than %d bytes", perr.StartLine, MaxRowBytes)
			}
			return Row{}, &ParseError{Code: CodeRowMalformed, Line: perr.StartLine, Column: perr.Column,
				Message: perr.Err.Error()}
		}
		return Row{}, err
	}
	line, _ := c.r.FieldPos(0)
	if c.offset-start > MaxRowBytes {
		return Row{}, fatal(CodeFileTooComplex, line, "row %d is larger than %d bytes", line, MaxRowBytes)
	}
	if len(rec) > MaxColumns {
		return Row{}, fatal(CodeFileTooComplex, line, "row %d has more than %d columns", line, MaxColumns)
	}
	for _, cell := range rec {
		if !utf8.ValidString(cell) {
			return Row{}, fatal(CodeFormatUnsupported, line, "row %d is not valid UTF-8", line)
		}
	}
	return Row{Line: line, Cells: rec}, nil
}

func (c *csvReader) Format() string   { return FormatCSV }
func (c *csvReader) Sheets() []string { return nil }
func (c *csvReader) Header() []string { return c.header }
func (c *csvReader) Close() error     { return nil }
func (c *csvReader) Next() (Row, error) {
	for {
		row, err := c.read()
		if err != nil {
			return Row{}, err
		}
		if blank(row.Cells) {
			continue
		}
		if cerr := c.counter.count(row.Line); cerr != nil {
			return Row{}, cerr
		}
		return row, nil
	}
}

// xlsxReader reads one sheet of an XLSX workbook row by row.
type xlsxReader struct {
	f       *excelize.File
	rows    *excelize.Rows
	sheets  []string
	header  []string
	line    int
	counter rowCounter
}

// OpenXLSX opens an XLSX workbook (ZIP signature required, unpack limits
// of E-49) and starts reading the named sheet, the first when sheet is
// empty; an unknown sheet is CodeSheetNotFound.
func OpenXLSX(r io.Reader, sheet string) (Reader, error) {
	br := bufio.NewReader(io.LimitReader(r, MaxFileBytes))
	if sig, _ := br.Peek(len(zipSignature)); string(sig) != zipSignature {
		return nil, fatal(CodeFormatUnsupported, 0, "the file is no XLSX workbook (no ZIP signature)")
	}
	f, err := excelize.OpenReader(br, excelize.Options{UnzipSizeLimit: xlsxUnzipSizeLimit, UnzipXMLSizeLimit: xlsxUnzipXMLSizeLimit})
	if err != nil {
		if strings.Contains(err.Error(), "unzip size exceeds") {
			return nil, fatal(CodeFileTooComplex, 0, "the workbook unpacks to more than %d bytes", xlsxUnzipSizeLimit)
		}
		return nil, fatal(CodeFormatUnsupported, 0, "the file is no readable XLSX workbook")
	}
	x := &xlsxReader{f: f, sheets: f.GetSheetList()}
	if sheet == "" {
		if len(x.sheets) == 0 {
			_ = f.Close()
			return nil, fatal(CodeSheetNotFound, 0, "the workbook has no sheet")
		}
		sheet = x.sheets[0]
	} else if !slices.Contains(x.sheets, sheet) {
		_ = f.Close()
		return nil, fatal(CodeSheetNotFound, 0, "the workbook has no sheet %q", sheet)
	}
	rows, err := f.Rows(sheet)
	if err != nil {
		_ = f.Close()
		return nil, fatal(CodeFormatUnsupported, 0, "the sheet %q cannot be read", sheet)
	}
	x.rows = rows
	for {
		row, rerr := x.read()
		if rerr != nil {
			_ = x.Close()
			if errors.Is(rerr, io.EOF) {
				return nil, fatal(CodeFormatUnsupported, 0, "the sheet %q has no header row", sheet)
			}
			return nil, rerr
		}
		if !blank(row.Cells) {
			x.header = slices.Clone(row.Cells)
			return x, nil
		}
	}
}

// read returns the next row of the sheet with its limits checked.
func (x *xlsxReader) read() (Row, error) {
	if !x.rows.Next() {
		if err := x.rows.Error(); err != nil {
			return Row{}, fatal(CodeFormatUnsupported, x.line, "the sheet cannot be read")
		}
		return Row{}, io.EOF
	}
	x.line++
	cells, err := x.rows.Columns()
	if err != nil {
		return Row{}, fatal(CodeFormatUnsupported, x.line, "row %d cannot be read", x.line)
	}
	if len(cells) > MaxColumns {
		return Row{}, fatal(CodeFileTooComplex, x.line, "row %d has more than %d columns", x.line, MaxColumns)
	}
	size := 0
	for _, cell := range cells {
		size += len(cell)
	}
	if size > MaxRowBytes {
		return Row{}, fatal(CodeFileTooComplex, x.line, "row %d is larger than %d bytes", x.line, MaxRowBytes)
	}
	return Row{Line: x.line, Cells: cells}, nil
}

func (x *xlsxReader) Format() string   { return FormatXLSX }
func (x *xlsxReader) Sheets() []string { return x.sheets }
func (x *xlsxReader) Header() []string { return x.header }
func (x *xlsxReader) Next() (Row, error) {
	for {
		row, err := x.read()
		if err != nil {
			return Row{}, err
		}
		if blank(row.Cells) {
			continue
		}
		if cerr := x.counter.count(row.Line); cerr != nil {
			return Row{}, cerr
		}
		return row, nil
	}
}

// Close releases the sheet and the temporary files of the workbook.
func (x *xlsxReader) Close() error {
	var err error
	if x.rows != nil {
		err = x.rows.Close()
	}
	return errors.Join(err, x.f.Close())
}
