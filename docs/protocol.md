# Protocol (draft)

Draft, version 1. Nothing here is fixed until both the relay and dplayx.dll implement it.

## Overview

| Port | Protocol | Used by | For |
|---|---|---|---|
| 47700 | TCP, binary | dplayx.dll | everything the game does: hosting, the room list, joining, game traffic |
| 47701 | HTTP, JSON | people, monitoring | read-only room list and health check |

All connections go from the player to the server, so nobody needs a port forward.

There are four kinds of TCP connection, picked by the `role` byte in the header:

| Role | Name | Opened by | Lives for |
|---|---|---|---|
| 1 | control | host | the whole time the room is open: the room exists exactly as long as this connection |
| 2 | host data | host | one game connection per joiner, opened when the relay asks for it |
| 3 | joiner data | joiner | that joiner's game connection |
| 4 | list | anyone | one room-list request, then closed by the relay |

## Conventions

- Integers are **little-endian** (as on x86, so the DLL can write them with `*(DWORD *)p = v`).
- `u8`, `u16`, `u32`: unsigned integers of 1, 2 and 4 bytes.
- `str`: a `u8` length followed by that many bytes of UTF-8, no terminating zero. The DLL converts from and to the
  Windows code page.
- Room ids are random `u32` values, never 0.
- The relay closes any connection that breaks a rule below, without an answer unless an answer is listed.

## Header

Every TCP connection starts with a 6-byte header, then a role-specific part:

| Bytes | Field | Value |
|---|---|---|
| 0–3 | magic | `L2RL` |
| 4 | version | `1` |
| 5 | role | `1`–`4`, see above |

| Role | Follows the header |
|---|---|
| 1 control | nothing; frames follow (see Control) |
| 2 host data | `pair_code` (16 bytes), from a JOIN frame |
| 3 joiner data | `room_id` (`u32`) |
| 4 list | nothing; frames follow (see List) |

The client has 10 seconds to send the header and its role-specific part.

## Frames (roles 1 and 4)

Control and list connections carry frames:

| Bytes | Field |
|---|---|
| 0–1 | `len` (`u16`): length of `type` + `payload` |
| 2 | `type` (`u8`) |
| 3… | `payload` (`len - 1` bytes) |

A frame is at most 1024 bytes (`len` ≤ 1024). A frame type a side does not know is an error.

### Control (role 1)

| Type | Name | Direction | Payload |
|---|---|---|---|
| 1 | HOST | host → relay | `proto u32`, `max_players u8`, `name str` |
| 2 | HOSTED | relay → host | `room_id u32` |
| 3 | UPDATE | host → relay | `players u8`, `locked u8` (0 or 1) |
| 4 | JOIN | relay → host | `pair_code` (16 bytes) |
| 5 | PING | relay → host | – |
| 6 | PONG | host → relay | – |
| 7 | ERROR | relay → host | `code u8`, then the relay closes |

1. The host connects and sends HOST as its first frame. The relay answers HOSTED (the room is now in the list) or ERROR.
2. The host sends UPDATE whenever its player count or locked state changes. No periodic updates are needed.
3. The relay sends PING every 15 s. If no PONG arrives within 15 s, the relay closes the connection.
4. When the control connection closes, for any reason, the room is removed. Game connections already paired keep running.
   There is no "close room" frame: the host simply closes the connection.

Limits: `name` 1–32 bytes, `max_players` 2–5 (the game has five nobles), `players` 1–`max_players`.

### List (role 4)

| Type | Name | Direction | Payload |
|---|---|---|---|
| 8 | LIST | client → relay | – |
| 9 | ROOMS | relay → client | `count u8`, then `count` times: `room_id u32`, `proto u32`, `players u8`, `max_players u8`, `locked u8`, `name str` |

The client sends one LIST; the relay answers one ROOMS and closes. Rooms are oldest first, at most 20. All PROTOs are
listed: the DLL shows rooms from another build as incompatible, as it does on a LAN.

## Joining

1. The joiner picks a room from the list and opens a **joiner data** connection (role 3, `room_id`).
2. The relay checks the room (exists, not locked, not full), makes a fresh random 16-byte `pair_code` and sends JOIN with it
   on the host's control connection.
3. The host opens a **host data** connection (role 2, that `pair_code`).
4. The relay sends one status byte on each data connection, `0` = paired, and from then on copies bytes both ways. From
   here on it is the normal dplayx.dll game protocol.

If pairing fails, the relay sends one non-zero status byte (see Error codes) on that data connection and closes it.

The `pair_code` exists only between the relay and the host; the joiner never sees it. It is valid for 10 s and for one
pairing only.

## Error codes

Used in ERROR frames and in the status byte of data connections.

| Code | Meaning |
|---|---|
| 0 | OK (status byte only) |
| 1 | unsupported version: update the game or the mod |
| 2 | bad request: malformed header or frame, value out of range |
| 3 | no such room |
| 4 | room is locked or full |
| 5 | host did not answer in time |
| 6 | server full, or too many connections from your address |

## HTTP (port 47701, read-only)

| Path | Answer |
|---|---|
| `GET /v1/rooms` | JSON list of rooms: `id`, `name`, `players`, `max_players`, `proto`, `locked`, `created` |
| `GET /v1/rooms?proto=N` | the same, only rooms with that PROTO |
| `GET /healthz` | `ok` |

For people, scripts and monitoring only; the DLL does not use HTTP.

## Open questions

- Should the relay limit bandwidth per connection, and at what rate?
- Should a host be able to rejoin its own room after its control connection drops (for example after a short network
  outage), or is a new room fine?
