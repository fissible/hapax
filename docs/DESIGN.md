# hapax — Design

**Status:** in review, section by section.
**Working name:** `hapax`, from *hapax legomenon* — a word appearing exactly once in a
corpus. The concept names the tool; whether hapax ratio survives as a top-weighted
feature is an empirical question the evaluation harness answers.

---

## Problem

AI-generated prose has a recognizable register. Existing tooling either strips that
register toward a generic "human" voice, or measures style without changing anything.
Nothing open-source closes the loop: take a draft, measure it against the author's own
prior writing, rewrite only what misses, and verify the rewrite actually moved toward
the author rather than merely away from the model.

## Prior art (surveyed 2026-08-25)

Four clusters, none of which is this tool:

1. **Generic humanizers, no personal corpus** — `blader/humanizer`,
   `lguz/humanize-writing-skill`, `harshaneel/humanize`, `jpeggdev/humanize-writing`.
   Banned-word lists plus multi-pass rewrite instructions. Produce *a* human voice, not
   *the author's*.
2. **Deterministic AI-tell linters** — `vale-ai-tells` (80★), `etincel`, `deslopper`.
   Detect, never rewrite, know nothing about the author.
3. **Voice-skill scaffolders** — `personal-voice-capture` (9★),
   `ericporres/voice-as-a-skill`, `style-distiller` (4★), `personal-humanizer-maker`
   (4★), `claude-voice-editor` (3★), `getlago/inside-lago-voice-skill`. Read samples,
   emit a `SKILL.md` prose style guide. One-shot, agent-locked, unverified.
4. **Stylometry that refuses to rewrite** — `setec-voiceprint` (1★, GPL) implements
   Burrows' Delta and idiolect detection but states outright that it does not rewrite.
   *Write Like Me* is the nearest thing to fingerprint-plus-generate.

Commercial: **Idiolect** (closed SaaS; Score API, `rewrite_in_voice` MCP, listed in
Claude's connector directory) and **Grammarly Humanizer** (closed, sample upload for a
custom voice). Both are cloud-only — the corpus leaves the author's machine.

**The gap:** no OSS tool runs corpus → measured profile → rewrite → *rescore against the
profile* → iterate, with an AI-tell linter as a hard gate, entirely locally.

Supporting research: TinyStyler (arXiv 2406.15586) shows accurate authorial mimicry from
as few as 16 style exemplars, which argues for retrieving the author's actual sentences
over describing their style in prose.

---

## Decisions taken

| Decision | Choice | Rationale |
|---|---|---|
| Surface | CLI + library core; Claude Code skill and MCP server as thin wrappers later | Every competitor in cluster 3 is locked to one agent |
| Voice model | Numeric profile (for scoring) **and** exemplar selection (for rewriting) | The two halves are what close the loop |
| Runtime | Go, single static binary | Zero-dependency install; no ML runtime needed given the retrieval decision |
| Retrieval | Stylometric, no embeddings | Semantic embeddings match on *topic*; style is the axis that matters |
| LLM access | Ollama by default, API key optional | Works offline out of the box, upgrades to a frontier model for quality |
| Corpus intake | Directory of files + AI-contamination screening at ingest | Fingerprinting a contaminated corpus faithfully reproduces the slop being removed |
| Pipeline | Segment-wise score → rewrite-only-failures → rescore loop, preceded by a deterministic AI-tell pre-pass | Avoids the mid-document drift that sinks style-card approaches; deterministic fixes should not cost tokens |

---

## Section 1 — Architecture and dependency order

*Revised after review round 1. See `docs/REVIEW.md` for the objection log.*

### Two measurement scales

The central correction from review: **Burrows' Delta is invalid at paragraph scale.** It
assumes stable relative frequencies over a substantial sample. In a 60-word paragraph,
most function-word counts are 0–2, and z-normalization amplifies sampling noise into a
confident-looking number that means nothing.

Features are therefore **tiered by the sample size they require**, and every feature
declares its own minimum:

- **Tier A — dense, valid at paragraph scale.** Sentence-length mean and variance,
  punctuation densities, contraction rate, word-length distribution, clause-marker rate.
  (Terminology: Section 2 distinguishes a bounded membership *rate* from an unbounded
  per-token *density*; these names follow that and correct an earlier inversion.)
  These have many observations per paragraph.
- **Tier B — sparse, requires a rolling window of several hundred tokens.** Function-word
  distribution, hapax ratio, sentence-opener distribution. Delta operates here and
  nowhere else.

A paragraph is scored on Tier A directly and on Tier B via a rolling window centered on
it. When neither has enough sample, `score` returns **insufficient evidence** — never a
number. No score is emitted with more precision than the sample supports.

### Scoring and selection are different objectives

Second correction: the original design reused one nearest-neighbor search for both
scoring and exemplar retrieval. That is degenerate. Retrieving corpus segments nearest to
a failing draft retrieves the author's *most AI-like* writing and conditions the model on
the very defect it is meant to remove.

The two share a feature *extractor* and nothing else:

- **`score`** measures a segment against the profile.
- **`select`** picks segments that are **representative of the author** — medoids and
  high-density regions of the **named** profile — diversified by structure. Register comes
  from the `--profile` flag, never inferred from the draft, and selection never consults
  the draft's style vector.

