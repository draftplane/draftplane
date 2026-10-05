# Meridian Station Data: Calibration-Aware Readings and Tiered Retention

## Background and motivation

Today the answer to "which instrument measured this" lives in a handful of free-text columns —
`readings.sensor_label`, `uploads.station_name`, `readings.units`, `alerts.station_label`,
`daily_summary.source_note` — and nothing ties them together. Meridian keeps no history of calibrations,
has no retention policy at all, and stores corrections by overwriting values in place, so fixing one bad
offset quietly changes years of stored data with no way back.

This year showed what that costs. A probe swap at `HLLW` in March, with the new offset typed into the old
settings screen, shifted six winters of humidity readings at once, and the growers' frost-hour totals moved
overnight. In August a single day's file from `RVRB` was stored three times after modem retries, tripling
the daily rain figure that went into the flood wardens' weekly bulletin.

This plan replaces all of that with a calibration-aware, instrument-level data model for the network's
**fixed** stations. The core loop: a logger sends a file, Meridian decodes it into raw readings tied to an
**ingest batch**, and each column resolves to a persistent **channel** (one quantity, one physical sensor,
one mounting height). Calibration records adjust raw values at read time, so recalibrating never rewrites
what a sensor reported. One scheduled job computes hourly and daily rollups from the calibrated values, and
retention tiers decide how long each resolution stays in the hot database. Operators use both the console
and alert rules that run over the rollups.

**Mobile** platforms (two survey vans, the river buoys) are deliberately out of scope for this cut, but the
schema is shaped so they arrive later without a migration.

Raw readings are kept exactly as they are. Everything derived from them is a **clean break**: the
`daily_summary` table and the hand-maintained `monthly_extremes` sheet are dropped and rebuilt from raw,
with no attempt to reconcile old and new figures and no fuzzy matching of station names.

### Keeping the rigor proportionate

Meridian is a measurement network, not a forecasting product; it exists so the valley's growers, road
crews and flood wardens get numbers they can act on. The engineering effort is calibrated to that. We do
prevent *silent corruption*: overwritten raw values, files counted twice, unit mix-ups, a calibration
applied on top of itself. We do **not** gold-plate: no per-reading checksums, no rollups finer than an hour,
no sensor-health scoring treated as a launch blocker. Each safeguard in this plan earns its place by
protecting the figures people rely on, or their trust in those figures.

## Ground rules

None of these is reopened during the build.

1. **Raw values are written once.** The decoder inserts a reading and nothing ever updates it. Every
   correction is layered on top, as a calibration record or a quality flag; no code path edits a raw row.

2. **Every measured quantity is a channel, in one table.** `quantity` is text, checked against a short list
   (`'air_temp' | 'rel_humidity' | 'rain_tip' | 'wind_speed' | 'wind_dir' | 'soil_temp'`). Only fixed
   stations write to it in v1, but a van's thermometer is a channel like any other, so mobile platforms
   need no schema change when they come. `readings.channel_id` and `flags.channel_id` do not care what
   carries the sensor.

3. **Derived data has one producer: the rollup job.** No trigger cascades, and no materialized views
   refreshed by whichever reader arrives first. The hourly tier is computed from calibrated raw values, one
   row per channel per hour; the daily tier is computed from the hourly tier and never from raw, so the two
   cannot disagree. Mobile rollups wait, because they need position as well as time.

4. **One writer for the rollup tables.** The console and the alert evaluator are readers; only the rollup
   job holds write access to the rollup tables, so a misbehaving reader has no way to corrupt them.
   Every other process gets a read-only role. A single read-write role for all processes was considered and
   rejected, since any reader could then skew a daily figure without anyone noticing.

5. **UTC hours are the only grouping.** The network covers two time zones and both observe daylight
   saving; station-local time is never a grouping key. `stations.tz` exists only so the console can label
   its axes.

6. **Hourly values are medians.** One spike (a spider on an anemometer cup, a bird on a rain gauge) cannot
   move a median, where it would move a mean. Rain tips are the exception and are summed, never averaged.

7. **Flags add, they never remove.** A suspicious reading gets a flag with a reason code and the version of
   the rule that raised it, so history can be replayed when a rule changes. Nothing is deleted for looking
   wrong.

