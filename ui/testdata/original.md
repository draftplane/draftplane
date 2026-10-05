# Members, Lending, and Branches

## Context

Toolshed's idea of a person is scattered text fields: `loans.borrower_name`, `holds.patron_name`,
`notes.author_name`, `tool_events.actor_name`, `tools.added_by_volunteer_id`. There's no unified
concept of "who" across the kiosk and desk screens, no branch-level separation, and no verified
membership — anyone can claim to be anyone.

This plan introduces a verified, card-backed membership and permission model for **member** people. It
is the first cut; **volunteer** people (who add tools to the shelves on their own) are explicitly
deferred, but the data model is shaped so they slot in later with no migration.

**This is a pre-production clean break.** There is no production data and no users. We drop the old
text-name columns and the `volunteers` table outright — no backfill, no lossy name-matching.

### Framing (don't lose this)

Toolshed is a **lending tool, not a security product** — it fills the missing checkout desk for
neighbourhoods that pool their tools. Safety is right-sized accordingly: we prevent *broken trust*
(un-forgeable cards, no cross-branch leaks of member details, no spoofable names), but we do **not**
gold-plate enforcement (no deposit escrow, no fines that survive a returned tool, no elaborate card-family
machinery as a v1 blocker). Every safety item below is included because it protects the pooled tools or
the members' confidence in them — not for compliance theater.

### The shape of the system, in one paragraph

A person joins at a branch desk with a photo ID. Toolshed verifies them, prints a member card, and resolves
them to a persistent **person**. Branch membership *is* the access model — if you're a member of the branch,
you can borrow that branch's tools; lapse, lose borrowing. Toolshed manages **roles** (coordinator/member)
itself, decoupled from any neighbourhood association's own officers. When a member walks up to the kiosk,
the card scan identifies *that member* (self-service mode) and all lending is grouped into a **visit** — one
trip to the shed of coherent lending that can span several tools. Checkout and return are first-class on
**both** screens: at the volunteer desk directly, and at the kiosk (assisted or fully self-service).

## Design decisions (settled — not open questions)

1. **Verified from day one.** Membership is proven with a photo ID at the desk, not claimed. Model and cards
   ship together; there is no spoofable interim.

2. **Single `people` table; members and volunteers are peers.** `type` is `'member' | 'volunteer'`. Only
   `'member'` is populated in v1, but the column and single-table design stay so volunteer people —
   including volunteers checking other volunteers' returns — slot in later with zero schema migration.
   `loans.person_id` / `notes.person_id` work regardless of person type.

3. **Toolshed is a minimal in-house card office.** Not a hand-rolled full membership suite, not a hosted
   service (which would add a vendor dependency conflicting with Toolshed's self-hosted ethos). It owns only
   what it must.
   - **Desk member** → Toolshed checks a photo ID + prints a card.
   - **Kiosk member** → the only path that forces a card office to exist; Toolshed issues its own cards for
     it (the desk does the human check underneath).
   - **Volunteer intake** → deferred (Toolshed-as-registrar validating volunteer badges).

4. **Toolshed-issued cards carry a check digit and a stamped stripe.** `kiosk` and `desk` are separate
   programs that both *verify*; only the card office holds the stamping key, so a stolen kiosk cannot mint.
   A key list falls out for free. (A single house PIN rejected: any kiosk could forge.)

5. **Resolution is always `(card_series, card_number)`, never name.** The card's printed number survives
   name and address changes. `people.email` is denormalized **display data only**, never a lookup key.

6. **The branch governs *borrowing*; Toolshed governs *roles*.** Branch membership determines who can borrow
   a branch's tools (granted at the desk, lapsing yearly). Toolshed's own role layer (coordinator/member)
   determines who can run a branch. **An association's own officer list is never read** — mirroring it is
   surprising (an absent treasurer would outrank the actual shed coordinator) and creates a sync burden.

7. **Paper card, not smart card** (v1). A smart card needs a branch *coordinator* to enrol it at a reader — a
   wall a casual neighbour won't climb. A paper card with a barcode lets a member self-serve at any kiosk
   that hasn't opted into photo checks (the default). Smart cards (coordinator enrolment + instant
   cancellation) are deferred as the larger-shed option.

