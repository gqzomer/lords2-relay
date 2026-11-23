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

// TODO: tests for handle, one per step as it gets written. Start a Server on "127.0.0.1:0" (or call s.handle on the
// server side of tcpPair) and act as the client:
//   - header: wrong magic, wrong version (expect error 1), unknown role, too short -> connection closed
//   - frames: readFrame/writeFrame round trip; len 0 and len > 1024 rejected
//   - list: LIST -> ROOMS with the rooms from the registry, oldest first, at most 20
//   - control: HOST -> HOSTED and the room is in the registry; bad HOST -> ERROR 2; UPDATE changes the room;
//     closing the connection removes the room
//   - joining: joiner gets status 0 and the bytes reach the host; unknown room -> 3; locked or full room -> 4;
//     host never connects -> 5; a pair_code works only once

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
	for _, n := range []uint16{0, MaxFrameLength + 1} {
		client, server := tcpPair(t)
		_, _ = client.Write(binary.LittleEndian.AppendUint16(nil, n))
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
