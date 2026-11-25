// Package relay accepts TCP connections from dplayx.dll, pairs a joiner's
// connection with a new connection from the host, and copies bytes between them.
// The wire format is in docs/protocol.md.
//
// Log levels:
//   - Error: the server itself is broken (only in main: a listener stopped).
//   - Warn:  a client that speaks our protocol broke it: a bug in dplayx.dll or in this relay. Worth a look.
//   - Info:  the life of rooms and games, including normal refusals (room full, old version).
//   - Debug: noise: connections that are not ours (scanners), room list requests, UPDATEs.
//
// Every line of a connection has remote and role; once known also room, and pair (the first bytes of the
// pair_code, in hex), which links a joiner's lines to the host's data connection for that joiner.
package relay

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
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
	MaxFrameLength = 2048 // len of a frame (type + payload), at most: ROOMS with 20 rooms of 64-byte names is ~1.5 KB
	FrameLenSize   = 4    // len is a u32, as in dplayx.dll's own frames, so the DLL reuses its sendf / readf2
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
	StatusOK          = 0
	ErrCodeVersion    = 1
	ErrCodeBadRequest = 2
	ErrCodeNoRoom     = 3
	ErrCodeRoomClosed = 4
	ErrCodeTimeout    = 5
	ErrCodeServerFull = 6
)

const (
	HeaderTimeout = 10 * time.Second // to send the header and its role-specific part
	PingInterval  = 15 * time.Second
	PongTimeout   = 15 * time.Second
	PairCodeSize  = 16
	PairTimeout   = 10 * time.Second
)

func readFrame(conn net.Conn) (typ byte, payload []byte, err error) {

	var buf [FrameLenSize]byte
	_, err = io.ReadFull(conn, buf[:])

	if err != nil {
		return 0, nil, err
	}

	var framelen = binary.LittleEndian.Uint32(buf[:])

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

	total := FrameLenSize + 1 + len(payload) // length + type byte + payload

	b := make([]byte, 0, total)

	b = appendU32(b, uint32(1+len(payload)))
	b = appendU8(b, typ)
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
	room uint32        // for the log of the host data connection
	done chan struct{} // closed once the game connection has ended
}

type Server struct {
	addr    string
	rooms   *lobby.Registry
	log     *slog.Logger
	mu      sync.Mutex                  // guards hosts and waiting
	hosts   map[uint32]*hostConn        // room id -> control connection
	waiting map[[16]byte]*waitingJoiner // pair_code -> joiner
	idKey   [32]byte                    // random per run: the key for joinerID

	// TODO (later): connection counts per IP address, to answer error 6 (server full / too many connections)
}

func New(addr string, rooms *lobby.Registry, log *slog.Logger) *Server {
	s := &Server{
		addr:    addr,
		rooms:   rooms,
		log:     log,
		hosts:   make(map[uint32]*hostConn),
		waiting: make(map[[16]byte]*waitingJoiner),
	}
	rand.Read(s.idKey[:])
	return s
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
	_ = conn.SetReadDeadline(time.Now().Add(HeaderTimeout))

	var buf [HeaderSize]byte // 6 empty bytes: [0 0 0 0 0 0]
	_, err := io.ReadFull(conn, buf[:])

	if err != nil {
		log.Debug("no header", "err", err) // port scanners, health probes
		return
	}

	bufMagic := string(buf[:4])
	bufVersion := buf[4]
	bufRole := buf[5]

	if bufMagic != Magic {
		log.Debug("not our protocol", "magic", bufMagic) // slog quotes and escapes it
		return
	}

	log = log.With("role", roleName(bufRole))

	if bufVersion != Version {
		log.Info("refused: old or new client version", "version", bufVersion, "want", Version)
		// Tell the client to update, in the form its role expects. An unknown role gets no answer.
		switch bufRole {
		case RoleControl, RoleList:
			_ = writeFrame(conn, ERROR, []byte{ErrCodeVersion})
		case RoleHostData, RoleJoinerData:
			_, _ = conn.Write([]byte{ErrCodeVersion})
		}
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
		log.Warn("bad header: unknown role", "role_byte", bufRole)
	}
}

func roleName(role byte) string {
	switch role {
	case RoleControl:
		return "control"
	case RoleHostData:
		return "host-data"
	case RoleJoinerData:
		return "joiner"
	case RoleList:
		return "list"
	}
	return fmt.Sprintf("unknown(%d)", role)
}

// pairID is the first bytes of a pair_code, enough to link log lines without logging the secret itself.
func pairID(code [PairCodeSize]byte) string {
	return hex.EncodeToString(code[:4])
}

