# Changelog — account

> **Current Switch client (2026-10-13): Harbor**, at
> `~/REPOS/Openpak/emulators/harbor`. Harbor is an independent
> OpenPak project based on Eden. Citron, Ryujinx (desktop and Android), and
> the former OpenPak Eden checkouts have been removed. References to those
> forks below record historical evidence only: do not run their commands,
> reinstall them, or select them for current work. Use Harbor's README and
> current handoff for builds and testing; old results do not establish Harbor parity.

Generated from git history on 2026-09-15. `git log` stays the source
of truth; this file is the readable summary.

## Unreleased

- **Automatic account erasure** (migration 0027). Deleting closes the account at once and sets
  `erase_after` one grace period away (`ACCOUNT_DELETION_GRACE`, default 720h); the status it had
  is kept. Signing in with the right password answers 409 `deletion_pending` with the date, and
  `POST /v1/login/restore` reopens it and signs in; `RestoreAccountDeletion` is the admin restore
  (home only, refused once the date has passed); a restore emits `account_restored`.
  `ListDueAccountDeletions` feeds the website's finaliser, which erases every service and then
  calls `FinalizeAccountDeletion`. Finalize now also deletes console links, friendships, blocks
  and invitations (both directions), and adapter and play sessions. `GetAccount` reports
  `erase_after_unix`. Closed accounts drop out of friend, request and block lists.
- **Federation admission rules** (migration 0025). A node is one region, operator and approval on
  one or more servers. The node key only enrols servers (`JoinServer`); each server gets its own
  `opsrv_` key, announces on its own with a hardware report (cores, RAM, disk, SSD, IPv4/IPv6,
  port speed) and takes a daily home-measured bandwidth self-test (`StartSelfTest` /
  `RecordSelfTest`). `SetNodeApproved` is refused (FAILED_PRECONDITION, naming each shortfall)
  while the node is below the minimum; an approved node whose servers degrade or stop reporting
  leaves `ListNodes` but keeps its approval. A region key that is not in rotation can only
  announce, enrol and self-test: it cannot register (stamp) sessions or read anything. Figures are
  `ACCOUNT_FEDERATION_*` (defaults 8 cores / 16 GB / 160 GB SSD per node, 2 cores / 4 GB / public
  IPv4 / 1 Gbps per server, 99.5 % availability), readable through `GetRequirements`.
- Per-node admin waiver of the hardware figures (`SetNodeWaiver`, reason required, optional expiry)
  for OpenPak's own test nodes; everything else still applies.
- Coverage plan: `federation_locations` (`ListLocations`, `PutLocation`, `DeleteLocation`), seeded
  empty; a node's `region` is its location code, `country` is kept for routing.

## v0.16.0 — 2026-10-16

- **Presence is pushed.** `presence_changed` (migration 0022): when a session begins, ends or lapses,
  the core tells each friend who is online somewhere, once per network that friend is on (account =
  the friend, namespace = their network, subject = whose presence moved). A network with none of
  the person's friends online hears nothing. Renewals are not events; lapses are found by diffing
  who is live every 5 s.
- Folds in the work that production already ran as local images (2026-10-15): `Identity.ListAccounts`
  (the account directory for adapter backfills), `Sessions.GetPlayHistory` (a console's Play Activity),
  and `last_seen_unix` / `friends_since_unix` on `Friend`.

## v0.13.0 — 2026-09-26

- Playtime runs from a session's first game-server login to its end. nx-baas and nn-account call
  `Sessions.MarkOnline` when a game server asks them for a player's identity at login; that sets
  `adapter_sessions.online_since` on the player's live titled presence session (a later login does
  not reset it), and `GetPlaytime` counts `online_since` → last renewal. A title left open without
  reaching a game server counts nothing. Replaces v0.12.0's timed per-mark increments, which
  needed every game server to report on a timer (migration 0021; 0020's columns are unused).

## v0.12.0 — 2026-09-26

- Playtime counts only time a game server confirmed the player online, not time a title sat open:
  `Sessions.MarkOnline` (game servers, every ~30 s, batched) adds the time since a player's
  previous mark — gaps over 90 s add nothing — to their live titled presence session
  (`adapter_sessions.online_seconds`), the session-end trigger carries it into
  `play_sessions.online_seconds`, and `GetPlaytime` sums that. Client and OS still come from
  presence, so a game server only needs the account id.

## v0.11.0 — 2026-09-26

- Playtime per title, client and OS: `RegisterSessionRequest.os` / `Presence.os` (windows, macos,
  linux, android, ios; empty for a console), table `play_sessions` filled by a trigger whenever a
  session ends (deleted, or registered over after its lease ran out or from another client/OS),
  and `Sessions.GetPlaytime` adding finished and live sessions up per combination. The website
  marks a title tested from it. A reconnect after a dead lease now restarts `created_at`, so
  presence no longer reports "since" a session that ended.

## v0.10.1 — 2026-09-24

- deps: grpc 1.83.2, x/crypto 0.57, x/net 0.59, pgx 5.11 (Dependabot)

## v0.10.0 — 2026-09-23

- Cross-network presence with client labels: `RegisterSessionRequest.client` and
  `Presence.client` (field 6, optional, max 64 bytes) — "switch", "ryujinx", "eden", "citron",
  "wiiu", "3ds", "cemu", "azahar", or empty when the adapter cannot tell. Migration `0018`
  adds `adapter_sessions.client` (default `''`).
- One live session per (account, namespace, title), was (account, title): signed in on a
  Switch and a Wii U at once no longer fight over one title-less row. Migration `0018`
  carries the SQL to put the old key back if the core is ever rolled back.
- `GetPresence` prefers a session in a title over a title-less one, then the newest.

## v0.9.3 — 2026-09-19

- Mail headers for real SMTP providers (Zoho): RFC 2047-encoded subjects (the translated
  ones are not ASCII), a `Message-ID`, `Content-Transfer-Encoding: 8bit`, and
  `ACCOUNT_SMTP_FROM` may carry a display name (`OpenPak <noreply@openpak.org>`).

## v0.9.2 — 2026-09-19

- Emails in the website's languages: verification, email change (new and old address) and
  password reset follow the request's `Accept-Language` (German, French, Brazilian
  Portuguese, Italian, Spanish; English otherwise) — `internal/mailtext`. The website sends
  the page language; console registrations over gRPC stay English.

