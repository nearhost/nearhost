package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"net/http"
	"strings"
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

// HTTP's deflate is zlib-wrapped; raw DEFLATE, which some servers send, is
// read too.
func TestBodyTextDeflate(t *testing.T) {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write([]byte("wrapped"))
	zw.Close()
	if text, note := BodyText(z.Bytes(), header("Content-Encoding", "deflate"), false); text != "wrapped" || note != "" {
		t.Errorf("zlib deflate: %q, %q", text, note)
	}
	var raw bytes.Buffer
	fw, _ := flate.NewWriter(&raw, flate.DefaultCompression)
	fw.Write([]byte("raw"))
	fw.Close()
	if text, note := BodyText(raw.Bytes(), header("Content-Encoding", "deflate"), false); text != "raw" || note != "" {
		t.Errorf("raw deflate: %q, %q", text, note)
	}
}

// A compressed body that decodes to more than is read, or that ends early,
// says only its start is shown.
func TestBodyTextDecodedInPart(t *testing.T) {
	defer func(n int) { captureBodyMax = n }(captureBodyMax)
	captureBodyMax = 16 // so at most 64 decoded bytes are read
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write([]byte(strings.Repeat("a", 200)))
	w.Close()
	if text, note := BodyText(gz.Bytes(), header("Content-Encoding", "gzip"), false); len(text) != 64 || note != "only the start was kept" {
		t.Errorf("long body: %d bytes, %q", len(text), note)
	}
	half := gz.Bytes()[:len(gz.Bytes())/2]
	if _, note := BodyText(half, header("Content-Encoding", "gzip"), false); note != "only the start was kept" {
		t.Errorf("body ending early: %q", note)
	}
}
