package relay

// Helpers for the payload types in docs/protocol.md: u8, u16, u32 (little-endian) and str (u8 length + bytes).

import (
	"encoding/binary"
	"errors"
)

var errBadPayload = errors.New("bad payload")

// Writing: each helper appends one value to b and returns the longer slice, like the built-in append.

func appendU8(b []byte, v uint8) []byte   { return append(b, v) }
func appendU16(b []byte, v uint16) []byte { return binary.LittleEndian.AppendUint16(b, v) }
func appendU32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }

// appendStr appends a str. The caller keeps s at most 255 bytes; room names are at most 32.
func appendStr(b []byte, s string) []byte {
	b = append(b, byte(len(s)))
	return append(b, s...)
}

// payloadReader reads values from a payload, front to back.
//
// Reading past the end does not panic: the reader remembers the problem, returns zero values from then on,
// and done reports it. So a parser reads every field first and checks once at the end:
//
//	r := payloadReader{b: payload}
//	players, locked := r.u8(), r.u8()
//	if err := r.done(); err != nil { ... }
type payloadReader struct {
	b   []byte
	bad bool
}

// take returns the next n bytes, or nil (and marks the reader bad) if fewer are left.
func (r *payloadReader) take(n int) []byte {
	if r.bad || len(r.b) < n {
		r.bad = true
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

func (r *payloadReader) u8() uint8 {
	if v := r.take(1); v != nil {
		return v[0]
	}
	return 0
}

func (r *payloadReader) u16() uint16 {
	if v := r.take(2); v != nil {
		return binary.LittleEndian.Uint16(v)
	}
	return 0
}

func (r *payloadReader) u32() uint32 {
	if v := r.take(4); v != nil {
		return binary.LittleEndian.Uint32(v)
	}
	return 0
}

func (r *payloadReader) str() string {
	n := r.u8()
	return string(r.take(int(n)))
}

// done reports errBadPayload if a read ran past the end, or if bytes are left over.
func (r *payloadReader) done() error {
	if r.bad || len(r.b) != 0 {
		return errBadPayload
	}
	return nil
}