8. **Sites are renamed `branches` and map 1:1 to a physical shed by street address.** Provisioning is
   **lazy**: the home branch auto-provisions on joining; the full list of sheds in a town populates a
   **desk-only** switcher; unprovisioned sheds show a "Set up" affordance. *Listing* sheds is cheap and
   private (automatic); *provisioning* is a public, consequential act (deliberate, attributable). Any member
   may provision; the founder becomes coordinator. (Large-shed caveat: the first member to click "Set up"
   becomes coordinator — accepted for v1; a future "association officer can claim coordinator" path is
   noted in Deferred.)

9. **The kiosk "session" is reconceived as a `visit`** — one trip to the shed of coherent lending, spanning
   tools and physical scans. Walking away for ten minutes starts a new visit. The physical scan is *not* a
   domain entity. See Phase 4.

10. **No self-checkout guard.** A member checking out a tool they donated is the normal small-shed flow, not
    a hole. Normal person attribution already shows who lent and who borrowed; nothing special is added.

11. **Categories stay deliberately lightweight and semantically open.** We do not know yet whether members
    will model a category as a long-lived shelf or an ephemeral project kit, so the model commits to
    neither: explicit create/list (no idempotent-ensure, no name uniqueness), an optional `shelf` label,
    and archival. A nullable `shelf` + creation metadata will let us *measure* which pattern emerges.

12. **Lapse freshness is asymmetric by screen, by design (v1).** Membership is the only thing read from the
    yearly renewal sheet, and reading it requires a volunteer at the desk. The **desk** screen re-checks
    renewals on every scan (fresh membership, no stored copy); the **kiosk** screen re-evaluates only at the
    member's next desk visit (limited by the card's printed expiry — a kiosk can't ask a volunteer). No
    renewal sheet is stored anywhere. The asymmetry is a known wart (Deferred: tighten it).

## Deferred (tracked, out of scope for v1)

- **Volunteer intake** — volunteers adding tools alone (`trusted_branches`, `badge-scan` /
  `desk_override`, check digits on outside badges). The "volunteer adds a tool alone" workflow waits on it;
  the person model is built general so it lands with no migration.
- **Tighten the desk/kiosk lapse asymmetry** (Decision 12) — smart cards + instant cancellation, short-lived
  kiosk cards with a desk membership check, or a stored renewal sheet.
- **Smart cards + enrolment** — larger-shed enrolment and instant cancellation.
- **"Association officer can claim coordinator"** override for the first-clicker-coordinator case in large
  sheds.
- **Optional explicit visit resume** across a walk-away (param exists; default is new-visit).
- **Multi-card households** (`member_cards` allows it; v1 creates one per person).
- **Self-serve data erase** — v1 ships soft Deactivate; hard Erase is a manual desk procedure.
- **Branch closing-for-adoption** — v1 hard-deletes empty branches.
- **Per-category rules / lending policies** (incl. the self-checkout *enforcement* gate, quorum) — categories
  are not an access separation in v1.
- **Categories-as-shelves** auto-labelling (read the shelf barcode → resolve category) — the `shelf` field is
  the seed.
- **Reminder / requested-return / waitlist loop** — a separate circulation effort. (The self-service
  *flows* are in scope; the member-facing reminder layer is not.)
- **Coordinator suspend that survives a renewal** (v1 Deactivate is self-reversible at the desk).
- **Cross-branch tool move** — deliberately omitted (a tool-loss surface; tools are cheap to re-add — retire
  and re-add at the right branch instead).

---

# Phase 1 — Data model

Clean break: new tables, new `person_id` columns, drop the old text-name fields in one migration set.
No backfill. Integrity is app-enforced per Toolshed convention (no FK constraints), but uniqueness invariants
are enforced with indexes (as the codebase already does).

### 1a. Rename `sites` → `branches`, add street addresses

```sql
alter table sites rename to branches;
alter table branches add column street_address text;            -- null for the founding shed
alter table branches add column short_name text;                -- branch short name (display + signage)
alter table branches add column kind text not null default 'shed';  -- 'shed' | 'home'
alter table branches add column founder_person_id uuid;         -- founder

create unique index branches_street_address on branches (street_address)
  where street_address is not null;
create unique index branches_home_founder on branches (founder_person_id)
  where kind = 'home';                                          -- one home branch per person (race guard)
```

`tools.site_id` is **kept** (not renamed) to limit blast radius during this large migration; the
table rename is enough for clarity. (Column rename is a later cosmetic pass if desired.)

