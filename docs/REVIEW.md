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

## Round 12 — VERDICT: REVISE (raised while scoping the discrimination gate)

**"A predeclared minimum AUC" declared no minimum, no orientation and no tie rule.** Three
omissions, and the middle one is the dangerous kind. `d` is a distance, so discrimination
means `P(d_author < d_distractor)`; an implementation reaching for the conventional
"probability the positive scores higher" computes 1 − AUC and reports 0.15 for a profile
that separates cleanly. That is low enough to read as failure and high enough not to read as
a bug, and no arithmetic anywhere would object.

→ Orientation written into Section 2 as a formula rather than left to the reader. Ties count
as a half, the Mann–Whitney convention, stated because `d` ties often: it is a mean of six
rank-transformed deviations that are themselves capped by the reference size.

**The floor is a judgement and is labelled as one.** 0.80. Unlike the band minimums nothing
derives it — it answers how good is good enough. What informs it is that this tool's output
drives edits to the user's own writing: ADR 0006's loop accepts a rewrite whenever `d`
improves, so a barely-discriminating `d` turns that loop into noise-driven vandalism. Also
recorded is that v1's six Tier A features at paragraph scale may well not clear it, and that
this is the designed behaviour rather than a number to relax later.

**The same bootstrap degeneracy, mirrored.** The band floor found that a rate observed as
zero resamples to zero and reports an over-confident upper bound. Perfect separation is the
same failure from the other end: AUC observed at 1.0 resamples to 1.0 and reports a lower
bound of 1.0 for a profile that has merely never misordered a pair yet.

