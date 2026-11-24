// Package relay accepts TCP connections from dplayx.dll, pairs a joiner's
// connection with a new connection from the host, and copies bytes between them.
// The wire format is in docs/protocol.md.
package relay

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/gqzomer/lords2-relay/internal/lobby"
)

// Roles in the connection header (docs/protocol.md).
const (
	RoleControl    = 1 // host: stays open for the whole game, receives JOIN requests
	RoleHostData   = 2 // host: one per joiner, opened after a JOIN request
	RoleJoinerData = 3 // joiner: carries that joiner's game traffic
	RoleList       = 4 // anyone: The protocol added it after this file was written.
)

const (
	Magic          = "L2RL"
	Version        = 1
	HeaderSize     = 6
	MaxRooms       = 20
	MaxFrameLength = 1024
)

const (
	HOST   = 1
	HOSTED = 2
	UPDATE = 3
	JOIN   = 4
	PING   = 5
	PONG   = 6
	ERROR  = 7
	LIST   = 8
	ROOMS  = 9
)

const (
	ErrCodeVersion    = 1
	ErrCodeBadRequest = 2
	ErrCodeNoRoom     = 3
	ErrCodeRoomClosed = 4
	ErrCodeTimeout    = 5
	ErrCodeServerFull = 6
)

const (
	PingInterval = 15 * time.Second
	PongTimeout  = 15 * time.Second
	PairCodeSize = 16
	PairTimeout  = 10 * time.Second
)

// TODO: more constants from docs/protocol.md, so the code has no bare numbers:
//   - timings: 10 s for the header, PING every 15 s, 15 s to answer a PING, a pair_code lives 10 s

// TODO: frame helpers, used by control (role 1) and list (role 4) connections:
//   - small helpers for the payload types: u8, u16, u32 (little-endian) and str (u8 length + UTF-8 bytes)

func readFrame(conn net.Conn) (typ byte, payload []byte, err error) {

	var buf [2]byte
	_, err = io.ReadFull(conn, buf[:])

	if err != nil {
		return 0, nil, err
	}

	var framelen = binary.LittleEndian.Uint16(buf[:])

	if framelen == 0 || framelen > MaxFrameLength {
		return 0, nil, errors.New("frame too long")
	}

	payload = make([]byte, int(framelen))
	_, err = io.ReadFull(conn, payload[:])

	if err != nil {
		return 0, nil, err
	}

	return payload[0], payload[1:], nil
}

func writeFrame(conn net.Conn, typ byte, payload []byte) error {
	if 1+len(payload) > MaxFrameLength {
		return errors.New("payload too large")
	}

	total := 3 + len(payload) // 2 length bytes + 1 type byte + payload

	b := make([]byte, 0, total)

	b = binary.LittleEndian.AppendUint16(b, uint16(1+len(payload)))
	b = append(b, typ)
	b = append(b, payload...)

	_, err := conn.Write(b)
	return err
}

// hostConn is a room's control connection. Several goroutines write to it
// (the PING goroutine, handleControl itself, and joiners sending JOIN),
// so every write goes through send.
type hostConn struct {
	conn net.Conn
	mu   sync.Mutex
}

func (h *hostConn) send(typ byte, payload []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return writeFrame(h.conn, typ, payload)
}

// waitingJoiner is a joiner whose host has not connected its data connection yet.
type waitingJoiner struct {
	conn net.Conn
	done chan struct{} // closed once the game connection has ended
}

type Server struct {
	addr    string
	rooms   *lobby.Registry
	log     *slog.Logger
	mu      sync.Mutex                  // guards hosts and waiting
	hosts   map[uint32]*hostConn        // room id -> control connection
	waiting map[[16]byte]*waitingJoiner // pair_code -> joiner

	// TODO (later): connection counts per IP address, to answer error 6 (server full / too many connections)
}

