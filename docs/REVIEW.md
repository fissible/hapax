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

---

# Section 3 — The `text` contract and the artifact store

## Round 1 — VERDICT: REVISE

Six required changes, all accepted. One was a false premise rather than an omission.

**"List items are not sentences" is wrong.** Authored prose appears inside list items,
footnotes, captions and definition descriptions routinely, and Markdown structures nest —
inline code inside prose, quotes containing lists, lists containing quotes. A flat
segment-class model cannot represent any of that, and excluding list items would have
silently removed real prose from the feature population.

→ Replaced with a structural tree distinguishing **containers** from feature-bearing **leaf
text runs**. Whether a list item is prose is decided per item by sentential structure, never
by its container.

**Not every boundary exists in both forms.** `e` + combining acute normalizes to a single
`é`, so the boundary between those two raw code points has no position in the normalized
text. "Constrained to positions valid in both forms" was therefore not always satisfiable.

→ Boundaries constrained to grapheme-cluster boundaries that are also NFC-stable, with a
deterministic outward snap — start backward, end forward — so a span can only grow to the
nearest representable edge and never drops authored content. Invalid UTF-8 rejects the
document rather than being repaired, since repair shifts every offset; BOM stripped and
recorded; CRLF preserved exactly.

**The proposed ablation test establishes nothing.** Removing sentence-derived features and
checking whether discrimination collapses does not show that segmentation *errors* supply
the signal: a collapse may simply mean genuine sentence-level style matters, and no collapse
does not show the errors are harmless.

→ Replaced with a segmentation-robustness evaluation against adjudicated boundaries, plus
controlled boundary perturbations, stratified by author and by error-prone construction —
since the concern is precisely that error rates differ across authors.

**Apostrophes do three different jobs.** Contraction, possessive and quotation. Conflating
possessive with contraction would make contraction rate track how often an author writes
about people's things.

→ Three roles distinguished, possessives excluded from contraction counting, hyphens and
dashes enumerated by codepoint, and a stated recognition precedence with an exact
terminal-punctuation peeling rule.

**Scanning the database for corpus strings cannot prove the privacy invariant.** It misses
normalized, fragmented, encoded, compressed and indexed copies, and says nothing about WAL,
journals, backups or logs.

→ Restated as a prohibition on any reversible prose representation or textual derivative —
naming token sequences, snippets, cached parse text and FTS content — scoped across
sidecars, backups and diagnostic output, with scanning demoted to one regression control
behind an explicit persistence allowlist.

Also accepted: the required exemplar set and count are fixed by the profile and invocation
contract before rehydration, with no substitution and no silent reduction, and a reindexed
profile yields a new identity rather than the same result.

## Round 2 — VERDICT: REVISE

Three narrow defects, all the same kind: the text promised a rule and did not state it. The
snap direction was called "stated" without being stated; terminal punctuation was separated
"by a stated rule" that did not exist; ASCII hyphen was named but not given its codepoint.

→ All three specified concretely.

## Round 3 — VERDICT: APPROVE

No remaining Section 3 blockers.

## Round 7 — VERDICT: REVISE (raised while scoping the deviation slice)

**The transformed deviation had no declared scale, and two later rules presuppose one.**
Correction 2 outputs an empirical-CDF rank, which is a percentile in [0,1]. `z_max`
winsorization is vacuous on a bounded percentile, and a mean of percentiles is not "the same
form as Burrows' Delta", which averages |z|. Round 5 fixed the order the corrections compose
in and left the codomain unstated, which is the same class of omission one step further on.

→ The rank is mapped back through the normal quantile function. Comparability across
features is what ranking buys and it survives the mapping; the quantity lands back on the
scale `z_max`, the Delta analogy, and the band logic all already assume.

**An empirical CDF returns 0 and 1, where the normal quantile is infinite.** With thirty
reference segments this is not an edge case — it is every segment more extreme than the
reference has seen, which at thirty points is one in thirty per tail.

