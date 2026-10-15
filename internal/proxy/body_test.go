package proxy

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"testing"
)

func header(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestBodyText(t *testing.T) {
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write([]byte(`{"message":"Column not found"}`))
	w.Close()
	for _, c := range []struct {
		name       string
		body       []byte
		header     http.Header
		cut        bool
		text, note string
	}{
		{"plain", []byte("hello"), header("Content-Type", "text/plain"), false, "hello", ""},
		{"json is indented", []byte(`{"a":1}`), header("Content-Type", "application/json; charset=utf-8"), false, "{\n  \"a\": 1\n}", ""},
		{"gzip is decoded", gz.Bytes(), header("Content-Type", "application/json", "Content-Encoding", "gzip"), false, "{\n  \"message\": \"Column not found\"\n}", ""},
		{"brotli is not", []byte{1, 2, 3}, header("Content-Encoding", "br"), false, "", "br-encoded, 3 bytes kept"},
		{"binary", []byte{0xff, 0xfe, 0}, header("Content-Type", "image/png"), false, "", "3 bytes of binary"},
		{"cut inside a rune", []byte("caf\xc3"), header(), true, "caf", "only the start was kept"},
		{"empty", nil, header(), false, "", ""},
	} {
		text, note := BodyText(c.body, c.header, c.cut)
		if text != c.text || note != c.note {
			t.Errorf("%s: got %q, %q; want %q, %q", c.name, text, note, c.text, c.note)
		}
	}
}