func New(addr string, rooms *lobby.Registry, log *slog.Logger) *Server {
	return &Server{
		addr:    addr,
		rooms:   rooms,
		log:     log,
		hosts:   make(map[uint32]*hostConn),
		waiting: make(map[[16]byte]*waitingJoiner),
	}
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

	defer conn.Close()

	// A client gets a few seconds to send its header, so idle sockets cannot pile up.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	var buf [HeaderSize]byte // 6 empty bytes: [0 0 0 0 0 0]
	_, err := io.ReadFull(conn, buf[:])

	if err != nil {
		log.Warn("reading header", "err", err)
		return
	}

	bufMagic := string(buf[:4])
	bufVersion := buf[4]
	bufRole := buf[5]

	if bufMagic != Magic {
		log.Warn("bad header", "reason", "wrong magic", "got", bufMagic)
		return
	}

	if bufVersion != Version {
		log.Warn("bad header", "reason", "wrong version", "got", bufVersion)
		return
	}

	switch bufRole {
	case RoleList:
		s.handleList(conn, log)
	case RoleControl:
		s.handleControl(conn, log)
	case RoleHostData:
		s.handleHostData(conn, log)
	case RoleJoinerData:
		s.handleJoiner(conn, log)
	default:
		log.Warn("bad header", "reason", "invalid role", "got", bufRole)
		return
	}

	// TODO (docs/protocol.md has the details for each step):
	//   3. RoleList (4): read one LIST frame, answer one ROOMS frame (rooms.List(0), oldest first, at most 20), close.
	//   4. RoleControl (1): the first frame must be HOST: rooms.Create, answer HOSTED or ERROR. Then loop over frames:
	//      UPDATE -> rooms.Update, PONG -> note the time. Send PING every 15 s; no PONG within 15 s: close.
	//      When the connection closes, for any reason: rooms.Remove (a defer is a good fit).
	//   5. RoleJoinerData (3): read room_id (u32). Check the room exists, is not locked, is not full. Make a random
	//      16-byte pair_code (crypto/rand), send JOIN on the host's control connection, wait up to 10 s for the host.
	//   6. RoleHostData (2): read pair_code (16 bytes), find the waiting joiner, send status byte 0 to both, then
	//      pipe(hostConn, joinerConn). On failure send the non-zero error code as the status byte, then close.
	//   7. Clear the deadline (conn.SetReadDeadline(time.Time{})) once the header is read: control connections live
	//      for the whole game and piped game traffic must never time out.
}

func (s *Server) handleList(conn net.Conn, log *slog.Logger) {
	typ, payload, err := readFrame(conn)

	if err != nil {
		log.Warn("failed to read frame", "reason", err)
		return
	}

	if typ != LIST || len(payload) != 0 {
		log.Warn("failed to read frame", "reason", "unexpected type or payload")
		return
	}

	rooms := s.rooms.List(0)

	if len(rooms) > MaxRooms {
		rooms = rooms[:MaxRooms]
	}

	b := make([]byte, 0, 1+len(rooms)*44) // a room takes at most 44 bytes
	b = append(b, byte(len(rooms)))
	for _, room := range rooms {
		var locked byte
		if room.Locked {
			locked = 1
		}

		b = binary.LittleEndian.AppendUint32(b, room.ID)
		b = binary.LittleEndian.AppendUint32(b, room.Proto)
		b = append(b, byte(room.Players))
		b = append(b, byte(room.MaxPlayers))
		b = append(b, locked)
		b = append(b, byte(len(room.Name)))
		b = append(b, room.Name...)
	}

	err = writeFrame(conn, ROOMS, b)

	if err != nil {
		log.Warn("failed to write frame", "reason", err)
	}
}

