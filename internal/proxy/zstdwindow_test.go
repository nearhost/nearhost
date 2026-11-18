package proxy

import "testing"

// zstdRawFrame builds a zstd frame holding payload in one raw block, with a
// window descriptor byte of wd. The frame header declares the window size
// 2^(10+wd>>3) bytes before any content.
func zstdRawFrame(wd byte, payload string) []byte {
	n := len(payload)
	frame := []byte{0x28, 0xB5, 0x2F, 0xFD, 0x00, wd}
	// Block header: last block, raw type, size n.
	h := 1 | n<<3
	frame = append(frame, byte(h), byte(h>>8), byte(h>>16))
	return append(frame, payload...)
}

// TestBodyTextZstdWindowCap checks that a frame declaring a 64 MiB window is
// refused before the decoder allocates it, and shows as undecoded, while a
// frame with a small window still decodes.
func TestBodyTextZstdWindowCap(t *testing.T) {
	h := header("Content-Encoding", "zstd", "Content-Type", "text/plain")

	// Window descriptor 0x80: exponent 16, so 2^26 bytes = 64 MiB.
	text, note := BodyText(zstdRawFrame(0x80, "hello"), h, false)
	if text != "" || note != "zstd-encoded, 14 bytes kept" {
		t.Errorf("64 MiB window = %q, %q; want an undecoded note", text, note)
	}

	// Window descriptor 0x50: exponent 10, so 1 MiB.
	text, note = BodyText(zstdRawFrame(0x50, "hello"), h, false)
	if text != "hello" || note != "" {
		t.Errorf("1 MiB window = %q, %q; want hello", text, note)
	}

	// The limit itself, 8 MiB (exponent 13), still decodes.
	text, _ = BodyText(zstdRawFrame(0x68, "hello"), h, false)
	if text != "hello" {
		t.Errorf("8 MiB window = %q; want hello", text)
	}
}