### Pipeline

```
corpus/ ─▶ [text] ─▶ [corpus] ─▶ [features] ─▶ [profile.json] ─▶ [eval] ─▶ thresholds
              │          │                          │                          │
              │      contamination                  │                          │
              │      screen (tells)                 ▼                          ▼
draft.md ─────┴─▶ [tells] ─▶ [segment vectors] ─▶ [score] ──────────────┐
                  mechanical                          │                 │
                  pre-pass                            ▼            fails?│
                                       [select representative] ◀─────────┘
                                                      │
                                                      ▼
                                            [rewrite via llm]
                                                      │
                                    ┌─────────────────┼─────────────────┐
                                    ▼                 ▼                 ▼
                              [preserve gate]   [tells gate]      [rescore → d]
                                    └─────────────────┴─────────────────┘
                                                      │
                              d improved by ≥ε, and preserve/tells not regressed?
                                          accept : keep current
```

*Sketch only — the authoritative acceptance rule is stated under "Acceptance is monotonic
by construction" below.*

### Components, ordered leaves → roots

| # | Component | Responsibility | Depends on |
|---|---|---|---|
| 0 | `text` | Unicode normalization, tokenization, sentence-boundary detection, contraction handling, Markdown extraction with a configurable retention policy, English-only language gate. **Every feature is determined by these choices**, so the contract comes first. | — |
| 1 | `tells` | Deterministic AI-tell linter. Rule *schema* before rules: id, pattern, severity, source span, suppression syntax, register scope. Serves both draft linting and corpus contamination screening. | 0 |
| 2 | `corpus` | Walk, parse, dedupe, per-source provenance (path, mtime, git date), minimum-length gates, contamination screening, register tagging, held-out split reserved for `eval`. | 0, 1 |
| 3 | `features` | Tiered extractor. Each feature declares its minimum sample size and tier. | 0 |
| 4 | `profile` | Versioned, provenance-carrying, **named per register** (`essays`, `email`). Per-feature distribution statistics only — means and variances, used to normalize feature deviations so a writer whose sentence length naturally varies is not penalized for varying. Outlier handling. **Refuses to emit** below a minimum corpus size. Declares **no** band boundaries: every emitted band, and all fallback and collapse behavior, belongs to `eval`. | 2, 3 |
| 5 | `eval` | Held-out calibration harness. Consumes the held-out source-document split and register-matched distractor segments from `corpus`, and the distribution statistics from `profile`. Produces both the discrimination metric and the band boundaries `score` uses. Score has no meaning without it, so it is not optional. Protocol below. | 2, 3, 4 |
| 6 | `score` | Tier A at paragraph scale, Tier B over rolling windows. Emits a calibrated band plus per-feature deltas plus direction, or *insufficient evidence*. Requires an explicit `--profile`. | 3, 4, 5 |
| 7 | `select` | Author-representative exemplars: medoids and high-density segments of the **named** profile, diversified by structure. Never nearest-neighbor to the draft. | 2, 3, 4 |
| 8 | `preserve` | Deterministic semantic-preservation gate. Numbers, named entities, negations, URLs and quoted strings must survive an edit or it is rejected. Applied to **every** mutation, mechanical and LLM alike. | 0 |
| 9 | `llm` | Provider interface. Ollama first, Anthropic as the one named cloud provider. Corpus text fenced as untrusted data in prompt assembly. Hard `--local-only` mode; cloud failure is a hard error, never a silent fallback. | — |
| 10 | `rewrite` | The loop, depending on interfaces (`Scorer`, `Selector`, `Gate`, `Provider`, `Store`) rather than concrete components. Monotonic acceptance rule below. Retains before/after artifacts and rejection reasons. | 6, 7, 8, 9 |
| 11 | `cli` | Built **early** as a thin shell against stub interfaces, so artifact formats and exit codes become real contracts rather than afterthoughts. | interfaces only |

### Evaluation protocol

Calibration is a release gate, not a report. Ablation of the feature set is explicitly
**post-v1**; the following is the v1 minimum.

- **Split unit is the whole source document,** held out *before* profiling. Splitting at
  paragraph level would leak: paragraphs from one document share topic, register and
  occasion, which inflates every metric.
- **Distractors are register-matched** writing by other authors, supplied by the user with
  **`--distractors <dir>`, which is required for calibration**. Comparing against mismatched
  registers measures genre, not authorship — which is why v1 ships no distractor set at all
  rather than a mismatched one. Issue #2 surveyed the openly licensed field and nothing
  cleared: the sources with clean licences are institutional, edited, or single-register, and
  a figure calibrated against them would measure era or house style. A user's own pile of
  other people's writing — saved articles, newsletters, received mail — is better
  register-matched than anything shippable, and never leaves their machine.
- **Two metrics, not one.** (i) *Discrimination:* pairwise ranking accuracy / AUC of
  held-out author segments against distractors. (ii) *Band calibration:* for each emitted
  band, the observed rate of author versus distractor segments landing in it, with
  confidence intervals. Discrimination alone cannot justify a band label.
- **Published minimums.** Minimum corpus size, minimum segment size per tier, and the
  confidence intervals ship in the README as measured numbers, not claims.