### 1b. People and cards

```sql
create table people (
  id uuid default gen_random_uuid() primary key,
  type text not null,                 -- 'member' | 'volunteer' (only 'member' in v1)
  display_name text not null,
  email text,                         -- DISPLAY ONLY, nullable
  deactivated_at timestamptz,         -- soft Deactivate; null = active
  created_at timestamptz default now() not null,
  updated_at timestamptz default now() not null
);

create table member_cards (
  id uuid default gen_random_uuid() primary key,
  person_id uuid not null,
  card_series text not null,          -- 'paper'
  card_number text not null,          -- printed STABLE number (string-encoded)
  created_at timestamptz default now() not null
);
create unique index member_cards_series_number on member_cards (card_series, card_number);
```

### 1c. Branch membership + roles — the checkout-time borrowing source of truth

`branch_members` is the **authoritative checkout-time borrowing store** (resolved safety/design review
finding). `syncRenewals` materializes **one row per current member** (default `role='member'`); the yearly
renewal sheet is the *reconciliation* source, not the checkout-time source. `assertActiveMember` is a single
indexed lookup. This also makes future volunteer grants a plain insert (volunteers have no renewal).

```sql
create table branch_members (
  id uuid default gen_random_uuid() primary key,
  branch_id uuid not null,
  person_id uuid not null,
  role text not null default 'member',   -- 'coordinator' | 'member'
  source text not null default 'renewal', -- 'renewal' | 'grant' (forward-compat for volunteers)
  created_at timestamptz default now() not null
);
create unique index branch_members_branch_person on branch_members (branch_id, person_id);
```

### 1d. Visits (replaces `volunteers`)

```sql
create table visits (
  id uuid default gen_random_uuid() primary key,   -- desk-generated, never kiosk-asserted
  person_id uuid not null,
  handle text not null,               -- human-readable, e.g. 'amber-otter-42'
  created_at timestamptz default now() not null,
  last_active_at timestamptz default now() not null
);
create unique index visits_handle on visits (handle);   -- handles are human-referenced
```

No `category_id` — a visit spans categories; category is conveyed per-action.

### 1e. Categories — lightweight + open (Decision 11)

```sql
alter table categories add column shelf text;           -- optional stable label for volunteers
alter table categories add column archived_at timestamptz;
alter table categories add column created_by_person_id uuid;
```

No name-uniqueness constraint (ephemeral project kits may legitimately repeat names). A default category is
created per branch (see Phase 2 `ensureDefaultCategory`).

### 1f. Attribution columns; drop legacy name fields

```sql
alter table loans add column lent_by_person_id uuid;
alter table loans add column borrowed_in_visit_id uuid;
alter table loans add column returned_to_person_id uuid;
alter table loans drop column lent_by_volunteer_id;
alter table loans drop column returned_to_volunteer_id;

alter table notes add column person_id uuid;
alter table notes add column visit_id uuid;
alter table notes drop column author_volunteer_id;
alter table notes drop column author_name;

alter table holds add column person_id uuid;
alter table holds drop column patron_name;
create unique index holds_tool_person on holds (tool_id, person_id);  -- one hold per person; real upsert

alter table tool_events add column person_id uuid;
alter table tool_events drop column actor_name;

drop table volunteers;
```

### 1g. Card + PIN state

```sql
create table kiosk_pins (
  id uuid default gen_random_uuid() primary key,
  person_id uuid not null,
  family_id uuid not null,            -- reset family; reuse of a reset PIN cancels the family
  pin_hash text not null,             -- sha-256 of a >=4-digit PIN with a per-person salt
  kiosk_id text not null,
  expires_at timestamptz not null,
  cancelled_at timestamptz,
  created_at timestamptz default now() not null
);
create unique index kiosk_pins_hash on kiosk_pins (pin_hash);

create table cancelled_cards (
  card_number text primary key,
  expires_at timestamptz not null     -- swept after expiry
);

create table pin_reset_codes (
  code text primary key,              -- never printed on a receipt
  person_id uuid not null,
  kiosk_id text not null,
  return_screen text not null,        -- re-verified at the reset screen
  code_check text not null,           -- S256 only
  consumed_at timestamptz,            -- single-use; consumed atomically; replay cancels issued PINs
  expires_at timestamptz not null,    -- ~60s
  created_at timestamptz default now() not null
);
```

