# Design review log

Section 1 of `docs/DESIGN.md` was reviewed adversarially before any code was written. The
reviewer was instructed to be hostile rather than agreeable. This log records objections
that changed the design, objections that were rejected and why, and the arguments that
settled each.

It is kept in the repository because the objections are more instructive than the design
they produced.

---

## Round 1 — VERDICT: REVISE

### Accepted

**Burrows' Delta is invalid at paragraph scale.** Delta assumes stable relative frequencies
over a substantial sample. In a ~60-word paragraph most function-word counts are 0–2, and
z-normalization amplifies sampling noise. Emitting an 0–100 score there implies precision
the method does not have.

→ Feature tiers by required sample size; Delta restricted to rolling windows; `insufficient
evidence` as a first-class return value. ADR 0003.

**Shared scoring/retrieval feature space is degenerate.** Nearest-neighbor retrieval
against a failing draft selects the author's passages most similar to the *defective* text,
conditioning the model on evidence of the flaw it is meant to remove.

→ `score` and `select` separated; selection uses profile medoids and high-density regions,
never draft similarity. ADR 0004.

**Missing components.** No normalization/tokenization contract (which determines every
feature), no corpus qualification, no calibration harness, no semantic-preservation gate,
no profile versioning or provenance, no prompt-injection handling for untrusted corpus
text, and `cli` placed last rather than early as a contract-fixing shell.

→ `text` added as component 0; `eval` and `preserve` added; profile versioned, provenance-
carrying and per-register with a minimum-corpus refusal; `cli` moved early; corpus text
fenced as untrusted during prompt assembly.

**Real-use failure modes.** Mixed-genre corpora flattening legitimate register changes;
Markdown stripping removing genuine voice; rewrite iterations optimizing the score rather
than the prose; "converged" masking oscillation or cap exhaustion.

→ Named per-register profiles; configurable retention policy; the acceptance rule in
ADR 0006.

### Rejected

**"Cut iterative rewriting; ship one pass."** The score → rewrite → rescore loop is the
product thesis and the entire differentiator against the existing scaffolder projects. The
reviewer's underlying concern — score-gaming and oscillation — was real and was addressed
mechanically instead, via monotonic acceptance with preservation and tell gates, a pass
cap, and retention of the original.

*Reviewer conceded in round 2:* "The gated loop is the product thesis. With monotonic
acceptance, preservation, clean tells, a finite pass cap, and retention of the original, it
has a sound v1 safety envelope."

**"Cut cloud providers; ship Ollama only."** Behind a provider interface a second
implementation is small, and small local models are measurably weaker at style mimicry —
the hard task. Shipping local-only would ship a tool whose core function underperforms.

*Reviewer withdrew in round 2*, while correctly noting the real cost is provider lifecycle
(credentials, retries, cancellation, request-size limits, integration tests) rather than
lines of code, and conditioning withdrawal on `--local-only` being a tested guarantee with
no silent fallback. Both accepted. ADR 0007.

**"Validate the feature set by ablation before v1."** Accepted as a goal, rejected as a
release gate: that is a research program, not a release.

*Partially upheld.* The reviewer conceded ablation, but correctly held that author-versus-
distractor ranking accuracy alone cannot calibrate band boundaries — it establishes
discrimination, not that a label means anything. Band calibration became a separate gate.
ADR 0005.

---

## Round 2 — VERDICT: REVISE

Five remaining blockers, all accepted:

1. Evaluation needed a defined split unit and leakage controls — hold out whole documents
   before profiling, register-matched distractors, version-stamped results.
2. Band calibration needed its own release gate, not just ranking accuracy.
3. Target register must be explicit (`--profile essays`) rather than left to an unspecified
   classifier.
4. The deterministic pre-pass must itself be preservation-gated, and monotonic acceptance
   and ties defined, so "never worse than input" is mechanically true rather than asserted.
5. The cloud provider must be named and `--local-only` tested as no initialization, no
   fallback, no telemetry, no outbound connection.

---

## Round 3 — VERDICT: REVISE

Three consistency defects, all accepted:

1. **Only discrimination had a floor.** Band calibration had no predeclared acceptance
   criterion or failure behavior. → Two gates defined, each with its own failure mode.
2. **Diagram contradicted the rule.** The diagram required all three measures to improve;
   the formal rule permitted unchanged tells. → The formal rule declared authoritative, the
   diagram labeled a sketch and corrected, and `preserve`/`tells` stated explicitly as
   non-regression guards rather than improvement requirements.
3. **The compared scalar was undefined.** `score()` returns a band plus feature deltas, so
   `score(candidate) > score(current) + ε` had no meaning — and the direction was wrong for
   a distance measure. → `d`, the calibrated profile distance, defined explicitly as the
   compared quantity, with the direction corrected and refusal behavior specified for
   unavailable measurements.

---

## Round 4 — VERDICT: REVISE

One blocker, accepted: **two meanings of "band" in one design.** `profile` claimed its
variance "defines the author's tolerance band" while `eval` was declared to own the
calibrated bands `score` emits.

→ `profile` now describes per-feature distribution statistics only — means and variances
used to normalize feature deviations — and declares no band boundaries. Every emitted band,
and all fallback and collapse behavior, belongs to `eval`.

