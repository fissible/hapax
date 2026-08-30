# ADR 0010 — A pure-Go SQLite artifact store

**Status:** Accepted (2026-08-29, from the `cli` design round)

## Context

Every component so far produces artifacts with content-addressed identities — profiles,
references, thresholds, releases, exemplar selections, rewrite attempts — and nothing
persists them. `cli` cannot be a thin adapter over components whose results vanish, and
building it against an unbuilt store would repeat the mistake component row 12 warned about:
formats and contracts becoming afterthoughts.

The store is also where the privacy invariant stops being a design intention and becomes an
implementation with a schema. That is not a detail of a command-line front end.

## Decision

**`store` is its own component, built before `cli`.** It exposes typed per-artifact
operations — never a generic "put an artifact" API, which would make the persistence
allowlist unenforceable by construction.

**SQLite through `modernc.org/sqlite`,** the CGO-free implementation. ADR 0001 commits to a
single static binary; a CGO driver would end that, and cross-compilation with it is a
recurring cost rather than a one-time one. The price is a large dependency implementing a
database engine in Go, and slower writes than the C library — neither of which binds at the
scale of one author's corpus.

**The corpus stays as files the user owns.** The store holds derived artifacts and span
references. It is never the system of record for anyone's writing.

**Migration is forward-only,** with the schema version recorded in the database. An older
binary meeting a newer database refuses rather than guessing.

## Consequences

- `modernc.org/sqlite` and its transitive dependencies join the module. Driver updates are
  ours to track.
- The privacy invariant's scope is now concrete: the database file, its WAL and journal
  sidecars, any backup or export the tool writes, and all log and diagnostic output.
- The persistence allowlist is enforced by the typed API's shape, and reviewed when extended.
  Corpus-text scanning stays a regression control, never proof.
- Rehydration failure is an ordinary state, not an error: a span references a file the user
  may edit, move or delete.
