// Package config reads the server settings from environment variables,
// so the same image runs locally and on the VPS with different settings.
package config

import "os"

type Config struct {
	LobbyAddr string // read-only HTTP (room list, health check), e.g. ":47701"
	RelayAddr string // TCP relay, everything the game does, e.g. ":47700"
	LogLevel  string // debug, info, warn or error

	// TODO (later, with the per-IP limits in the relay): settings for those limits, e.g. MAX_CONNS_PER_IP and
	// MAX_CONNS, so they can be tuned on the VPS without a rebuild. Add them to the README's settings table too.
}

func FromEnv() Config {
	return Config{
		LobbyAddr: env("LOBBY_ADDR", ":47701"),
		RelayAddr: env("RELAY_ADDR", ":47700"),
		LogLevel:  env("LOG_LEVEL", "info"),
	}
}

// env returns the environment variable key, or def when it is unset or empty.
func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
