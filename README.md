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
