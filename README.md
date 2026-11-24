# lords2-relay

Lobby and relay server for Lords2HD multiplayer (Lords of the Realm II). Players host and find rooms from inside the game,
and all game traffic goes through this server, so nobody needs a port forward. The game talks to it over one TCP port
(47700); a small read-only HTTP API (47701) shows the room list to people and monitoring.

Status: **working, not yet tested with the game**. The whole protocol in [docs/protocol.md](docs/protocol.md) is
implemented and covered by tests: room list, hosting (with PING/PONG), joining and pairing, and the relay itself.
Still to do: per-IP connection limits (error 6).

- Architecture plan: see the Lords2HD architecture document (link to be added).
- Wire protocol (draft): [docs/protocol.md](docs/protocol.md)
- Client side: `multiplayer/dplayx` in [lords2-hd](https://github.com/skaldic/lords2-hd)

## Layout

```
cmd/lords2-relay/     main: starts the lobby and the relay, stops cleanly on Ctrl+C / docker stop
internal/config/      settings from environment variables
internal/lobby/       room registry (in memory) and the HTTP API
internal/relay/       TCP relay: header, pairing, pipe
deploy/               files for the VPS: compose.yaml, cloud-init.yaml, update.sh
docs/                 protocol and notes
.github/workflows/    tests on every push; image to GHCR on main
```

## Running locally

Requires Go 1.24 or newer.

```sh
go test ./...                 # run the tests
go run ./cmd/lords2-relay     # lobby on :47701, relay on :47700

curl localhost:47701/healthz
curl localhost:47701/v1/rooms
```

In a container (Docker or Podman):

```sh
docker compose up --build     # or: podman compose up --build
```

Settings (environment variables):

| Variable | Default | Meaning |
|---|---|---|
| `LOBBY_ADDR` | `:47701` | read-only HTTP address (room list, health check) |
| `RELAY_ADDR` | `:47700` | TCP relay address |

## Deploying to DigitalOcean

1. Push to `main`: GitHub Actions tests, builds the image and pushes it to `ghcr.io/gqzomer/lords2-relay`. Make the package
   public once (GitHub → Packages → settings), or the VPS needs a login to pull it.
2. Create a Droplet from the **Docker** Marketplace image, smallest plan, region London or New York, with an SSH key.
   Paste `deploy/cloud-init.yaml` into **User data**.
3. Add a **Cloud Firewall**: inbound TCP 22 (SSH), 47700 (relay) and 47701 (lobby) only.
4. Updating later: `ssh root@<droplet> /opt/lords2-relay/update.sh` (drops games in progress).


## To do

- [x] Registry: `Create`, `Update`, `Remove` in `internal/lobby/registry.go` (start from `List`), with tests
- [ ] Registry: `Get` for one room, for the joining checks (exists, not locked, not full), with a test
- [ ] Relay: protocol constants (`RoleList`, frame types, error codes, timings)
- [ ] Relay: read and check the header (`internal/relay/relay.go`, `handle`)
- [ ] Relay: frame reading and writing (`len`, `type`, `payload`)
- [ ] Relay: list connections (LIST, ROOMS)
- [ ] Relay: control connections (HOST, HOSTED, UPDATE, PING/PONG); the room is removed when the connection closes
- [ ] Relay: joining (JOIN, pair codes, status byte), then `pipe`
- [ ] Relay: tests for `handle`, one per step above (`internal/relay/relay_test.go`)
- [x] HTTP: drop the host routes from `internal/lobby/handler.go`, keep `/v1/rooms` and `/healthz`; drop `ExpireLoop`
- [ ] Limits per IP address (rooms, connections)
- [ ] Phase 0 on a VPS: a plain TCP forward to measure how a relay feels in a real game