- **Two release gates, one per metric.**
  - *Discrimination floor:* a predeclared minimum AUC. Below it the profile is
    `uncalibrated` — `score` emits the raw distance and per-feature deltas but **no band**,
    and `rewrite` refuses to run against that profile.
  - *Band calibration floor:* each band must be backed by a minimum count of held-out
    segments, and its observed author-versus-distractor rate must fall inside its declared
    confidence interval. A band failing either test is **not emitted**; segments that would
    have landed in it report the adjacent wider band or `uncalibrated`. Individual bands can
    fail while discrimination passes, and the profile remains usable for the bands that hold.
- **Provenance.** Every eval result is stamped with corpus hash, profile version and
  feature-set version. A result that cannot name what produced it is not a result.

### Registers are explicit, never inferred

There is no register classifier in v1. `--profile <name>` is **required** whenever more
than one profile exists, on `score` and `rewrite` alike. Guessing which of the author's
voices a draft is reaching for is a whole research problem; asking is one flag.

### Acceptance is monotonic by construction

**This rule is authoritative; the diagram above is a sketch of it.**

The comparable quantity is **`d`, the calibrated profile distance** — a single continuous
scalar, lower meaning closer to the author. Bands are labels derived from `d` via
calibrated thresholds; `d` itself, not the band, is what the loop compares. Comparing
bands would make sub-threshold improvement invisible and let the loop stall.

`current` begins as the input text. A candidate — mechanical or LLM-produced — is accepted
if and only if, against `current`:

1. `d(candidate) ≤ d(current) − ε`, and
2. `preserve(current → candidate)` passes, and
3. `tells(candidate) ⊑ tells(current)`, a **severity-lexicographic vector**
   comparison over derived, verdict-eligible findings only — see ADR 0006.

Improvement is required on `d` alone. Conditions 2 and 3 are non-regression guards, not
improvement requirements: a rewrite that moves the prose toward the author while leaving
the tell vector unchanged is a good rewrite. Differences inside ε are rejections.

Condition 3 was a scalar count in an earlier version, which let one severe finding be
traded for several minor ones. ADR 0006 carries the corrected rule and its restrictions:
severity-lexicographic ordering, derived and verdict-eligible findings only, both sides
from the same rule-set digest and options with suppression off, and no comparison at all
when either report was truncated. **While every shipped rule is unvalidated this condition
is inert** — it blocks nothing, which is the honest state.

**Refusal.** If `d` is unavailable for either side — insufficient evidence at both tiers,
or an `uncalibrated` profile — no acceptance is possible. The segment is reported as
unscoreable and passed through untouched. Absence of a measurement is never treated as
an improvement.

The deterministic `tells` pre-pass is **not** exempt: it runs through the same gate, and a
rule whose edit fails `preserve` has that individual edit reverted rather than the whole
pass discarded. Because every accepted state is strictly better than the one before it on
`d`, and the input is the first state, the output is never worse than the input —
mechanically, not aspirationally.

### `--local-only` is a tested guarantee

Asserted by test, not by documentation: with `--local-only` (or `HAPAX_LOCAL_ONLY=1`) no
cloud provider is constructed, no credential is read from environment or config, no
outbound connection is attempted, and no telemetry is emitted. The test harness fails on
any attempted dial rather than trusting the code path.

### Commands

- `hapax init ~/writing/ --profile essays` — build corpus and profile; report contamination
- `hapax profile` — the profile, human-readable
- `hapax eval` — calibration report: how well the profile distinguishes the author
- `hapax score draft.md` — band, per-feature deltas, insufficient-evidence markers. **No LLM, no network**
- `hapax tells draft.md` — linter only
- `hapax rewrite draft.md` — the full gated loop

### Notes

`score`, `tells` and `eval` are useful with no LLM and no network at all, which gives the
project a far wider top-of-funnel than a rewriter alone. Components 0–8 are the entire
differentiator; 9–10 are commodity.

---

## Section 2 — Feature set, distance, and calibration

*Revised after review round 1. See `docs/REVIEW.md`.*

Unblocks issues #2–#5, each of which needs the register protocol, the band set, the
threshold artifacts, or the feature-versioning scheme defined here.

**What this section fixes and what it defers.** It defines *protocols and artifact shapes*.
It does not pre-specify numbers that only data can supply — every constant named here is
produced by running the derivation on real data and published with its uncertainty. The
distinction matters: a design that invents thresholds is the failure mode this project
exists to avoid.

### Nested splits, declared once

Three disjoint roles, split by **whole document** and never by segment:

| Split | Used for |
|---|---|
| **Train** | Feature selection, weights `w`, `λ`, `z_max`, all tuning |
| **Calibrate** | Threshold and band-boundary estimation |
| **Test** | Reported discrimination and band-rate figures |

Anything fitted on Calibrate or Test contaminates the numbers it produces. `z_max` in
particular must be fixed on Train, never chosen after seeing calibration results.

### Tier assignment is derived — and the derivation must not conflate three quantities

