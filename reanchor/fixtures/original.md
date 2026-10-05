# Station Data: Calibrated Readings, Rollups, and Retention

## Context

Meridian's readings carry their provenance as loose text: `uploads.station_name`, `readings.sensor_label`,
`readings.units`, `alerts.station_label`, `daily_summary.source_note`. There's no stable notion of which
instrument produced a value, no record of how that instrument was calibrated, and no retention policy —
the `readings` table grows without limit, and any correction silently rewrites history.

Two incidents this year made the cost concrete. In March a technician replaced the humidity probe at `HLLW`
and typed its new offset into the old settings screen, which applied the offset to six years of stored
readings; the growers' frost-hour totals for the previous winter changed overnight and nobody could say
which version was right. In August a modem retry stored the same day's file three times at `RVRB`, the
daily rain total tripled, and the inflated figure reached the flood wardens' weekly bulletin before anyone
noticed.

This plan introduces a calibrated, instrument-aware data model for the network's **fixed** stations. It is
the first cut; **mobile** platforms (the two survey vans and the river buoys) are explicitly deferred, but
the model is shaped so they slot in later with no migration.

**This is a clean break for derived data.** Raw readings survive byte for byte, but every summary table is
rebuilt from them. We drop `daily_summary` and the hand-maintained `monthly_extremes` sheet outright — no
attempt to reconcile them with the new rollups, no lossy matching of station names.

### Framing (keep this in view)

Meridian is a **measurement network, not a forecasting product** — it exists so growers, road crews and
flood wardens in the valley get numbers they can act on. Rigor is sized accordingly: we prevent *silent
corruption* (overwritten raw values, double-counted files, unit mix-ups, a calibration applied twice), but
we do **not** gold-plate the pipeline (no per-reading checksums, no sub-minute rollups, no elaborate
sensor-health scoring as a v1 blocker). Every safeguard below is included because it protects the numbers
people act on, or their confidence in them — not for leaderboard theater.

### The shape of the system, in one paragraph

A station's data logger sends a file over its cellular modem. Meridian decodes the file into raw readings,
records it as an **ingest batch**, and resolves every column to a persistent **channel** — one measured
quantity, from one physical sensor, at one mounting height. Calibration is the correction model: a raw
value is adjusted by whichever calibration record covers its timestamp, at read time, so a recalibration
never rewrites what the sensor actually reported. Rollups (hourly and daily) are computed from calibrated
values by one scheduled job, and the retention tiers decide how long each resolution is kept. Operators
work on **both** surfaces: in the console directly, and through the alert rules that run over the rollups.

## Design decisions (settled — not open questions)

1. **Raw is immutable from day one.** A decoded reading is inserted once and never updated. Corrections
   are calibration records or quality flags layered on top; there is no edit path to a raw row, and none
   is planned.

2. **One `channels` table; every quantity is a peer.** `quantity` is a text column checked against a short
   list (`'air_temp' | 'rel_humidity' | 'rain_tip' | 'wind_speed' | 'wind_dir' | 'soil_temp'`). Only fixed
   stations populate it in v1, but the single-table design stays so mobile platforms — a van's thermometer
   is a channel like any other — slot in later with zero schema migration. `readings.channel_id` and
   `flags.channel_id` work regardless of what carries the sensor.

3. **The rollup job is the only producer of derived data.** Not a trigger cascade, not a materialized view
   refreshed by whichever reader gets there first. It owns only what it must.
   - **Hourly tier** → computed from calibrated raw values; one row per channel per hour.
   - **Daily tier** → computed from the hourly tier, never from raw, so the two can never disagree.
   - **Mobile platforms** → deferred (their rollups need a position dimension as well as time).

4. **Rollup tables have exactly one writer.** The console and the alert evaluator both *read* them; only
   the rollup job writes to the rollup tables, so a buggy reader cannot corrupt them. A read-only role for
   everyone else falls out for free. (One read-write role for every process rejected: any reader could
   quietly skew a daily figure.)

5. **Grouping is always by UTC hour, never by the station's clock.** The network spans two time zones and
   both observe daylight saving; station-local time is never a grouping key. `stations.tz` is **display
   data only**, used by the console to label axes.

6. **Medians, not means.** An hourly value is the median of the readings in that hour, so one spike — a
   spider on the anemometer cup, a bird on the rain gauge — cannot drag the hour. Rain is the exception:
   tips are summed, and a tip count is never averaged.