→ The segment is ranked within the reference plus itself, *m* = *n* + 1 values, at the
(*i* − ½)/*m* position with midranks for ties — symmetric in both tails and never 0 or 1.
Ranking against the *n* reference values alone cannot be made symmetric: *n* points leave
*n* + 1 gaps, so any position over *n* is short a cell at one end. Declared rather than
chosen silently, because it is visible in the output: it caps |deviation| at
Φ⁻¹(1 − 1/2*m*) — 2.14 at thirty reference segments, 2.58 at a hundred, 1.69 at ten. At
small reference sizes that cap, not `z_max`, is the operative limit.

**The sign had no stated fate.** `d` takes absolute values, which invites discarding the
sign at the source.

→ Kept. Below the author's usual comma density and above it are different facts, and
`rewrite` needs the direction. It costs a sign bit and cannot be recovered later.

## Round 6 — VERDICT: REVISE (raised while scoping the distance `d`)

**The weighting scheme was asserted as fitted without the conditions for fitting.** Section 2
declared `w` "learned, not asserted", fitted on Train against author-versus-distractor
separation with regularization and constraints stated. None of those terms was stated
anywhere, and two of the preconditions do not currently hold: there is no distractor pool
with author diversity to separate against, and fitting 150+ weights on a personal corpus's
Train split is the over-parameterization the same section rejects Mahalanobis for. A design
cannot reject a covariance estimate at 150+ dimensions as unsupportable and then require a
weight vector of the same dimension from the same data.

→ v1 declares uniform weights, records the choice in the profile artifact and the reported
record, and puts the scheme and its version in the scoring cache identity so a later fitted
scheme cannot be served from a cache built under this one. Fitting stays the intended
destination and arrives with its objective, regularization, constraints, and missing-feature
rule actually stated.

**`λ` was named in three places and defined in none.** It appeared in the Train-fitted list,
in the weighting paragraph, and in the cache identity. Nothing said what quantity it
weighted. A symbol that reaches the reported record without a definition is a field no one
can fill in.

→ Struck. Neither available reading survives the uniform choice: as a regularization
strength it has nothing left to restrain, and as a Tier A/Tier B blend it duplicates
machinery that already exists, since `d` averages over whichever features a segment makes
available and a Tier-A-only score already carries its own threshold artifact. A future
fitted-weights slice reintroduces it with a definition.

## Round 5 — VERDICT: REVISE (raised while scoping the eval deviation slice)

**Two corrections were named without saying how they compose.** Section 2 gives a
length-aware denominator and an empirical-CDF rank transform, and left their relationship
implicit. That is not a detail — ranking raw values and ranking standardized values are
different estimators, and only one of them lets segment length affect where a segment lands.

→ They compose in order: standardize with the length-aware denominator, then rank *that*
quantity. Ranking raw values would silently drop correction 1 and readmit the
paragraph-Delta error; standardizing without ranking would reinstate the incomparability the
section is named for.

**The reference distribution had no declared split.** "The author's held-out distribution"
does not say which held-out split, and the choice decides whether a reported figure is
honest.

→ Built on Calibrate, with figures reported on Test. Train is excluded because the profile
was fitted on it. The cost is stated rather than hidden: half the data per purpose, and a
coarse CDF at small Calibrate sizes, so the minimum reference size joins the published
minimums.

**One denominator cannot serve three feature families.** Correction 1 was written as though
the sampling variance were a single formula.

→ Each feature declares its family — bounded membership rate, unbounded per-token density,
or mean — and the family is part of the manifest, and therefore of the cache identity.

## Round 4 — VERDICT: REVISE (raised while reviewing the slice 2d tests)

One defect, found by the reviewer of the frozen-first test suite rather than of the prose.

**The normalized-to-raw offset map is not needed and claiming it obscures the contract.**
Section 3 required parsing to maintain a map from normalized positions to raw ones. That is
only true of a parser that consumes the normalized form. Parsing the raw bytes instead makes
every reported offset a raw offset already, so the map translates nothing — and a map that
exists but is always the identity is a place for a bug to hide.

→ Section 3 now states that structural parsing runs over the raw admitted bytes and that NFC
is applied only when a span is resolved to text. The requirement that survives unchanged is
the one that mattered: only raw offsets persist.
