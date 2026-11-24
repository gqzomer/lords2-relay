package relay

// Tests for Server.handle: each test plays the client side of the protocol in docs/protocol.md.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/gqzomer/lords2-relay/internal/lobby"
)

// newTestServer returns a Server with an empty registry and a logger that discards everything.
// The Server never listens: dial hands it connections directly.
func newTestServer() *Server {
	return New("", lobby.NewRegistry(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// dial connects a client to s.handle and sends the header plus extra (the role-specific part).
func dial(t *testing.T, s *Server, version, role byte, extra []byte) net.Conn {
	t.Helper()
	client, server := tcpPair(t)
	t.Cleanup(func() { client.Close() })
	go s.handle(server)

	msg := append([]byte(Magic), version, role)
	if _, err := client.Write(append(msg, extra...)); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	return client
}

// hostPayload builds a HOST payload: proto u32, max_players u8, name str.
func hostPayload(maxPlayers byte, name string) []byte {
	b := binary.LittleEndian.AppendUint32(nil, 1)
	b = append(b, maxPlayers, byte(len(name)))
	return append(b, name...)
}

// startHost opens a control connection, sends HOST and returns the connection and the new room id.
func startHost(t *testing.T, s *Server, maxPlayers byte) (net.Conn, uint32) {
	t.Helper()
	ctl := dial(t, s, Version, RoleControl, nil)
	if err := writeFrame(ctl, HOST, hostPayload(maxPlayers, "Gersom")); err != nil {
		t.Fatal(err)
	}
	typ, payload := mustReadFrame(t, ctl)
	if typ != HOSTED || len(payload) != 4 {
		t.Fatalf("want HOSTED with a u32, got type %d payload % x", typ, payload)
	}
	return ctl, binary.LittleEndian.Uint32(payload)
}

func mustReadFrame(t *testing.T, c net.Conn) (byte, []byte) {
	t.Helper()
	typ, payload, err := readFrame(c)
	if err != nil {
		t.Fatalf("reading frame: %v", err)
	}
	return typ, payload
}

// readStatus reads the single status byte of a data connection.
func readStatus(t *testing.T, c net.Conn) byte {
	t.Helper()
	var b [1]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		t.Fatalf("reading status byte: %v", err)
	}
	return b[0]
}

// expectError reads an ERROR frame with the given code, then expects the relay to close.
func expectError(t *testing.T, c net.Conn, code byte) {
	t.Helper()
	typ, payload := mustReadFrame(t, c)
	if typ != ERROR || !bytes.Equal(payload, []byte{code}) {
		t.Fatalf("want ERROR %d, got type %d payload % x", code, typ, payload)
	}
	expectClosed(t, c)
}

// expectClosed checks that the relay closes the connection without sending anything (more).
func expectClosed(t *testing.T, c net.Conn) {
	t.Helper()
	var b [1]byte
	n, err := c.Read(b[:])
	if n > 0 {
		t.Fatalf("want the connection closed, got byte %#x", b[0])
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatal("want the connection closed, but it is still open")
	}
}

// eventually polls cond until it is true, for things the relay does without answering (UPDATE, cleanup).
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting until %s", what)
}

// pair runs the whole join: joiner connects, host gets JOIN, host opens its data connection.
// It returns both data connections (status bytes already checked) and the pair code.
func pair(t *testing.T, s *Server, ctl net.Conn, room uint32) (hostData, joiner net.Conn, code []byte) {
	t.Helper()
	joiner = dial(t, s, Version, RoleJoinerData, binary.LittleEndian.AppendUint32(nil, room))

	typ, code := mustReadFrame(t, ctl)
	if typ != JOIN || len(code) != PairCodeSize {
		t.Fatalf("want JOIN with a %d-byte code, got type %d payload % x", PairCodeSize, typ, code)
	}

	hostData = dial(t, s, Version, RoleHostData, code)
	if st := readStatus(t, hostData); st != StatusOK {
		t.Fatalf("host data status: got %d, want 0", st)
	}
	if st := readStatus(t, joiner); st != StatusOK {
		t.Fatalf("joiner status: got %d, want 0", st)
	}
	return hostData, joiner, code
}

// --- header ---

func TestHeaderRejected(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"wrong magic", []byte("XXXX\x01\x04")},
		{"unknown role", []byte("L2RL\x01\x09")},
		{"too short", []byte("L2R")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, server := tcpPair(t)
			defer client.Close()
			go newTestServer().handle(server)

			_, _ = client.Write(tc.data)
			_ = client.(*net.TCPConn).CloseWrite() // "too short": the relay sees EOF instead of waiting 10 s
			_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
			expectClosed(t, client)
		})
	}
}