7. **Quality flags are additive.** A suspicious reading is flagged, never deleted. A flag carries a reason
   code and the version of the rule that raised it, so a rule change can be replayed over history.

8. **Calibration is time-ranged.** Each record has `valid_from` and an optional `valid_to`; ranges for one
   channel may not overlap, which an exclusion constraint enforces rather than application code.

9. **Ingest is idempotent.** A batch is identified by the SHA-256 of the file's bytes, so a logger that
   re-sends a file after a dropped call produces a no-op, not a second copy of every reading.

10. **Units are normalized at decode.** Every raw value is stored in SI units with its source unit kept
    beside it; a logger configured in Fahrenheit is a decoder concern, never a query concern.

11. **Retention is per tier, not per station.** No station gets a longer raw window than another. A
    research request for old raw data is answered from the cold archive, not by widening the hot tier.

12. **Clean break.** The legacy `daily_summary` table, the `uploads.station_name` column and the
    `monthly_extremes` sheet are dropped; nothing reads them after Phase 6.

13. **Manual changes carry a name.** Every manual flag and every calibration record stores the operator's
    console handle and a free-text note. There is no approval step; the name is there so a question about
    a correction has a person to go to.

14. **One decoder per logger family, chosen by sniffing.** The file's own header decides the family, never
    the station's configuration, so a logger swapped for a different model in the field keeps working
    from its first file.

## Deferred (tracked, out of scope for v1)

- **Mobile platforms** (survey vans, river buoys): position-aware channels and rollups. The `channels`
  table already admits them; the rollup key does not.
- **Automatic drift detection.** Comparing a channel against its neighbours would catch a sensor that
  slowly reads warm — a silent-drift failure mode — but a v1 heuristic would flag every frost hollow and
  sheltered gully as faulty. Revisit once there is a year of calibrated data to tune against.
- **Per-reading uncertainty.** Calibration certificates carry an uncertainty figure; propagating it into
  rollups is real statistics work, and nobody has asked for it yet.
- **Radar and satellite overlays** in the console.
- **Open data downloads** for the university's hydrology group. They currently get a monthly CSV by hand,
  and that continues.
- **Backfilling the paper logbooks** (2016–2019). Transcription is a volunteer project with its own
  timeline and its own quality rules.
- **Sub-hourly rollups.** Fifteen-minute aggregates were requested for the flood wardens; the alert rules
  in Phase 7 read raw for that window instead.

# Phase 1 — Schema

All schema changes land in one migration, `0007_calibrated_readings.sql`, applied with ingest paused
(loggers buffer at least 72 hours on their own, so a short pause loses nothing). Raw readings are copied,
not rewritten: the new `readings` table is filled from the old one with `channel_id` resolved by the
mapping script in 1b, and the old table is renamed `readings_legacy` and kept for one release.

### 1a. Rename `sites` → `stations`, add siting metadata

```sql
alter table sites rename to stations;
alter table stations add column elevation_m numeric(6,1);
alter table stations add column siting_class smallint;           -- 1 (best) to 5, per the siting guide
alter table stations add column tz text not null default 'UTC';  -- display only (Decision 5)
alter table stations add column commissioned_at timestamptz;
alter table stations add column retired_at timestamptz;          -- null = active
alter table stations add constraint stations_code_unique unique (code);
```

`readings.site_id` is **not** renamed in this migration — the legacy table keeps it, and the new
`readings` table never had it. `stations.code` (the four-letter code painted on each enclosure, e.g.
`HLLW`) becomes the human handle in the console; the uuid stays the key everywhere else.

### 1b. Sensors and channels

```sql
create table sensors (
  id uuid default gen_random_uuid() primary key,
  station_id uuid not null references stations (id),
  model text not null,                -- manufacturer model, e.g. 'HMP155'
  serial text not null,               -- as printed on the housing
  installed_at timestamptz not null,
  removed_at timestamptz,             -- null = still mounted
  created_at timestamptz default now() not null,
  updated_at timestamptz default now() not null
);

create table channels (
  id uuid default gen_random_uuid() primary key,
  sensor_id uuid not null references sensors (id),
  quantity text not null,             -- 'air_temp' | 'rel_humidity' | 'rain_tip' | ...
  height_cm integer not null,         -- mounting height above ground
  si_unit text not null,              -- 'K', '%', 'mm', 'm/s', 'deg'
  created_at timestamptz default now() not null
);
create unique index channels_sensor_quantity_height on channels (sensor_id, quantity, height_cm);
```

