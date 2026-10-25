# Account Server

OpenPak's Account Server.

| | |
| --- | --- |
| Network | Console-agnostic (no console protocol code) |
| Role | Account core: accounts, web sessions, email verification and recovery, generic adapter links, moderation, and the canonical friend graph |
| Depends on | PostgreSQL |
| Image | `ghcr.io/openpak/account` |
| Based on | [Pretendo `account`](https://github.com/PretendoNetwork/account) (AGPL-3.0) |
| Licence | AGPL-3.0-only |

## What it serves

| Endpoint | What |
| --- | --- |
| gRPC (internal) | Identity, Links, Social, Sessions, Invitations, Events, Registration, Federation, Moderation and Media contracts, internal-key authenticated |
| HTTP | JSON web and API routes: register, verify, login, sessions, recovery, whoami, email change, 2FA |

## Run

```sh
cp example.env .env   # fill in required values
set -a; source .env; set +a
go run ./cmd/account
```

## Docs

- [Protocol](docs/protocol.md)
- [Evidence](docs/evidence.md)
- [Deploy](docs/deploy.md)
- [Notes](docs/notes.md)
- [Handles](docs/handles.md)
- [Public codes](docs/public-codes.md)
- [Routing](docs/routing.md)
- [Two-factor](docs/two-factor.md)
- [Docs index](docs/README.md)

## Licence

AGPL-3.0-only. See [LICENSE](LICENSE).