8. **Calibrations cover time ranges that never overlap.** `valid_from` is required and `valid_to` optional;
   an exclusion constraint in the database, not application code, forbids two records covering the same
   instant on one channel.

9. **A file is ingested at most once.** Its batch identity is the SHA-256 of its bytes, so a logger that
   re-sends after a dropped call gets a no-op.

10. **SI units from decode onward.** The decoder converts every value and keeps the source unit beside it.
    A logger set to Fahrenheit is the decoder's problem and never a query's.

11. **Retention is set per tier.** No station gets a longer raw window than another; requests for old raw
    data are served from the cold archive instead.

12. **Manual changes are attributed.** Manual flags and calibration records store the operator's console
    handle and a note. There is no approval step.

13. **The file decides its decoder.** The decoder sniffs the header rather than trusting station
    configuration, so a logger replaced by a different model in the field works from its first file.

## The batch model

Where a reading came from is answered by its batch. Each reading carries `batch_id`, and each batch records
the station, the file name exactly as the logger sent it, the logger family, and the hash of the file. An
operator who doubts a value opens its batch in the console, and from there the original file in the
archive.

The content hash is the batch's whole identity. File names repeat, modem retries re-send entire files, and
two loggers can produce the same name on the same day; hashing makes all three harmless, since a replayed
`batch_id` can only duplicate a *log line* — it can never duplicate a reading.

Before any reading is inserted, the file is copied into the archive at
`<station code>/<yyyy>/<mm>/<sha256>`. Copying comes first so the archive is always a superset of the
database, and a copy that fails aborts the ingest before anything is written.

A logger that loses power mid-write sends a truncated file, and the complete file on its next call. They
hash differently and both become batches. The second batch's early readings collide with the first's on
`(channel_id, at)`, the unique index skips them, and only the new tail goes in; `reading_count` records what
was actually inserted, which is how the console marks a partial file.

Logger clocks drift. When a batch's `last_reading_at` runs more than ten minutes ahead of `received_at`,
the batch is accepted with a `clock_ahead` flag, and the step check is relaxed for an hour after the clock
is corrected.

A batch is not a unit of correction. Fixing a bad value never means editing or replacing its batch: the
batch stays exactly as received, and the fix is a calibration record or a flag on the readings it produced.
That keeps the archive, the batch rows and the raw readings in agreement for the life of the network.

## Station and sensor lifecycle

- Retiring a station sets `retired_at`. Its channels stop accepting batches and its daily history remains
  queryable indefinitely.
- Removing a sensor sets `removed_at`; readings stamped after it are rejected at ingest with a message that
  names the sensor, which catches loggers nobody reconfigured after a swap.
- Every station marks one air-temperature channel as its reference for the console's headline figure. If
  the reference sensor is removed, the earliest-installed remaining sensor becomes the reference, and a
  station with none left shows no headline figure until an operator picks one.
- A mast moved across a field becomes a retired station plus a new one, since a new siting class and
  elevation change what every reading means; the new row's `succeeds` column keeps the long record drawable
  as one line.
- Calibration records are closed, never edited, so the sequence of corrections is preserved as data.
- v1 offers no deletion of anything. Retiring and removing cover every case seen so far.

## Risks and rejected alternatives

- **Drift that no rule catches.** A sensor that slowly reads warm is a silent-drift failure mode, and none of
  the three v1 rules sees it. Comparing neighbours would, but every frost hollow and sheltered gully would
  look faulty. We accept the gap until a year of calibrated data exists to tune against.
- **Backdated certificates.** A lab certificate that arrives a month late forces a recompute of every
  covered hour. The cost never exceeds one channel and one month, and we judged it acceptable rather than
  freezing old rollups.
- **Means instead of medians** were considered for their simplicity and rejected: one bird on a rain gauge
  or spider on a cup would move the hour.
- **A queue service for rollups** was considered and rejected in favour of a pending table; at this volume
  a table is easier to inspect and cannot lose work.
- **Recomputing in place** (adjusting an hour by the difference a new reading makes) was considered for its
  speed and rejected: an error in one pass would then persist into every later pass.
- **Storing calibrated values** beside raw ones would make reads cheaper, but a backdated certificate would
  then mean rewriting stored rows, which is the exact failure this plan exists to remove.
