package lobby

import (
	"errors"
	"strings"
	"testing"
)

// mustCreate is a test helper: it creates a room or stops the test.
// t.Helper() makes a failure point at the caller's line instead of this one.
func mustCreate(t *testing.T, r *Registry, name string, maxPlayers int, proto uint32) Room {
	t.Helper()
	room, err := r.Create(name, maxPlayers, proto)
	if err != nil {
		t.Fatalf("Create(%q, %d, %d): %v", name, maxPlayers, proto, err)
	}
	return room
}

func TestCreate(t *testing.T) {
	r := NewRegistry()
	room := mustCreate(t, r, "Gersom's game", 4, 13121)

	if room.ID == 0 {
		t.Error("ID is 0; the protocol never uses 0 as a room id")
	}
	if room.Name != "Gersom's game" || room.MaxPlayers != 4 || room.Proto != 13121 {
		t.Errorf("room = %+v, want the name, max players and proto passed to Create", room)
	}
	if room.Players != 1 {
		t.Errorf("Players = %d, want 1 (the host)", room.Players)
	}
	if room.Locked {
		t.Error("a new room is locked")
	}
	if room.Created.IsZero() {
		t.Error("Created is not set")
	}

	list := r.List(0)
	if len(list) != 1 || list[0].ID != room.ID {
		t.Errorf("List(0) = %+v, want just the new room", list)
	}
}

// A table-driven test: one row per case, the same check for each.
// This is the usual way to test many inputs in Go.
func TestCreateValidation(t *testing.T) {
	tests := []struct {
		name       string // the test case's name, shown when it fails
		roomName   string
		maxPlayers int
		wantErr    error // nil = must succeed
	}{
		{"ok", "game", 4, nil},
		{"smallest room", "game", MinPlayers, nil},
		{"largest room", "game", MaxPlayers, nil},
		{"longest name", strings.Repeat("a", MaxNameLen), 4, nil},
		{"name of multi-byte characters", "Château de Gersom", 4, nil},
		{"empty name", "", 4, ErrBadRequest},
		{"name too long", strings.Repeat("a", MaxNameLen+1), 4, ErrBadRequest},
		{"name too long in bytes", strings.Repeat("é", 17), 4, ErrBadRequest}, // 17 characters, 34 bytes
		{"name not UTF-8", "game\xff", 4, ErrBadRequest},
		{"one player", "game", 1, ErrBadRequest},
		{"six players", "game", 6, ErrBadRequest},
		{"negative players", "game", -1, ErrBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			_, err := r.Create(tt.roomName, tt.maxPlayers, 1)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Create(%q, %d) error = %v, want %v", tt.roomName, tt.maxPlayers, err, tt.wantErr)
			}
			if err != nil && len(r.List(0)) != 0 {
				t.Error("Create failed but the room was added anyway")
			}
		})
	}
}

func TestCreateUniqueIDs(t *testing.T) {
	r := NewRegistry()
	seen := make(map[uint32]bool)
	for range 1000 {
		room := mustCreate(t, r, "game", 2, 1)
		if seen[room.ID] {
			t.Fatalf("ID %d handed out twice", room.ID)
		}
		seen[room.ID] = true
	}
	if n := len(r.List(0)); n != 1000 {
		t.Errorf("List(0) has %d rooms, want 1000", n)
	}
}

func TestUpdate(t *testing.T) {
	r := NewRegistry()
	room := mustCreate(t, r, "game", 4, 1)

	if err := r.Update(room.ID, 3, true); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got := r.List(0)[0]
	if got.Players != 3 || !got.Locked {
		t.Errorf("after Update(3, locked): players=%d locked=%v", got.Players, got.Locked)
	}

	for _, players := range []int{0, 5} { // 5 is more than this room's max of 4
		if err := r.Update(room.ID, players, false); !errors.Is(err, ErrBadRequest) {
			t.Errorf("Update with %d players: error = %v, want ErrBadRequest", players, err)
		}
	}
	if got := r.List(0)[0]; got.Players != 3 || !got.Locked {
		t.Errorf("a refused Update changed the room: players=%d locked=%v", got.Players, got.Locked)
	}

	if err := r.Update(room.ID+1, 2, false); !errors.Is(err, ErrNoRoom) {
		t.Errorf("Update of an unknown room: error = %v, want ErrNoRoom", err)
	}
}

func TestRemove(t *testing.T) {
	r := NewRegistry()
	a := mustCreate(t, r, "a", 2, 1)
	b := mustCreate(t, r, "b", 2, 1)

	r.Remove(a.ID)
	list := r.List(0)
	if len(list) != 1 || list[0].ID != b.ID {
		t.Errorf("after Remove(a): List(0) = %+v, want only b", list)
	}

	r.Remove(a.ID) // a second time: must not panic or touch b
	if len(r.List(0)) != 1 {
		t.Error("removing a room twice removed another room")
	}
}

func TestListFiltersAndSorts(t *testing.T) {
	r := NewRegistry()
	first := mustCreate(t, r, "first", 2, 100)
	mustCreate(t, r, "other build", 2, 200)
	third := mustCreate(t, r, "third", 2, 100)

	list := r.List(100)
	if len(list) != 2 || list[0].ID != first.ID || list[1].ID != third.ID {
		t.Errorf("List(100) = %+v, want first then third", list)
	}
	if n := len(r.List(0)); n != 3 {
		t.Errorf("List(0) has %d rooms, want all 3", n)
	}
}

// List hands out copies: changing one must not change the registry.
func TestListReturnsCopies(t *testing.T) {
	r := NewRegistry()
	mustCreate(t, r, "game", 2, 1)

	r.List(0)[0].Name = "changed"
	if got := r.List(0)[0].Name; got != "game" {
		t.Errorf("changing List's result changed the registry: name = %q", got)
	}
}
