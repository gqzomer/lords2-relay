// Package lobby keeps the list of open rooms, in memory only.
//
// The relay calls Create when a host sends HOST on its control connection, Update on UPDATE,
// and Remove when the control connection closes (docs/protocol.md). The HTTP handler only reads.
package lobby

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sort"
	"sync"
	"time"
	"unicode/utf8"
)

// Limits from docs/protocol.md.
const (
	MinPlayers = 2
	MaxPlayers = 5  // the game has five nobles
	MaxNameLen = 32 // bytes, not characters: "é" is 2 bytes in UTF-8
)

// Errors the methods return. Callers compare with errors.Is(err, ErrNoRoom).
// The relay turns them into the protocol's error codes (2 bad request, 3 no such room).
var (
	ErrBadRequest = errors.New("bad request")
	ErrNoRoom     = errors.New("no such room")
)

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

// Create adds a room for a new host and returns a copy of it.
func (r *Registry) Create(name string, roomSize int, proto uint32) (Room, error) {

	if len(name) == 0 || len(name) > MaxNameLen || !utf8.ValidString(name) {
		return Room{}, ErrBadRequest
	}

	if roomSize < MinPlayers || roomSize > MaxPlayers {
		return Room{}, ErrBadRequest
	}

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
		Name:       name,
		Players:    1,
		MaxPlayers: roomSize,
		Proto:      proto,
		Locked:     false,
		Created:    time.Now(),
	}

	r.rooms[id] = room

	return *room, nil
}

// Update sets the player count and the locked flag of a room.
func (r *Registry) Update(id uint32, players int, locked bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	room := r.rooms[id]

	if room == nil {
		return ErrNoRoom
	}

	if players > room.MaxPlayers || players < 1 {
		return ErrBadRequest
	}

	room.Locked = locked
	room.Players = players

	return nil
}

// Remove deletes a room. Removing a room that is already gone is fine: the relay calls this
// whenever a control connection closes, without checking first.
func (r *Registry) Remove(id uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.rooms, id)
}

// TODO (exercise): Get(id uint32) (Room, error) returns a copy of one room, or ErrNoRoom.
// The relay needs it when a joiner asks for a room: the room must exist, not be locked and not be full
// (Players < MaxPlayers), else the joiner gets error 3 or 4. Same lock-then-work pattern; add a test.
func (r *Registry) Get(id uint32) (Room, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	room := r.rooms[id]

	if room == nil {
		return Room{}, ErrNoRoom
	}

	return *room, nil
}
