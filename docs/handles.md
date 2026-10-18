# Permanent account handles

A handle is the account's public identifier, distinct from its editable display
name. It is 6–15 ASCII letters or digits, begins with a letter, preserves case,
and is globally unique ignoring case. Registration requires it; omitting the
display name defaults that name to the handle. Neither a user, an adapter nor a
case-only edit can rename it. There is no SetHandle API.

Core owns format validation, reserved identities and profanity policy. Brands
and staff roles are reserved as whole names; OpenPak and XboxLive prefixes are
also reserved. Test handles such as XboxTest and SwitchTest are allowed. The
English profanity baseline matches whole identifiers, clear camel-case words,
common leetspeak, repeated letters, numeric affixes and common gamer prefixes/suffixes. It does not match
arbitrary substrings of ordinary names. The lists are reviewable in
`internal/identity/handle*.go`; additions require positive and false-positive
examples in tests. Availability is public; callers must rate-limit checks.

`Identity.CheckHandle` is advisory. Registration and home-only `ClaimHandle`
claim names atomically. `ClaimHandle` exists only for one assignment to a
legacy account, with identical retries accepted. Account creation emits
`account_created`; a legacy assignment emits `handle_assigned`, inside the
same transaction. A handle never changes, so there is no handle_changed event.
`GetAccount`, `ListAccounts` and HTTP `whoami` all return it.

The database owns claims, format and immutability constraints. Legacy link
public codes also reserve names during transition. Name-bearing links cannot
publish another account's claimed handle, or diverge from their own handle.
PlayStation link subjects must equal the chosen handle too. Numeric console
identifiers and friend codes stay independent.

Deletion requests disable access but retain the handle. Home operators explicitly
invoke `FinalizeAccountDeletion` only after the deletion request; it destroys
core credentials and emits `account_deleted`. The database starts a 90-day hold
at permanent deletion. During the hold neither lookup nor signup identifies the
old owner. After it expires a new account may claim the handle, with a new
account UUID and new console identities. Repeating finalization does not extend
the hold. PSN link subjects are retired, preserving old link IDs for history;
adapters must retire local name reservations on account_deleted too.

The gRPC API is internal-key authenticated. Claim and finalization are home-only.
No public endpoint can claim a legacy account or finalize deletion. HTTP and
console registration both require a handle; there is no pending-account bypass.
The older Go registration helpers are for legacy fixtures only. New transports
must call RegisterWithHandle.

## Network contract

Use the handle verbatim as Wii U NNID, Xbox/Xbox 360 gamertag, and PlayStation
online ID. Never derive it from email or display name or append a collision
suffix. Keep existing identity IDs stable during the approved alpha migration.
A collision is an error to investigate, never permission to invent another name.
Use the display name where a protocol asks for a display name or nickname,
truncated only when a client buffer requires it. Switch and 3DS numeric friend
codes and Wii/DS per-game codes remain separate.

Future PS4/PS5 account services must use the same core handle. Prefer sharing
the PlayStation identity/link with the PS3 account service; do not create a new
username namespace or format rules. Numeric platform IDs remain opaque and
independent. PS4/PS5 service implementation is outside this rollout.

## Alpha migration

`go run ./cmd/handle-report -choices <private-json>` reads the database using
ACCOUNT_DATABASE_URL, in a read-only transaction. `-snapshot <private-json>`
can use an exported read-only snapshot instead. It prints names, selected
handles, affected networks and unresolved issues, never emails or credentials.
Names and account IDs belong in private scratch artifacts, never commits.

Review every existing account choice and console-bound identity before live
changes. Stop enrollment/backfills during migration. Migrate local names and
core links together, retaining XUIDs, PIDs, link IDs and PlayStation user IDs;
PSN subject changes must update the existing link, not unlink/relink. Claim
handles only after the owner has reviewed the full report. Existing console
caches may require re-enrolment; no general rename feature or hidden override
is exposed to users. Deploy the website last, after updated adapters and core.
The website drops old OP codes completely; there is no alias compatibility.


The reviewed apply command is `go run ./cmd/handle-migrate -choices <private-json>
-snapshot <private-json>`. Its default is read-only; `-apply` writes inside one
transaction, rejects stale account/link state, preserves link IDs, emits one
assignment event per newly assigned account and adds the post-backfill constraint
requiring a handle on active accounts. It is idempotent for the same reviewed
choices. Stop adapters/backfills first, then apply core, update NNIDs with
nn-account's offline `cmd/nn-handle-migrate`, and use xb-account's existing audited
internal applier for its canonical handle. Bound consoles require explicit
operator force approval and profile re-enrolment. Recover Xbox 360 profiles to
refresh their cached gamertags. Resume adapters and verify every name before the
website's OP-code retirement migration and signup changes are deployed.
