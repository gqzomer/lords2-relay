// Package lobby keeps the list of open rooms (in memory only) and serves it over HTTP.
package lobby

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// ErrNotImplemented marks the parts still to be written.
var ErrNotImplemented = errors.New("not implemented yet")

// Room is one open game. Fields with a json tag are shown to players.
type Room struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Players    int       `json:"players"`
	MaxPlayers int       `json:"max_players"`
	Proto      uint32    `json:"proto"` // dplayx.dll PROTO: only matching rooms can be joined
	Locked     bool      `json:"locked"`
	Created    time.Time `json:"created"`

	lastSeen  time.Time // last update from the host
	hostToken string    // secret the host proves itself with; never sent to joiners
}

// Registry is safe to use from many goroutines at once: every method takes mu.
type Registry struct {
	mu    sync.Mutex
	rooms map[string]*Room
}

func NewRegistry() *Registry {
	return &Registry{rooms: make(map[string]*Room)}
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

// Create adds a room and returns it with a fresh ID and host token.
// TODO: validate the name (length, characters), generate ID and token with crypto/rand,
// enforce a limit of rooms per IP address.
func (r *Registry) Create(name string, maxPlayers int, proto uint32) (Room, string, error) {
	return Room{}, "", ErrNotImplemented
}

// Update sets the player count and locked flag, and refreshes lastSeen.
// TODO: check hostToken, then update the room.
func (r *Registry) Update(id, hostToken string, players int, locked bool) error {
	return ErrNotImplemented
}

// Remove deletes a room when the host closes it.
// TODO: check hostToken, then delete.
func (r *Registry) Remove(id, hostToken string) error {
	return ErrNotImplemented
}

// expire removes rooms whose host has not sent an update within timeout.
func (r *Registry) expire(now time.Time, timeout time.Duration) (removed []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, room := range r.rooms {
		if now.Sub(room.lastSeen) > timeout {
			delete(r.rooms, id)
			removed = append(removed, id)
		}
	}
	return removed
}

// ExpireLoop runs expire every few seconds until ctx is cancelled.
func ExpireLoop(ctx context.Context, r *Registry, timeout time.Duration, log *slog.Logger) {
	t := time.NewTicker(timeout / 3)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			for _, id := range r.expire(now, timeout) {
				log.Info("room expired", "room", id)
			}
		}
	}
}
