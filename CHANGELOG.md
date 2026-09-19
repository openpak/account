# Changelog — account

Generated from git history on 2026-09-15. `git log` stays the source
of truth; this file is the readable summary.

## v0.9.2 — 2026-09-19

- Emails in the website's languages: verification, email change (new and old address) and
  password reset follow the request's `Accept-Language` (German, French, Brazilian
  Portuguese, Italian, Spanish; English otherwise) — `internal/mailtext`. The website sends
  the page language; console registrations over gRPC stay English.

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

