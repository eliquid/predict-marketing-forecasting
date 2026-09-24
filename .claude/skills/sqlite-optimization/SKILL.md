---
name: sqlite-optimization
description: Use when tuning SQLite. Apply production safety checks.
---

# SQLite Optimization

_Version 0.1.0 · Jason Brown · MIT · linux, macos, windows · tags: sqlite, optimization, security, production, go_

Apply this skill to Go services that use SQLite or a SQLite-compatible database. Optimize only after measuring the query and workload; preserve durability, correctness, and security before pursuing throughput. Do not add a database, ORM, cache, or background system without a demonstrated need.

## When to Use

* Designing SQLite schema, connection setup, migrations, or production deployment.
* Investigating slow queries, lock contention, WAL growth, or unexpected `SQLITE_BUSY` failures.
* Reviewing SQLite security, backups, integrity, or performance changes.
* Establishing measurable optimization baselines for a Go + SQLite service.

Do not use for cross-node, high-write-concurrency, or multi-primary workloads without first confirming SQLite fits the deployment and write model. SQLite permits one writer at a time, including in WAL mode.

## Acceptance Criteria

Before calling SQLite work complete, verify all applicable items:

* Required invariants are database constraints, not only Go validation.
* Every connection has required connection-local PRAGMAs enabled and is health-checked.
* Changed query plans have been inspected with `EXPLAIN QUERY PLAN` using representative data.
* A measured baseline and post-change result exist, or the verification gap is explicit.
* Backups have a documented restore-and-integrity-check procedure.
* Security boundaries cover SQL injection, database-file access, extension loading, and secrets.

## 1. Fit the Workload and Set the Durability Contract

1. Confirm the database lives on reliable local storage accessible to one deployment unit. Do not place SQLite/WAL files on network filesystems unless the SQLite documentation and the storage provider explicitly support the locking semantics.
2. Identify write rate, transaction duration, reader concurrency, database size, available memory, recovery point objective, and recovery time objective. Establish these before selecting PRAGMAs.
3. Choose durability deliberately:
   * `synchronous=FULL` when a power-loss durability guarantee is required for each committed transaction.
   * In WAL mode, `synchronous=NORMAL` is often a sound performance/durability trade-off: an application crash does not corrupt the database, but a system/power failure can lose recently committed work.
   * Never use `synchronous=OFF` for durable or security-relevant data.
4. Record the choice and its failure-mode consequence in deployment documentation. Completion: a reviewer can tell which failures may lose acknowledged work.

## 2. Initialize and Verify Each Database Connection

Set persistent database-mode settings during creation/migration; set connection-local settings after every connection opens. Exact DSN hooks are driver-specific, so inspect the installed driver's documentation rather than guessing DSN parameters.

```sql
-- Persistent database mode: run during controlled initialization.
PRAGMA journal_mode = WAL;

-- Per connection: enforce relational correctness and bounded lock waiting.
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;

-- Pick deliberately; see the durability contract above.
PRAGMA synchronous = NORMAL;
```

* WAL improves read/write concurrency, but it still permits only one writer. Keep write transactions short.
* Do not enable WAL when the application relies on `ATTACH DATABASE` unless the resulting behavior has been tested for that use case.
* Treat `cache_size`, `mmap_size`, `temp_store=MEMORY`, `page_size`, and `journal_size_limit` as measured workload-specific tuning—not universal defaults. Cap memory use and consider 32-bit processes, containers, and I/O-error semantics before enabling large memory maps.
* Configure a small, intentional Go connection limit. For write-heavy single-process services, one application-level write queue or one writer connection may be simpler and more predictable than retry storms. Allowing multiple read connections in WAL mode is appropriate only after measuring contention and confirming driver behavior.
* Use `PingContext` after initialization and fail startup if required PRAGMAs or the database path are unavailable. Completion: an integration test proves foreign-key violations fail on a fresh pooled connection.

## 3. Model Integrity in the Schema

1. Use explicit types/affinities and `NOT NULL`, `CHECK`, `UNIQUE`, and `FOREIGN KEY` constraints for every durable invariant. Use transactions for multi-statement state transitions.
2. Prefer `INTEGER PRIMARY KEY` for a surrogate key when its semantics fit; it aliases SQLite's rowid and avoids a separate key index.
3. Use `WITHOUT ROWID` only for tables with non-integer or composite primary keys after measuring its storage and query-plan effects.
4. Avoid `AUTOINCREMENT` unless the requirement is never to reuse a deleted rowid; it adds overhead and does not mean "auto-generated ID" in the general sense.
5. Store money or precise quantities as scaled integers, not `REAL`. Store timestamps in one documented canonical representation and validate ranges at the boundary.
6. Add secondary indexes only for demonstrated `WHERE`, join, `ORDER BY`, or `GROUP BY` paths. Each index consumes space and increases write cost. Completion: migrations include constraints and each new index names the query it serves.

## 4. Design Queries and Indexes From Real Plans

