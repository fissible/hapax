# The `select` oracle

An independent implementation, in Python, of the exemplar-selection procedure
declared in `docs/DESIGN.md` — standardization, the self-referential mid-rank
and probit transform, valid-pair Delta, k-nearest density, the 75% eligibility
cut, and round-robin stratum medoids.

It generated every pinned value in `internal/exemplar`: the selected identities,
the per-round medoid sums, the exact tie sequences, and the digests of the
complete density and eligible records.

## Why it is here rather than deleted

The test review's central finding was that the suite checked the certificate
against itself. The fix was to derive the expected values from something that
is *not* the implementation. That property only survives if this stays
independent:

- **Never port a line from `internal/exemplar` into this.** If the two are
  written from each other they agree by construction and prove nothing.
- **Read `docs/DESIGN.md`, not the Go**, when extending it.

It is Python in a Go repository on purpose. A second Go implementation would be
too easy to make agree by accident.

## Running it

The scripts read `candidates.json`, a dump of real feature vectors from the
pipeline. That dump is not committed — it is produced by a throwaway Go test
that walks the fixtures and writes the vectors, which is quicker to rewrite than
to keep in sync. `oracle.py` documents the shape it expects at the top.

## A known limit, and why the digests are rounded

Python's `NormalDist.inv_cdf` and Go's `math.Sqrt2 * math.Erfinv` are the same
function by different algorithms and agree to about one unit in the last place —
Go gives `0.29466127872705117` where Python gives `...122`. The frozen tests
therefore hash density values at **nine fixed decimals** rather than at full
precision. Hashing the full strings pins that last bit, which is not a property
this project claims: DESIGN says determinism is same-binary, not
cross-implementation.
