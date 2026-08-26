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
most function-word counts are 0–2, and z-normalisation amplifies sampling noise into a
confident-looking number that means nothing.

Features are therefore **tiered by the sample size they require**, and every feature
declares its own minimum:

- **Tier A — dense, valid at paragraph scale.** Sentence-length mean and variance,
  punctuation rates, contraction rate, word-length distribution, clause-marker density.
  These have many observations per paragraph.
- **Tier B — sparse, requires a rolling window of several hundred tokens.** Function-word
  distribution, hapax ratio, sentence-opener distribution. Delta operates here and
  nowhere else.

A paragraph is scored on Tier A directly and on Tier B via a rolling window centred on
it. When neither has enough sample, `score` returns **insufficient evidence** — never a
number. No score is emitted with more precision than the sample supports.

### Scoring and selection are different objectives

Second correction: the original design reused one nearest-neighbour search for both
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
| 0 | `text` | Unicode normalisation, tokenisation, sentence-boundary detection, contraction handling, Markdown extraction with a configurable retention policy, English-only language gate. **Every feature is determined by these choices**, so the contract comes first. | — |
| 1 | `tells` | Deterministic AI-tell linter. Rule *schema* before rules: id, pattern, severity, source span, suppression syntax, register scope. Serves both draft linting and corpus contamination screening. | 0 |
| 2 | `corpus` | Walk, parse, dedupe, per-source provenance (path, mtime, git date), minimum-length gates, contamination screening, register tagging, held-out split reserved for `eval`. | 0, 1 |
| 3 | `features` | Tiered extractor. Each feature declares its minimum sample size and tier. | 0 |
| 4 | `profile` | Versioned, provenance-carrying, **named per register** (`essays`, `email`). Per-feature distribution statistics only — means and variances, used to normalise feature deviations so a writer whose sentence length naturally varies is not penalised for varying. Outlier handling. **Refuses to emit** below a minimum corpus size. Declares **no** band boundaries: every emitted band, and all fallback and collapse behaviour, belongs to `eval`. | 2, 3 |
| 5 | `eval` | Held-out calibration harness. Consumes the held-out source-document split and register-matched distractor segments from `corpus`, and the distribution statistics from `profile`. Produces both the discrimination metric and the band boundaries `score` uses. Score has no meaning without it, so it is not optional. Protocol below. | 2, 3, 4 |
| 6 | `score` | Tier A at paragraph scale, Tier B over rolling windows. Emits a calibrated band plus per-feature deltas plus direction, or *insufficient evidence*. Requires an explicit `--profile`. | 3, 4, 5 |
| 7 | `select` | Author-representative exemplars: medoids and high-density segments of the **named** profile, diversified by structure. Never nearest-neighbour to the draft. | 2, 3, 4 |
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
- **Distractors are register-matched** writing by other authors. v1 bundles a small
  permissively-licensed distractor set; `--distractors <dir>` overrides it. Comparing
  against mismatched registers measures genre, not authorship.
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
3. `tells(candidate) ≤ tells(current)`.

Improvement is required on `d` alone. Conditions 2 and 3 are non-regression guards, not
improvement requirements: a rewrite that moves the prose toward the author while leaving
the tell count unchanged is a good rewrite. Differences inside ε are rejections.

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