The desk's photo-check `state` is a short-lived **stamped slip**, mandatory, tied to the in-flight
reset context (kiosk_id, return_screen, code_check, kiosk `state`).

### 1h. Types (`packages/db/src/types.ts`)

- Rename `Site` → `Branch` (+ `streetAddress`, `shortName`, `kind`, `founderPersonId`).
- Remove `Volunteer`. Add `Person`, `MemberCard`, `BranchMember`, `Visit`.
- `Category`: add `shelf`, `archivedAt`, `createdByPersonId`.
- `Loan`: `lentByPersonId`, `borrowedInVisitId`, `returnedToPersonId` (drop volunteer fields).
- `Note`: `personId`, `visitId` (drop `authorVolunteerId`/`authorName`).
- `Hold`: `personId` (drop `patronName`). `ToolEvent`: `personId` (drop `actorName`).
- Precise types; no `any`/`unknown` intermediaries (per README.md).

**Atomicity note:** Phase 1's drops break `truncateAll` (hard-codes `volunteers`/`sites`),
`seedSiteAndCategory`, and the dev seed across all three packages. Phase 1+2 are **one green-able unit**;
the test-helper + seed rewrite is a Phase-2 task, and `seedSiteAndCategory` must seed a person +
membership for permission tests.

# Phase 2 — DB layer (repositories + lib)

### Repositories

- New: `people.ts` (`create`, `findById`, `update`; **no** `findByEmail`-as-identity), `member-cards.ts`
  (`create`, `findBySeriesAndNumber`, `findByPersonId`), `branch-members.ts` (`upsert`,
  `findByBranchAndPerson`, `findByPerson`, `listCoordinators`, `setRole`, `remove`, `replaceForPerson`,
  `countMembers`), `visits.ts` (`create`, `findById`, `touch`), `kiosk-pins.ts`, `pin-codes.ts`,
  `cancelled-cards.ts`.
- Rename `sites.ts` → `branches.ts` (+ `findByStreetAddress`, `findOrCreateHome` atomic upsert).
- `categories.ts`: add `create`, `listByBranch(includeArchived?)`, `archive`, `findById` (shelf/archived
  columns).
- **Remove** `volunteers.ts`.
- `repositories/index.ts`: drop `volunteers`; add the new repos + renamed `branches`.

### Lib (all DB writes go through here, per README.md — including PIN persistence)

- `lib/people.ts`
  - `resolvePersonFromCard(repos, { cardSeries, cardNumber, displayName, email })` — the single
    find-or-create path; refreshes display name/email; **reactivates** a soft-deactivated person at the desk.
  - `deactivatePerson(repos, personId)` — sets `deactivated_at`, cancels the person's kiosk PINs + live
    cards. Enforces the lifecycle guards below.
