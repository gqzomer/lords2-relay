# Protocol (draft)

Draft for discussion with the dplayx.dll authors. Nothing here is fixed yet.

Two ports on the server:

| Port | Protocol | Used for |
|---|---|---|
| 47701 | HTTP + JSON | Lobby: create, update and list rooms |
| 47700 | TCP | Relay: control connection and game traffic |

All connections go from the player to the server. Nobody needs a port forward.

## Lobby (HTTP)

| Method and path | Who | Body | Answer |
|---|---|---|---|
| `GET /v1/rooms?proto=N` | joiner | – | list of rooms with that PROTO |
| `POST /v1/rooms` | host | `{"name", "max_players", "proto"}` | `{"id", "host_token"}` |
| `PUT /v1/rooms/{id}` | host, every 5 s | `{"host_token", "players", "locked"}` | 204 |
| `DELETE /v1/rooms/{id}` | host | `{"host_token"}` | 204 |
| `POST /v1/rooms/{id}/join` | joiner | – | `{"pair_code"}` |
| `GET /healthz` | monitoring | – | `ok` |

A room without an update for 15 s (`ROOM_TIMEOUT`) is removed. `host_token` is secret: it proves a request comes from the
host that created the room, and it is never shown in the room list.

## Relay (TCP)

Every relay connection starts with a fixed 22-byte header:

| Bytes | Field | Value |
|---|---|---|
| 0–3 | magic | `L2RL` |
| 4 | version | `1` |
| 5 | role | `1` control, `2` host data, `3` joiner data |
| 6–21 | token | control: host token · data: pair code (16 bytes) |

### Flow

1. Host creates the room over HTTP, then opens a **control** connection (role 1, host token). It stays open for the whole game;
   when it closes, the room is removed.
2. Joiner picks a room, gets a `pair_code` from `POST /v1/rooms/{id}/join`, and opens a **joiner data** connection (role 3,
   pair code).
3. The relay sends a `JOIN` frame on the host's control connection: 1 byte type `1` + the 16-byte pair code.
4. The host opens a **host data** connection (role 2, same pair code).
5. The relay pairs the two data connections and copies bytes both ways. From here on it is the normal dplayx.dll protocol.

Control frames from the relay to the host: type `1` JOIN (+ pair code), type `2` PING (every 15 s; the host answers with type
`2`, so a dead control connection is noticed).

A pair code is valid for 30 s and for one pairing only.

## Open questions

- HTTP for the lobby, or the lobby inside the TCP control protocol too (no HTTP client needed in the DLL)?
- TLS for the lobby, or plain HTTP (the tokens then travel unencrypted)?
- Should the relay limit bandwidth per connection, and at what rate?
