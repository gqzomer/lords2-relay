package relay

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// tcpPair returns two connected TCP sockets on localhost: client and server side.
func tcpPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return client, <-accepted
}

// A frame written with writeFrame must come back unchanged from readFrame.
func TestFrameRoundTrip(t *testing.T) {
	client, server := tcpPair(t)
	defer client.Close()
	defer server.Close()

	tests := []struct {
		name    string
		typ     byte
		payload []byte
	}{
		{"empty payload", LIST, nil},
		{"small payload", UPDATE, []byte{1, 2, 3}},
		{"largest payload", ROOMS, bytes.Repeat([]byte{0xAB}, MaxFrameLength-1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := writeFrame(client, tc.typ, tc.payload); err != nil {
				t.Fatal(err)
			}
			_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
			typ, payload, err := readFrame(server)
			if err != nil {
				t.Fatal(err)
			}
			if typ != tc.typ {
				t.Errorf("type: got %d, want %d", typ, tc.typ)
			}
			if !bytes.Equal(payload, tc.payload) {
				t.Errorf("payload: got % x, want % x", payload, tc.payload)
			}
		})
	}
}

// writeFrame must refuse a payload that would make len larger than MaxFrameLength.
func TestWriteFrameTooLarge(t *testing.T) {
	client, server := tcpPair(t)
	defer client.Close()
	defer server.Close()

	if err := writeFrame(client, ROOMS, make([]byte, MaxFrameLength)); err == nil {
		t.Fatal("expected an error for a payload of MaxFrameLength bytes")
	}
}

// readFrame must reject len 0 and len > MaxFrameLength.
func TestReadFrameBadLength(t *testing.T) {
	for _, n := range []uint32{0, MaxFrameLength + 1, 0xFFFFFFFF} {
		client, server := tcpPair(t)
		_, _ = client.Write(binary.LittleEndian.AppendUint32(nil, n))
		_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := readFrame(server); err == nil {
			t.Errorf("len %d: expected an error", n)
		}
		client.Close()
		server.Close()
	}
}

// The relay's pipe must pass bytes both ways, unchanged.
func TestPipe(t *testing.T) {
	host, hostSide := tcpPair(t)
	joiner, joinerSide := tcpPair(t)
	go pipe(hostSide, joinerSide)

	check := func(from, to net.Conn, msg string) {
		t.Helper()
		if _, err := from.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		_ = to.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(to, buf); err != nil {
			t.Fatal(err)
		}
		if string(buf) != msg {
			t.Fatalf("got %q, want %q", buf, msg)
		}
	}

	check(joiner, host, "hello host")
	check(host, joiner, "hello joiner")

	host.Close()
	joiner.Close()
}
