# Public codes on links (CR-3)

Decided 2026-09-19, for the website's contract request CR-3: how does a
surface that is not the Switch adapter show and resolve the Switch friend code?

## What was wrong with reading the subject

A link's `subject_id` is the console's own user id. It is opaque, adapter-owned,
and not the number people read out to each other. The website used to format a
subject into something that looked like a friend code, then learned the friend
code is a separate value the adapter mints (`SW-dddd-dddd-dddd`, unique, and
regenerable on request). It then called the adapter's own HTTP endpoint to get
it, so the website depended on a second service's private API.

## Decision

The adapter still mints the code and stays its only author. It **publishes**
the code onto its active link in the core. Everyone else reads it from the core.

- `Links.SetLinkPublicCode(link_id, public_code, lookup_key)`: the adapter
  calls this when it mints or regenerates a code. It replaces the previous code
  in one statement, so a retired code stops resolving straight away. An empty
  `public_code` unpublishes.
- `Links.GetActiveLinkByAccount` and `GetLinkBySubject` now also return
  `public_code`. The website's existing `GetActiveLinkByAccount("switch", …)`
  call gets the friend code without any new call.
- `Links.ListAccountLinks(account_id, namespace?)`: every active link with
  its subject and code, for pages that list them all.
- `Links.GetLinkByPublicCode(namespace, public_code)`: resolves a code as a
  person typed it (for "add friend by code").

The core stores the code and never mints, validates the shape of, or renders
it. No console knowledge enters the core.

## Format

- `public_code` is the display form, up to 64 characters, with no control
  characters. For Switch that is `SW-1234-5678-9012`.
- `lookup_key` is the canonical form the adapter compares on itself (for
  Switch, the 12 digits `123456789012`). The core reduces it to lower-case
  letters and digits. If it is left empty, the core uses the display form
  reduced the same way (`sw123456789012`).
- A lookup reduces the typed input the same way, then matches it against
  either the key or the reduced display form. So `SW-1234-5678-9012`,
  `sw 1234 5678 9012` and, given the digits key, `123456789012` all resolve.
- A key is unique per namespace among **active** links: a code resolves to at
  most one account. Publishing a key that another active link holds returns
  `AlreadyExists`, and the adapter mints again.
- Only an active link can carry a code. Unlinking clears the code, so a later
  relink never brings back a code that may have been handed to someone else.

## Why not the alternatives

- **Core-minted field**: the core would have to know the Switch code shape and
  its collision rules. That breaks the console-neutral rule the architecture
  test enforces in spirit, if not by import.
- **Adapter read contract only** (the interim): every surface would need the
  adapter's address and key, and resolving a code would mean scanning the
  adapter's store. It also fails whenever the adapter is down, even though the
  code has not changed.

## Until the adapter publishes

`public_code` stays empty until the Switch adapter calls `SetLinkPublicCode`.
That needs a one-line change there wherever it mints or regenerates, plus a
one-off backfill of existing codes. Until then the website should fall back to
the adapter endpoint when `public_code` is empty.
