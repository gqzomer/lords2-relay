// Package lobby keeps the list of open rooms, in memory only.
//
// The relay calls Create when a host sends HOST on its control connection, Update on UPDATE,
// and Remove when the control connection closes (docs/protocol.md). The HTTP handler only reads.
//
// Create and Update never refuse a value: they bring it into range and return what they stored. A slightly wrong
// value from dplayx.dll (0 players before the host's own player exists, a room size of 16) must not take the room
// down; the relay compares the result with what it was sent and logs a warning when they differ.
package lobby

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Limits from docs/protocol.md.
const (
	MinPlayers = 2
	MaxPlayers = 5  // the game has five nobles
	MaxNameLen = 64 // bytes, not characters: "é" is 2 bytes in UTF-8. dplayx.dll's own handshake carries 64, so a
	// name that works by address works here too (Jen's Mod Menu appends a 38-byte version marker to the name)

	DefaultName = "Lords2 game" // for a host that sends an empty name
)

// ErrNoRoom is returned for a room id that is not (or no longer) in the registry.
// Callers compare with errors.Is(err, ErrNoRoom); the relay turns it into error code 3.
var ErrNoRoom = errors.New("no such room")

// Room is one open game. The json tags name the fields in the HTTP room list.
type Room struct {
	ID         uint32    `json:"id"`
	Name       string    `json:"name"`
	Players    int       `json:"players"`
	MaxPlayers int       `json:"max_players"`
	Proto      uint32    `json:"proto"` // dplayx.dll PROTO: only matching builds can play together
	Locked     bool      `json:"locked"`
	Created    time.Time `json:"created"`
}

// Registry is safe to use from many goroutines at once: every method takes mu.
type Registry struct {
	mu    sync.Mutex
	rooms map[uint32]*Room
}

func NewRegistry() *Registry {
	return &Registry{rooms: make(map[uint32]*Room)}
}

// List returns a copy of the open rooms for one PROTO (0 = all), oldest first.
// Worked example: the other methods follow the same lock-then-work pattern.
func (r *Registry) List(proto uint32) []Room {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Room, 0, len(r.rooms))
	for _, room := range r.rooms {
		if proto == 0 || room.Proto == proto {
			out = append(out, *room) // a copy, so callers cannot change the registry
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// Create adds a room for a new host and returns a copy of it, with the name and size as stored (see CleanName and
// ClampRoomSize).
func (r *Registry) Create(name string, roomSize int, proto uint32) Room {
	r.mu.Lock()
	defer r.mu.Unlock()

	var id uint32

	for id == 0 || r.rooms[id] != nil {
		var b [4]byte
		rand.Read(b[:])
		id = binary.LittleEndian.Uint32(b[:])
	}

	room := &Room{
		ID:         id,
		Name:       CleanName(name),
		Players:    1,
		MaxPlayers: ClampRoomSize(roomSize),
		Proto:      proto,
		Locked:     false,
		Created:    time.Now(),
	}

	r.rooms[id] = room

	return *room
}

// CleanName makes any name fit the protocol: invalid UTF-8 becomes "?", spaces around it go, it is cut to
// MaxNameLen bytes without splitting a character, and an empty name becomes DefaultName.
func CleanName(name string) string {
	name = strings.TrimSpace(strings.ToValidUTF8(name, "?"))
	if len(name) > MaxNameLen {
		cut := MaxNameLen
		for cut > 0 && !utf8.RuneStart(name[cut]) { // step back to the first byte of a character
			cut--
		}
		name = strings.TrimSpace(name[:cut])
	}
	if name == "" {
		return DefaultName
	}
	return name
}

// ClampRoomSize brings a room size into MinPlayers..MaxPlayers.
func ClampRoomSize(n int) int {
	return min(max(n, MinPlayers), MaxPlayers)
}

// Update sets the player count and the locked flag of a room and returns a copy of it. players is brought into
// 1..MaxPlayers of that room. The only error is ErrNoRoom.
func (r *Registry) Update(id uint32, players int, locked bool) (Room, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	room := r.rooms[id]

	if room == nil {
		return Room{}, ErrNoRoom
	}

	room.Locked = locked
	room.Players = min(max(players, 1), room.MaxPlayers)

	return *room, nil
}

// Remove deletes a room. Removing a room that is already gone is fine: the relay calls this
// whenever a control connection closes, without checking first.
func (r *Registry) Remove(id uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.rooms, id)
}

// Get returns a copy of one room, or ErrNoRoom.
func (r *Registry) Get(id uint32) (Room, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	room := r.rooms[id]

	if room == nil {
		return Room{}, ErrNoRoom
	}

	return *room, nil
}
