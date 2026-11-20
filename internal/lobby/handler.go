package lobby

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
)

// NewHandler returns the lobby's HTTP routes. The API is described in docs/protocol.md.
func NewHandler(rooms *Registry, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// Health check for Docker and the uptime monitor.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	// Room list. Worked example of a finished handler: GET /v1/rooms?proto=13
	mux.HandleFunc("GET /v1/rooms", func(w http.ResponseWriter, r *http.Request) {
		var proto uint64
		if p := r.URL.Query().Get("proto"); p != "" {
			var err error
			if proto, err = strconv.ParseUint(p, 10, 32); err != nil {
				http.Error(w, "bad proto", http.StatusBadRequest)
				return
			}
		}
		writeJSON(w, http.StatusOK, rooms.List(uint32(proto)))
	})

	// TODO: the host side. Each one decodes a JSON body, calls the Registry and answers with JSON.
	mux.HandleFunc("POST /v1/rooms", notImplemented)           // create a room
	mux.HandleFunc("PUT /v1/rooms/{id}", notImplemented)       // host update: players, locked
	mux.HandleFunc("DELETE /v1/rooms/{id}", notImplemented)    // host closes the room
	mux.HandleFunc("POST /v1/rooms/{id}/join", notImplemented) // joiner asks for a pairing code

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notImplemented(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented yet", http.StatusNotImplemented)
}