* Select only required columns and rows. Use `WHERE`, `LIMIT`, keyset pagination, aggregates, joins, subqueries, and SQL-side filtering rather than loading excess data into Go.
* Bind every value. Never construct SQL by concatenating untrusted input. Dynamic identifiers require a closed, code-owned allowlist; placeholders bind values, not identifiers.
* Use `QueryContext`, `QueryRowContext`, and `ExecContext`; close rows immediately after checking the query error and check `rows.Err()` after iteration.
* Reuse prepared statements for hot, repeated SQL when the installed driver benefits from it. Bound parameters also improve statement-cache reuse.
* Create composite indexes in query order: equality/`IN`/`IS` constrained left-most columns first, then the range column, then columns that can satisfy ordering or coverage. Index use generally stops after the first range inequality, and gaps or an unconstrained left-most column prevent normal prefix use.
* Use `EXPLAIN QUERY PLAN` against realistic statistics and data. Investigate full scans, temporary B-trees for sort/group, and automatic indexes. An automatic index is a signal to evaluate a persistent index, not a blanket mandate to add one.

```sql
EXPLAIN QUERY PLAN
SELECT id, created_at
FROM orders
WHERE account_id = ? AND state = ? AND created_at >= ?
ORDER BY created_at DESC
LIMIT ?;
```

* Run `PRAGMA optimize;` at a controlled cadence (such as before closing a short-lived connection or during a quiet maintenance window for long-running services). Do not blindly run expensive `ANALYZE` or `VACUUM` in request paths. Completion: plan output explains which relevant index is used and why it is selective.

## 5. Control Transactions, Contention, and WAL

1. Batch related writes in one short transaction; do not commit each row independently. Do not hold a transaction open across network calls, template rendering, user input, or slow computation.
2. Handle `SQLITE_BUSY` and `SQLITE_LOCKED` according to an explicit policy: bounded busy timeout, limited retry with jitter only for idempotent work, and clear application-level errors when exhausted. Do not retry non-idempotent writes unless the transaction outcome is known.
3. Monitor WAL growth, checkpoint duration/failures, busy errors, query latency, database size, and free disk. Long-lived readers can prevent checkpoints from reclaiming WAL space.
4. Keep automatic checkpointing unless measured tail latency justifies a controlled background checkpoint. Run `PRAGMA wal_checkpoint(FULL)` or `TRUNCATE` only in an operationally safe maintenance path: they can wait for readers/writers; `TRUNCATE` is disruptive. Set a measured `journal_size_limit` if bounded WAL disk use is required.
5. Run `VACUUM` only with a disk-space budget and maintenance plan; it rewrites the database and can block writes. Consider incremental auto-vacuum only when file-size reclamation is a recurring, measured need, and configure it when the database is created. Completion: load testing demonstrates the selected timeout, writer policy, and worst acceptable busy behavior.

## 6. Secure the Data and Operations Boundaries

**External boundary**

* Authenticate and authorize every request before it can read or mutate tenant or user data. Include ownership/tenant predicates in queries; parameterization does not solve broken access control.
* Validate and normalize all input, enforce page-size limits, and rate-limit expensive search/export endpoints. Use FTS5 for text search when appropriate; do not emulate broad text search with unbounded `%term%` scans.
* Protect browser state-changing flows with CSRF defenses and secure session cookies. SQLite is not a substitute for authorization.

**Application boundary**

* Use parameterized SQL exclusively. Keep SQL errors internal; return stable domain errors without paths, SQL text, or sensitive values.
* Do not enable arbitrary SQLite extensions or load extension paths from users/configuration. Treat SQL functions, virtual tables, triggers, and migrations as trusted code reviewed with the application.
* Use explicit transactions and constraints to prevent lost invariants. Keep sensitive data out of logs and tracing attributes.

**Data and operations boundary**

* Restrict the database directory and `-wal`/`-shm` sidecar files to the service account. Do not serve the database directory as static content or expose it through file-download endpoints.
* Keep encryption keys, backup credentials, and DSNs out of source control and logs. SQLite itself does not provide transparent database encryption; use an explicitly selected, supported encryption solution only when the threat model requires encryption at rest.
* Apply OS/disk encryption, least-privilege file ownership, and protected backups. Treat backups as production-sensitive data.
* Upgrade the SQLite library/Go driver on a deliberate cadence and audit advisories. Do not trust a system SQLite version without identifying the version actually linked by the driver.

## 7. Backups, Recovery, and Maintenance

1. Back up with SQLite-aware tooling: the online backup API, `VACUUM INTO`, or a coordinated snapshot procedure. Do not copy only the main database file while it is active in WAL mode; account for consistency and the WAL/SHM files.
2. Encrypt/protect backup storage, retain according to the recovery objective, and copy at least one backup off the primary host.
3. Regularly restore into an isolated location and run `PRAGMA integrity_check;` plus application smoke queries. A backup is not verified until restoration succeeds.
4. Alert on backup age/failure, restore-test failure, disk pressure, integrity failures, WAL growth, busy errors, and unusual latency. Completion: the recovery procedure has a dated successful restore test.

## Go Review Checklist

