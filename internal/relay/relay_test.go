package relay

import (
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