- **Column maps drifting from the hardware.** A technician who swaps a probe and forgets the column map
  produces readings filed under the wrong channel. The removal check catches the common case (readings
  after `removed_at`); a swap between two mounted sensors is still invisible, and we accept that for v1.
- **Open question:** should a retired station's raw archive be kept forever, or follow the same expiry as
  live stations? Current answer: the same expiry, revisited if researchers ask.

## Out of scope for v1 (tracked)

- **Mobile platforms**: vans and buoys need position-aware channels and rollups. The `channels` table
  already admits them; the rollup key does not.
- **Per-reading uncertainty** from calibration certificates, propagated through rollups. Real statistics
  work, and nobody has asked for it.
- **Radar and satellite overlays** in the console.
- **Open data downloads** for the university's hydrology group, who keep getting a monthly CSV by hand.
- **Transcribing the 2016–2019 paper logbooks**, a volunteer project on its own timeline.
- **Sub-hourly rollups.** The flood wardens asked for fifteen-minute aggregates; the flood rules read raw
  for that window instead.

# Execution plan

## Phase 1 — Schema

Every schema change ships in a single migration, `0007_calibrated_readings.sql`, run while ingest is paused.
Loggers buffer at least 72 hours themselves, so a short pause costs nothing. Raw readings are copied rather
than rewritten: the new `readings` table is filled from the old one, with `channel_id` resolved by the label
mapping script, and the old table survives one release as `readings_legacy`.

### Stations (renamed from `sites`)

```sql
alter table sites rename to stations;
alter table stations add column elevation_m numeric(6,1);
alter table stations add column siting_class smallint;           -- 1 (best) to 5, per the siting guide
alter table stations add column tz text not null default 'UTC';  -- display only (Decision 5)
alter table stations add column commissioned_at timestamptz;
alter table stations add column retired_at timestamptz;          -- null = active
alter table stations add constraint stations_code_unique unique (code);
```

`readings.site_id` keeps its name in the legacy table and never exists in the new one. The four-letter code
painted on each enclosure (`HLLW`, `RVRB`) becomes the handle operators use in the console; the uuid remains
the key everywhere else.

### Sensors, channels, and placement

A sensor is a physical instrument with a serial number. A channel is one quantity that sensor reports at one
height, so a combined temperature and humidity probe is a single sensor with two channels.

```sql
create table sensors (
  id uuid default gen_random_uuid() primary key,
  station_id uuid not null references stations (id),
  model text not null,                -- vendor model string, e.g. 'HMP155'
  serial text not null,               -- from the label on the housing
  installed_at timestamptz not null,
  removed_at timestamptz,             -- null while mounted
  created_at timestamptz default now() not null,
  updated_at timestamptz default now() not null
);

create table channels (
  id uuid default gen_random_uuid() primary key,
  sensor_id uuid not null references sensors (id),
  quantity text not null,             -- one of the quantities in rule 2
  height_cm integer not null,         -- above ground, to the sensing element
  si_unit text not null,              -- 'K', '%', 'mm', 'm/s', 'deg'
  created_at timestamptz default now() not null
);
create unique index channels_sensor_quantity_height on channels (sensor_id, quantity, height_cm);
```

Swapping a probe creates a new sensor and new channels. Readings never migrate between channels, and that
is what keeps each calibration record attached to the hardware it was measured on.

`scripts/map_legacy_labels.go` maps every distinct `readings.sensor_label` to a channel. Labels it cannot
resolve are printed for a human rather than guessed, and the migration halts until the list is empty.

### Calibration records

```sql
create extension if not exists btree_gist;

create table calibrations (
  id uuid default gen_random_uuid() primary key,
  channel_id uuid not null references channels (id),
  offset_si double precision not null default 0,
  gain double precision not null default 1,
  valid_from timestamptz not null,
  valid_to timestamptz,               -- null = open-ended
  certificate text,                   -- lab certificate number, if any
  created_at timestamptz default now() not null,
  exclude using gist (channel_id with =, tstzrange(valid_from, valid_to) with &&)
);
```