func TestWrongVersion(t *testing.T) {
	s := newTestServer()

	for _, role := range []byte{RoleControl, RoleList} {
		c := dial(t, s, Version+1, role, nil)
		expectError(t, c, ErrCodeVersion)
	}
	for _, role := range []byte{RoleHostData, RoleJoinerData} {
		c := dial(t, s, Version+1, role, nil)
		if st := readStatus(t, c); st != ErrCodeVersion {
			t.Errorf("role %d: status got %d, want %d", role, st, ErrCodeVersion)
		}
		expectClosed(t, c)
	}
}

// --- list ---

func TestList(t *testing.T) {
	s := newTestServer()
	for i := 0; i < MaxRooms+2; i++ {
		if _, err := s.rooms.Create(string(rune('a'+i)), 4, 7); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond) // distinct Created times, so "oldest first" is well defined
	}

	c := dial(t, s, Version, RoleList, nil)
	if err := writeFrame(c, LIST, nil); err != nil {
		t.Fatal(err)
	}
	typ, p := mustReadFrame(t, c)
	if typ != ROOMS {
		t.Fatalf("want ROOMS, got type %d", typ)
	}
	if int(p[0]) != MaxRooms {
		t.Fatalf("count: got %d, want %d", p[0], MaxRooms)
	}

	// First room: the oldest one, "a". Layout: id u32, proto u32, players, max_players, locked, name str.
	r := p[1:]
	if got := binary.LittleEndian.Uint32(r[4:8]); got != 7 {
		t.Errorf("proto: got %d, want 7", got)
	}
	if r[8] != 1 || r[9] != 4 || r[10] != 0 {
		t.Errorf("players/max/locked: got %d/%d/%d, want 1/4/0", r[8], r[9], r[10])
	}
	if name := string(r[12 : 12+r[11]]); name != "a" {
		t.Errorf("first room: got %q, want the oldest, %q", name, "a")
	}
	expectClosed(t, c)
}

func TestListBadRequest(t *testing.T) {
	c := dial(t, newTestServer(), Version, RoleList, nil)
	_ = writeFrame(c, HOST, nil) // anything but LIST
	expectClosed(t, c)
}

// --- control ---

func TestControlRoomLifecycle(t *testing.T) {
	s := newTestServer()
	ctl, id := startHost(t, s, 4)

	room, err := s.rooms.Get(id)
	if err != nil {
		t.Fatalf("room %d not in the registry after HOSTED", id)
	}
	if room.Name != "Gersom" || room.MaxPlayers != 4 || room.Proto != 1 {
		t.Errorf("room: got %+v", room)
	}

	// UPDATE has no answer, so wait until the registry shows it.
	if err := writeFrame(ctl, UPDATE, []byte{3, 1}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "UPDATE is applied", func() bool {
		r, _ := s.rooms.Get(id)
		return r.Players == 3 && r.Locked
	})

	_ = writeFrame(ctl, PONG, nil) // an unasked PONG is fine

	ctl.Close()
	eventually(t, "the room is removed", func() bool {
		_, err := s.rooms.Get(id)
		return err != nil
	})
}

func TestControlBadHost(t *testing.T) {
	tests := []struct {
		name    string
		typ     byte
		payload []byte
	}{
		{"first frame is not HOST", UPDATE, []byte{1, 0}},
		{"payload too short", HOST, []byte{1, 0, 0}},
		{"name length does not match", HOST, append(hostPayload(4, "abc"), 'x')},
		{"empty name", HOST, hostPayload(4, "")},
		{"max_players too high", HOST, hostPayload(6, "abc")},
		{"max_players too low", HOST, hostPayload(1, "abc")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer()
			c := dial(t, s, Version, RoleControl, nil)
			if err := writeFrame(c, tc.typ, tc.payload); err != nil {
				t.Fatal(err)
			}
			expectError(t, c, ErrCodeBadRequest)
			if n := len(s.rooms.List(0)); n != 0 {
				t.Errorf("registry has %d rooms, want 0", n)
			}
		})
	}
}

func TestControlBadFrameAfterHost(t *testing.T) {
	tests := []struct {
		name    string
		typ     byte
		payload []byte
	}{
		{"UPDATE locked not 0 or 1", UPDATE, []byte{1, 2}},
		{"UPDATE more players than max", UPDATE, []byte{5, 0}},
		{"UPDATE zero players", UPDATE, []byte{0, 0}},
		{"UPDATE too short", UPDATE, []byte{1}},
		{"frame type the relay sends", JOIN, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer()
			ctl, id := startHost(t, s, 4)
			if err := writeFrame(ctl, tc.typ, tc.payload); err != nil {
				t.Fatal(err)
			}
			expectError(t, ctl, ErrCodeBadRequest)
			eventually(t, "the room is removed", func() bool {
				_, err := s.rooms.Get(id)
				return err != nil
			})
		})
	}
}

