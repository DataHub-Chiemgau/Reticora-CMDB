package document

import (
	"archive/zip"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"
)

// ProblemMimeMismatch is the problem type of an upload whose content does
// not match its declared Content-Type (SEC-09, WP-268).
const ProblemMimeMismatch = "https://reticora.io/problems/mime-mismatch"

// sniffLen is the number of leading bytes checked before the body is read
// completely, as many as http.DetectContentType considers.
const sniffLen = 512

// allowedMimeTypes restricts uploads to common, safe office/image formats.
// Executables, scripts and HTML are excluded on purpose: a stored file is
// later downloaded by other users, and serving attacker-controlled HTML/JS
// from our own origin would be an XSS vector. Each type needs a matching
// signature (contentSignatures); application/octet-stream, which says
// nothing about the content, is not accepted (SEC-09).
var allowedMimeTypes = map[string]bool{
	"application/pdf":    true,
	"application/json":   true,
	"application/xml":    true,
	"application/zip":    true,
	"application/msword": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
	"application/vnd.ms-excel": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         true,
	"application/vnd.ms-powerpoint":                                             true,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
	"text/plain": true,
	"text/csv":   true,
}

// signatureKind names how a whitelisted type is recognized.
type signatureKind int

const (
	// sniffed: http.DetectContentType names the type itself.
	sniffed signatureKind = iota
	// pdfMagic: the content starts with %PDF-.
	pdfMagic
	// zipMagic: a ZIP archive (PK\x03\x04).
	zipMagic
	// ooxml: a ZIP archive with the entry [Content_Types].xml.
	ooxml
	// ole2: a compound file (doc, xls, ppt).
	ole2
	// utf8Text: valid UTF-8 the sniffer takes for plain text, not HTML.
	utf8Text
)

// contentSignatures is the signature table of the whitelist: every
// allowed type and how its content is recognized.
var contentSignatures = map[string]signatureKind{
	"application/pdf":    pdfMagic,
	"application/json":   utf8Text,
	"application/xml":    sniffed, // the sniffer's text/xml is its alias
	"application/zip":    zipMagic,
	"application/msword": ole2,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ooxml,
	"application/vnd.ms-excel": ole2,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         ooxml,
	"application/vnd.ms-powerpoint":                                             ole2,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ooxml,
	"image/png":  sniffed,
	"image/jpeg": sniffed,
	"image/gif":  sniffed,
	"image/webp": sniffed,
	"text/plain": utf8Text,
	"text/csv":   utf8Text,
}

var (
	magicPDF  = []byte("%PDF-")
	magicZIP  = []byte("PK\x03\x04")
	magicOLE2 = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
)

// executableMagics are the starts of programs and scripts no declared type
// excuses: PE (MZ), ELF, Mach-O (32/64 bit, both byte orders, universal) and
// the shebang of a script.
var executableMagics = []struct {
	name  string
	magic []byte
}{
	{"PE executable", []byte("MZ")},
	{"ELF executable", []byte("\x7fELF")},
	{"Mach-O executable", []byte{0xFE, 0xED, 0xFA, 0xCE}},
	{"Mach-O executable", []byte{0xFE, 0xED, 0xFA, 0xCF}},
	{"Mach-O executable", []byte{0xCE, 0xFA, 0xED, 0xFE}},
	{"Mach-O executable", []byte{0xCF, 0xFA, 0xED, 0xFE}},
	{"Mach-O executable", []byte{0xCA, 0xFE, 0xBA, 0xBE}},
	{"script", []byte("#!")},
}

// errUnsupportedType is the refusal of a missing or non-whitelisted
// Content-Type (415).
var errUnsupportedType = errors.New("content type is not allowed for document uploads")

// mismatchError is the refusal of content that does not match its
// declared type (422 mime-mismatch).
type mismatchError struct{ detail string }

func (e *mismatchError) Error() string { return e.detail }

func mismatch(format string, args ...any) error {
	return &mismatchError{detail: fmt.Sprintf(format, args...)}
}