The read path computes `raw * gain + offset_si` using the record whose range contains the reading's
timestamp. With no such record the raw value is served as-is under an `uncalibrated` flag; it is not dropped
and it does not borrow a neighbouring record's correction.

Recalibrating closes the open record and opens its successor in one transaction, so a channel never has two
records, or none, at any instant. A late lab certificate is the one legitimate way to get a `valid_from` in
the past, and it queues the covered hours for recompute.

### Ingest batches (replacing the `uploads` table)

```sql
create table batches (
  id uuid default gen_random_uuid() primary key,
  station_id uuid not null references stations (id),
  file_sha256 bytea not null,
  file_name text not null,            -- as sent by the logger
  logger_family text not null,        -- 'campbell' | 'vaisala' | 'davis'
  received_at timestamptz default now() not null,
  first_reading_at timestamptz,
  last_reading_at timestamptz,
  reading_count integer not null default 0
);
create unique index batches_file_sha256 on batches (file_sha256);

alter table readings add column batch_id uuid not null references batches (id);
```

The old `uploads` table was keyed on file name, and the Davis loggers write `DATA.TXT` every day, so a re-sent
file looked exactly like a new one. Keying on content makes the duplicate check exact. `uploads` is dropped
once the copy completes, since only the ingest path ever read it.

### Quality flags

```sql
create table flags (
  reading_id bigint not null references readings (id),
  channel_id uuid not null,
  reason text not null,               -- 'range' | 'step' | 'stuck' | 'uncalibrated' | 'manual'
  rule_version integer not null,
  raised_at timestamptz default now() not null,
  primary key (reading_id, reason)
);
```

v1 ships three automatic rules. **Range** rejects values outside a per-quantity window (for air temperature,
−45 °C to +50 °C). **Step** catches a jump of more than 8 K between readings ten minutes apart. **Stuck**
catches a quantity that should vary but repeats the same value to the last digit for six hours. Operators
can add `manual` flags in the console. A flag hides nothing; it only keeps the reading out of rollups.

### Rollup tables and the legacy drop

```sql
create table rollup_hourly (
  channel_id uuid not null references channels (id),
  hour_utc timestamptz not null,
  value_median double precision,      -- null when every reading in the hour is flagged
  value_min double precision,
  value_max double precision,
  sample_count smallint not null,
  flagged_count smallint not null,
  computed_at timestamptz default now() not null,
  primary key (channel_id, hour_utc)
);

create table rollup_daily (
  channel_id uuid not null references channels (id),
  day_utc date not null,
  value_median double precision,
  value_min double precision,
  value_max double precision,
  hour_count smallint not null,       -- hours with a non-null median
  computed_at timestamptz default now() not null,
  primary key (channel_id, day_utc)
);

drop table daily_summary;
alter table stations drop column last_upload_note;
```

Minimum and maximum ignore flagged readings, as the median does. A day with under 18 usable hours still
gets a row, and `hour_count` tells the console to grey it out.

### Retention tiers

| Tier | Resolution | Hot retention | Source |
|---|---|---|---|
| raw | logger interval, 1–10 min | 90 days | decoder |
| hourly | 1 hour | 730 days | rollup job (from raw) |
| daily | 1 day | no expiry | rollup job (from hourly) |

A nightly job moves raw partitions past 90 days into the cold archive as compressed Parquet, one file per
station per month, then drops them from the hot database. Hourly rows older than two years are simply
deleted; the daily tier was computed from them and carries the long record forward.

#### Why ninety days for raw

A full calibration cycle is a quarter (site visits are quarterly), and certificates can trail a visit by a
month, so ninety days is the shortest raw window that always covers both. With 41 stations averaging nine
channels at ten-minute intervals, that is about 4.8 million raw rows per quarter, comfortably within the
existing database host.

### Go types (`internal/model/types.go`)

```go
type Quantity string

const (
    AirTemp     Quantity = "air_temp"
    RelHumidity Quantity = "rel_humidity"
    RainTip     Quantity = "rain_tip"
    WindSpeed   Quantity = "wind_speed"
    WindDir     Quantity = "wind_dir"
    SoilTemp    Quantity = "soil_temp"
)

type Reading struct {
    ID         int64
    ChannelID  uuid.UUID
    BatchID    uuid.UUID
    At         time.Time // always UTC
    RawSI      float64
    SourceUnit string
}

type CalibratedReading struct {
    Reading
    Value         float64
    CalibrationID *uuid.UUID // nil when uncalibrated
}
```