## v0.9.1 — 2026-09-19

- release: the registry auth file is set per step; the job-level runner context made the
  workflow invalid

## v0.9.0 — 2026-09-19

Website contracts (branch `contracts-2026-09-19`):

- `GET /v1/whoami`; sessions accepted as `Authorization: Bearer`; login answers `account_id` (CR-4)
- Moderation service: `SearchAccounts`, audited `SetAccountBanned`, reports queue `FileReport`/`ListReports`/`ResolveReport` (CR-5, CR-6); migration `0013`
- Links: adapter-published public codes, `SetLinkPublicCode`/`ListAccountLinks`/`GetLinkByPublicCode` (CR-3); migration `0014`
- Media service: avatars in Postgres, `PutAvatar`/`GetAvatar`/`DeleteAvatar` (CR-2); migration `0015`
- `Social.ListBlocks`
- `POST /v1/verify/resend`
- Email change: `POST /v1/email/change`, `POST /v1/email/confirm`, event `email_changed`; migration `0016`
- Two-factor authentication (TOTP + recovery codes), `/v1/2fa/*`, `/v1/login/2fa`, `Identity.VerifySecondFactor`; password-only gRPC checks fail closed for 2FA accounts; new env `ACCOUNT_TOTP_KEY`; migration `0017`
- Unbanning no longer revives a deletion-pending account
- Events: `Emit` wakes subscribers after commit (fixes a flaky subscribe test)
- Tests: `ACCOUNT_TEST_DATABASE_URL` overrides the throwaway database

- release: job-scoped registry auth file [b76de6b]
- release: build on the self-hosted runner with podman; private-repo minutes are metered [6f8278e]

## v0.8.0 — 2026-09-10

- Events: `friend_accepted` and `friend_removed`
- Go 1.26, the house version

## v0.7.0 — 2026-09-09



## v0.6.2 — 2026-09-08



## v0.6.1 — 2026-09-08



## v0.6.0 — 2026-09-08



## v0.5.0 — 2026-09-08



## v0.4.0 — 2026-09-08



## v0.3.0 — 2026-09-08



## v0.2.1 — 2026-09-08



## v0.2.0 — 2026-09-07



## v0.1.3 — 2026-09-07



## v0.1.2 — 2026-09-07



## v0.1.1 — 2026-09-07



## v0.1.0 — 2026-09-07



## v0.1.0 — 2026-09-07

- Ship a container image on tag: rootless-ready Dockerfile + ghcr release workflow [9c3ad5c]
- Links.GetActiveLinkByAccount [d0ca730]
- Social/Sessions/Invitations review round 2: delivery-time policy revalidation, session binding, at-least-once delivery with AckInvitations, forward migration 0007 [eeea811]
- fix: typed-nil mailer wiring panic + post-migration friendship lookup [928169d]
- fix: follow-up email-flow findings and migration safety [aa1bc7d]
- fix(security review): close P0/P1/P2 findings from 2026-09 audit [4da120b]
- feat(M3 groundwork): social graph, live sessions, invitations services + gRPC [31474c8]
- test: include social tables in integration reset [b6f0100]
- chore: social/sessions/invitations contract foundation (M3 groundwork) [09b83d8]
- feat: RequestAccountDeletion core RPC (deletion-pending stage, FR-8) [08d36f4]
- fix: SHA-256 pre-hash of bcrypt inputs (72-byte limit) across all credential paths [2897181]
- fix: embed UnimplementedRegistrationServer [77031aa]
- feat: adapter credential domain (SetAdapterCredential/VerifyAdapterCredential) [6505a41]
- feat: VerifyPasswordByID internal RPC for adapter-resolved console logins [d43c4db]
- feat: Registration RPC (adapter-delegated account creation), SMTP mailer [2f61a06]
- feat: durable invalidation events, VerifyPasswordByID, ban/unban with event emission [111e5fc]
- feat: M1 account core — identity, sessions, recovery, generic adapter links, internal gRPC contracts, web API [53ff113]

