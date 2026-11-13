package blob

import (
	"mime"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

// Download describes how a presigned GET URL delivers its object: always
// as an attachment (Content-Disposition: attachment), never rendered by the
// browser, so a polyglot file (e.g. a valid PDF with embedded HTML) cannot
// run on the origin of the frontend proxy (SEC-09, SEC-11, finding of
// WP-829). The zero value is a nameless application/octet-stream download.
type Download struct {
	// FileName is the suggested file name (RFC 6266, RFC 5987); a path is
	// cut to its last element. Empty: attachment without a name.
	FileName string
	// ContentType is the stored, verified type of the object. Empty,
	// unparsable or a type a browser executes (HTML, XHTML, SVG, scripts):
	// application/octet-stream.
	ContentType string
	// MaxAge shortens the validity of a signed URL below PresignTTL (the
	// download period of a full export ends earlier, WP-1151); zero or more
	// than PresignTTL: PresignTTL.
	MaxAge time.Duration
}

// expires is the validity of a signed URL of dl in whole seconds, at least
// one.
func (dl Download) expires() int64 {
	ttl := PresignTTL
	if dl.MaxAge > 0 && dl.MaxAge < ttl {
		ttl = dl.MaxAge
	}
	return max(int64(ttl/time.Second), 1)
}

// OctetStream is the type of a download without a verified type.
const OctetStream = "application/octet-stream"

// maxFileNameBytes bounds the suggested file name.
const maxFileNameBytes = 200

// activeTypes are types a browser renders as active content; they are
// never named as response-content-type.
var activeTypes = map[string]bool{
	"text/html":                 true,
	"application/xhtml+xml":     true,
	"image/svg+xml":             true,
	"text/javascript":           true,
	"application/javascript":    true,
	"application/ecmascript":    true,
	"text/ecmascript":           true,
	"text/xsl":                  true,
	"application/xslt+xml":      true,
	"multipart/x-mixed-replace": true,
}

// contentType returns the response-content-type of the download: the
// media type of ContentType with at most its charset, else
// application/octet-stream.
func (d Download) contentType() string {
	mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(d.ContentType))
	if err != nil || activeTypes[mediaType] || !strings.Contains(mediaType, "/") {
		return OctetStream
	}
	kept := map[string]string{}
	if charset, ok := params["charset"]; ok {
		kept["charset"] = charset
	}
	if formatted := mime.FormatMediaType(mediaType, kept); formatted != "" {
		return formatted
	}
	return OctetStream
}

// disposition returns the response-content-disposition of the download:
// attachment, with the file name as quoted ASCII fallback (filename) and,
// for a name beyond it, as UTF-8 ext-value (filename*, RFC 5987).
func (d Download) disposition() string {
	name := safeFileName(d.FileName)
	if name == "" {
		return "attachment"
	}
	fallback := asciiFileName(name)
	if fallback == name {
		return `attachment; filename="` + fallback + `"`
	}
	return `attachment; filename="` + fallback + `"; filename*=UTF-8''` + rfc5987Encode(name)
}

// safeFileName cuts name to its last path element, drops control
// characters and invalid UTF-8 and bounds its length; "." and ".." give no
// name.
func safeFileName(name string) string {
	name = strings.ToValidUTF8(name, "")
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(strings.TrimRight(name, "/"), "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == ' ' || r == ' ' || r == '/' {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	for len(name) > maxFileNameBytes {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	if name == "" || path.Clean(name) != name || name == "." || name == ".." {
		return ""
	}
	return name
}

// asciiFileName is the quoted-string fallback of name: printable ASCII
// without quote, backslash and percent sign; anything else becomes "_".
func asciiFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '%' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// rfc5987Encode percent-encodes name as ext-value (RFC 5987 attr-char).
func rfc5987Encode(name string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if isAttrChar(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}

// isAttrChar reports whether c is an attr-char of RFC 5987.
func isAttrChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}
