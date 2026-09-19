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

## Contracts added for the website (2026-09-19)

The website's contract requests CR-2..CR-6, plus the account-security routes it
needed. gRPC is internal-key authenticated like everything else; the rows
marked *home* also refuse region-node keys.

| Contract | What |
| --- | --- |
| `GET /v1/whoami` (CR-4) | account_id, email, display_name, status (`active`/`banned`/`deletion-pending`), email_verified, two_factor_enabled for the session. Every authenticated route also takes the session as `Authorization: Bearer`; `POST /v1/login` now answers `account_id` too |
| `Moderation.SearchAccounts`, `SetAccountBanned` (CR-5, *home*) | search by id, email, display name or link subject; bans revoke sessions, emit `account_banned`/`account_unbanned`, and record reason + actor in `moderation_actions` |
| `Moderation.FileReport`, `ListReports`, `ResolveReport` (CR-6) | the reports queue; listing and resolving are *home* |
| `Links.SetLinkPublicCode`, `ListAccountLinks`, `GetLinkByPublicCode` (CR-3) | the adapter publishes the code it mints (the Switch friend code); `public_code` also rides on `GetActiveLinkByAccount`. See [docs/public-codes.md](docs/public-codes.md) |
| `Media.PutAvatar` (*home*), `GetAvatar`, `DeleteAvatar` (*home*) (CR-2) | 256 and 512 px PNGs in Postgres (≤256 KiB / ≤1 MiB), one row per account, conditional fetch by version |
| `Social.ListBlocks` | the blocker's own list |
| `POST /v1/verify/resend` | new verification link for the signed-in account, one per 10 minutes, one answer for every outcome |
| `POST /v1/email/change`, `/v1/email/confirm` | link to the new address, notice to the old; applying it emits `email_changed` |
| `POST /v1/2fa/setup`, `/enable`, `/disable`, `/v1/login/2fa`; `Identity.VerifySecondFactor` | TOTP + recovery codes. `VerifyPassword`/`VerifyPasswordByID` fail closed for 2FA accounts unless the caller sets `second_factor_supported`. See [docs/two-factor.md](docs/two-factor.md) |

## Tests


```sh
docker run -d --name openpak-pg-test -e POSTGRES_PASSWORD=test \
  -e POSTGRES_DB=account_test -p 54329:5432 postgres:18-alpine
go vet -tags integration ./...
go test -tags integration -race -count=1 ./...
```

The tests drop and recreate the `public` schema of that database. To use a
different one, set `ACCOUNT_TEST_DATABASE_URL`, for example
`postgres://postgres:x@127.0.0.1:25432/account_test?sslmode=disable`.

## License

AGPL-3.0-only (OpenPak-authored code). Behavior ported from
Pretendo/account (AGPL-3.0); see header comments for provenance.