*Also fixed on self-review before this round:* `select` still said it filtered exemplars to
match the draft's register, contradicting the explicit `--profile` decision from round 2.
Register now comes from `--profile` only and is never inferred.

## Round 5 — VERDICT: REVISE

One blocker, accepted, and a direct consequence of the round 4 fix: **`eval` was missing a
dependency on `corpus`.** Once `profile` carries only distribution statistics, it cannot
supply the held-out source-document split or the register-matched distractor segments.

→ `eval` dependency corrected from `3, 4` to `2, 3, 4`, with the consumed inputs named.

## Round 6 — VERDICT: APPROVE

No remaining Section 1 blockers.

---

## What the review changed

Two objections were structural and would have produced a tool that lied about its own
accuracy: paragraph-scale Delta, and the degenerate shared feature space that would have
fed the model the author's most AI-adjacent writing as evidence of their voice. Four
further rounds were consistency work — an undefined comparison scalar, a diagram
contradicting its own rule, a term meaning two things, and a dependency edge that only
became wrong once an earlier fix landed.

Three proposed v1 cuts were rejected and argued down: cutting the iterative loop, cutting
cloud providers, and gating release on feature ablation. Two were conceded by the reviewer;
the third was partially upheld and improved the design.

---

# Section 2 — Feature set, distance, and calibration

## Round 1 — VERDICT: REVISE

Six required changes, all accepted. Four were statistical errors rather than omissions.

**The tier derivation conflated three quantities.** "Sampling SD below *k* × between-author
SD" mixes within-author variation across occasions, finite-segment measurement error, and
variation in author *means*. Only the first two are properties of the feature; the third is
a property of a declared population and changes when that population changes.

→ Population declared explicitly; components separated by clustered resampling over held-out
whole documents; minimum defined against a bootstrap upper bound rather than a point
estimate; degenerate cases marked *not usable* rather than assigned a minimum.

**Standardizing by the profile SD ignores segment length.** A short segment carries far more
measurement variance than the profile estimate does, so using the profile's σ alone
understates short-segment noise and manufactures confident deviations — the paragraph-Delta
error of Section 1 recurring at the normalization step.

→ Denominator now combines profile variance with length-dependent sampling variance at the
observed length.

**Equal |z| is not equal evidence.** Comparing standardized values across bounded,
zero-heavy, right-skewed rates and roughly symmetric features is indefensible.

→ Count models or variance-stabilizing transforms for rates; empirical-CDF rank transform
against the author's held-out distribution as the general mechanism.

**Weights and `λ` were entirely unspecified** — uniform, expert, inverse-redundancy and
learned weights are materially different models. Clipping was presented as a principle when
it is a robust-loss choice that discards genuine *not you* evidence. Tier-A-only scores were
sharing thresholds with a blended score drawn from a different distribution.

→ Nested Train/Calibrate/Test splits declared; `w`, `λ` and `z_max` fitted on Train only;
winsorization named as such with required sensitivity analysis; `d_A` given its own
threshold artifact.

Also accepted: feature-redundancy pruning is a declared Train-only procedure evaluated
end-to-end, not a substitute for covariance handling, with residual correlation recorded as
a limitation; lexical diversity is a versioned contract, since MTLD and vocd-D have their
own short-sample instabilities; cache identity is content-hashed over every scoring
artifact, because version integers do not force a bump when a golden file can be
regenerated.

## Round 2 — VERDICT: REVISE

**Band quantiles were assigned to the wrong thresholds.** The design set
`t_low = Q_author(...)` and `t_high = Q_distractor(...)`, which controls neither declared
error rate while still producing non-overlapping bands — a failure invisible in testing.

→ Each threshold is now drawn from the distribution whose error it bounds:
`t_high = Q_author(1 − p_author)`, `t_low = Q_distractor(p_distractor)`.

## Round 3 — VERDICT: REVISE

Two corrections. **Crossing was mis-framed** as a target that could not be met, when each
threshold may individually meet its rate and only the *pair* fails to leave a non-empty
middle band → restated as pair incompatibility. **Quantile equalities cannot hold exactly**
on finite, tie-prone samples → conservative selection rule added, boundary randomization
rejected because a score that changes on re-run is unacceptable.

## Round 4 — VERDICT: REVISE

**The conservative extrema were reversed.** Author error decreases as `t_high` rises and
distractor error increases as `t_low` rises, so the rule as written drove both errors toward
zero — collapsing both bands and, worse, guaranteeing the crossing check never fires, which
hides exactly the incompatible pair it exists to detect.

→ Smallest `t_high` and largest `t_low` respecting their targets, with the monotonicity
stated.

## Round 5 — VERDICT: APPROVE

No remaining Section 2 blockers.

---

## What the Section 2 review changed

Every substantive finding was a case of a number looking more trustworthy than its
derivation supported: a tier rule that would have produced fragile thresholds from noise, a
normalization that understated short-segment uncertainty, standardized values compared
across incomparable distributions, and — three times running, in the band logic — arithmetic
that was internally consistent and controlled nothing. The last of these was the most
instructive: reversed quantiles and reversed extrema both produce output that looks correct
and passes any test not written against the error rates themselves.