- `lib/branches.ts`
  - `syncRenewals(repos, personId, renewals)` — reconcile `branch_members` (source `'renewal'`) against the
    *provisioned* branches the renewal sheet currently names for the person; **never** removes rows on a
    read error or a sheet that comes back empty for no stated reason.
  - `ensureHomeBranch(repos, person)` + `ensureDefaultCategory(repos, branchId, person)` — zero-friction
    floor: a home branch and one default category on first joining.
  - `provisionBranch(repos, person, shed)` — idempotent on `street_address` (concurrent loser joins as a
    member, doesn't error); founder gets `role='coordinator'`; creates the branch's default category.
  - `assertActiveMember` / `assertCoordinator` — checkout-time checks over `branch_members`.
  - `assertPersonCanBorrowTool(repos, personId, toolId)` — resolves `tool → category → branch →
    membership`; called inside the lib write functions so `check_out`/`add_note` (which only carry a
    `tool_id`) enforce branch access uniformly.
  - Lifecycle: `transferCoordinatorAndRemove`, `promoteEarliestMember`, `garbageCollectIfEmpty` (keys off
    materialized membership; requires a positive, non-errored zero-member result).
- `lib/visits.ts`
  - `resolveVisit(repos, { personId, visitId? })` — **lazy issuance**: present-and-owned → touch + return;
    **present-but-not-owned → treat as absent** (mint fresh, never touch/echo another person's visit);
    absent → mint (desk id + generated handle). A visitless scan by a person within a few seconds of a
    prior mint **coalesces** to that visit (mitigates parallel-first-scan fragmentation).
- `lib/pins.ts` (persistence half of the kiosk, kept in `@toolshed/db` so the layering invariant holds)
  - `issueKioskPin(repos, { personId, kioskId, familyId? })`, `redeemKioskPin` (hash lookup, reset,
    family reuse-detection → cancel family + associated cards), `consumeResetCode` (atomic
    `UPDATE … WHERE consumed_at IS NULL RETURNING`; replay → cancel PINs issued from the code),
    `isCardCancelled(cardNumber)`, `cancelPersonPins(personId)`.
- `lib/loans.ts` / `lib/holds.ts` / `lib/notes.ts`: each write takes `personId` (+ `visitId` where
  applicable), calls `assertPersonCanBorrowTool`, writes the new columns. **`placeHold` upserts on the unique
  `(tool_id, person_id)` index** (replaces the non-atomic delete-then-create). Add `retireTool(repos,
  { personId, toolId })`.
- `lib/categories.ts`: `createCategory`, `listCategories`, `archiveCategory` (with access checks).
- **Remove** `lib/volunteers.ts`. Barrel `index.ts`: drop `volunteerLib`; add `personLib`, `branchLib`,
  `visitLib`, `pinLib`, `categoryLib`.
- `__tests__/helpers/mock-repos.ts`: drop `volunteers`; add the new repos.

# Phase 3 — Card package (`packages/cards`) — stateless only

`@toolshed/cards` is a **leaf**: pure check digits + barcode parsing, **no DB edge** (persistence lives in
`lib/pins.ts`). Dependency: `bwip-js`.

```
packages/cards/src/
  index.ts
  keys.ts        Ed25519 stamping key load (TOOLSHED_STAMP_PRIVATE_KEY / _PUBLIC_KEY); key list w/ `kid`
  stripes.ts     stampCard / verifyCard (signature + fields only), PIN gen + hash
  barcode.ts     Code 128 encode/decode, check digit, series prefix
  photo.ts       photo-ID hash + compare (perceptual, never stored raw)
  middleware.ts  extractCardNumber; verify (delegates cancellation + person-active check to the caller's lib)
  types.ts
```

- **Card fields (mandatory):** `iss` (branch short name), `aud` (`toolshed-desk` or `toolshed-kiosk`, per
  issuing screen), `sub`=`personId`, `personType`, `iat`, `nbf`, `exp` (~1y), `jti`, reserved `scope`
  (`full` for now). Each verifier enforces its own `aud` and the expected `iss`. The stripe carries `kid`;
  verify against current+previous key.
- `readRenewals` reads the yearly renewal sheet filtered to **paid** memberships; tolerant of lapsed members
  being absent (see Phase 6 for how the switcher reconciles that).
- Env: `TOOLSHED_CARD_PREFIX`, `TOOLSHED_PHOTO_DIR`, `TOOLSHED_RENEWAL_SHEET`.

# Phase 4 — Visits: the kiosk attribution model

A *visit* is one trip to the shed — a coherent burst of lending spanning tools and physical scans. Not the
scan (survives a dropped card), not the person (spans visits). Walking away ends the visit → the next scan
mints a new visit (the correct separation).

**Lazy issuance.** Every lending/browse flow takes an **optional** `visit_id`:
- **Absent** → `resolveVisit` mints (or coalesces to a just-minted) visit; the flow acts, attributed to it,
  and prints `visit_id` + `handle` prominently with a note to scan the receipt on later checkouts.
- **Present + owned** → validated, action attributed, `last_active_at` touched.
- **Present + not owned** → treated as absent (mint fresh; never touch/echo another person's visit).

Solves the first scan (which legitimately omits the id) and the walk-away with one rule. Attribution to the
**person** is always correct (it comes from the card), so a dropped `visit_id` only fragments grouping —
never misattributes. `register_volunteer` is **deleted**; the visit is established by the first carded
scan; category is per-action; identity is the card.

# Phase 5 — Card office flows (minimal in-house office, in `packages/desk`)

**Topology (documented constraint):** the **desk program is the card office**. `kiosk` depends on it for
card *issuance/renewal* but **not** for *verification* (env-distributed public key). Screen handlers are
**thin adapters** over framework-agnostic office logic (in `@toolshed/cards` checks + `lib/pins.ts`
persistence) so the orchestration is unit-testable and movable if the office ever splits out.

v1 flows only: `photo_check` + card print, and `pin_reset`.

- `desk/screens/join` — validate `kiosk_id`, **`return_screen` against a per-kiosk exact-match list**
  (`toolshed-desk`) / **any screen of the same kiosk** (`toolshed-kiosk`), `code_check` **S256-only**,
  `state`. Ask for the photo ID; stash the tied `state`/check in a stamped slip.
- `desk/screens/photo` — compare the photo ID → person; `readRenewals`; `resolvePersonFromCard`;
  `ensureHomeBranch` + `ensureDefaultCategory`; `syncRenewals`; **discard the photo**; print a Toolshed
  card; return to the kiosk `return_screen`. Set `Print-Policy: no-receipt`.
- `desk/screens/pin` — `pin_reset` (atomic consume + check verify + **re-verify the tied `return_screen`** →
  issue a stamped card + PIN) and `pin_change` (rotate; reuse → cancel family). The **desk** screen's renewal
  path performs a fresh renewal-sheet read → `readRenewals` + `syncRenewals` (Decision 12); the **kiosk**
  screen's renewal does not (re-sync only at the next desk visit).
- `desk/keys` — public key(s) with `kid`.
- Throttle `join`, `pin`, and kiosk scans (per-kiosk / per-card).
- Kiosk discovery is built with Phase 7 (it's a kiosk concern), not here.

Seed two **public** kiosks: `toolshed-desk` (exact-match screens) and `toolshed-kiosk` (any screen).

# Phase 6 — Desk integration

- `desk/join/page.tsx` — "Join with a photo ID" → check against `desk/screens/join`.
- `desk/print/route.ts` — swap the slip for a stamped card; keep the card number in the desk's
  **`volatile; locked; same-desk`** cache; return to `/`. Define desk renewal handling (fresh renewal-sheet
  read per Decision 12).
- `lib/cards.ts` — `getCurrentPerson()` / `requireCurrentPerson()`: scan → verify (stripe + `aud=toolshed-desk`
  + `iss`) → **cancellation check + load person, reject if missing/`deactivated_at`**. Double-scan protection
  (a second scan or a strict screen check) on all lending actions.
- **Branch context (desk-only lens):** switcher listing the person's branches. Provisioned → switchable;
  unprovisioned → "Set up" pill (`provisionBranch`); **lapsed members who are absent from the renewal
  sheet** can't be auto-listed — offer a manual "add by card number → Request renewal" entry rather than a
  phantom pill. The home branch is the default active context. Active branch is screen state only; never
  sent to the kiosk.
- **Category picker** — replace the hardcoded `categories[0]` with a real list/picker (active + archived);
  create/archive actions.
- **Lending actions** — writes call `requireCurrentPerson()` then `assertActiveMember`/`assertCoordinator`,
  pass `personId` to lib (no `patronName`/`authorName` in forms). Reads filter to accessible branches.
- **Components** — drop the `patronName`/`authorName` inputs; show the scanned person; render
  `display_name` (with `(deactivated)` suffix) on notes/holds/events; render the **visit handle** on
  loans/events plus a visit-filtered view ("everything `amber-otter-42` borrowed"). Add a `retire_tool`
  desk action. `ToolWithNotesAndHolds` gains resolved person + visit display info.
- **Member settings** — Deactivate (soft). Erasure flow enforces the lifecycle guards.

# Phase 7 — Kiosk integration (barcode scanner + card checks)

- `packages/kiosk/src/index.ts` — replace `KeyboardWedgeInput` with the scanner SDK's serial input (2.4
  ships it); attach card checks (`requireScannedCard`) using `@toolshed/cards` to verify the stripe
  (+ `aud=toolshed-kiosk` + `iss`) and the lib to check cancellation + person-active; **require a photo
  match for any branch but the home one** (reject a bare card number). Advertise the card office for
  discovery here.
- Thread the validated person context to every flow handler.
- **Remove `register_volunteer`** and its test. **Update `README.md`'s kiosk setup command** to the serial
  input (the keyboard `toolshed kiosk --wedge …` line becomes invalid).
- Flows:
  - Drop all `volunteer_id` params (person comes from the card); add optional `visit_id` (mint+echo on
    absence).
  - `check_out` takes a per-action `category_id`; **the kiosk first calls `list_categories` and surfaces the
    target branch/category** before lending (target-clarity replaces a cross-branch move). Error helpfully
    if the category's branch is unprovisioned / the person lacks access.
  - New: `list_categories` (accessible branches), `create_category` (branch + name + optional `shelf`; no
    idempotent-ensure, no uniqueness), `retire_tool`.
  - `add_note` / `reply_to_note` / `place_hold` attribute via `person_id`; rely on
    `assertPersonCanBorrowTool`. These are the **self-service** surface (assisted + fully self-service) —
    first-class, not kiosk convenience.
- Update `__tests__/integration.test.ts` for cards, visits, categories, retire.

# Lifecycle rules (cross-cutting)

- **Sole-coordinator voluntary departure** (deactivate / leave-branch) with members remaining → **force
  designation of a new coordinator first** (`transferCoordinatorAndRemove`), enumerated per branch.
- **Involuntary departure** (membership lapsed, discovered at renewal; can't prompt) → **auto-promote the
  earliest-joined remaining member** (`promoteEarliestMember`).
- **Last member leaves** → **hard-delete** the branch, its categories, and its tools
  (`garbageCollectIfEmpty`), only on a confirmed non-errored zero-member result. (1:1 `street_address` key
  means a later neighbour founds it fresh.)
- **Deactivate** (soft, v1): set `deactivated_at`, **cancel kiosk PINs + live cards**, keep PII + card
  record (a desk visit reactivates), render `(deactivated)`. Loan history preserved everywhere.
- **Erase** (data request, deferred manual): scrub `display_name`→"Former member", null `email`, delete
  `member_cards`, keep loan history (now anonymous). A future joining creates a fresh person.
- **Left a branch** (per-branch access loss): no anonymization; past loans keep the real name; the person
  drops from the active roster.
- **Cancellation ≠ erasure of history.** Losing borrowing never rewrites past attribution.

# Environment variables

- `TOOLSHED_STAMP_PRIVATE_KEY`, `TOOLSHED_STAMP_PUBLIC_KEY` — Ed25519 keypair (+ `kid`); the card office
  stamps private, verifiers use public.
- `TOOLSHED_CARD_PREFIX`, `TOOLSHED_PHOTO_DIR`, `TOOLSHED_RENEWAL_SHEET`.

# Build order

1. **Phase 1 + 2** (one green-able unit) — data model + repos/lib + test-helper/seed rewrite. Foundation.
2. **Phase 3** — `@toolshed/cards` (stateless checks + barcode parsing). Independent.
3. **Phase 5** — card office flows in the desk (thin adapters). Testable by hand at the running desk.
4. **Phase 6** — desk joining + branch switcher + category picker + person/visit-attributed screens. The
   desk requires a scan.
5. **Phase 7** — kiosk scanner input + card checks + visits + category/retire flows. The kiosk requires a
   card.

(Phase 4 is a contract realized inside Phases 2/6/7, not a separate build step.)

# Verification

1. `npm test` passes after each unit; the Phase 1+2 cut is green together.
2. Card printer configured; a desk join resolves to a person; home branch + default category
   auto-provision; checking out a tool / placing a hold / adding a note shows correct person + visit
   attribution.
3. Card fields (`iss`/`aud`/`jti`/`kid`) verified; a `toolshed-kiosk` card is rejected at the desk screen
   and vice versa; a return screen off the list is rejected; a reset code can't be replayed.
4. Branch switcher lists the person's branches; "Set up" provisions (founder=coordinator); a lapsed member
   is offered a manual request-renewal, not a phantom pill.
5. A renewed member of a provisioned branch gains borrowing automatically; a lapse cancels **desk**
   borrowing at the next scan and **kiosk** borrowing by the next desk visit.
6. A member at the kiosk scans a card; the first scan mints a visit + handle; later scans reuse it; walking
   away yields a new visit; lending spans categories under one visit; the handle is visible at the desk.
7. `list_categories`/`create_category`/`retire_tool` work at the kiosk and at the desk; a wrong-branch tool
   is fixed by retire + re-add (no move).
8. Sole-coordinator deactivation forces handoff; last-member departure hard-deletes the branch; deactivated
   members render `(deactivated)` with loans intact and their PINs cancelled.
9. Self-service: a member places holds / adds notes at the kiosk, attributed to that member.
