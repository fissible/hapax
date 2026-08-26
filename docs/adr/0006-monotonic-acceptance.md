# ADR 0006 — Monotonic acceptance; output never worse than input

**Status:** Accepted (2026-08-26, from review rounds 2–3)

## Context

An iterative rewrite loop optimising a score will game the score: repetitive function
words, distorted syntax, dropped facts. Review initially proposed cutting iteration to a
single pass. Iteration is the product thesis and was defended; the safety envelope had to
be made mechanical instead.

## Decision

The comparable quantity is **`d`, the calibrated profile distance** — one continuous
scalar, lower meaning closer to the author. Bands are labels derived from `d`; the loop
compares `d`, never bands, since band comparison hides sub-threshold progress and stalls
the loop.

`current` begins as the input. A candidate is accepted iff, against `current`:

1. `d(candidate) ≤ d(current) − ε`, and
2. `preserve(current → candidate)` passes, and
3. `tells(candidate) ≤ tells(current)`.

Improvement is required on `d` alone; conditions 2 and 3 are non-regression guards. Ties
inside ε are rejections. Passes are capped.

**Refusal:** if `d` is unavailable on either side — insufficient evidence at both tiers, or
an uncalibrated profile — no acceptance is possible. The segment is reported unscoreable
and passed through untouched. Absence of measurement is never treated as improvement.

The deterministic `tells` pre-pass is **not exempt** from this gate. A rule whose edit fails
`preserve` has that individual edit reverted, not the whole pass.

`preserve` is deterministic: numbers, named entities, negations, URLs and quoted strings
must survive an edit.

## Consequences

- Output is never worse than input by construction, since every accepted state improves on
  its predecessor and the input is the first state.
- Before/after artifacts and rejection reasons are retained, making rejections auditable.
