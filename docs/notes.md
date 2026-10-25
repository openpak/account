# Notes: account

## Overview

Console-agnostic identity service: accounts, web sessions, email
verification/recovery, generic adapter links, moderation foundation, and the
canonical friend graph (in progress). Wii U/3DS protocols live in
[`nn-account`](https://github.com/openpak/nn-account); this core never imports Nintendo protocol code.

Status: **M1 in progress** — see `PRD.md` in [`nn-account`](https://github.com/openpak/nn-account) for scope.

## Layout

- `proto/openpak/account/v1` — internal contracts (Identity, Links, Social, Sessions, Invitations, Events, Registration, Federation, Moderation, Media). Stubs are committed; regenerate with
  `protoc -I proto --go_out=. --go_opt=module=openpak/account --go-grpc_out=. --go-grpc_opt=module=openpak/account proto/openpak/account/v1/<file>.proto`
- `cmd/account` — single binary serving gRPC (internal) and HTTP (web/API)
- `internal/identity` — registration, verification, login, sessions, recovery
- `internal/links` — generic adapter link state machine (pending → active → revoking → revoked)
- `internal/grpcapi` — internal gRPC with fail-closed key auth
- `internal/web` — HTTP JSON API (register/verify/login/logout/sessions/recovery, whoami, email change, 2FA)
- `internal/moderation` — account search and the reports queue
- `internal/media` — avatar storage
- `internal/architecture` — guard test: no Nintendo imports in the core
- `migrations` — versioned SQL migrations (embedded)

## Licence

AGPL-3.0-only (OpenPak-authored code). Behavior ported from
Pretendo/account (AGPL-3.0); see header comments for provenance.