// declaredType returns the media type of a Content-Type header, lower case
// and without parameters, when it is on the whitelist; a missing or other
// type fails with errUnsupportedType.
func declaredType(header string) (string, error) {
	if strings.TrimSpace(header) == "" {
		return "", errUnsupportedType
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return "", errUnsupportedType
	}
	if !allowedMimeTypes[mediaType] {
		return mediaType, errUnsupportedType
	}
	return mediaType, nil
}

// checkHead checks the first bytes of an upload (at most sniffLen) against
// the declared, whitelisted type before the body is read completely:
// executables, scripts and HTML are refused whatever the declaration, and
// the signature of the declared type must match. The ZIP directory of
// OOXML and the UTF-8 of text types are checked by checkBody.
func checkHead(declared string, head []byte) error {
	for _, m := range executableMagics {
		if bytes.HasPrefix(head, m.magic) {
			return mismatch("the content is a %s, not %s", m.name, declared)
		}
	}
	detected := sniffType(head)
	if detected == "text/html" {
		return mismatch("the content is HTML, not %s", declared)
	}
	switch contentSignatures[declared] {
	case pdfMagic:
		if !bytes.HasPrefix(head, magicPDF) {
			return mismatch("the content is no PDF document (%s)", detected)
		}
	case zipMagic, ooxml:
		if !bytes.HasPrefix(head, magicZIP) {
			return mismatch("the content is no ZIP archive (%s)", detected)
		}
	case ole2:
		if !bytes.HasPrefix(head, magicOLE2) {
			return mismatch("the content is no compound document (%s)", detected)
		}
	case utf8Text:
		if detected != "text/plain" {
			return mismatch("the content is %s, not %s", detected, declared)
		}
	case sniffed:
		if detected == "text/xml" {
			detected = "application/xml"
		}
		if detected != declared {
			return mismatch("the content is %s, not %s", detected, declared)
		}
	}
	return nil
}

// checkBody checks the complete body where the first bytes do not suffice:
// an OOXML document is a ZIP archive with [Content_Types].xml, a text type
// is valid UTF-8 throughout.
func checkBody(declared string, body []byte) error {
	switch contentSignatures[declared] {
	case ooxml:
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return mismatch("the content is no readable ZIP archive, not %s", declared)
		}
		for _, f := range zr.File {
			if f.Name == "[Content_Types].xml" {
				return nil
			}
		}
		return mismatch("the ZIP archive has no [Content_Types].xml, it is no %s", declared)
	case utf8Text:
		if !utf8.Valid(body) {
			return mismatch("the content is no valid UTF-8 text, not %s", declared)
		}
	}
	return nil
}

// sniffType is the media type http.DetectContentType names, without
// parameters.
func sniffType(head []byte) string {
	detected, _, _ := strings.Cut(http.DetectContentType(head), ";")
	return strings.TrimSpace(detected)
}

// verifyUpload checks an upload with the declared Content-Type header
// (SEC-09): the type must be on the whitelist (errUnsupportedType), the
// first sniffLen bytes are checked before the body is read, then the whole
// body. It returns the verified type and the body; a body above maxBytes
// fails with *http.MaxBytesError, content that does not match its type
// with *mismatchError. The verified type is what is stored and handed to
// the blob store.
func verifyUpload(w http.ResponseWriter, r *http.Request, header string, maxBytes int64) (verified string, body []byte, err error) {
	declared, err := declaredType(header)
	if err != nil {
		return declared, nil, err
	}
	br := bufio.NewReaderSize(http.MaxBytesReader(w, r.Body, maxBytes), sniffLen)
	head, err := br.Peek(sniffLen)
	if err != nil && !errors.Is(err, io.EOF) {
		return declared, nil, err
	}
	if herr := checkHead(declared, head); herr != nil {
		return declared, nil, herr
	}
	var buf bytes.Buffer
	if _, err = buf.ReadFrom(br); err != nil {
		return declared, nil, err
	}
	body = buf.Bytes()
	if berr := checkBody(declared, body); berr != nil {
		return declared, nil, berr
	}
	return declared, body, nil
}