Only the read path builds a `CalibratedReading`, so any function that needs corrected values has to ask
for them in its signature.

### Rehearsing the migration

The migration runs twice before it runs for real. The first rehearsal uses last night's copy of production
on a scratch host and measures two things: how long the `readings` copy takes (the pause window has to
cover it with room to spare), and how many legacy labels the mapping script fails to resolve. The second
rehearsal happens after those labels are fixed by hand in the console's column maps, and has to finish with
an empty unresolved list and a row count in `readings` equal to the count in `readings_legacy`.

If the real run fails partway, the transaction rolls back and ingest resumes against the old schema; the
loggers' own buffers cover the gap. Nothing in this phase is destructive until `readings_legacy` is dropped,
one release later.

## Phase 2 — Persistence layer (repositories and jobs)

### Repositories

- New: `sensors.go` (`create`, `findByID`, `remove`), `channels.go` (`create`, `findBySensor`,
  `findByStation`), `calibrations.go` (`open`, `close`, `coveringAt`), `batches.go` (`insertIfNew`,
  `findBySHA`), `flags.go` (`raise`, `listForChannel`), and `rollups.go`.
- `sites.go` becomes `stations.go`, gaining `findByCode` and `retire`.
- `uploads.go` and `summaries.go` are deleted, and `repos.go` stops registering them.

### Job functions (every write flows through here, including rollup persistence)

- `jobs/ingest.go` — `IngestFile(ctx, stationID, path)`: archive the file, hash it, look for an existing
  batch, decode, then insert the batch row and its readings in one transaction. A known hash returns the
  existing batch without writing anything.
- `jobs/calibrate.go` — `Recalibrate(ctx, channelID, offset, gain, validFrom)`: atomically closes the open
  record and opens the next; a `validFrom` in the past queues the affected hours.
- `jobs/flag.go` — `RunRules(ctx, batchID)` after each ingest, plus `FlagManually` for the console.
- `jobs/rollup.go` — `RecomputeHours(ctx, channelID, hours)`: computes the hourly rows and upserts them in
  one statement, replacing the old non-atomic delete-then-insert. The daily rows those hours touch are
  recomputed afterwards.
- `jobs/expire.go` — `ExpireRaw(ctx, before)`: the nightly move into the cold archive.
- `jobs/summarize.go` is deleted; the cron entry that called it is replaced by Phase 4.

## Phase 3 — `internal/decode`: stateless parsing and unit normalization

The decode package is a leaf. It parses, and that is all: it holds no database connection, never reads the
clock, and leaves persistence to `jobs/ingest.go`. Bytes go in and typed rows come out.

```
internal/decode/
  decode.go      Sniff(bytes) → family; Decode(family, bytes) → []Row
  campbell.go    TOA5 / TOB1 tables; the header rows carry column names and units
  vaisala.go     WXT message frames, one frame per reading
  davis.go       DATA.TXT lines; fixed columns, US units
  units.go       source unit → SI, keeping the source unit
  hash.go        batch identity: SHA-256 of the file bytes, nothing else
  rows.go        Row{Column, At, Value, SourceUnit}
```

Columns resolve to channels through `stations.column_map`, a JSON object edited in the console, and never
through anything inside the file: two loggers of one family can name the same column differently.

The decoder's database role reads raw readings only; the scheduler's role reads the hourly tier and is the
only one that may write the rollup tables, and neither role can touch the other's. The decoder's settings
come from the environment:

```sh
# decoder and ingest
MERIDIAN_DB_URL=postgres://meridian@db.internal/meridian
MERIDIAN_INBOX_DIR=/var/lib/meridian/inbox
MERIDIAN_ARCHIVE_DIR=/var/lib/meridian/archive
```

Nothing has a default in code. A missing variable stops the process at start-up and names the variable,
instead of running against a guessed path.

## Phase 4 — Rollup scheduling (in `cmd/rollupd`)