A sensor is a physical instrument with a serial number; a channel is one quantity it reports at one
height. A combined temperature and humidity probe is one sensor with two channels. Replacing a probe
creates a new sensor and new channels — readings never move between channels, which is what keeps a
calibration record attached to the hardware it was measured on.

The mapping script (`scripts/map_legacy_labels.go`) resolves every distinct `readings.sensor_label` to a
channel. Labels it cannot resolve are listed, not guessed; the migration stops while any remain.

### 1c. Calibration records — the read-time correction source of truth

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

A calibrated value is `raw * gain + offset_si`, evaluated in the read path (Phase 2) against the record
whose range covers the reading's timestamp. A reading with no covering record is served raw and carries an
`uncalibrated` flag; it is never dropped, and it never borrows the nearest record's correction.

Recalibration closes the open record (`valid_to = now()`) and opens a new one in the same transaction, so
there is no instant at which a channel has two records or none. Backdated corrections (a lab certificate
that arrives a month after the visit) are the one case where `valid_from` is in the past; they are allowed
and they trigger a rollup recompute for the affected hours (Phase 5).

### 1d. Ingest batches (replaces `uploads`)

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

`uploads` was keyed by file name, which the Davis loggers reuse every day (`DATA.TXT`), so a re-sent file
and a new file were indistinguishable. Keying on the content hash makes the duplicate check exact. The old
`uploads` table is dropped after the copy; nothing outside the ingest path ever read it.

### 1e. Quality flags — lightweight + open (Decision 7)

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

Three automatic rules ship in v1: a **range** check per quantity (air temperature outside −45 °C to
+50 °C), a **step** check (more than 8 K between consecutive readings ten minutes apart), and a **stuck**
check (the same value, to the last digit, for six hours on a quantity that should vary). Operators add
`manual` flags from the console. Flags never hide a reading; they exclude it from rollups.

### 1f. Rollup tables; drop legacy summary columns

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

`value_min` and `value_max` are taken over unflagged readings only, like the median. A day with fewer than
18 usable hours still gets a row, with `hour_count` saying how thin it is; the console greys it out.

### 1g. Retention tiers

| Tier | Resolution | Kept in the hot database | Written by |
|---|---|---|---|
| raw | as logged (1–10 min) | 90 days | the decoder |
| hourly | one hour | 2 years | the rollup job, from raw |
| daily | one day | indefinitely | the rollup job, from hourly |

Expiry is a nightly job that moves raw partitions older than 90 days to the cold archive (compressed
Parquet, one file per station per month) and drops them from the hot database. Hourly rows past two years
are deleted outright; the daily tier is derived from them and keeps the long record.

#### Sizing the raw tier

Ninety days is the shortest window that covers a full calibration cycle (quarterly site visits) plus a
month of slack for late certificates. At the current 41 stations and an average of 9 channels each, a
ten-minute interval gives roughly 4.8 million raw rows per quarter — small enough that the hot tier never
needs its own hardware.

