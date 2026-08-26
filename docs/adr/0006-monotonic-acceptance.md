# ADR 0006 — Monotonic acceptance; output never worse than input

**Status:** Accepted (2026-08-26, from review rounds 2–3)

## Context

An iterative rewrite loop optimizing a score will game the score: repetitive function
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
3. `tells(candidate) ⊑ tells(current)`, where `⊑` compares a **vector**, not a
   count — see below.

Improvement is required on `d` alone; conditions 2 and 3 are non-regression guards. Ties
inside ε are rejections. Passes are capped.

**Refusal:** if `d` is unavailable on either side — insufficient evidence at both tiers, or
an uncalibrated profile — no acceptance is possible. The segment is reported unscoreable
and passed through untouched. Absence of measurement is never treated as improvement.

The deterministic `tells` pre-pass is **not exempt** from this gate. A rule whose edit fails
`preserve` has that individual edit reverted, not the whole pass.

`preserve` is deterministic: numbers, named entities, negations, URLs and quoted strings
must survive an edit.

**Tell counts are compared as a severity-lexicographic vector, not a number.** An earlier
version of condition 3 compared a single total, which permits trading one severe finding for
several minor ones — the total falls while the prose gets worse in the way that matters.

Comparison runs highest severity first: fewer errors wins outright, and only on a tie do
warnings decide, then infos. A componentwise "no level may increase" rule was tried and
rejected, because it makes four new infos worse than one new error, which inverts the
question. Four infos in place of one error is an improvement.

Three restrictions on what may enter the vector, each closing a way for the gate to block
work it cannot justify:

- **Only verdict-eligible categories.** A formatting rule firing more often is not evidence
  the rewrite got worse at the thing being measured.
- **Only DERIVED findings.** An unvalidated rule that could veto a rewrite would be making
  exactly the claim its provenance denies. A consequence worth stating plainly: while every
  shipped rule is unvalidated, this gate is **inert** — it blocks nothing. That is the
  honest state, and better than a gate that is confidently wrong.
- **Both sides from the same rule-set digest and the same options**, with suppression
  disabled on both. A candidate that could waive its own findings would win by writing a
  comment. A **truncated** report is a lower bound, not a count, and cannot participate at
  all.

## Consequences

- Output is never worse than input by construction, since every accepted state improves on
  its predecessor and the input is the first state.
- Before/after artifacts and rejection reasons are retained, making rejections auditable.
