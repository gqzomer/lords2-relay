package relay

import (
	"bytes"
	"testing"
)

// Values written with the append helpers must come back unchanged from payloadReader.
func TestPayloadRoundTrip(t *testing.T) {
	var b []byte
	b = appendU8(b, 0xAB)
	b = appendU16(b, 0x1234)
	b = appendU32(b, 0xDEADBEEF)
	b = appendStr(b, "Gersom")
	b = appendStr(b, "")

	// Little-endian: the low byte comes first.
	want := []byte{0xAB, 0x34, 0x12, 0xEF, 0xBE, 0xAD, 0xDE, 6, 'G', 'e', 'r', 's', 'o', 'm', 0}
	if !bytes.Equal(b, want) {
		t.Fatalf("bytes: got % x, want % x", b, want)
	}

	r := payloadReader{b: b}
	if v := r.u8(); v != 0xAB {
		t.Errorf("u8: got %#x", v)
	}
	if v := r.u16(); v != 0x1234 {
		t.Errorf("u16: got %#x", v)
	}
	if v := r.u32(); v != 0xDEADBEEF {
		t.Errorf("u32: got %#x", v)
	}
	if v := r.str(); v != "Gersom" {
		t.Errorf("str: got %q", v)
	}
	if v := r.str(); v != "" {
		t.Errorf("empty str: got %q", v)
	}
	if err := r.done(); err != nil {
		t.Errorf("done: %v", err)
	}
}

func TestPayloadReaderBad(t *testing.T) {
	tests := []struct {
		name string
		b    []byte
		read func(r *payloadReader)
	}{
		{"u32 from 3 bytes", []byte{1, 2, 3}, func(r *payloadReader) { r.u32() }},
		{"str longer than the payload", []byte{5, 'a', 'b'}, func(r *payloadReader) { r.str() }},
		{"str without its length byte", nil, func(r *payloadReader) { r.str() }},
		{"bytes left over", []byte{1, 2}, func(r *payloadReader) { r.u8() }},
		// After a short read every later read fails too, even one that would fit in the bytes left.
		{"read after a short read", []byte{1, 2, 3}, func(r *payloadReader) { r.u32(); r.u8() }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := payloadReader{b: tc.b}
			tc.read(&r) // must not panic
			if err := r.done(); err == nil {
				t.Error("done: want an error")
			}
		})
	}
}
