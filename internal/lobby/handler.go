package lobby

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
)

// NewHandler returns the read-only HTTP routes (room list, health check). See docs/protocol.md.
// The game itself never uses HTTP: hosting and joining go through the TCP relay.
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

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