* [ ] Driver and SQLite library versions are identified; DSN/connection initialization is driver-specific and tested.
* [ ] Database access uses contexts, parameter binding, closed rows, `rows.Err()`, and explicit `sql.ErrNoRows` handling.
* [ ] `foreign_keys=ON` is applied and tested for every connection.
* [ ] Transactions are short; their failure and retry semantics are explicit.
* [ ] SQL invariants use constraints; authorization is enforced independently.
* [ ] Indexes are justified by representative `EXPLAIN QUERY PLAN` output and write cost.
* [ ] WAL settings, checkpoints, and lock behavior are monitored and load-tested.
* [ ] Backups are encrypted/protected and restoration plus `integrity_check` is periodically verified.

## Source Notes

This skill synthesizes the following supplied sources. SQLite's official optimizer documentation is authoritative for query-planner behavior; vendor and blog claims are treated as workload-dependent and require measurement.

1. Android Developers — https://developer.android.com/topic/performance/sqlite-performance-best-practices
2. PowerSync — https://powersync.com/blog/sqlite-optimizations-for-ultra-high-performance
3. SQLite — https://www.sqlite.org/optoverview.html
4. Philip Rösler — https://phiresky.github.io/blog/2020/sqlite-performance-tuning/
5. OneUptime — https://oneuptime.com/blog/post/2026-02-02-sqlite-production-setup/view
6. SQLite Forum — https://www.sqliteforum.com/p/sqlite-best-practices-review

## Verification Procedure

1. Inspect schema and migration SQL for constraints, indexes, and unsafe extensions. Completion: every invariant and index has a documented purpose.
2. Run representative request and write workloads; capture latency, throughput, `SQLITE_BUSY` count, WAL size, checkpoint behavior, and storage. Completion: a before/after comparison identifies the environment and dataset.
3. Capture `EXPLAIN QUERY PLAN` for changed and highest-cost queries. Completion: no unexplained scan, temporary sort, or automatic index remains on a latency-critical path.
4. Run integration tests on a fresh database and a newly opened pooled connection. Completion: foreign keys, transactions, cancellation, and error handling are exercised.
5. Restore a current backup to an isolated path and run `PRAGMA integrity_check;`. Completion: restore and integrity results are recorded without secrets.

---

## Notes for this project (Predict Marketing)

This skill is general-purpose reference, kept verbatim from source above. Read
`AGENTS.md` §4a first for what this project's schema actually is before applying
any of the above.

An audit against this skill ran in round 29 and the findings are fixed. The
checklist should come back clean apart from the three items below that are
deliberately not met — contexts, a connection pool and backups. What the audit
changed, and what to leave alone:

- **`foreign_keys` is on**, set in the DSN in `openDB` so it reaches every
  connection the driver opens rather than only the first. `forecasts` has always
  declared `FOREIGN KEY (run_id) REFERENCES runs(id)`; SQLite defaults the pragma
  off, so for a long time that reference was decorative. An earlier version of
  this note claimed the project had no foreign keys at all. That was wrong.
- **`synchronous` is left at the driver's default of FULL**, deliberately. This
  writes once per forecast and is not latency-bound, so there is nothing to buy
  by weakening durability. Section 1 asks for the choice to be recorded: this is
  the record.
- **`PRAGMA user_version` gates every open** (`checkSchema`). Section 3's warning
  about `CREATE TABLE IF NOT EXISTS` not being a migration was not theoretical
  here — the repo's own `pm.db` predated per-entity storage, and every command
  that touched it failed with a bare `no such column: entity`. A file whose shape
  this build cannot read is now refused with an explanation.
- **`forecasts_median` is a partial index** on `(entity, run_id, metric, day)
  WHERE quantile = 0.5`. `forecast_accuracy` ends `WHERE f.quantile = 0.5`, and
  quantile is the *last* column of the primary key, so every query through the
  view scanned the whole table. Section 4's `EXPLAIN QUERY PLAN` step is what
  found it. Measured on 2.3M rows: accuracy 1.6s → 12ms, filter validation
  1.16s → 1ms. If a plan for a view query ever shows `SCAN forecasts` again, that
  index stopped being used.
- **`SetMaxOpenConns(1)`**, not a connection pool — unchanged, and intentional.
  `AGENTS.md` records why: this is a single-user CLI tool, and one connection
  removes a whole class of contention this skill is written to manage. Section 2's
  pooled-connection guidance does not apply unless that changes.
- **No contexts** on queries, against Section 4's advice. There is no request
  lifecycle and nothing to cancel in a CLI that runs one command and exits, so
  `QueryContext` everywhere would be churn. Revisit only if this ever serves.
- **Backups are deliberately absent** (Section 7). Everything in the database is
  derived from the CSV exports and can be rebuilt by re-importing them, so the
  exports are the thing worth backing up, not this file.

Non-finite values are worth knowing about, because the database is not the guard:
SQLite converts a NaN bind to NULL, which `value REAL NOT NULL` then rejects, but
it stores `Infinity` without complaint. `parseCell` in `ingest.go` is what keeps
both out, and `sqlite_test.go` pins that division of labour.