func (s *Server) handleList(conn net.Conn, log *slog.Logger) {
	typ, payload, err := readFrame(conn)

	if err != nil {
		log.Warn("reading LIST", "err", err)
		return
	}

	if typ != LIST || len(payload) != 0 {
		log.Warn("bad LIST frame", "type", typ, "payload", fmt.Sprintf("% x", payload))
		return
	}

	// Only rooms a player can still join: a locked room (game started) is left out. The HTTP list still shows it.
	rooms := make([]lobby.Room, 0, MaxRooms)
	for _, room := range s.rooms.List(0) {
		if !room.Locked && len(rooms) < MaxRooms {
			rooms = append(rooms, room)
		}
	}

	b := make([]byte, 0, 1+len(rooms)*(11+lobby.MaxNameLen)) // a room takes at most 11 + MaxNameLen bytes
	b = append(b, byte(len(rooms)))
	for _, room := range rooms {
		b = appendU32(b, room.ID)
		b = appendU32(b, room.Proto)
		b = appendU8(b, uint8(room.Players))
		b = appendU8(b, uint8(room.MaxPlayers))
		b = appendStr(b, room.Name)
	}

	err = writeFrame(conn, ROOMS, b)

	if err != nil {
		log.Info("sending ROOMS", "err", err)
		return
	}
	log.Debug("room list sent", "rooms", len(rooms))
}

func (s *Server) handleControl(conn net.Conn, log *slog.Logger) {
	typ, payload, err := readFrame(conn)

	if err != nil {
		log.Warn("reading HOST", "err", err)
		return
	}

	if typ != HOST {
		log.Warn("first frame is not HOST", "type", typ)
		_ = writeFrame(conn, ERROR, []byte{ErrCodeBadRequest})
		return
	}

	r := payloadReader{b: payload}
	proto := r.u32()
	roomSize := r.u8()
	name := r.str()

	if err := r.done(); err != nil {
		log.Warn("bad HOST payload", "payload", fmt.Sprintf("% x", payload))
		_ = writeFrame(conn, ERROR, []byte{ErrCodeBadRequest})
		return
	}

	// Out-of-range values are corrected, not refused: hosting must not fail over a detail.
	// An empty name is allowed (it becomes lobby.DefaultName); anything else that changed is a DLL bug.
	room := s.rooms.Create(name, int(roomSize), proto)
	log = log.With("room", room.ID)

	if (name != "" && room.Name != name) || room.MaxPlayers != int(roomSize) {
		log.Warn("HOST values corrected", "name", name, "stored_name", room.Name,
			"max_players", roomSize, "stored_max_players", room.MaxPlayers)
	}
	start := time.Now()
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

	err = writeFrame(conn, HOSTED, appendU32(nil, room.ID))

	if err != nil {
		log.Info("host left before HOSTED", "err", err)
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(PingInterval + PongTimeout))

	log.Info("room opened", "name", room.Name, "max_players", room.MaxPlayers, "proto", proto)

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
			up := time.Since(start).Round(time.Second)
			var ne net.Error
			switch {
			case errors.Is(err, io.EOF):
				log.Info("room closed by host", "up", up)
			case errors.As(err, &ne) && ne.Timeout():
				log.Warn("room closed: host stopped answering PING", "up", up)
			default:
				log.Info("room closed", "err", err, "up", up)
			}
			return
		}

		switch typ {
		case UPDATE:
			r := payloadReader{b: payload}
			players := r.u8()
			locked := r.u8()

			if err := r.done(); err != nil {
				log.Warn("bad UPDATE payload", "payload", fmt.Sprintf("% x", payload))
				_ = hc.send(ERROR, []byte{ErrCodeBadRequest})
				return
			}

			// Like HOST: values out of range are corrected (players into 1..max_players, locked: any non-zero
			// is locked) and logged, and the room stays.
			updated, err := s.rooms.Update(room.ID, int(players), locked != 0)

			if err != nil { // cannot happen while this connection is open: only handleControl removes its room
				log.Error("UPDATE: room missing from the registry", "err", err)
				return
			}
			if updated.Players != int(players) || locked > 1 {
				log.Warn("UPDATE values corrected", "players", players, "stored_players", updated.Players,
					"locked", locked)
			}
			log.Debug("room updated", "players", updated.Players, "locked", updated.Locked)

		case PONG:
			// The host is alive: give it until the next PING plus PongTimeout.
			_ = conn.SetReadDeadline(time.Now().Add(PingInterval + PongTimeout))

		default:
			log.Warn("unexpected frame on control connection", "type", typ)
			_ = hc.send(ERROR, []byte{ErrCodeBadRequest})
			return
		}
	}
}