The earlier formulation ("sampling SD below *k* × between-author SD") confused three
different things: within-author variation across occasions, finite-segment measurement
error, and variation in author *means*. Only the first two are properties of the feature.
The third is a property of a **declared population** — which authors, which registers, what
document mix, what segment-length distribution — and it changes when that population
changes.

The derivation therefore specifies its population and separates the components by
resampling:

- **Reference population declared explicitly**: author set, register mix, document mix,
  segment-length distribution, and per-author weighting. Results are reported *relative to
  that population* and are not portable to another without re-derivation.
- **Held-out whole documents**; sampling *within* author for measurement noise, *across*
  authors for between-author signal.
- **Length in tokens**, with non-overlapping draws. Prose is autocorrelated: overlapping
  windows manufacture false precision.
- **Clustered bootstrap by document and author** for uncertainty on every estimate.
- **Minimum sample size** for feature *f* is the smallest *L* at which the bootstrap upper
  bound on measurement noise falls below the declared fraction of between-author signal —
  a bound, not a point estimate, so a fragile threshold cannot be picked out of noise.
- **Degenerate cases are outcomes, not errors**: where estimated between-author variance is
  ~0 or its interval is too wide, the feature is marked **not usable**, not assigned a
  minimum.

A formal hierarchical model would also serve; clustered resampling is chosen as the simpler
route to the same separation.

### The candidate feature set is a manifest, not prose

Section 1 names Tier A and Tier B candidates in a sentence. Prose cannot be
versioned, keyed or checked, and the rewrite of this section replaced the
provisional tier tables without putting a concrete set anywhere — a gap found
while implementing.

The candidate set is therefore recorded here explicitly. Every claimed tier is
**provisional** until the derivation above runs; nothing in this table asserts a
minimum sample size.

**Manifest version 1**, exposed by the implementation as `SetVersion`. Adding,
removing or redefining any entry below — including a change to the function-word or
clause-marker vocabulary — bumps it, and the two must never disagree.

**One exception, stated rather than assumed:** a version that has never been used to
produce a stored artifact is not yet meaningful, so changes made before its first use
amend it in place instead of bumping. Bumping a version no artifact was ever written
under would manufacture a version that never existed. The obligation begins at first
use, and from that point every change bumps.

**It is asserted provenance, not cache identity.** Cache identity is the content
hash defined under "Cache identity: artifacts, not version integers" below, which
covers the selected feature set, every transform and parameter, and the rest of the
scoring inputs. A version integer cannot serve as identity, for the reason given
there: someone can regenerate a golden file without bumping it. `SetVersion`'s job is narrower, and narrower than an earlier draft of this
paragraph claimed.

**It cannot force a bump.** Changing the computation and regenerating the golden
values, leaving both the package constant and its external pin at the same number,
passes every check. Nothing inside a repository can prevent that, for the same
reason the corpus digests in issue #1 are provenance rather than a security
boundary: whoever can change the behaviour can change everything that describes it.

What the pinned golden values do buy is narrower still: a change that alters an
output **for the golden vector** cannot be made silently, because the diff shows a
changed number rather than a changed line of arithmetic. A change affecting only
inputs outside the golden text leaves every pinned expectation intact and is not
caught — which is a further argument for the CI guard below rather than against
pinning. The hash decides reuse; the golden values make a behaviour
change visible; the integer is readable provenance travelling with stored artifacts.

A genuine mechanical guard is possible and is not built here: a CI check that fails
when a diff touches the feature computation without also touching `SetVersion`.
Recorded as the real enforcement mechanism, deferred to its own slice.

| Feature | Claimed tier | Status |
|---|---|---|
| word-length mean | A | implemented |
| word-length distribution | A | candidate; the mean is a summary of it, not a replacement |
| comma density | A | implemented |
| semicolon density | A | implemented |
| colon density | A | implemented |
| surface clause-marker rate | A | implemented |
| aggregate function-word rate | A | implemented, **unvalidated** — see below |
| sentence-length mean and variance | A | blocked on sentence segmentation |
| contraction rate | A | blocked on the contractible-opportunity denominator |
| function-word distribution | B | not implemented |
| lexical diversity | B | not implemented; see the versioned-contract note below |
| sentence-opener distribution | B | not implemented |

**The aggregate function-word rate is explicitly unvalidated.** Collapsing the
Tier B function-word *distribution* into a single ratio discards the per-word
identity signal that makes function words useful in authorship work, and may
measure content density instead. It is included as a candidate so the derivation
can rule on it, not because it has earned a place.

**Clause markers are a surface feature.** Several markers (`as`, `that`, `when`,
`which`) do not reliably signal a clause, so the feature is named for what it
counts — marker occurrences — rather than for clauses. Its vocabulary overlaps
the function-word list; that is not double counting, since they are separate
dimensions, but it is expected residual correlation and must be measured before
any weighting is fitted.

### Rates and densities are different quantities

A **rate** is a proportion of tokens and lies in [0,1]: the function-word rate
and the clause-marker rate are membership counts over lexical tokens.

A **density** is a count per lexical token and is unbounded above: `"word,,,"`
has a comma density of 3. Punctuation features are densities. Presenting them
"per 100 words" is display scaling and must not change the stored value.

Conflating the two invites a range check that is wrong for half the feature set.

### Undefined values are marked, not encoded as numbers

