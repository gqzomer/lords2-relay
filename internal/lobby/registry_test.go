package lobby

import (
	"errors"
	"strings"
	"testing"
)

// mustCreate is a test helper: it creates a room and checks it kept the name and size it was given.
// t.Helper() makes a failure point at the caller's line instead of this one.
func mustCreate(t *testing.T, r *Registry, name string, maxPlayers int, proto uint32) Room {
	t.Helper()
	room := r.Create(name, maxPlayers, proto)
	if room.Name != name || room.MaxPlayers != maxPlayers {
		t.Fatalf("Create(%q, %d, %d) stored %q, %d", name, maxPlayers, proto, room.Name, room.MaxPlayers)
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
// Create never refuses: it stores a corrected name and size.
func TestCreateCorrects(t *testing.T) {
	tests := []struct {
		name       string // the test case's name, shown when it fails
		roomName   string
		maxPlayers int
		wantName   string
		wantMax    int
	}{
		{"ok", "game", 4, "game", 4},
		{"smallest room", "game", MinPlayers, "game", MinPlayers},
		{"largest room", "game", MaxPlayers, "game", MaxPlayers},
		{"longest name", strings.Repeat("a", MaxNameLen), 4, strings.Repeat("a", MaxNameLen), 4},
		{"name of multi-byte characters", "Château de Gersom", 4, "Château de Gersom", 4},
		{"empty name", "", 4, DefaultName, 4},
		{"only spaces", "   ", 4, DefaultName, 4},
		{"spaces around", "  game ", 4, "game", 4},
		{"name too long", strings.Repeat("a", MaxNameLen+1), 4, strings.Repeat("a", MaxNameLen), 4},
		// "é" is 2 bytes: one character too many is cut to MaxNameLen/2 whole characters, never half an "é"
		{"name too long in bytes", strings.Repeat("é", MaxNameLen/2+1), 4, strings.Repeat("é", MaxNameLen/2), 4},
		{"name too long, cut inside a character", "a" + strings.Repeat("é", MaxNameLen/2), 4, "a" + strings.Repeat("é", MaxNameLen/2-1), 4},
		// Jen's Mod Menu appends "|L2M1:" and 32 hex digits: the whole name must survive
		{"name with a mod marker", "Lords2 Session1|L2M1:67d6eb0557e113b822e476e439cb0c28", 4, "Lords2 Session1|L2M1:67d6eb0557e113b822e476e439cb0c28", 4},
		{"name not UTF-8", "game\xff", 4, "game?", 4},
		{"one player", "game", 1, "game", MinPlayers},
		{"six players", "game", 6, "game", MaxPlayers},
		{"sixteen players", "game", 16, "game", MaxPlayers},
		{"negative players", "game", -1, "game", MinPlayers},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			room := r.Create(tt.roomName, tt.maxPlayers, 1)
			if room.Name != tt.wantName || room.MaxPlayers != tt.wantMax {
				t.Errorf("Create(%q, %d) stored %q, %d; want %q, %d",
					tt.roomName, tt.maxPlayers, room.Name, room.MaxPlayers, tt.wantName, tt.wantMax)
			}
			if got, _ := r.Get(room.ID); got != room {
				t.Errorf("Get = %+v, want what Create returned, %+v", got, room)
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

	got, err := r.Update(room.ID, 3, true)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Players != 3 || !got.Locked {
		t.Errorf("after Update(3, locked): players=%d locked=%v", got.Players, got.Locked)
	}
	if stored := r.List(0)[0]; stored != got {
		t.Errorf("Update returned %+v, but the registry has %+v", got, stored)
	}

	// Out of range: corrected into 1..max (4 here), not refused.
	for _, tc := range []struct{ players, want int }{{0, 1}, {-3, 1}, {5, 4}, {200, 4}} {
		got, err := r.Update(room.ID, tc.players, false)
		if err != nil || got.Players != tc.want {
			t.Errorf("Update with %d players: got %d players, err %v; want %d", tc.players, got.Players, err, tc.want)
		}
	}

	if _, err := r.Update(room.ID+1, 2, false); !errors.Is(err, ErrNoRoom) {
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

func TestGet(t *testing.T) {
	r := NewRegistry()
	room := mustCreate(t, r, "game", 4, 7)
	if _, err := r.Update(room.ID, 2, true); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := r.Get(room.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != room.ID || got.Name != "game" || got.MaxPlayers != 4 || got.Proto != 7 ||
		got.Players != 2 || !got.Locked {
		t.Errorf("Get = %+v, want the room with players 2 and locked", got)
	}

	// Get hands out a copy, like List: changing it must not change the registry.
	got.Players = 4
	if again, _ := r.Get(room.ID); again.Players != 2 {
		t.Errorf("changing Get's result changed the registry: players = %d", again.Players)
	}

	if _, err := r.Get(room.ID + 1); !errors.Is(err, ErrNoRoom) {
		t.Errorf("Get of an unknown room: error = %v, want ErrNoRoom", err)
	}

	r.Remove(room.ID)
	if _, err := r.Get(room.ID); !errors.Is(err, ErrNoRoom) {
		t.Errorf("Get after Remove: error = %v, want ErrNoRoom", err)
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
