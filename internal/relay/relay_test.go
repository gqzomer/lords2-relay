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