A rate over zero lexical tokens is undefined. It is carried as an explicit
definedness flag beside the value, never as a sentinel number and never as NaN:
`encoding/json` refuses NaN, NaN compares unequal to itself, and hashing needs a
canonical bit pattern — all three matter because these values are persisted and
keyed per Section 2's cache identity rules.

### Feature transforms: equal |z| is not equal evidence

Standardizing a bounded, zero-heavy, right-skewed rate by mean and SD produces a number
that is computable and not comparable to the same number from a symmetric feature. Two
corrections:

**1. Scale by expected variance at the actual segment length, not the profile SD.** A
40-token segment carries far more measurement variance than the profile estimate does.
Using the profile's σ alone understates short-segment noise and manufactures confident
deviations — the paragraph-Delta error recurring at the normalization step. The denominator
combines profile variance with the length-dependent sampling variance of the feature at the
observed length.

**2. Transform before comparing.** Count and rate features get an explicit count model or a
variance-stabilizing transform. The general mechanism, applied to every feature, is an
**empirical-CDF (rank) transform against the author's held-out distribution**, which makes
features comparable by construction rather than by assumption and is robust to skew and
outliers.

**The two corrections compose, in that order.** An earlier draft named both and left their
composition unstated, which is not a detail: the orderings are different estimators. The
deviation is the length-aware standardization of correction 1, and the empirical-CDF
transform of correction 2 is then applied *to that quantity*, not to the raw feature value.

Ranking raw values would drop correction 1 entirely — segment length would never enter, and
a short segment could reach an extreme percentile on sampling noise alone, which is the
paragraph-Delta error correction 1 exists to prevent. Standardizing without ranking would
reinstate the problem this section is named for, since equal |z| is not equal evidence
across a bounded membership rate and a symmetric mean.

**The reference distribution is built on Calibrate and reported figures come from Test.**
Section 2 assigns thresholds to Calibrate and reported figures to Test, so ranking a Test
segment against a Calibrate-derived reference keeps the reported number honest. Train is
excluded because the profile was fitted on it and ranks against it would be optimistic. The
cost is accepted and stated: each split carries half the data it otherwise would, and an
empirical CDF over a small Calibrate set is coarse — thirty segments give percentiles in
steps of a thirtieth, and the minimum reference size is therefore a published figure like
every other minimum.