func (s *Server) handleHostData(conn net.Conn, log *slog.Logger) {
	var code [PairCodeSize]byte
	_, err := io.ReadFull(conn, code[:])

	if err != nil {
		log.Warn("reading pair_code", "err", err)
		return
	}
	log = log.With("pair", pairID(code))

	s.mu.Lock()
	w := s.waiting[code]
	if w != nil {
		delete(s.waiting, code)
	}
	s.mu.Unlock()

	if w == nil {
		log.Warn("unknown or expired pair_code") // a host that answered JOIN too late, or a bad code
		_, _ = conn.Write([]byte{ErrCodeBadRequest})
		return
	}
	defer close(w.done)

	log = log.With("room", w.room, "joiner", w.conn.RemoteAddr().String())

	_ = conn.SetReadDeadline(time.Time{})
	_ = w.conn.SetReadDeadline(time.Time{})

	// The host gets a joiner id instead of the joiner's address (which it must never learn), so dplayx.dll's
	// per-address limits still tell players apart. The joiner gets the status byte alone.
	_, _ = conn.Write(append([]byte{StatusOK}, s.joinerID(w.conn)...))
	_, _ = w.conn.Write([]byte{StatusOK})

	log.Info("game connection started")
	start := time.Now()

	toJoiner, toHost := pipe(conn, w.conn)

	log.Info("game connection ended", "duration", time.Since(start).Round(time.Second),
		"bytes_to_joiner", toJoiner, "bytes_to_host", toHost)
}

func (s *Server) handleJoiner(conn net.Conn, log *slog.Logger) {
	var buf [4]byte
	_, err := io.ReadFull(conn, buf[:])

	if err != nil {
		log.Warn("reading room_id", "err", err)
		return
	}

	requestedRoom := binary.LittleEndian.Uint32(buf[:])
	log = log.With("room", requestedRoom)

	room, err := s.rooms.Get(requestedRoom)

	// check if room exists
	if err != nil {
		log.Info("join refused: no such room")
		_, _ = conn.Write([]byte{ErrCodeNoRoom})
		return
	}

	// check if room is full or locked
	if room.Locked || room.Players >= room.MaxPlayers {
		log.Info("join refused: room full or locked", "players", room.Players, "max_players", room.MaxPlayers,
			"locked", room.Locked)
		_, _ = conn.Write([]byte{ErrCodeRoomClosed})
		return
	}

	s.mu.Lock()
	hc := s.hosts[room.ID]
	s.mu.Unlock()

	// check if host is still connected
	if hc == nil {
		log.Info("join refused: host is disconnecting")
		_, _ = conn.Write([]byte{ErrCodeNoRoom})
		return
	}

	var code [PairCodeSize]byte
	rand.Read(code[:])

	log = log.With("pair", pairID(code))

	w := &waitingJoiner{
		conn: conn,
		room: room.ID,
		done: make(chan struct{}),
	}

	s.mu.Lock()
	s.waiting[code] = w
	s.mu.Unlock()

	err = hc.send(JOIN, code[:])

	if err != nil {
		log.Info("join refused: sending JOIN to the host failed", "err", err)
		_, _ = conn.Write([]byte{ErrCodeNoRoom})
		s.mu.Lock()
		delete(s.waiting, code)
		s.mu.Unlock()
		return
	}

	log.Info("join requested, JOIN sent to host")

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
		log.Warn("join timed out: host did not open its data connection", "after", PairTimeout)
		_, _ = conn.Write([]byte{ErrCodeTimeout})
		return
	}
	s.mu.Unlock()
	// the host claimed it at the last moment → wait for the game to end
	<-w.done

}

// joinerID returns 4 bytes that stand for c's remote IP address without revealing it: the start of an HMAC-SHA256
// of the address, keyed with idKey. The same address gets the same id for as long as the relay runs, so a host can
// count connections per player; without idKey nobody can turn an id back into an address (not even by trying all
// 2^32 IPv4 addresses), and a restart makes a new key, so ids from different runs cannot be linked.
func (s *Server) joinerID(c net.Conn) []byte {
	var addr []byte
	if a, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		addr = a.IP.To16() // one form for IPv4 and IPv4-mapped IPv6, so both give the same id
	} else {
		addr = []byte(c.RemoteAddr().String())
	}
	mac := hmac.New(sha256.New, s.idKey[:])
	mac.Write(addr)
	return mac.Sum(nil)[:4]
}

// pipe copies bytes both ways between two connections until either side closes,
// and returns how many bytes went each way (for the log).
// Finished helper: this is the whole "relay" part once two connections are paired.
func pipe(a, b net.Conn) (aToB, bToA int64) {
	noDelay(a) // small game messages go out at once (Nagle off), as dplayx.dll does
	noDelay(b)

	done := make(chan struct{})
	go func() {
		bToA, _ = io.Copy(a, b)
		closeWrite(a)
		close(done)
	}()
	aToB, _ = io.Copy(b, a)
	closeWrite(b)
	<-done
	a.Close()
	b.Close()
	return aToB, bToA
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
