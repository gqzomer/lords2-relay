// Package relay accepts TCP connections from dplayx.dll, pairs a joiner's
// connection with a new connection from the host, and copies bytes between them.
// The wire format is in docs/protocol.md.
package relay

import (
	"context"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/gqzomer/lords2-relay/internal/lobby"
)

// Roles in the connection header (docs/protocol.md).
const (
	RoleControl    = 1 // host: stays open for the whole game, receives JOIN requests
	RoleHostData   = 2 // host: one per joiner, opened after a JOIN request
	RoleJoinerData = 3 // joiner: carries that joiner's game traffic
)

type Server struct {
	addr  string
	rooms *lobby.Registry
	log   *slog.Logger
}

func New(addr string, rooms *lobby.Registry, log *slog.Logger) *Server {
	return &Server{addr: addr, rooms: rooms, log: log}
}

// ListenAndServe accepts connections until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		ln.Close() // makes Accept return, ending the loop below
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // normal shutdown
			}
			return err
		}
		go s.handle(conn) // one goroutine per connection
	}
}

func (s *Server) handle(conn net.Conn) {
	log := s.log.With("remote", conn.RemoteAddr().String())

	// A client gets a few seconds to send its header, so idle sockets cannot pile up.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// TODO:
	//   1. read the fixed-size header (magic, version, role, token) with io.ReadFull
	//   2. RoleControl:    check the host token, keep the conn, send JOIN frames when joiners arrive
	//   3. RoleJoinerData: look up the pairing code, send JOIN to that room's control conn, wait for the host
	//   4. RoleHostData:   match the pairing code to the waiting joiner, then pipe(hostConn, joinerConn)
	//   5. clear the deadline (conn.SetReadDeadline(time.Time{})) before piping game traffic
	log.Info("connection accepted, relay logic not written yet")
	conn.Close()
}

// pipe copies bytes both ways between two connections until either side closes.
// Finished helper: this is the whole "relay" part once two connections are paired.
func pipe(a, b net.Conn) {
	noDelay(a) // small game messages go out at once (Nagle off), as dplayx.dll does
	noDelay(b)

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(a, b)
		closeWrite(a)
		close(done)
	}()
	_, _ = io.Copy(b, a)
	closeWrite(b)
	<-done
	a.Close()
	b.Close()
}

func noDelay(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
}

// closeWrite tells the other side "no more data", so its read ends cleanly.
func closeWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
		return
	}
	c.Close()
}