func (s *Server) handleControl(conn net.Conn, log *slog.Logger) {
	typ, payload, err := readFrame(conn)

	if err != nil {
		log.Warn("failed to read frame", "reason", err)
		return
	}

	if typ != HOST || len(payload) < 6 {
		log.Warn("failed to read frame", "reason", "unexpected type or payload")
		_ = writeFrame(conn, ERROR, []byte{ErrCodeBadRequest})
		return
	}

	n := int(payload[5])

	if len(payload) != 6+n {
		log.Warn("failed to read frame", "reason", "unexpected payload size", "value", len(payload))
		_ = writeFrame(conn, ERROR, []byte{ErrCodeBadRequest})
		return
	}

	proto := binary.LittleEndian.Uint32(payload[:4])
	roomSize := int(payload[4])
	name := string(payload[6 : 6+n])

	room, err := s.rooms.Create(name, roomSize, proto)

	if err != nil {
		log.Warn("failed to create a room", "reason", err)
		_ = writeFrame(conn, ERROR, []byte{ErrCodeBadRequest})
		return
	}

	defer s.rooms.Remove(room.ID)

	hc := &hostConn{conn: conn}

	s.mu.Lock()
	s.hosts[room.ID] = hc
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.hosts, room.ID)
		s.mu.Unlock()
	}()

	err = writeFrame(conn, HOSTED, binary.LittleEndian.AppendUint32(nil, room.ID))

	if err != nil {
		log.Warn("failed to create a room", "reason", err)
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(PingInterval + PongTimeout))

	log.Info("room created", "id", room.ID, "name", name)

	// The PING goroutine. close(done) stops it when handleControl returns.
	done := make(chan struct{})
	defer close(done)

	go func() {
		ticker := time.NewTicker(PingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := hc.send(PING, nil); err != nil {
					conn.Close() // wakes up readFrame below, so handleControl returns
					return
				}
			case <-done:
				return
			}
		}
	}()

	for {
		typ, payload, err := readFrame(conn)
		if err != nil {
			log.Info("host disconnected", "reason", err)
			return
		}

		switch typ {
		case UPDATE:
			if len(payload) != 2 || payload[1] > 1 {
				log.Warn("bad UPDATE payload", "payload", payload)
				_ = hc.send(ERROR, []byte{ErrCodeBadRequest})
				return
			}

			err := s.rooms.Update(room.ID, int(payload[0]), payload[1] == 1)

			if err != nil {
				log.Warn("failed to update room", "reason", err)
				_ = hc.send(ERROR, []byte{ErrCodeBadRequest})
				return
			}

		case PONG:
			// The host is alive: give it until the next PING plus PongTimeout.
			_ = conn.SetReadDeadline(time.Now().Add(PingInterval + PongTimeout))

		default:
			log.Warn("unexpected frame type", "type", typ)
			_ = hc.send(ERROR, []byte{ErrCodeBadRequest})
			return
		}
	}
}

func (s *Server) handleHostData(conn net.Conn, log *slog.Logger) {
}

func (s *Server) handleJoiner(conn net.Conn, log *slog.Logger) {
	var buf [4]byte
	_, err := io.ReadFull(conn, buf[:])

	if err != nil {
		log.Warn("error reading room id", "reason", err)
		return
	}

	requestedRoom := binary.LittleEndian.Uint32(buf[:])

	room, err := s.rooms.Get(requestedRoom)

	// check if room exists
	if err != nil {
		log.Warn("joiner requested invalid room", "value", requestedRoom)
		_, _ = conn.Write([]byte{ErrCodeNoRoom})
		return
	}

	// check if room is full or locked
	if room.Locked || room.Players >= room.MaxPlayers {
		log.Warn("join failed", "reason", "room is either full or locked", "room", room.ID)
		_, _ = conn.Write([]byte{ErrCodeRoomClosed})
		return
	}

	s.mu.Lock()
	hc := s.hosts[room.ID]
	s.mu.Unlock()

	// check if host is still connected
	if hc == nil {
		log.Warn("join failed", "reason", "host has disconnected")
		_, _ = conn.Write([]byte{ErrCodeNoRoom})
		return
	}

	var code [PairCodeSize]byte
	rand.Read(code[:])

	w := &waitingJoiner{
		conn: conn,
		done: make(chan struct{}),
	}

	s.mu.Lock()
	s.waiting[code] = w
	s.mu.Unlock()

	err = hc.send(JOIN, code[:])

	if err != nil {
		log.Warn("join failed", "reason", err)
		_, _ = conn.Write([]byte{ErrCodeNoRoom})
		s.mu.Lock()
		delete(s.waiting, code)
		s.mu.Unlock()
		return
	}

	select {

	case <-w.done:
		return // game played and finished
	case <-time.After(PairTimeout):
		// host never showed up (handled below)
	}

	s.mu.Lock()
	if s.waiting[code] == w {
		// still ours: nobody claimed it
		delete(s.waiting, code)
		s.mu.Unlock()
		log.Info("join timed out", "room", room.ID)
		_, _ = conn.Write([]byte{ErrCodeTimeout})
		return
	}
	s.mu.Unlock()
	// the host claimed it at the last moment → wait for the game to end
	<-w.done

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
