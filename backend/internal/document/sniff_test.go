package document

import (
	"archive/zip"
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The tests of this file cover SEC-09 (WP-268): the declared Content-Type
// of an upload must be on the whitelist and match the signature of the
// content; executables, scripts and HTML are refused whatever the
// declaration.

// zipArchive returns a ZIP archive with the named (empty) entries.
func zipArchive(t *testing.T, names ...string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		if _, err := zw.Create(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// verify runs verifyUpload over body with the declared Content-Type.
func verify(contentType, body string) (string, error) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	mimeType, _, err := verifyUpload(httptest.NewRecorder(), r, contentType, 1<<20)
	return mimeType, err
}

func TestVerifyUploadAcceptsWhitelistWithSignature(t *testing.T) {
	ole2 := string(magicOLE2) + strings.Repeat("\x00", 24)
	docx := zipArchive(t, "[Content_Types].xml", "word/document.xml")
	cases := map[string]string{
		"application/pdf":               "%PDF-1.7\n%\xe2\xe3\xcf\xd3\n",
		"image/png":                     "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR",
		"image/jpeg":                    "\xff\xd8\xff\xe0\x00\x10JFIF\x00",
		"image/gif":                     "GIF89a\x01\x00\x01\x00",
		"image/webp":                    "RIFF\x24\x00\x00\x00WEBPVP8 ",
		"application/zip":               zipArchive(t, "readme.txt"),
		"application/msword":            ole2,
		"application/vnd.ms-excel":      ole2,
		"application/vnd.ms-powerpoint": ole2,
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   docx,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         zipArchive(t, "[Content_Types].xml", "xl/workbook.xml"),
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": zipArchive(t, "[Content_Types].xml"),
		"application/xml":  `<?xml version="1.0" encoding="UTF-8"?><config/>`,
		"application/json": `{"name":"Grüße","list":[1,2]}`,
		"text/csv":         "name;room\nSwitch 1;Büro\n",
		"text/plain":       "Notiz mit Umlauten: äöü\n",
	}
	for declared, body := range cases {
		got, err := verify(declared+"; charset=utf-8", body)
		if err != nil || got != declared {
			t.Errorf("%s: verified %q, %v; want it accepted", declared, got, err)
		}
	}
	if len(cases) != len(allowedMimeTypes) {
		t.Errorf("cases cover %d of %d whitelisted types", len(cases), len(allowedMimeTypes))
	}
	for declared := range allowedMimeTypes {
		if _, ok := contentSignatures[declared]; !ok {
			t.Errorf("%s is whitelisted without a signature", declared)
		}
	}
}

func TestVerifyUploadRejectsMismatch(t *testing.T) {
	cases := []struct{ name, declared, body string }{
		{"PDF as image/png", "image/png", "%PDF-1.7\n"},
		{"HTML as text/plain", "text/plain", "<!DOCTYPE html><html><script>alert(1)</script></html>"},
		{"HTML as text/csv", "text/csv", "<html><body>x</body></html>"},
		{"PE as text/plain", "text/plain", "MZ\x90\x00\x03\x00\x00\x00"},
		{"PE as PDF", "application/pdf", "MZ%PDF-1.7"},
		{"ELF as text/plain", "text/plain", "\x7fELF\x02\x01\x01"},
		{"Mach-O as application/zip", "application/zip", "\xcf\xfa\xed\xfe\x07\x00\x00\x01"},
		{"shebang as text/plain", "text/plain", "#!/bin/sh\nrm -rf /\n"},
		{"%PDF without hyphen", "application/pdf", "%PDF1.7\n"},
		{"ZIP without [Content_Types].xml as docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			zipArchive(t, "word/document.xml")},
		{"truncated ZIP as xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "PK\x03\x04broken"},
		{"text as application/zip", "application/zip", "just text"},
		{"PDF as msword", "application/msword", "%PDF-1.7\n"},
		{"invalid UTF-8 as text/plain", "text/plain", "plain \xff\xfe text"},
		{"PNG as application/json", "application/json", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"},
		{"plain text as application/xml", "application/xml", "no markup"},
		{"GIF as image/jpeg", "image/jpeg", "GIF89a\x01\x00\x01\x00"},
	}
	for _, c := range cases {
		_, err := verify(c.declared, c.body)
		var mm *mismatchError
		if !errors.As(err, &mm) {
			t.Errorf("%s: %v, want a mismatch", c.name, err)
		}
	}
}

func TestVerifyUploadRejectsUnsupportedType(t *testing.T) {
	for _, declared := range []string{"", "application/octet-stream", "text/html", "image/svg+xml", "application/javascript", ";;"} {
		if _, err := verify(declared, "%PDF-1.7"); !errors.Is(err, errUnsupportedType) {
			t.Errorf("%q: %v, want errUnsupportedType", declared, err)
		}
	}
}

func TestVerifyUploadLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 600)))
	_, _, err := verifyUpload(httptest.NewRecorder(), r, "text/plain", 100)
	var maxErr *http.MaxBytesError
	if !errors.As(err, &maxErr) {
		t.Fatalf("body above the limit: %v, want *http.MaxBytesError", err)
	}
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 600)))
	if _, body, verr := verifyUpload(httptest.NewRecorder(), r, "text/plain", 600); verr != nil || len(body) != 600 {
		t.Fatalf("body at the limit: %d bytes, %v", len(body), verr)
	}
}