`cmd/rollupd` is a small binary on a ticker (`MERIDIAN_ROLLUP_TICK`, five minutes). Each tick it gathers
the hours that changed, whether from new readings, new flags or backdated calibrations, and recomputes them.
The work list is a table, `rollup_pending (channel_id, hour_utc)`, keyed so that queueing the same hour twice
is free; there is no separate queue service.

- No hour is computed until `MERIDIAN_ROLLUP_LAG` (15 minutes) after it closes, which absorbs the usual
  few-minutes-late file at no cost.
- Later arrivals go through the pending table instead: a late row redoes the hour, and then its day.
- Every recompute rebuilds the hour from its readings rather than adjusting it, so a bad pass is corrected
  by the next one instead of compounding.
- A Postgres advisory lock is held for the length of a run. If a second instance is started by accident it
  waits rather than racing the first.

Expiry is configured alongside it: `MERIDIAN_RAW_DAYS=90` and `MERIDIAN_HOURLY_DAYS=730`.

## Phase 5 — Console views and attribution

- **Stations.** The list shows siting class, elevation and whether a station is retired. Each station page
  lists its sensors and channels with the calibration currently in force.
- **Readings.** Charts read `rollup_hourly` unless zoomed in past one day, when they switch to raw. Flagged
  readings draw hollow and show the flag's reason on hover.
- **Calibrate.** A form for channel, offset, gain, effective time and certificate, calling
  `jobs.Recalibrate`. With a backdated time it states how many hours will be recomputed before asking for
  confirmation.
- **Batches.** Per-station batch history with reading counts, partial-file markers and clock warnings, plus
  a direct path to each archived file by its hash.
- **Flags.** The last day's automatic flags, grouped by rule, for an operator to confirm or clear with a
  note.
- The `monthly_extremes` page goes; the daily tier answers the same question.

## Phase 6 — Alert rules over the rollups

The evaluator runs after every rollup pass. Frost rules read `rollup_hourly`: air temperature at 150 cm
below 0 °C for two hours in a row. Flood rules are the deliberate exception, computing a fifteen-minute rain
rate straight from raw tips, because rule 3 keeps sub-hourly aggregates out of the rollup tables.

- Rules live in `alert_rules (quantity, comparison, threshold, window_minutes, tier)` and are edited in the
  console.
- Every alert names the station, the channel and the hour or window that tripped it, so whoever receives it
  can open the console on exactly that chart.
- A flagged reading can never trip an alert. A stuck rain gauge reporting zero for six hours is a quality
  problem, not a drought.
- Alert mail is sent from the address in `MERIDIAN_ALERT_FROM`.

## Sequencing

1. The schema migration and the label mapping script, rehearsed first on a copy of production.
2. The decode package, which depends on nothing and which the ingest job needs.
3. Repositories and job functions.
4. The rollup scheduler, followed by a single full recompute of all history.
5. Console views and alert rules, in parallel.
6. One release after the console ships, drop `readings_legacy`.

The migration is scheduled for a dry week, since a pause during heavy rain would delay the flood rules by
the length of the pause. The full recompute in step 4 runs overnight with ingest live: it walks history
oldest first, one station at a time, and the pending table absorbs anything new that arrives meanwhile.

## Exit criteria

- Sending one file twice returns the first batch on the second call: a batch cannot be ingested twice.
- A truncated file followed by the complete one leaves `reading_count` on the second batch equal to the
  tail alone.
- A backdated recalibration puts exactly the covered hours into `rollup_pending`, and their medians shift by
  the expected offset.
- A manual flag removes a reading from the hourly median and the daily figure; clearing the flag restores
  both.
- With the clock set 91 days ahead, the expiry job moves the raw partition into the archive, leaves hourly
  rows alone, and the archived Parquet file reproduces the raw rows exactly.
- A reading stamped after its sensor's `removed_at` is rejected with a message that names the sensor.
- Of two `rollupd` processes, one computes and the other waits on the advisory lock.
- Each logger family's sample file in `testdata/` decodes to the checked-in expected rows, units included.
- Raising the step threshold and replaying a month keeps the flags raised under the old rule version and
  gives new ones the new `rule_version`.
- Replaying last January's raw files through the frost rules reproduces the alerts the old script sent.
- The second migration rehearsal ends with no unresolved labels and equal row counts in `readings` and
  `readings_legacy`.
