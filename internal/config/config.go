// Package config reads the server settings from environment variables,
// so the same image runs locally and on the VPS with different settings.
package config

import (
	"os"
	"time"
)

type Config struct {
	LobbyAddr   string        // HTTP lobby, e.g. ":47701"
	RelayAddr   string        // TCP relay, e.g. ":47700"
	RoomTimeout time.Duration // a room without an update for this long is removed
}

func FromEnv() Config {
	return Config{
		LobbyAddr:   env("LOBBY_ADDR", ":47701"),
		RelayAddr:   env("RELAY_ADDR", ":47700"),
		RoomTimeout: envDuration("ROOM_TIMEOUT", 15*time.Second),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
