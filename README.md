# OpenPak Account Core

Console-agnostic identity service: accounts, web sessions, email
verification/recovery, generic adapter links, moderation foundation, and the
canonical friend graph (in progress). Wii U/3DS protocols live in
[`nn-account`](../nn-account); this core never imports Nintendo protocol code.

Status: **M1 in progress** — see `PRD.md` in `../nn-account` for scope.

## Run

```sh
cp example.env .env   # fill in required values
set -a; source .env; set +a
go run ./cmd/account
```

Requires PostgreSQL. Migrations apply automatically at startup.

## Layout

- `proto/openpak/account/v1` — internal contracts (Identity, Links)
- `cmd/account` — single binary serving gRPC (internal) and HTTP (web/API)
- `internal/identity` — registration, verification, login, sessions, recovery
- `internal/links` — generic adapter link state machine (pending → active → revoking → revoked)
- `internal/grpcapi` — internal gRPC with fail-closed key auth
- `internal/web` — HTTP JSON API (register/verify/login/logout/sessions/recovery)
- `internal/architecture` — guard test: no Nintendo imports in the core
- `migrations` — versioned SQL migrations (embedded)

## Contracts added for the phone and the status page (2026-09-08)

| RPC | Why it could not reuse what existed |
| --- | --- |
| `Sessions.GetPresence` | who is online and in which title, from the live session leases — so a friends list and a status page can never disagree |
| `Sessions.GetPlayerCounts` | players per title and per network. Counts **accounts, not sessions**: someone with a console and an emulator open is one person, so the total is its own query rather than a sum |
| `Invitations.ListInvitations` | `PollInvitations` is namespace-scoped, starts a redelivery window and is consumed by `AckInvitations` — a phone calling it would take an invitation away from the console that has to receive it. This one consumes nothing and carries neither payload nor nonce |
| `Invitations.RejectInvitation` | the decline button, scoped to the recipient |
| `POST /v1/profile` | country, birthday and display name were settable at sign-up and never afterwards |

Two events were added because push had nothing to hang on: `friend_requested` and
`invitation_sent`, both emitted **inside the transaction that creates the thing** — a
notification is never promised for something that did not commit. `invalidation_events.type` is
a closed set, so migration `0009` had to admit them; that constraint did its job and is worth
keeping closed.

## Tests


```sh
docker run -d --name openpak-pg-test -e POSTGRES_PASSWORD=test \
  -e POSTGRES_DB=account_test -p 54329:5432 postgres:18-alpine
go vet -tags integration ./...
go test -tags integration -race -count=1 ./...
```

## License

AGPL-3.0-only (OpenPak-authored code). Behavior ported from
Pretendo/account (AGPL-3.0); see header comments for provenance.