### 1h. Types (`internal/model/types.go`)

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
    At         time.Time // UTC, always
    RawSI      float64
    SourceUnit string
}
```

`Reading` carries the raw value only. The calibrated value is a separate type, `CalibratedReading`, built
by the read path, so a function that needs corrected data says so in its signature.

# Phase 2 — Storage layer (repositories + jobs)

### Repositories

- New: `sensors.go` (`create`, `findByID`, `remove`), `channels.go` (`create`, `findBySensor`,
  `findByStation`), `calibrations.go` (`open`, `close`, `coveringAt`), `batches.go` (`insertIfNew`,
  `findBySHA`), `flags.go` (`raise`, `listForChannel`), `rollups.go`.
- Rename `sites.go` → `stations.go` (+ `findByCode`, `retire`).
- **Remove** `uploads.go` and `summaries.go`.
- `repos.go`: drop `uploads` and `summaries`; register the new repositories.

### Jobs (all writes go through here, per CONTRIBUTING.md — including rollup persistence)

- `jobs/ingest.go`: `IngestFile(ctx, stationID, path)` — hash, check for an existing batch, decode, insert
  the batch row and its readings in one transaction. A duplicate hash returns the existing batch and writes
  nothing.
- `jobs/calibrate.go`: `Recalibrate(ctx, channelID, offset, gain, validFrom)` — closes the open record and
  opens the new one atomically; a backdated `validFrom` enqueues the affected hours for recompute.
- `jobs/flag.go`: `RunRules(ctx, batchID)` after every ingest, and `FlagManually` for the console.
- `jobs/rollup.go`: `RecomputeHours(ctx, channelID, hours)` — computes the hourly rows and upserts them,
  which replaces the non-atomic delete-then-insert the old summary script used; then recomputes the daily
  rows those hours touch.
- `jobs/expire.go`: `ExpireRaw(ctx, before)` — the nightly move to the cold archive.
- **Remove** `jobs/summarize.go`. Its only caller was the cron entry that Phase 5 replaces.

# Phase 3 — Decoder package (`internal/decode`) — stateless only

`internal/decode` is a **leaf**: pure parsing, **no database edge** (persistence lives in `jobs/ingest.go`).
It takes bytes and returns typed rows; it never opens a connection and never reads the clock.

```
internal/decode/
  decode.go      Sniff(bytes) → family; Decode(family, bytes) → []Row
  campbell.go    TOA5 / TOB1 table parsing; header rows give column names and units
  vaisala.go     WXT message frames; one frame per reading
  davis.go       DATA.TXT lines; fixed columns, US units
  units.go       source unit → SI, keeping the source unit (Decision 10)
  hash.go        batch identity: SHA-256 of the file bytes, nothing else
  rows.go        Row{Column, At, Value, SourceUnit}
```

Column names map to channels through `stations.column_map` (a JSON object maintained in the console), not
through anything in the file, because two loggers of the same family name their columns differently.

# Phase 4 — Batches: the ingest attribution model

A batch is the unit of "where did this come from". Every reading carries `batch_id`; every batch carries the
station, the file name as sent, the logger family and the content hash. When an operator asks why a value
looks wrong, the console shows the batch, and from the batch the original file in the archive.

**Archive first.** Every accepted file is copied into `MERIDIAN_ARCHIVE_DIR` under
`<station code>/<yyyy>/<mm>/<sha256>` before any of its readings are inserted, so the archive always holds
at least what the database does. The copy is the first step of an ingest, not the last, and a failed copy
aborts the ingest with nothing written.

**Batch identity is the content hash and nothing else.** File names repeat, modem retries re-send whole
files, and two loggers can produce identical names on the same day. The hash makes all three harmless: a
replayed `batch_id` only duplicates a log line — never a reading.

**Partial files.** A logger that loses power mid-write sends a truncated file, then the complete one on the
next call. The two hash differently, so both become batches; the second one's readings collide with the
first one's on `(channel_id, at)` and are skipped by the unique index, and only the new tail is inserted.
The batch row records `reading_count` as the number actually inserted, which is how the console tells a
partial file from a full one.

**Clock skew.** Loggers drift. A batch whose `last_reading_at` is more than ten minutes ahead of
`received_at` is accepted but flagged `clock_ahead`, and its readings carry a `step` check exemption for
the first hour after the correction lands.

# Phase 5 — Rollup scheduler (minimal in-process, in `cmd/rollupd`)

`cmd/rollupd` is one small binary with a ticker. Every five minutes it collects the hours that changed —
new readings, new flags, backdated calibrations — and recomputes them. There is no queue service; the work
list is a table, `rollup_pending (channel_id, hour_utc)`, with a unique key so enqueueing twice is free.

- An hour is not computed until 15 minutes after it ends (`MERIDIAN_ROLLUP_LAG`), so the common case of a
  file arriving a few minutes late costs nothing.
- Anything later still is handled by the pending table: late row → redo the hour, then redo its day.
- A recompute is a full recompute of that hour from the readings, never an incremental adjustment, so a
  bug in one pass is fixed by the next pass rather than compounded.
- The scheduler holds a Postgres advisory lock while it runs; a second instance started by mistake waits
  instead of racing.

# Phase 6 — Console integration

- `console/stations` — the station list gains siting class, elevation and retirement state; the per-station
  page lists sensors and channels with their current calibration.
- `console/readings` — the chart reads `rollup_hourly` by default and raw only when zoomed below one day;
  flagged readings render hollow, with the reason on hover.
- `console/calibrate` — the recalibration form (channel, offset, gain, effective time, certificate) calls
  `jobs.Recalibrate`; a backdated time shows how many hours will be recomputed before it is confirmed.
- `console/batches` — a batch list per station with counts, partial-file markers and clock warnings, and a
  direct path to the archived file by its hash.
- `console/flags` — a queue of the automatic flags raised in the last day, grouped by rule, where an
  operator confirms a flag or clears it with a note.
- Remove the `monthly_extremes` page; the daily tier answers the same question.

# Phase 7 — Alerting integration (frost and flood rules)

The alert evaluator runs after each rollup pass. Frost rules read `rollup_hourly` (air temperature at
150 cm below 0 °C for two consecutive hours). Flood rules are the exception to rollup-only reading: a rain
rate over the last fifteen minutes is computed from raw tips directly, because Decision 3 deliberately keeps
sub-hourly aggregates out of the rollup tables.

- Rules are rows in `alert_rules (quantity, comparison, threshold, window_minutes, tier)`, edited in the
  console.
- An alert carries the station, the channel and the hour or window that tripped it, so a recipient can
  open the console at the exact chart.
- Flagged readings never trip an alert. A stuck rain gauge that reports zero for six hours is a quality
  problem, not a drought.

# Lifecycle rules (cross-cutting)

- **Retiring a station** sets `retired_at`; its channels stop accepting batches, and its history stays
  queryable forever in the daily tier.
- **Removing a sensor** sets `removed_at`. Readings after that time are rejected at ingest with a clear
  message naming the sensor, which catches a logger that was not reconfigured after a swap.
- **The reference sensor.** Each station marks one air-temperature channel as its reference, used for the
  station's headline figure in the console. When the reference sensor is removed, the earliest-installed
  remaining sensor becomes the reference; if none remains, the station shows no headline figure until an
  operator chooses one.
- **Moving a station** (a mast relocated across a field) is a retirement plus a new station, because siting
  class and elevation change the meaning of every reading. A `succeeds` column ties the new station to the
  old one so the console can draw one long record across the move.
- **A recalibration never edits an old record**, only closes it. The history of corrections is itself data.
- **Deleting anything** is not offered in v1. Retiring and removing cover every real case we have.

# Environment variables

```sh
# decoder and ingest
MERIDIAN_DB_URL=postgres://meridian@db.internal/meridian
MERIDIAN_INBOX_DIR=/var/lib/meridian/inbox
MERIDIAN_ARCHIVE_DIR=/var/lib/meridian/archive

