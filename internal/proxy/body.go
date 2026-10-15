package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
)

// BodyText is a kept body as text to read: decoded when it was sent
// compressed with gzip, deflate or zstd, and indented when it is JSON. note
// says why there is no text (binary, or an encoding that is not decoded) or
// that only part of the body was kept; text is empty with a note alone.
func BodyText(body []byte, header http.Header, cut bool) (text, note string) {
	if len(body) == 0 {
		return "", ""
	}
	raw := body
	switch enc := strings.ToLower(strings.TrimSpace(header.Get("Content-Encoding"))); enc {
	case "", "identity":
	case "gzip", "x-gzip", "deflate", "zstd":
		decoded, err := decode(enc, body)
		if err != nil && len(decoded) == 0 {
			return "", fmt.Sprintf("%s-encoded, %d bytes kept", enc, len(raw))
		}
		body = decoded
	default:
		return "", fmt.Sprintf("%s-encoded, %d bytes kept", enc, len(raw))
	}
	if !utf8.Valid(body) && !(cut && utf8.Valid(trimPartialRune(body))) {
		return "", fmt.Sprintf("%d bytes of binary", len(body))
	}
	if cut {
		note = "only the start was kept"
		return string(trimPartialRune(body)), note
	}
	if mt, _, _ := mime.ParseMediaType(header.Get("Content-Type")); mt == "application/json" || strings.HasSuffix(mt, "+json") {
		var out bytes.Buffer
		if json.Indent(&out, body, "", "  ") == nil {
			return out.String(), ""
		}
	}
	return string(body), ""
}

// decode undoes a content encoding, as far as the bytes allow: a body cut
// short decodes up to where it was cut.
func decode(enc string, body []byte) ([]byte, error) {
	var r io.Reader
	switch enc {
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r = zr
	case "deflate":
		r = flate.NewReader(bytes.NewReader(body))
	case "zstd":
		zr, err := zstd.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		r = zr
	}
	return io.ReadAll(io.LimitReader(r, int64(captureBodyMax)*4))
}

// trimPartialRune drops a rune cut in half at the end of b.
func trimPartialRune(b []byte) []byte {
	for i := 0; i < utf8.UTFMax && len(b) > 0; i++ {
		if utf8.Valid(b) {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}