**Each feature declares its sampling-variance family**, because correction 1's denominator
is not one formula. The manifest distinguishes a bounded membership **rate**, whose sampling
variance at *n* lexical tokens is the binomial *p*(1−*p*)/*n*; an unbounded per-token
**density**, whose count model gives λ/*n*; and a **mean**, which needs the within-segment
variance of the quantity being averaged and therefore requires `features` to expose it. The
family is part of the feature manifest and therefore part of the profile's cache identity:
changing a feature's sampling model changes every deviation computed from it.

### The distance `d`

`d` is a weighted robust mean of transformed deviations — Manhattan in transformed space,
the same form as Burrows' Delta, generalized beyond function words.

**Winsorization, named as such.** Deviations are winsorized at `z_max`. This is a
robust-loss choice, not a principle: it stops one broken feature dominating, and it
deliberately discards strong *not you* evidence. `z_max` is fixed on Train and shipped with
a sensitivity analysis over its value.

**Weights are learned, not asserted.** `w` and `λ` are fitted on Train against a declared
objective — author-versus-distractor separation — with regularization and constraints
stated, and a defined rule for missing features. Uniform, expert, inverse-redundancy and
learned weights are materially different models; the choice is recorded, not implied.

**Tier-A-only scores get their own calibration.** `d_A` alone is not drawn from the same
distribution as the blended `d`, so it cannot share thresholds. It carries a separate
threshold artifact, and a segment scored Tier-A-only is reported as such.

Availability rules, matching ADR 0006:

- Tier B unavailable ⇒ `d_A` with its own thresholds, flagged
- Neither tier meets its minimum ⇒ **insufficient evidence**, no `d`, no band, and
  `rewrite` passes the segment through untouched

### Feature redundancy: a declared procedure, not a gesture

Mahalanobis is rejected because a personal corpus is unlikely to support a well-conditioned
covariance estimate at 150+ dimensions, and an ill-conditioned one is worse than ignoring
correlation.

"Drop correlated features" is not a replacement for it, and this section does not claim
otherwise. What is required instead:

- A **predeclared, Train-only** redundancy procedure with its correlation measure,
  threshold, tie-breaking rule, and whether it runs per register or globally
- Evaluation **end-to-end on Test** — the selected feature set is judged by the score it
  produces, not by the pruning having occurred
- An explicit acknowledgment that pairwise pruning does not address nonlinear dependence,
  multivariate redundancy, or double-counting among surviving correlated features

Residual correlation is a **recorded limitation**, not a solved problem.

### Bands: two error targets, and a corrected crossing rule

The earlier definition used one α for two incompatible jobs and produced a contradiction.

The two error types have **asymmetric costs** and get **separate declared targets**:

- `p_author` — tolerated rate of the author's own held-out writing being called `not you`.
  Telling someone their own prose isn't theirs is the more damaging error.
- `p_distractor` — tolerated rate of another author's writing being called `in range`.

```
in range : d ≤ t_low        not you : d ≥ t_high        drifting : between

t_high = Q_author(1 − p_author)        author-distance quantile
t_low  = Q_distractor(p_distractor)    distractor-distance quantile
```

Each threshold is drawn from the distribution whose error it bounds. `not you` is the
upper region, so bounding the author's false-`not you` rate fixes `t_high` from the
**author** distances: `P(d_author ≥ t_high) = p_author`. `in range` is the lower region, so
bounding the distractor's false-`in range` rate fixes `t_low` from the **distractor**
distances: `P(d_distractor ≤ t_low) = p_distractor`. Taking each threshold from the other
distribution controls neither declared rate, even though the bands still fail to overlap —
a failure that would be invisible in testing.

**Ties and discreteness.** With finite samples and a score that can tie, the equalities above
do not hold exactly. Each threshold is therefore chosen as the **tightest one that still
respects its target** — respecting the direction in which each error moves:

- `not you` is `d ≥ t_high`, so author error *decreases* as `t_high` rises. The choice is
  the **smallest** `t_high` whose achieved author error is ≤ `p_author`.
- `in range` is `d ≤ t_low`, so distractor error *increases* as `t_low` rises. The choice is
  the **largest** `t_low` whose achieved distractor error is ≤ `p_distractor`.

Taking the opposite extreme in either case drives that error toward zero, collapsing the
band — and, worse, guarantees the crossing check below never fires, hiding a genuinely
incompatible pair.
Achieved rates are reported next to their targets rather than assumed equal to them. No
boundary randomization — a score that changes on re-run is not acceptable here.

**When `t_low ≥ t_high` the two targets are jointly unsatisfiable with a non-empty middle
band.** Note precisely what this does and does not mean. Each threshold may still meet its
own declared rate — the pair simply cannot also leave a `drifting` region between them. It
is **pair incompatibility, not a failed target**, and it is *not* proof of
non-discrimination: distributions can cross under stringent tail targets while retaining
good AUC. Discrimination is judged by the AUC gate in ADR 0005 and nowhere else.

The crossed case has a deterministic outcome rather than a contradictory one: **no bands are
emitted, `d` and feature deltas still are, and the report states that the two targets are
jointly unsatisfiable at the current separation, giving both achieved rates.** Overlapping
classification is thereby impossible.

Both thresholds carry **clustered bootstrap confidence intervals** by document and author.
A threshold whose interval is too wide to be actionable is not shipped.

### Registers: user-named, distractor pools declared

No fixed taxonomy and no classifier, per Section 1. Profiles are user-named
(`--profile essays`). What Section 2 adds is the operational half issue #2 needs:

**Register matching is by declaration, not inference.** A distractor pool is bound to a
profile explicitly by the user or by the bundled manifest. Calibration figures are reported
per `(profile, distractor pool)` pair, since a discrimination number computed against a
mismatched pool measures genre and is meaningless.

### Lexical diversity is a versioned contract

Raw TTR falls monotonically with length; comparing it across unequal segments measures
length. MTLD or vocd-D replace it — but neither is magic: MTLD destabilizes when the text is
too short to contain enough factors, vocd-D's fitted curve is unstable on short samples and
depends on its sampling protocol, and both remain topic-sensitive.

Therefore: the measure, its parameters and its sampling protocol are a **versioned
contract**; its minimum valid length is derived by the same procedure as every other
feature; and fixed-window variants use **exactly matched window lengths in profile and
scoring**. The hapax ratio that names this project has the same length dependence and takes
the same treatment.

### Cache identity: artifacts, not version integers

Issue #3 needs cache keys that make stale reuse impossible. Contract version integers alone
do not, and a golden-vector test does not force a bump — someone can regenerate the golden
file. Two requirements:

**The version integer is asserted alongside the golden vector.** The checked-in golden
values record the feature-set version next to the expected numbers.

This does **not** force a bump, and an earlier draft of this bullet claimed it did while the
sentence above it said the opposite — a contradiction inside one paragraph. Regenerating the
values and leaving the version alone passes. What the pairing buys is that a change altering
an output for the golden vector cannot be made silently: the diff shows a changed number
rather than a changed line of arithmetic. A change touching only inputs outside the golden
text is not caught at all.

Forcing the bump needs a check outside the values themselves — a CI rule failing when a diff
touches the feature computation without touching the version. That is the real enforcement
and it is not yet built.

**Scoring artifacts are keyed by content, not by name.** The cache identity includes the
hash of: the selected feature set, every transform and its parameters, the derived minimum
sample sizes, `z_max`, `w`, `λ`, missingness rules, seeds, split identity, the threshold
artifact, and the distractor-pool identity. Anything that can change a score is in the key.

### Numerical targets are outputs, not inputs

`p_author`, `p_distractor`, the AUC floor, the noise/signal fraction, `k`, `z_max` and every
minimum sample size are **declared before measurement and published with their measured
outcomes and intervals**. Where a target cannot be met, the honest result is a refusal to
emit that band — never a quietly relaxed target.

---

## Section 3 — The `text` contract and the artifact store

*Revised after review round 1. See `docs/REVIEW.md`.*

Component 0 and the persistence layer. Everything measured in Section 2 is defined by the
choices here, which is why the contract precedes the features that depend on it.

### A structural tree, not a flat class list

An earlier draft classified each segment into one flat class and asserted that list items
are not sentences. That is false often enough to be harmful: authored prose appears inside
list items, footnotes, captions and definition descriptions routinely, and Markdown
structures nest — inline code lives *inside* prose, quotes contain lists, lists contain
quotes.

Parsing produces a **tree**, and the model distinguishes two things:

- **Containers** — list, table, footnote section, block quote, definition list. Containers
  are structure, never a feature-bearing unit themselves.
- **Text runs** — the leaves. Each carries a role and its own admission verdict.

| Leaf role | In feature population | Note |
|---|---|---|
| Paragraph prose | **Yes** | The core case |
| Prose inside a list item | **Yes** | A list item containing sentences is prose in a container |
| Footnote, caption, definition description | **Yes** | Authored prose; container differs, writing does not |
| Heading, definition term | No | Fragmentary and verbless; would corrupt sentence-length features |
| Bare list item (non-sentential) | No | A label or fragment, not prose — decided per item, not per container |
| Inline code span | No | Excised from its containing run; surrounding prose is retained |
| Block quote content | **Configurable, default no** | Usually another's words; some authors write original blockquotes |
| Code block, table cell, front matter | No | Not prose |

Whether a list item is prose is decided **per item** by sentential structure, not by its
container. Non-included leaves are recorded rather than discarded — needed for spans, for
rehydration, and so a policy change can be applied without reparsing.

**A run with no words left after excision is outside the population, wherever it sits.** A
paragraph that is nothing but a code span or an image has no authored prose in it, and
admitting it would add a paragraph observation carrying no measurement — diluting every
per-paragraph statistic with an empty row. Only a role exclusion outranks this; the
block-quote policy does not, since a policy about *whose* words they are cannot make an
empty run measurable. Added in slice 2d.

### Spans, normalization, and boundaries that may not exist

Issue #3 decided the exemplar cache stores spans rather than sentences, so no second copy of
private prose exists. That collides with normalization, and not every desired boundary is
representable.

**Spans are `(byte offset, byte length)` into the raw file bytes.** Normalization form is
**NFC**, applied after span capture, and only raw offsets persist.

An earlier draft required parsing to maintain an offset map from normalized positions to
raw ones. That map is only necessary if the parser consumes the normalized form. It does
not: **structural parsing runs over the raw admitted bytes**, so every offset the parser
reports is already a raw offset and the map has nothing to translate. NFC is applied when a
span is resolved to text, never before. Revised during slice 2d — see `docs/REVIEW.md`.

**Not every normalized boundary has a raw counterpart.** `e` + combining acute normalizes to
a single `é`: the boundary *between* those two raw code points has no position in the
normalized text. The rule is therefore explicit rather than assumed:

- Boundaries are constrained to **grapheme-cluster boundaries** — stricter than UTF-8
  code-point boundaries. This condition already makes them NFC-stable: canonical
  decomposition, canonical reordering, and canonical composition operate within a
  grapheme cluster and cannot cross a UAX #29 grapheme boundary.
- A desired boundary with no valid representation **snaps outward**, never inward: a span's
  **start** boundary snaps *backward* and its **end** boundary snaps *forward*, each to the
  nearest NFC-stable grapheme-cluster boundary. A span therefore only ever grows to the
  nearest representable edge, so it can never silently drop authored content

Byte-level admission rules, all explicit because offsets are byte offsets:

- **Invalid UTF-8** ⇒ the document is rejected at admission with a clear error. Not
  repaired, since repair shifts every offset in the file
- **BOM** is stripped at admission and its presence recorded; offsets are relative to the
  stripped content
- **Line endings are preserved exactly.** CRLF is not normalized to LF — doing so would
  shift every subsequent offset while leaving the file on disk unchanged

### Tokenization

Every decision here changes every feature. Word boundaries follow **UAX #29**, with a stated
policy for each ambiguity rather than an inherited default:

**Apostrophes carry three different jobs and must be distinguished:**

- **Contraction** (`don't`, `we'll`) — one token, contraction flag set
- **Possessive** (`John's`, `authors'`) — one token, possessive flag set, *not* counted as a
  contraction. Conflating the two makes contraction rate track how often an author writes
  about people's things
- **Quotation mark** (typographic `'…'`, or ASCII `'` used as a quote) — punctuation, not a
  word character

Typographic (U+2019) and ASCII (U+0027) apostrophes are equivalent for classification.

**Hyphens and dashes are distinct characters with distinct roles**, classified by codepoint
rather than by appearance: ASCII hyphen-minus (U+002D) and non-breaking hyphen (U+2011) join
compounds into **one token** (`well-known`); en dash (U+2013), em dash (U+2014) and minus
sign (U+2212) are separators.

**Recognition precedence**, highest first, so that overlapping patterns resolve
deterministically: URL → email → file path → number → word. Each of URL, email and path is
one token, classed **non-lexical** and excluded from word-length and lexical-diversity
features. Numbers are one token, classed separately; decimal points are not sentence
boundaries.

**Terminal-punctuation peeling** resolves greedy matches deterministically. After a URL,
email, path or number is matched, characters are removed one at a time from the right while
**both** conditions hold:

1. the final character is in the terminal set — `.` `,` `;` `:` `!` `?` `"` `'` `)` `]` `}` — and
2. removing it leaves a string still valid as that token class

with the additional rule that a closing bracket or quote is peeled **only if unbalanced**
within the token, so a URL containing balanced parentheses keeps them. Peeling stops at the
first character failing either condition; peeled characters become punctuation tokens in
order.

### Contraction rate needs a denominator

A raw contraction count measures verbosity, not preference. The rate is *contractions per
contractible opportunity*, requiring detection of realized contractions (`don't`) and
unrealized ones (`do not`) alike. That means a bidirectional lexicon of expandable pairs,
versioned as part of the contract — and it is why possessives must be excluded, since they
create no contractible opportunity.

### Sentence segmentation: a measured approximation, tested properly

Sentence boundaries are the hardest part of the contract and every sentence-length feature
rests on them. Abbreviations, initials, decimals, ellipses, quoted sentences and list
punctuation all defeat naive splitting.

v1 uses rule-based segmentation with an abbreviation lexicon — no ML dependency, per
ADR 0001. The error rate is measured against a hand-annotated fixture and published.

**Segmentation errors are systematic rather than random**: they correlate with an author's
use of abbreviations, initials, decimals and quotation. A feature could therefore appear
discriminative because the segmenter fails *differently* on different authors.

An earlier draft proposed testing this by removing sentence-derived features and checking
whether discrimination collapses. That does not establish the claim in either direction — a
collapse may simply mean genuine sentence-level style matters, and no collapse does not show
the errors are harmless. The actual test is a **segmentation-robustness evaluation**:

- Score against **adjudicated boundaries** on the annotated fixture, and compare with scores
  from rule-based segmentation on the same text
- Apply **controlled boundary perturbations** and measure how discrimination responds
- **Stratify results by author and by error-prone construction**, since the concern is
  precisely that error rates differ across authors

### Language gate operates per leaf

Per document is too coarse: a French quotation inside an English essay should not disqualify
the essay, nor enter the features. Detection is per text run. Non-English runs are recorded,
excluded from the feature population, and reported. A document whose prose is predominantly
non-English is rejected at admission — v1 is English only.

### The artifact store

SQLite via a pure-Go driver, so the single static binary of ADR 0001 survives. The corpus
stays as files the user owns — **hapax is never the system of record for anyone's writing.**

| Artifact | Identity | Holds |
|---|---|---|
| `snapshot` | Content hash of membership plus every admission policy | The set a profile is relative to |
| `document` | Path plus content hash within a snapshot | Register, split role, admission status, language verdict |
| `node` | Document plus tree position | Container or leaf, role, **raw byte span**, admission verdict |
| `feature_vector` | Leaf plus feature-contract identity | The vector; never the text |
| `profile` | Content hash over snapshot, register, policies and contract versions | Per-feature distribution statistics |
| `exemplar` | Profile plus leaf reference | **A span reference only** |
| `threshold` | Profile plus distractor-pool plus calibration-protocol identity | `t_low`, `t_high`, achieved rates, intervals, or the pair-incompatible verdict |
| `eval_result` | All of the above, hashed | Discrimination and band figures with provenance |

### The privacy invariant, stated as a prohibition

"No prose text is written to the store" is the intent, but scanning the database for strings
matching corpus text **cannot prove it** — that check misses normalized, fragmented,
encoded, compressed and indexed copies, and says nothing about material outside the main
database file.

The invariant is therefore a prohibition on **any reversible prose representation or textual
derivative**, which explicitly includes token sequences, snippets, cached parse text,
full-text-search content, and any encoding or compression of the same. Feature vectors and
span references are the permitted derived forms.

Its **scope covers everything the store owns**: the database file, WAL and journal sidecars,
any backup or export the tool writes, and all log and diagnostic output.

Corpus-text scanning is retained as one **regression control**, not as proof. The primary
controls are the prohibition itself, an explicit allowlist of what may be persisted, and
review of anything added to it.

### Dangling spans, and exactly how many exemplars is enough

A span references a file the user may edit, move or delete, so rehydration failure is an
ordinary state rather than an error condition.

The required exemplar set is **fixed by the profile and invocation contract**: which
exemplars, and how many, are determined before rehydration is attempted, and both are part
of the identity of the result.

- **No automatic substitution and no silent reduction.** If a selected exemplar cannot be
  rehydrated, another is not quietly swapped in and the set is not quietly shrunk
- A `rewrite` that cannot rehydrate its required set **refuses**, naming what is stale
- Reindexing produces a **new** profile identity and may yield a different result — which is
  legitimate, but it must never be presented as the same result under the old identity
- Reindexing evicts artifacts whose documents are gone, per issue #3's deletion-phantom rule

### Schema migration

The store carries a schema version, migrated forward only. A migration that changes the
*meaning* of a stored artifact must also bump the relevant semantic contract version from
Section 2 — otherwise migrated artifacts are silently reinterpreted under new rules, which is
the stale-reuse failure the cache identity exists to prevent.