// --- joining ---

func TestJoin(t *testing.T) {
	s := newTestServer()
	ctl, room := startHost(t, s, 4)
	hostData, joiner, code := pair(t, s, ctl, room)

	// Bytes go both ways, unchanged.
	check := func(from, to net.Conn, msg string) {
		t.Helper()
		if _, err := from.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(to, buf); err != nil {
			t.Fatal(err)
		}
		if string(buf) != msg {
			t.Fatalf("got %q, want %q", buf, msg)
		}
	}
	check(joiner, hostData, "hello host")
	check(hostData, joiner, "hello joiner")

	// The pair code works only once.
	again := dial(t, s, Version, RoleHostData, code)
	if st := readStatus(t, again); st != ErrCodeBadRequest {
		t.Errorf("reused pair code: status got %d, want %d", st, ErrCodeBadRequest)
	}

	// A paired game outlives the control connection (docs/protocol.md, Control step 4).
	ctl.Close()
	eventually(t, "the room is removed", func() bool {
		_, err := s.rooms.Get(room)
		return err != nil
	})
	check(joiner, hostData, "still here")

	// The joiner leaves: the host's data connection ends cleanly.
	joiner.Close()
	if _, err := hostData.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("host data after joiner left: got %v, want EOF", err)
	}
}

// A game connection must not hit the 10 s header deadline from handle.
func TestJoinNoDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("takes 11 s")
	}
	s := newTestServer()
	ctl, room := startHost(t, s, 4)
	hostData, joiner, _ := pair(t, s, ctl, room)

	time.Sleep(11 * time.Second)

	// Both directions: a read deadline left on either relay-side conn only breaks the direction read from it.
	for _, c := range []struct {
		name     string
		from, to net.Conn
	}{
		{"joiner -> host", joiner, hostData},
		{"host -> joiner", hostData, joiner},
	} {
		_ = c.to.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.from.Write([]byte("x")); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if _, err := io.ReadFull(c.to, make([]byte, 1)); err != nil {
			t.Fatalf("%s: game connection died after 11 s: %v", c.name, err)
		}
	}
}

func TestJoinRejected(t *testing.T) {
	s := newTestServer()

	locked, lockedID := startHost(t, s, 4)
	_ = writeFrame(locked, UPDATE, []byte{1, 1})

	full, fullID := startHost(t, s, 2)
	_ = writeFrame(full, UPDATE, []byte{2, 0})

	eventually(t, "the UPDATEs are applied", func() bool {
		l, _ := s.rooms.Get(lockedID)
		f, _ := s.rooms.Get(fullID)
		return l.Locked && f.Players == 2
	})

	tests := []struct {
		name string
		room uint32
		want byte
	}{
		{"unknown room", 0, ErrCodeNoRoom},
		{"locked room", lockedID, ErrCodeRoomClosed},
		{"full room", fullID, ErrCodeRoomClosed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := dial(t, s, Version, RoleJoinerData, binary.LittleEndian.AppendUint32(nil, tc.room))
			if st := readStatus(t, j); st != tc.want {
				t.Errorf("status: got %d, want %d", st, tc.want)
			}
			expectClosed(t, j)
		})
	}
}

// A host data connection with a code nobody is waiting for gets status 2, and the relay keeps running.
func TestHostDataUnknownCode(t *testing.T) {
	s := newTestServer()
	c := dial(t, s, Version, RoleHostData, bytes.Repeat([]byte{0xAA}, PairCodeSize))
	if st := readStatus(t, c); st != ErrCodeBadRequest {
		t.Errorf("status: got %d, want %d", st, ErrCodeBadRequest)
	}
	expectClosed(t, c)
}

func TestJoinTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("takes PairTimeout (10 s)")
	}
	s := newTestServer()
	ctl, room := startHost(t, s, 4)

	j := dial(t, s, Version, RoleJoinerData, binary.LittleEndian.AppendUint32(nil, room))
	typ, code := mustReadFrame(t, ctl)
	if typ != JOIN {
		t.Fatalf("want JOIN, got type %d", typ)
	}

	// The host ignores the JOIN.
	_ = j.SetReadDeadline(time.Now().Add(PairTimeout + 3*time.Second))
	if st := readStatus(t, j); st != ErrCodeTimeout {
		t.Errorf("status: got %d, want %d", st, ErrCodeTimeout)
	}

	// The expired code no longer works.
	late := dial(t, s, Version, RoleHostData, code)
	if st := readStatus(t, late); st != ErrCodeBadRequest {
		t.Errorf("late host data: status got %d, want %d", st, ErrCodeBadRequest)
	}
}