# rollup scheduler
MERIDIAN_ROLLUP_LAG=15m
MERIDIAN_ROLLUP_TICK=5m

# expiry
MERIDIAN_RAW_DAYS=90
MERIDIAN_HOURLY_DAYS=730

# alerts
MERIDIAN_ALERT_FROM=alerts@meridian.internal
```

Two database roles: the decoder reads raw, rollup reads hourly, and each role can write only its own
tables. Neither reads the other's configuration.

None of these has a default in code. A missing variable stops the process at start-up with the variable's
name in the message, rather than running against a guessed path or a guessed database.

# Build order

1. Phase 1 (schema) and the mapping script, run against a copy of production first.
2. Phase 3 (decoder), because Phase 2's ingest job depends on it and it has no dependencies of its own.
3. Phase 2 (repositories and jobs).
4. Phase 4 needs no new code beyond Phase 2; it is the review of the attribution rules above.
5. Phase 5 (scheduler), then the one-time full recompute of history.
6. Phase 6 (console) and Phase 7 (alerts) in parallel.
7. Drop `readings_legacy` one release after Phase 6 ships.

# Verification

- Ingest the same file twice: the second call returns the first batch, so a batch can't be ingested twice.
- Ingest a truncated file and then the full one: `reading_count` on the second batch equals the tail only.
- Recalibrate with a backdated time: exactly the covered hours appear in `rollup_pending`, and their
  medians change by the expected offset.
- Flag a reading manually: it disappears from the hourly median and the daily figure, and reappears when
  the flag is removed.
- Run the expiry job with a clock set 91 days ahead: the raw partition moves to the archive, the hourly
  rows are untouched, and the archived Parquet file reproduces the raw rows exactly.
- A reading after a sensor's `removed_at` is rejected with a message naming the sensor.
- Launch two `rollupd` processes: one computes, the other waits on the advisory lock.
- Decode each logger family's sample file from `testdata/` and compare the rows with the checked-in
  expected output, units included.
- Change the step rule's threshold and replay a month: flags raised under the old version remain, and new
  ones carry the new `rule_version`.
- Frost rule: replay last January's raw files and confirm the alerts match the ones the old script sent.