→ The bound is the lesser of the bootstrap percentile and 1 − 3/*c* over the smaller class's
cluster count. The floor then implies fifteen clusters per class, which is less demanding
than the band gate's thirty and sixty — the right ordering, since a band makes a narrower
claim than the profile as a whole.

**Nothing said the gates compose, or in which direction.** ADR 0005 lists two gates and
leaves a caller to apply them.

→ Discrimination is prior: below its floor no band is emitted whatever the band-level
evidence says. The reverse does not hold. The composition is owned by a release verdict
rather than left as a convention, for the same reason the band collapse was moved inside the
calibration — a rule a caller must remember is a rule a caller will forget.

## Round 11 (continued) — raised by the reviewer of the frozen-first test suite

**The rule-of-three floor smuggled back the independence assumption the bootstrap exists to
avoid.** The floor was written as 3/*n* over segments. But a hundred error-free segments
drawn from one document are one independent observation, not a hundred, so 3/100 would claim
a bound of 0.03 from evidence supporting nothing of the sort — and it would do so
immediately after the bootstrap had gone to the trouble of resampling clusters precisely
because segments are not independent. A floor is not exempt from the assumption the estimator
rejects.

→ The denominator is the **cluster** count. The consequence is much more demanding and is
published rather than softened: a band needs ⌈3/*p*⌉ *clusters*, which is **60 held-out
author documents** and **30 distractor clusters** at the v1 targets. That is what a claim
about an error rate costs.

**Nothing applied the collapse.** The gate decided which bands may be emitted and left a
consumer to read the reports and apply the thresholds itself — which is exactly how a label
the gate refused gets emitted anyway.

→ The calibration carries the classification. The threshold artifact answers a geometric
question, which side of the boundaries a distance falls on; the calibration answers the one
that matters, which label may be emitted, collapsing refused bands to `drifting` and
refusing entirely when uncalibrated. `score` and `rewrite` are given only the second.

**ADR 0005 still carried the vacuous contract.** Section 2 superseded it, but an accepted ADR
saying a band fails by missing "its minimum held-out count or its declared interval" would
have let the old reading back in.

→ Amended in place, with the reason recorded: neither quantity existed.

## Round 11 — VERDICT: REVISE (raised while scoping the band calibration slice)

**The band calibration test did not test anything.** ADR 0005 and Section 1 both required
that a band's "observed author-versus-distractor rate must fall inside its declared
confidence interval". A point estimate always lies inside an interval computed from it, so
on that reading the check is vacuous; on the other reading it refers to a pre-declared
acceptable range which appears nowhere in the design. This is the third time in this review
that a rule has read as a control and controlled nothing — the crossing rule, `z_max`, and
now this.

→ Replaced with a bound against a target: a claiming band is emitted only when the one-sided
upper confidence bound on its own error rate, measured on Test, is at or below the target
that band's threshold was built to respect.

**Only two of the three bands make a claim.** The rule was written as though all three
needed calibrating. `drifting` asserts nothing about authorship, so there is no error rate to
bound and no evidence that could back it.

→ `drifting` is the fallback rather than a gated band. A failed claiming band collapses into
it, and when both claiming bands fail the result is `uncalibrated` rather than a band set in
which every segment lands in the one band that means nothing.

**"The observed rate of author versus distractor segments landing in it" names the wrong
quantity.** That is the band's composition, which depends on how many distractors the user
supplied. It is a property of the pool, not of the method.

→ The gated quantity is class-conditional — `P(distractor lands in range)` over all held-out
distractors — which is the same quantity the threshold targets bound, re-measured out of
sample. Composition is reported and not gated on.

**A bootstrap upper bound degenerates exactly where this gate lives.** The gate asks whether
an error rate is small, and the good case is a rate observed as zero — which resamples to
zero every time, so the bootstrap reports an upper bound of 0. That is the most
over-confident answer available and it is worst where the evidence is thinnest. Noticed while
building the fixtures, not from the prose.

→ The bound is the greater of the bootstrap percentile and 3/*n*, the rule-of-three bound on
a zero count. A declared conservatism, stated as such: never claim a tighter bound than a
perfect independent sample of this size could support.

**The minimum held-out count then stops being a separate rule.** Since the bound is at least
3/*n*, a target of *p* cannot be cleared below ⌈3/*p*⌉ — 60 author and 30 distractor segments
at the v1 targets. It is reported so a user knows how much more writing a band needs, and not
tested separately: one rule that implies the other is better than two that could disagree.

**The gate has to run on Test.** The thresholds were fitted on Calibrate to hit their targets
there, so re-measuring on Calibrate asks whether the fit fits.

## Round 10 (continued) — raised by the reviewer of the frozen-first test suite

**The seed could be recorded, identifying, and entirely unused.** Naming PCG fixed the
algorithm but not how a convenience method consumes it, so an implementation could record the
declared seed, put it in the identity, and draw from a fixed hidden stream. Every structural
assertion would still pass.

→ The draw is specified as a pure function of the seed: one SplitMix64 stream per class,
initialised to `seed + 1` and `seed + 2`, consumed one draw per cluster per resample, index
taken modulo the cluster count. That is reproducible by a second implementation, which is
what makes exact endpoint assertions possible — and an exact endpoint is the only thing that
proves the seed was used at all. Naming PCG also left its two seed words undeclared, which
this removes.

**The draw specified an index and never said what it indexed.** A cluster index is
meaningless without a declared cluster order, and first-seen ordering would make the interval
depend on the sequence the caller supplied its segments in.

→ Clusters are ordered lexicographically by label. That makes the procedure a function of the
population rather than of its presentation, which is the property the identity already
assumes elsewhere in this section.

**"Too wide to be actionable" had no test.** ADR 0005 has required it throughout; the
interval slice computed widths and never judged them.

→ Derived rather than declared: the two intervals must not overlap. If they do, the data does
not resolve the boundaries from each other and the three regions are not distinguishable.
Usability and actionability are reported separately because their remedies differ — more
writing versus better separation — and the artifact carries the conjunction so a consumer
cannot check one and miss the other.

**A correction to a finding, recorded because the reasoning is instructive.** The reviewer
held that a thin fixture of twenty distances over four documents qualifies only when all four
documents are drawn exactly once, giving 4!/4⁴ ≈ 9.4%, and called the fixture flaky.
Exhaustive enumeration over all 256 draws gives **56.25%**. The step that was missed is that
a resample omitting the document holding the largest value can still qualify on the next
value down. The fixture was changed anyway — to one document per distance — because the
finding it exists to demonstrate is about the tail rather than the cluster count, and the
fixture should say so directly.

## Round 10 — VERDICT: REVISE (raised while scoping the interval slice)

**The clustering unit was specified as "by document and author" and one half of it no longer
exists.** Section 2 has said clustered bootstrap by document and author throughout. The
resolution of issue #2 closed without a distractor corpus carrying per-author labels, so on
the distractor side the author cluster is usually unavailable — and nothing said what
happens then. Silently falling back to document clustering would be the worst outcome
available, because it does not fail: it produces a narrower interval than the truth, since
two documents by the same unlabelled author are counted as independent evidence.

→ The artifact names its clustering unit and flags document-only clustering as understating
uncertainty. Author clustering is used wherever labels exist. This is the same treatment ADR
0006 gives its inert tells gate: state the weakened guarantee rather than let the number
imply the strong one.

Writing that down also forced a distinction the phrase "by document and author" had been
hiding: the unit is not the same on both sides. Clustering the author's own distances by
author would collapse that entire class into one cluster and leave nothing to resample. The
author side clusters by document — the within-author variation — and the distractor side by
author, the between-author variation, exactly as this section's own resampling rule says.

**The threshold minimum turned out not to be a shipping minimum, by a factor of three.**
Round 9 derived ⌈1/*p*⌉ as the sample size at which a threshold exists. Simulating the
bootstrap on populations of that size shows why that is not enough: a threshold at the
derived minimum rests on a single tail observation, and any resample drawing it twice
qualifies nothing. At `p_author` = 0.05 with twenty author distances only about 58% of
resamples qualify — and it is not the cluster count that is short, since the figure barely
moves when every distance is given its own document. Sixty reach roughly 98%.

→ Published rather than declared as a second minimum: the 90% qualification floor already
enforces it, against the population actually supplied rather than a number picked in
advance.

**Every parameter of the bootstrap was unstated**, which for an interval means the reported
width was whatever a default produced.

→ Confidence level 0.95, two-sided, percentile method; 2000 resamples, so each endpoint is
the 50th order statistic; a fixed declared seed. All stand-ins, all in the interval's
identity. The seed matters beyond reproducibility: a bootstrap is the only randomness in
this pipeline, and Section 2 forbids a score that changes on re-run.

**A resample can fail to yield a threshold, and nothing said what that meant.** A resample
drawing few distinct clusters can leave no value meeting its target.

→ Counted and excluded from the percentiles rather than aborting, matching this section's
existing treatment of degenerate cases — but with a floor, because an interval assembled
from a heavily degenerate resample distribution describes a different population than the
one asked about. At least 90% of resamples must qualify.

**Nothing tied an interval to the boundaries it describes.** An interval computed from one
population and attached to thresholds calibrated on another is a statement about a boundary
nobody drew.

→ The population must reproduce the threshold artifact's identity. Cheap, because that
identity already covers the populations, the targets and every binding.

**Cluster labels do not belong on the distance.** A `deviation.Distance` knows nothing about
which document produced it, and adding it there would push corpus structure into the scoring
arithmetic.

→ They go on `ClassedDistance`, which is `eval`'s own pairing type, and they are optional:
`Calibrate` needs no clusters and is unchanged, `Interval` requires them. That keeps the
frozen calibration tests valid and puts the requirement where it is actually used.

## Round 9 — VERDICT: REVISE (raised while scoping the threshold and band slice)

**The crossing rule fired on good separation and stayed silent on bad.** Section 2 assigned
`t_low = Q_distractor(p_distractor)` and `t_high = Q_author(1 − p_author)` unconditionally,
and declared the two targets jointly unsatisfiable whenever `t_low ≥ t_high`. Reading that
condition rather than trusting the words: `t_low ≥ t_high` means the distractors' lower
quantile sits above the author's upper quantile, which is what **well-separated**
distributions look like. Measured on synthetic populations at the v1 targets, the refusal
fired on clean separation and did not fire on heavy overlap. As specified, the profile that
discriminates best emits no bands and the one that barely discriminates emits them.

This is the failure mode this section's own summary names — arithmetic that is internally
consistent and controls nothing — surviving three review rounds because the rule was checked
for internal consistency and never against a population.

→ The two quantiles are ordered before use: `t_low = min(A, D)`, `t_high = max(A, D)`. Both
declared targets still hold by monotonicity, since `min(A, D) ≤ D` and `max(A, D) ≥ A`. In
the overlap case this reproduces the old assignment exactly; in the separated case the
thresholds take their values from the opposite distributions, both achieved rates fall below
target, and `drifting` spans the gap where neither population has mass — the honest label
for a segment unlike both. The unsatisfiable case does not exist and its refusal path is
removed.

Section 2's warning against taking each threshold from the other distribution still stands
for the *unconditional* swap. Swapping only when `A < D` is safe because that inequality is
exactly what makes both bounds slack; the condition is the proof.

**`p_author` and `p_distractor` had no values.** Declared quantities named throughout, never
given numbers.

→ 0.05 and 0.10 for v1, asymmetric because the errors are, `p_author` tighter because
telling someone their own prose is not theirs is the more damaging error. Declared
stand-ins, in the threshold artifact's identity.

**The minimum sample sizes were declared nowhere and turn out not to need declaring.**

→ They are derived, which makes them the exception here. Thresholds are chosen from observed
distances, so the smallest achievable non-zero error on *n* observations is 1/*n*, and a
threshold meeting target *p* exists only when 1/*n* ≤ *p*. That forces ⌈1/`p_author`⌉ author
and ⌈1/`p_distractor`⌉ distractor distances — 20 and 10 at the v1 targets. Below either, no
threshold respecting that target exists at all, so the outcome is no bands rather than a
boundary extrapolated past the data.

**A threshold artifact was not bound to what it was calibrated on.** Round 8 established
that any proper subset of the manifest's tiers carries its own thresholds, and left the
binding implicit.

→ The artifact records the tier subset, the profile, the deviation reference, the feature
manifest digest, the weighting scheme and the distance algorithm, and banding refuses a
distance that differs on any of them. The same rule as the contributing-feature set on `d`,
one level up.

**Two things the artifact was calibrated against could not be read off the distances.**
Both raised by the reviewer of the frozen-first test suite. The **distractor pool**: Section
2 has required figures reported per `(profile, distractor pool)` pair throughout, and
nothing carried the pool. The **calibration cohort**: admitting only the Calibrate split
checks a role, not the identity of the documents filling it, so two cohorts producing the
same boundaries were indistinguishable and stale calibration evidence could be reused under
a new corpus.

→ Both are named at calibration, recorded, and in the identity. Neither is checked at
banding: both describe how the boundaries were drawn, not the segment being scored.

**The identity could have omitted the declared targets and the populations** and still
matched every test, because the cases that varied them also moved the boundaries.

→ Distinct target pairs can select the same observed bounds, and so can distinct
populations; both are in the identity, and both are now tested by cases that hold the
reported numbers fixed.

## Round 8 — VERDICT: REVISE (raised while scoping the distance `d`)

**`z_max` survived a change that removed its job.** Winsorization was specified when
deviations were unbounded standardized values. Round 7 made them bounded by construction,
and nobody went back to ask what winsorization was still for. The arithmetic answers it: at
a conventional 3 it does not bind until a feature carries 370 reference values, against an
illustrative reference size of thirty, so as specified it is inert; set low enough to bind
at realistic sizes it discards evidence the reference supports, and does so with one flat
constant while the existing bound already scales per feature with the evidence behind it.

→ Struck, with the same reasoning that struck `λ`: a parameter inherited from an earlier
design that a later one made redundant, kept only because nothing forced the question. The
cost of keeping it was not zero — a slot in the cache identity, a field in the reported
record, and an owed sensitivity analysis over a value that changes nothing.

**"Neither tier meets its minimum" named a minimum that was never stated.** The refusal rule
has been in Section 2 and ADR 0006 throughout, and the quantity it turns on did not exist.

→ A majority of the tier's manifest features must be available. Stated as a proportion
rather than a count so it does not weaken as the manifest grows, and declared rather than
derived, like every other minimum here.

**`d` was a bare scalar, and ADR 0006 compares two of them.** The acceptance loop requires
`d(candidate) ≤ d(current) − ε`. A rewrite can change which features are available, and a
mean over one feature set is not comparable to a mean over another — the loop would accept a
rewrite that moved only the denominator.

→ `d` carries the set of features that produced it, so the comparison can be refused rather
than silently made. Cheap here, and impossible to reconstruct later.

**Tier B does not exist, and the design read as though it were merely empty.** ADR 0003 puts
three features in Tier B, all needing a rolling window that is not built. The manifest
declares six Tier A features and nothing else, and `TierB` is not a declared constant. A
first pass at this round proposed declaring it and building the tier machinery against it;
that was wrong. An empty tier is a tier whose minimum can never be met, so every v1 score
would be flagged partial against something that does not exist, and the machinery would be
"exercised" only over a tier with no features in it.

→ The tier set is derived from the manifest instead. Today it resolves to one tier and there
is nothing to blend; the day a Tier B feature is added it resolves to two, with the per-tier
minimums and the partial-score rule already in force and no code change. The manifest digest
changes at that same moment, so no threshold artifact crosses the boundary.

**The Tier-A-only threshold rule was stated as a case rather than a rule.** It said what
happens when Tier B is unavailable and left Tier A unavailable with Tier B available
undefined — reachable in principle, and silent.

→ Generalized: any proper subset of the manifest's tiers carries its own threshold artifact
and is flagged. The reason was never specific to Tier A.

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

**The reference split was a caller-supplied label with nothing behind it.** Raised by the
reviewer of the frozen-first test suite. A reference is declared to be built on Calibrate,
but a standardization carried no record of where it came from, so Train or Test segments
passed with a Calibrate argument and the split boundary the whole reference rests on was
enforced by the caller remembering it.

→ Each standardization carries its own split, set at the point that actually knows it, and a
reference refuses any segment not from the split it claims. The package still cannot verify
a split it is never told — but it can refuse to accept a claim nobody made, which is the
difference between an unchecked argument and a checked field.

**Serialization order and undefined-cause precedence were being frozen in tests without
being stated anywhere.** Also raised by the test reviewer, and correct as an objection: a
test file is not where design is decided.

→ Both adopted into Section 2 rather than dropped. Manifest order is load-bearing because
the reference and the deviation record are hashed into the scoring cache identity and an
unordered set has no canonical hash. Precedence is load-bearing because the causes imply
different remedies, and which one a user is shown should not depend on the order the guards
happened to be written in.

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
