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
| 11 | `assemble` | Splices accepted replacements back into the original bytes. Ordered, non-overlapping raw spans; every untouched byte and excision preserved exactly; all-or-nothing output. | 3, 10 |
| 12 | `store` | SQLite artifact persistence: the declared artifact kinds and their identities, forward migration, the persistence allowlist enforcing the privacy invariant, and span rehydration with no substitution and no silent reduction. Typed per-artifact operations, never a generic put. | 0, 2, 3, 4 |
| 13 | `cli` | Composition root. Resolves the mode once, constructs the credential factory only on the cloud path, wires every component, and owns the command surface, output schema and exit codes. | all |

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
  - *Band calibration floor:* a claiming band is emitted only when the **upper confidence
    bound on its own error rate, measured on Test, is at or below its declared target**, and
    the class whose error it bounds carries enough held-out segments for that bound to exist.
    A band failing either test is **not emitted** and its segments report `drifting`; if
    neither claiming band is emitted the profile is `uncalibrated`. Individual bands can fail
    while discrimination passes, and the profile remains usable for the bands that hold. The
    rule is set out in full in Section 2 under "The band calibration floor".
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
cloud provider is constructed, no credential is read from environment or config, **no
connection to any non-loopback address is attempted**, and no telemetry is emitted. The
test harness fails on any dial outside loopback rather than trusting the code path.

The earlier wording said "no outbound connection", which contradicted itself: the default
provider is Ollama over HTTP to localhost, so local mode makes connections by design. The
guarantee is about *destination*, not about silence.

### `llm`, and how the guarantee is made mechanical

**`llm` owns the only way out, and a test says so about the source itself.** A runtime
harness cannot see a path it never exercises, so a test parses the package's own non-test
files and enforces the shape structurally: an import allowlist, so nothing can dial on the
package's behalf; a selector allowlist admitting only the non-dialling names from `net` and
`crypto/tls`, which is why `tls.Dial` needs no enumerating; exactly one `http.Client` and one
`http.Transport` literal, each with its policy fields checked by *value* rather than presence;
no assignment to a policy field after construction; no type derived from either; and no
`os.Getenv`, since the mode is resolved at the composition root and no library package reads
the environment.

It is a structural backstop and not a proof — it cannot see through arbitrary indirection,
and the enforcing dialer remains the primary assertion.

It is handed a dial function and builds its own
`http.Client` from it, with redirects refused and proxy discovery off. A caller cannot pass
a pre-built client whose redirect or proxy policy would route elsewhere, and there is no
path to a socket the test did not supply. `New` refuses a nil dialer rather than falling
back to `http.DefaultTransport`.

**The local endpoint must be a literal loopback address**, `127.0.0.0/8` or `[::1]`, with no
userinfo and no query. Hostnames are rejected, `localhost` included — resolving a name before
checking it leaves a rebinding gap, and requiring a literal removes DNS from the local path
entirely. This rule is scoped to the *local* provider: the cloud provider necessarily speaks
to a remote host, and its own endpoint rule is the opposite one — `https` only, a pinned host,
and never reachable at all in local mode, which is enforced at construction rather than by
parsing.

**The mode is resolved once, at the composition root, and is immutable here.** Today that
root is `cli`, which reads `HAPAX_LOCAL_ONLY` and the flag, with `--local-only` winning and a
malformed environment value failing closed. Stated generally because `cli` will not stay the
only entry point: *every* composition root resolves the mode once and passes the decision
inward, and no library package reads the environment.
`rewrite.RewriteRequest` also carries `LocalOnly`, which is a per-call boolean and therefore
cannot be the security boundary — a provider refuses any request whose flag disagrees with
the mode it was built in, rather than honouring the field.

**The cloud credential source is a factory, not an instance.** Passing a constructed
credential source into local mode would already have read the environment before anyone
checked the mode. The local branch never calls the factory, and the test asserts zero calls.

**What is sent is a declared schema, not a filtered string.** "The body must not contain the
profile ID" is unfalsifiable, because an identifier can legitimately occur in the author's
prose. Instead each provider's request body has a fixed key set, and the test decodes the body
and asserts exactly that set — no more, no less, so "the provider's own framing" cannot be a
licence for arbitrary extra fields. Identity fields are absent by construction, not by search.

| | local (Ollama) | cloud (Anthropic) |
|---|---|---|
| method and URL | `POST {endpoint}/api/generate` | `POST https://api.anthropic.com/v1/messages` |
| headers | `content-type: application/json` | plus `x-api-key`, `anthropic-version: 2023-06-01` |
| body keys, exactly | `model`, `prompt`, `stream` (false) | `model`, `max_tokens`, `messages` |
| token budget | — | `MaxTokens`, default 4096, non-positive refused |
| where the prompt goes | `prompt` | `messages[0].content`, with `role: "user"` |
| reply read from | `response` | `content[0].text`, requiring `type: "text"` |

The cloud host is pinned to that literal — a different host is a construction error, not a
configuration option — so the test asserts a URL rather than a property.

**Telemetry is defined so it can be refused**: no request other than the single provider
call per `Rewrite`, no proxy, no redirect, and no *application-owned* background work. In
local mode there is no DNS either, which follows from requiring a literal loopback address
rather than being a separate promise — the cloud path necessarily resolves its pinned host,
so "no DNS" is a property of local mode and is scoped to it. Not "no goroutines" — `http.Transport` runs its own while servicing a request, so that
would be false as stated. One HTTP exchange per `Provider.Rewrite`, not per loop attempt,
since the loop deliberately makes several.

**A cloud provider carries no local endpoint.** Configuring both is a construction error,
which makes a silent downgrade unconstructible rather than merely unused — a stronger
statement than spying on a factory, and one a test can make without a spy at all.

**The outbound header set is declared too**, not just the body: `content-type` and a fixed
`user-agent` of `hapax` for both, plus `x-api-key` and `anthropic-version` for the cloud. The
user agent is fixed because Go's default announces its version, and a declared set is what
stops an identity-bearing header being added under a name no value-scanning test would think
to look at.

**No retries in v1, and made mechanical rather than promised.** `http.Transport` will
transparently retry a request whose *reused* connection fails, so "we do not retry" is not a
property of our code alone. Keep-alives are disabled, which removes connection reuse and with
it that path — and gives a second useful property: one dial per request, so the exchange count
is observable at the dial seam rather than only at the client. A cloud failure is returned as
it is, with no downgrade to the local provider — which needs no spy, because a cloud provider
carrying a local endpoint is a construction error and the fallback is therefore
unconstructible.

**One more injected seam, declared rather than smuggled**: the trust roots, as an
`*x509.CertPool` and not a whole `*tls.Config`. Production leaves it nil and gets the system
roots; a test supplies a pool holding a certificate minted for the pinned host, so the cloud
path is exercised end to end with real verification. A `*tls.Config` would have been too much
authority to hand over — it can also disable verification, override the server name, or add
client certificates — so only the roots are injectable, and the pinned name is still verified.

**Construction does no I/O.** The credential factory and the provider factories are
side-effect-free until called; anything that must read a file or a socket takes the same
injected seam and is tested through it. Otherwise a dependency could read the environment
before anyone checked the mode, which is the hole the factory shape exists to close.

**Endpoint parsing is closed, not merely checked.** For the local provider: scheme `http` or
`https`, host lexically `127.0.0.0/8` or exactly `::1`, an explicit port, and userinfo, query
and fragment all rejected. Hostile spellings are part of the contract, not an afterthought.

**The limits are configuration, with declared defaults.** `MaxRequestBytes` defaults to
256 KiB and `MaxResponseBytes` to 1 MiB, both immutable once the provider is constructed, and
a non-positive value is refused rather than treated as unlimited — the failure mode of a zero
meaning "no limit" is exactly the one worth designing out. The defaults are generous against a
real request, which is one paragraph and three exemplars.

**Both request and response are size-bounded.** The request limit is on the final serialized
body in bytes, checked before the credential lookup and before the dial, so an oversized
prompt costs nothing. The response is bounded too — an oversized `Content-Length` is refused
outright, and a chunked body is read to at most `limit + 1` bytes and then closed, so the
bound holds without a declared length. The limit counts **wire bytes**, and compression is
disabled on the transport so that wire and decoded bytes are the same number: otherwise a
`Content-Length` under the limit could still decompress past it, which is a bound in name
only. ADR 0007 asks only for the request limit; an unbounded
read from a remote host is the same problem pointed the other way.

**The claim is "no identity fields", not "no identifier values".** A fixed outbound key set
proves the former. It cannot prove the latter, because the author's own prose may contain a
string equal to a profile ID, and no filter should be pretending otherwise.

### Commands

- `hapax index ~/writing/ --profile essays` — build corpus and profile; report contamination
- `hapax profile` — the profile, human-readable
- `hapax eval` — calibration report: how well the profile distinguishes the author
- `hapax score draft.md` — band, per-feature deltas, insufficient-evidence markers. **No LLM, no network**
- `hapax tells draft.md` — linter only
- `hapax rewrite draft.md` — the full gated loop

### Exit codes, and the split that produced them

Row 12 originally said `cli` should be built **early**, against stub interfaces, so that
artifact formats and exit codes became real contracts rather than afterthoughts. It was built
last instead. The intent was right and the ordering was wrong in a way worth recording: exit
codes appeared exactly once in this document — in that table row — and were never specified,
which is precisely the afterthought the row warned about.

Building `cli` against an unbuilt store would repeat the same mistake, so **`store` is its own
component and is built first**. It owns durable artifacts and privacy-sensitive lifecycle
rules; `cli` is then a thin adapter over command services and result classifications.

The codes partition on one question — *did the tool produce a verdict?*

| | meaning |
|---|---|
| 0 | completed, nothing adverse |
| 1 | completed, adverse finding |
| 2 | invalid invocation: unknown command, bad flag, malformed `HAPAX_LOCAL_ONLY` |
| 3 | operational failure: IO, store, provider |
| 4 | refusal, with a machine-readable reason |

0 and 1 mean the tool worked. 2, 3 and 4 mean it did not, and only 4 is a deliberate refusal
rather than a failure. A refusal carries a reason from a closed set — `uncalibrated`,
`insufficient-evidence`, `stale-exemplars`, `local-only-forbids-provider` — because a script
must not have to parse prose to tell them apart.

Two distinctions the codes deliberately do **not** carry, because the result document does:
`eval` reporting an uncalibrated profile is a *completed measurement*, so it exits 1 rather
than 4 — the measurement exists and is adverse. `score` on that same profile exits 4, because
no band can be issued at all. And `rewrite` exits 0 both when nothing needed changing and
when everything that did was improved; which of those happened is a named state in the output,
not an exit code, since a caller wanting the difference wants the detail with it.

A malformed `HAPAX_LOCAL_ONLY` is code 2 — an invalid invocation, not a refusal. *Failing
closed* means it can never select cloud mode or construct a credential factory; it does not
mean its classification becomes ambiguous.

**Mode resolution belongs to every composition root, not to `cli` specifically.** `cli` is
today's only one, but an MCP or skill wrapper would be another, and each must resolve one
immutable mode and pass it inward — otherwise the next wrapper bypasses the guarantee that
`llm`'s own tests cannot enforce from inside.

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
| **Train** | Feature selection and all tuning |
| **Calibrate** | Threshold and band-boundary estimation |
| **Test** | Reported discrimination and band-rate figures |

Anything fitted on Calibrate or Test contaminates the numbers it produces, and nothing
tunable may be chosen after seeing calibration results.

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

**The transformed deviation stays on a z scale.** An empirical-CDF rank is a percentile in
[0,1], and a percentile cannot be winsorized nor averaged into "Manhattan in
transformed space, the same form as Burrows' Delta" — Delta averages |z|. The rank is
therefore mapped back through the normal quantile function, which keeps the comparability
ranking bought while leaving the quantity on the scale the rest of Section 2 assumes.

**The plotting position is declared, because it is visible in the output.** An empirical CDF
over *n* reference values returns 0 for anything below all of them and 1 for anything above,
and the normal quantile is infinite at both. At thirty reference segments that is not an
edge case: it is one segment in thirty per tail. The segment is therefore ranked within the
reference **plus itself** — *m* = *n* + 1 values — at the (*i* − ½)/*m* position, with ties
taking their midrank. That is symmetric in both tails and never reaches 0 or 1.

The consequence is stated rather than left to be discovered from a surprising histogram: the
position bounds |deviation| at Φ⁻¹(1 − 1/2*m*), which is about **2.14 at thirty reference
segments, 2.58 at a hundred, and only 1.69 at ten**. The reference size caps deviation
magnitude on its own, and does so per feature rather than globally: a feature with a thin
reference is capped harder than one with a thick one, which is the correct ordering and the
one a single flat constant cannot express. This is published beside the minimum reference
size, and is a further reason that minimum is a real figure rather than a formality.

**Deviations are emitted in manifest order, and every manifest feature is
present.** This is a serialization contract, not a style preference: the reference
distribution and the deviation record are both hashed into the scoring cache identity, and
a set with no canonical order has no canonical hash. A feature that is unavailable appears
with its reason rather than being omitted, so a reader can tell "measured and typical" from
"not measured" without knowing the manifest by heart.

**Undefined causes have a declared precedence.** A deviation can fail to exist for several
reasons at once, and the reported one is the first reached checking the inputs in order:
the segment value, the segment sampling variance, the profile statistic, the profile
variance, then the combined variance. Left unstated, the reason a user is shown would depend
on the order an implementation happened to write its guards in — and the reasons imply
different remedies. Write more text, or fit a better profile, are not the same advice.

**The deviation keeps its sign.** `d` takes absolute values, so the sign does not survive
into the distance — but a segment below the author's usual comma density and one above it
are different facts, and `rewrite` needs the direction. Discarding it at the source is
irrecoverable; carrying it costs a float's sign bit.

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
**density**, modelled conditionally on the lexical-token count as exposure, giving λ/*n*; and
a **mean**, which needs the within-segment variance of the quantity being averaged and
therefore requires `features` to expose it. The mean uses the sample (*n*−1) variance,
matching the profile's convention, and is therefore undefined at *n* = 1.

The density model is a **working assumption, recorded as such rather than asserted**.
Punctuation is syntax-constrained, plausibly zero-inflated and overdispersed relative to
Poisson, and the numerator counts punctuation across all tokens while the denominator counts
lexical ones. It is modelled as Poisson with lexical exposure and a **declared dispersion of
φ = 1** — stated explicitly, because a quasi-Poisson variance is φλ/*n* and asserting λ/*n*
while calling the model quasi-Poisson would fix φ = 1 without saying so. φ awaits the same
calibration as every other declared minimum; until it is derived it is 1, and that is a
stand-in rather than a finding.

The family is part of the feature manifest and therefore part of the profile's cache
identity: changing a feature's sampling model changes every deviation computed from it, and
a cache must not serve one for the other. The manifest digest is computed by `features`,
which owns the manifest — an earlier arrangement had `profile` recompute it from the fields
it happened to know about, which cannot notice a field added elsewhere.

### The distance `d`

`d` is a weighted robust mean of transformed deviations — Manhattan in transformed space,
the same form as Burrows' Delta, generalized beyond function words.

**The robust loss is the rank transform, and `z_max` is struck.** Winsorization at a fixed
`z_max` was specified when deviations were unbounded standardized values, where one broken
feature really could dominate an average. After the rank transform it cannot: the plotting
position bounds every deviation at Φ⁻¹(1 − 1/2*m*), so under uniform weights over *k*
available features a single feature's largest possible share of `d` is its own cap over *k*.

The numbers show `z_max` has nothing left to do. A conventional `z_max` = 3 does not bind
until a feature carries 370 reference values, and this section's illustrative reference size
is thirty; set low enough to bind at realistic sizes — 2.0 binds from twenty-one — it
discards evidence the reference does support, and it discards it uniformly, ignoring that a
thinly-referenced feature is already capped lower. The existing bound scales with the
evidence behind each feature. A flat constant cannot, and would replace a better mechanism
with a worse one.

So `z_max` is struck rather than kept as an inert knob in the cache identity and the
reported record, with a sensitivity analysis owed over a value that changes nothing. If a
future scheme reintroduces an unbounded deviation, winsorization returns with it.

**The weighting is declared, not implied.** Uniform, expert, inverse-redundancy and
learned weights are materially different models, so the scheme is recorded and versioned
rather than left to be inferred from the code.

**v1 declares uniform weights** — `w = 1` for every feature available on a segment — and
defers fitting. Two reasons, both internal to this design. Fitting `w` against
author-versus-distractor separation needs a distractor pool with genuine author diversity,
and the corpus search closed without one; weights fitted on an engineering fixture would be
exactly the false calibration claim that decision was taken to avoid. And fitting 150+
weights on a personal corpus's Train split is the over-parameterization this section rejects
Mahalanobis for, under a different name — a badly estimated weight vector is worse than an
asserted flat one for the same reason an ill-conditioned covariance is worse than ignoring
correlation.

Uniform is a recorded stand-in, not a finding. It is named in the profile artifact and in
the reported record, and the scheme and its version are part of the scoring cache identity,
so a later fitted scheme cannot be served from a cache built under this one. A fitted scheme
remains the intended destination and arrives with its objective, regularization,
constraints, and missing-feature rule stated — the terms this paragraph previously asserted
without meeting.

**`λ` is struck.** It was named as Train-fitted in three places and defined in none. Neither
available reading survives the uniform choice: as a regularization strength it has no
fitting left to restrain, and as a Tier A/Tier B blend it duplicates machinery that already
exists, since `d` averages over whichever features a segment makes available and a
Tier-A-only score already carries its own threshold artifact. A future fitted-weights slice
reintroduces it with a definition, which is when it earns a place in the reported record.

**`d` is the mean of the absolute transformed deviations over the features a segment
actually makes available**, with uniform weights. A feature that is undefined on the
segment, or whose reference is too small, contributes nothing and is not counted in the
denominator — averaging it in as a zero would read as "exactly typical", which is the single
most misleading value available for something not measured.

**A tier's minimum is a majority of its manifest features.** Section 2 says neither tier
meeting its minimum gives insufficient evidence, and never said what the minimum was. It is
stated as a proportion rather than a count so it does not silently weaken as the manifest
grows: three of twenty available features would be 15% coverage under a fixed count of
three, and the same words would mean something quite different. Like every other minimum
here it is **declared, not derived**, and published with its measured consequences.

**`d` records the features that produced it.** ADR 0006's acceptance loop compares
`d(candidate) ≤ d(current) − ε`, and a rewrite can change which features are available — a
candidate long enough to define a feature the original was not, or short enough to lose one.
Two `d` values that are means over different feature sets are not on the same scale, and
comparing them would let the loop accept a rewrite that only moved the denominator. The
contributing set travels with the number so the comparison can be refused rather than
silently made.

**The tier set is derived from the manifest, not enumerated in the scoring code.** ADR 0003
assigns the function-word distribution, hapax ratio and sentence-opener distribution to Tier
B, and all three need a rolling window of several hundred tokens that is not built. The
manifest today declares six Tier A features and nothing else — `TierB` is not a declared
constant, because a tier with no features is a tier whose minimum can never be met, and
enumerating it would flag every v1 score as partial against something that does not exist.

Reading the tier set off the manifest makes the machinery general without inventing an empty
tier: today it resolves to one tier and there is nothing to blend, and the day a Tier B
feature is added it resolves to two, with the per-tier minimums and the partial-score rule
already in force. The manifest digest changes at that same moment, so no threshold artifact
built under the one-tier manifest can be served for the two-tier one.

**Any proper subset of the manifest's tiers carries its own threshold artifact.** Section 2
previously stated this only for the Tier-A-only case. Stated generally it covers Tier B
alone and any future tier without enumerating cases, and the reason is unchanged: a distance
over a subset of the tiers is not drawn from the same distribution as the blend, so it
cannot share thresholds. A score records the tiers that produced it, and is flagged when
they are not all of them.

Availability rules, matching ADR 0006:

- Some but not all tiers meet their minimum ⇒ a distance over those tiers, with its own
  thresholds, flagged as a partial score
- No tier meets its minimum ⇒ **insufficient evidence**, no `d`, no band, and `rewrite`
  passes the segment through untouched

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

**The two quantiles are ordered before use; there is no unsatisfiable case.** Write

```
A = Q_author(1 − p_author)          above this, author segments are rare
D = Q_distractor(p_distractor)      below this, distractor segments are rare

t_low = min(A, D)                   t_high = max(A, D)
```

An earlier draft assigned `t_low = D` and `t_high = A` unconditionally and declared the
pair jointly unsatisfiable when `t_low ≥ t_high`. That rule is wrong, and wrong in the
direction that matters: `D > A` is the **well-separated** case. When the author's distances
are small and the distractors' are large, the author's upper quantile sits *below* the
distractors' lower quantile, and the unconditional assignment produces regions that
overlap. Measured on synthetic populations at `p_author` = 0.05 and `p_distractor` = 0.10,
the refusal fired on clean separation and did not fire on heavy overlap — the profile that
discriminates best emitted no bands, and the one that barely discriminates emitted them.

Ordering the pair removes the case entirely, and **both declared targets still hold**, by
monotonicity of the CDF alone:

- distractor false-`in range` = `P(d_distractor ≤ min(A, D)) ≤ P(d_distractor ≤ D) = p_distractor`
- author false-`not you` = `P(d_author ≥ max(A, D)) ≤ P(d_author ≥ A) = p_author`

Where the distributions overlap, `A > D`, `min`/`max` reproduces the earlier assignment
exactly, and the achieved rates sit on their targets. Where they separate, `A < D`, the
thresholds take their values from the opposite distributions and both achieved rates fall
*below* target, with `drifting` spanning the gap in which neither population has mass. That
gap is the honest label for a segment unlike both: not a contradiction, an absence of
evidence.

This is not the unconditional swap Section 2 warns against two paragraphs above. Swapping
whenever `A < D` is safe precisely because the inequality that triggers it is the one that
makes each bound slack; swapping when `A > D` would violate both. The condition is the
proof.

**Band membership is tested in order, and the tie is broken away from the costlier error.**
`in range` first, then `not you`, then `drifting`. The order only matters when `A = D`, where
`t_low = t_high` and a distance sitting exactly on the boundary satisfies both `d ≤ t_low`
and `d ≥ t_high`. Testing `in range` first resolves that point to the label whose error is
the less damaging one, which is the same asymmetry that sets the two targets. Everywhere
else the three regions are disjoint and the order is immaterial.

**Declared targets, and the sample sizes they force.** `p_author` = 0.05 and
`p_distractor` = 0.10 for v1 — asymmetric because the errors are, `p_author` tighter because
telling someone their own prose is not theirs is the more damaging one. Both are declared
stand-ins awaiting measurement, like every other target here, and both are part of the
threshold artifact's identity.

The minimum sample sizes are **derived rather than declared**, which makes them the
exception in this section. Thresholds are chosen from the observed distances, so the
smallest achievable non-zero error rate on *n* observations is 1/*n*. A threshold meeting a
target of *p* therefore exists only when 1/*n* ≤ *p*: at least ⌈1/`p_author`⌉ author
distances and ⌈1/`p_distractor`⌉ distractor distances, which is 20 and 10 at the v1 targets.
Below either count no threshold respecting that target exists at all, and the honest
outcome is no bands rather than a boundary extrapolated past the data.

Thresholds are chosen from observed distances rather than by interpolation, which makes them
reproducible: **no boundary randomization, and no value the population never contained.**

**A threshold artifact is bound to everything it was calibrated against**: the profile, the
deviation reference, the feature manifest digest, the weighting scheme, the distance
algorithm, the tier subset, and the **declared distractor pool**. A distance scored over a
different tier subset, or ranked against a different reference, is not drawn from the
distribution these thresholds describe, and banding it against them would be the same error
as comparing two distances over different feature sets.

Two of those cannot be read off the distances and are therefore named at calibration: the
**distractor pool**, because this section already requires figures reported per
`(profile, distractor pool)` pair and a figure computed against a mismatched pool measures
genre; and the **calibration cohort**, because the Calibrate *split* is a role, not the
identity of the held-out documents filling it. Two cohorts can produce the same boundaries,
and an artifact that cannot tell them apart lets stale calibration evidence be reused under
a new corpus. Both are in the identity and neither is checked at banding, since both
describe how the boundaries were drawn rather than the segment being scored.

The declared targets and the observed populations are in the identity too, not merely the
boundaries they produced: distinct targets can select the same observed bounds, and so can
distinct populations.

**Both thresholds carry clustered bootstrap confidence intervals**, and a threshold whose
interval is too wide to be actionable is not shipped. The procedure is declared rather than
left to a library default, because every part of it moves the width of the interval it
produces.

**Clusters are resampled, not segments.** Paragraphs from one document share topic, register
and occasion, so resampling them independently manufactures precision the data does not
have — the same error as overlapping windows, one level up. Clusters are drawn with
replacement to the original cluster count, independently within each class, and every
segment in a drawn cluster comes with it.

**The cluster unit differs by class, and that is the point of clustering by both.** The
author's own distances all come from one author, so clustering them by author would collapse
the whole class into a single cluster and leave nothing to resample; they are clustered by
**document**, which is the within-author variation this side is supposed to measure. The
distractor distances are clustered by **author**, which is the between-author variation that
side is supposed to measure, with documents nested inside.

The resolution of issue #2 left no distractor corpus with per-author labels, so in practice
the distractor side usually falls back to the document. That is recorded rather than
silently skipped: an interval clustered by document alone **understates** uncertainty,
because it counts two documents by the same unlabelled author as independent evidence. The
artifact names its clustering unit and flags the weaker one.

**Declared parameters**, all stand-ins awaiting measurement and all part of the interval's
identity:

- **Confidence level 0.95**, two-sided, by the **percentile** method — the resample
  distribution's own 2.5th and 97.5th points. Bias-corrected variants need assumptions this
  design has not earned.
- **2000 resamples.** At a 0.95 level each tail endpoint is then the 50th order statistic of
  the resample distribution, which places it without claiming more resolution than the
  underlying data supports.
- **A fixed declared seed**, recorded in the artifact. Section 2 forbids a score that changes
  on re-run, and a bootstrap is the one place in this pipeline where randomness enters; an
  unrecorded seed would put it there. The declared value is `0x68617061785F7631`.
- **Percentile indices taken as order statistics, not interpolated.** On the *n* qualified
  resample values sorted ascending, with α = 1 − confidence, the endpoints are the values at
  ⌊α/2 × *n*⌋ and min(⌊(1 − α/2) × *n*⌋, *n* − 1). Every resample's threshold is a value some
  segment produced, so an endpoint taken this way is one too — the same refusal of a boundary
  the population never contained that governs the thresholds themselves.

**A resample that yields no qualifying threshold is an outcome, not an error**, matching the
treatment of degenerate cases above. Such resamples are counted and excluded from the
percentiles rather than aborting the estimate. But an interval assembled from a heavily
degenerate resample distribution is describing a different population than the one asked
about, so **at least 90% of resamples must qualify**; below that the interval is reported as
not usable and the threshold it belongs to is not shipped.

**Too wide to be actionable is a derived test, not another declared number.** ADR 0005 says
a threshold whose interval is too wide is not shipped, and never said how wide is too wide.
The geometry answers it without a stand-in: the interval on `t_low` and the interval on
`t_high` must not overlap. If they do, the data does not resolve the two boundaries from
each other, and `in range`, `drifting` and `not you` are not distinguishable regions — which
is exactly what "not actionable" means here, and it needs no number.

Usability and actionability are separate verdicts because they fail for different reasons and
have different remedies: a population that qualifies too few resamples needs more writing,
while one whose intervals overlap needs better separation. Both must hold before thresholds
ship, so the artifact also carries the conjunction, and a consumer cannot reach for one
verdict and miss the other.

**The interval is computed from the population that produced the thresholds**, verified by
requiring that population to reproduce the threshold artifact's identity. An interval drawn
from a different population is a statement about a boundary nobody calibrated.

**The sample size that admits a threshold is not the size that admits an interval**, and the
gap is large enough to publish rather than leave to be discovered. A threshold exists as soon
as one observation sits in the tail — that is what the ⌈1/*p*⌉ minimum above says. But a
resample that draws that one observation twice pushes the achieved rate over target and
qualifies nothing, so at exactly the threshold minimum most resamples fail. Measured: at
`p_author` = 0.05 with twenty author distances, **about 58% of resamples qualify even when
every distance is in its own document**, because the cluster count is not what is short —
the tail is. Sixty distances reach roughly 98%, and a hundred reach 100%.

No second minimum is declared for this, because the 90% qualification floor already
enforces it and does so against the population actually supplied rather than against a
number chosen in advance. The consequence is simply stated: **⌈1/*p*⌉ is the minimum to
compute a boundary, not the minimum to ship one.**

**The draw is specified, not delegated.** Reproducibility cannot rest on a standard-library
implementation detail — not on which source the runtime provides, and not on how a
convenience method happens to consume it. The resample indices are therefore a stated pure
function of the seed:

- Each class draws from its own **SplitMix64** stream, initialised to `seed + 1` for the
  author class and `seed + 2` for the distractor class, so one class's cluster count cannot
  shift the other's draws.
- Draws are consumed in order — for each resample, one draw per cluster — and the cluster
  index is the 64-bit output modulo the class's cluster count. The modulo bias at any
  realistic cluster count is below 2⁻⁵⁸ and is accepted rather than rejection-sampled, which
  would make the draw sequence depend on the cluster count.
- **Clusters are ordered lexicographically by label** before any index is taken. Without a
  declared order the index means nothing: first-seen ordering would make the interval depend
  on the order the caller happened to supply its segments in, which is not information about
  the author. Sorting is what makes the whole procedure a function of the population rather
  than of its presentation.

SplitMix64 is a published algorithm, so this is a specification a second implementation can
reproduce, which is the property that matters. It is not a claim about statistical quality
beyond what resampling needs.

### `preserve`: a deterministic gate that names what it cannot see

ADR 0006 requires that numbers, named entities, negations, URLs and quoted strings survive an
edit. Deterministic, with no model — which means every one of those five is a **surface
proxy** for the thing it stands for, and the useful part of this section is saying where each
proxy fails rather than implying it does not.

**Equality, not survival.** The rule is stated as "must survive", which is about loss. But a
rewrite that *invents* a number, a URL or a quotation fabricates a fact, and one that invents
a negation inverts a claim — failures at least as bad as losing one. So each class is compared
as a **multiset equality** between the current and candidate text, in both directions.

The cost is real and accepted rather than hidden: a meaning-preserving rephrasing that removes
a negation — "not unusual" becoming "common" — is rejected. That is the conservative
direction. A gate that permitted negation changes would permit a rewrite to invert what the
author said, and this tool edits people's own writing.

**Surface forms are compared, with no normalisation.** `5` and `five` are different, and so
are `1,000` and `1000`. Normalising would mean deciding what a numeral means — currencies,
ranges, percentages, ordinals — which is the semantic work this gate exists to avoid. A
rewrite that reformats a number is therefore rejected, and that is stated rather than
discovered later.

The same rule reaches further than it first appears. The tokenizer keeps `Anthropic's` whole,
so a rewrite that makes a name possessive loses one entity and invents another, and is
refused. This was found by auditing a fixture that assumed the opposite, not by reasoning
about it in advance, which is the argument for writing each proxy's cost down as a test.

**A named entity is a capitalised token that is not a function word.** There is no
deterministic way to find named entities, so this is the proxy: a token whose first rune is
upper case and whose lower-cased form is not in the declared function-word vocabulary. Its
failure modes, stated:

- It **over-collects** — `Monday`, `January`, any capitalised ordinary noun. That makes the
  gate stricter, which is the safe direction.
- It **under-collects** every entity that does not begin with an upper-case rune: `iPhone`,
  `danah boyd`, `von Neumann`'s particle. This is the dangerous direction and it is a
  limitation, not a bug to be found later.
- Excluding function words is what lets a sentence-initial `Anthropic` be seen while a
  sentence-initial `The` is ignored. Without it the gate would either miss every entity that
  opens a sentence or demand that every sentence keep its first word.

**URLs and quoted strings are matched over the text, not the token stream.** The tokenizer
splits `https://example.com/x` into eleven tokens, so a URL does not exist as a token at all;
it is found by scanning for `http://`, `https://` and `www.` and taking the run up to
whitespace, then trimming trailing characters that cannot end a URL — `.,;:!?` and closing
brackets and quotes. Without that trim, moving a URL from the middle of a sentence to its end
would change the matched item from one ending in a comma to one ending in a full stop, and the
gate would refuse a rewrite that touched nothing. A bare dotted domain with no scheme and no
`www.` is deliberately not matched: any dotted token would otherwise be a URL. Quoted strings are double-quoted spans only, straight or curly. **Single quotes
are excluded**, because an apostrophe and a closing single quote are the same character and
telling them apart is not deterministic.

**Negations are a closed declared list**, versioned like every other vocabulary here.
Contractions survive tokenisation whole — `Don't` is one token, and so is `Don’t` — so `n't`
forms are matched on the token rather than reconstructed from pieces. The list is declared in
full because *closed* has to mean something a test can check: an implementation free to
recognise an undeclared word would reject valid rewrites for a reason no reader could look up.

    cannot  neither  never  no  nobody  none  nor  not  nothing  nowhere  without
    aren't  can't  couldn't  didn't  doesn't  don't  hadn't  hasn't  haven't
    isn't  shouldn't  wasn't  weren't  won't  wouldn't

plus the curly-apostrophe form of each contraction. Forty-one entries, and nothing else: the
list bounds what an implementation may *recognise*, not merely what it may declare.

Matching folds case, and the reported item is **folded to lower case with it** — the one place
this gate normalises anything. It has to be: matching `Not` as a negation and then reporting it
as `Not` would make a current `Not` and a candidate `not` two different items, and the multiset
comparison would undo the case folding it just did. A capitalised negation is *also* a
capitalised token, so `Nothing` is reported as both a negation and an entity. That is the
entity proxy's declared over-collection doing exactly what it is documented to do, and the two
differences are independent.

The boundary is *negation*, not *diminishment*. `hardly`, `barely`, `scarcely` and `rarely`
weaken a claim without reversing it, and admitting them would make the gate refuse ordinary
rephrasing while adding no protection against the failure it exists to prevent — a rewrite
that inverts what the author said. `without` is in the list because absence is negation:
"without evidence" becoming "with evidence" is exactly the inversion this gate refuses.

**What it reports is every difference, by class, item and direction**, because a gate that
says only *no* leaves a user unable to act and a caller unable to explain. Inventions are
reported alongside losses, so the report is a list of *differences* rather than of things
missing.

**And it reports a separate non-prose identifier for each**, because the two audiences differ.
The item text is a rejection reason for the caller, who is about to be told why their rewrite
was refused. The store may not have it: its privacy invariant forbids any reversible prose
representation, and `rewrite`'s audit whitelist is where these identifiers land. An identifier
is the gate version, the class, the direction and a **digest** of the item — enough to tell two
failures apart and to count them over time, and not enough to read the text back out of it.

The digest has to be named, because "digest" alone is satisfied by hex or base64 of the item,
which is prose in a costume and would put the store in breach of its own invariant while
passing every test that only checks for the absence of literal text. It is the first **16 hex
characters of SHA-256** over the UTF-8 bytes of

    version \x00 class \x00 direction \x00 item

giving identifiers like `preserve-v1:number:lost:3d4c981bf761d9b8`. NUL separators mean no two
different tuples can share a preimage. The full identifier is what the audit stores; a caller
being told why its rewrite was refused gets the item text, which never leaves the process.

What that does and does not buy, stated rather than implied: the stored form is not decodable,
so nothing in the database, its sidecars, an export or a log can be read back into prose. It is
not proof against *guessing* — an item drawn from a small space, a one-digit number or a word
from the negation list, can be confirmed by anyone who computes the digest of a candidate.
Sixty-four bits of digest does not change that, because the entropy is in the item. A keyed
digest would defeat guessing, but the key would have to live where the audit lives to stay
verifiable, which returns the problem to its start. The invariant this gate is held to is that
the store contains no reversible prose representation, and a preimage-resistant digest meets
it; resistance to a determined guesser holding the store is not claimed.

### `rewrite`: the acceptance loop

ADR 0006 specifies the decision completely and leaves three quantities unstated. Two are
settled here; the third turned out to be the wrong shape.

**ε is a tolerance, not a threshold.** ADR 0006 says `d(candidate) ≤ d(current) − ε` and that
ties inside ε are rejections. A declared value in the range that looks natural — 0.01, say —
would be a constant compared against a quantity whose resolution changes with the corpus.
`d` is a mean over *k* features of deviations that are ranks against a reference of *n*
values, so its finest expressible change is about 2.5/((*n*+1)·*k*): **0.0135 at a reference
of thirty, 0.0041 at a hundred**. An ε of 0.01 therefore accepts a single-rank improvement on
a small corpus and silently rejects the same improvement on a larger one — the tool getting
*less* willing to improve as the evidence behind it grows.

So ε is a floating-point tolerance, `1e-9`, and its job is exactly the one the ADR names:
make ties rejections. The substantive protection against churn is the pass cap.

**The pass cap is three, and it counts attempts rather than acceptances.** Declared rather
than derived: a cap is a safety envelope, not an optimum, and this is a stand-in like every
other declared figure here. Its operational semantics are stated because the obvious reading
does not terminate:

- Every candidate consumes an attempt, **accepted or rejected**. A cap on acceptances alone
  would let a provider that never produces an acceptable candidate loop without end, which is
  the failure a cap exists to prevent.
- On acceptance, `current` becomes the candidate and the next attempt is made against it. On
  rejection, `current` is unchanged and the next attempt is made against the same text — the
  provider is asked again, not abandoned, since a rejection is a property of one candidate
  rather than of the segment.
- The loop stops when attempts reach the cap, or when the provider returns no candidate. The
  result is whatever `current` holds at that point, with every attempt and its verdict
  recorded.

Because acceptance requires a strict improvement in `d` and `current` only ever advances on
acceptance, the sequence of accepted distances is strictly decreasing and the loop is
monotone whatever the provider does.

**A candidate must admit exactly one segment.** A rewrite of a paragraph that arrives as two
paragraphs, or as something the lexical-token floor excludes, is not a rewrite of that
paragraph — it is a different edit whose `d` is not comparable to the original's. Candidates
are measured by the same path the original was, through `score`, and one that does not yield
exactly one segment is rejected without being scored.

**Refusal is passing through untouched.** Where `d` is unavailable on either side — the
segment unscoreable, or the profile uncalibrated — no acceptance is possible and the segment
is returned unchanged, reported as such. Absence of measurement is never improvement.

**Comparability is checked, not assumed.** Two distances built on different contributing
features are not comparable, and accepting on a fall in `d` between them would be accepting a
rewrite that moved the denominator. The contributing set travels on the distance for exactly
this, and a candidate whose set differs is rejected.

**The two non-regression guards, and their honest state.** `preserve` is deterministic —
numbers, named entities, negations, URLs and quoted strings must survive — and does not exist
yet; it arrives as its own component behind the interface this one defines. The tells guard
does exist: `tells.Comparison.Compare` orders derived, verdict-eligible findings
severity-lexicographically and refuses to compare reports from different rule sets, different
options, with suppressions honoured, or truncated. ADR 0006 already records that this gate is
**inert while every shipped rule is unvalidated**, and that remains true; it is wired in so
that validating a rule turns it on rather than requiring the loop to be rebuilt.

**The loop depends on the five interfaces the component table declares** — `Scorer`,
`Selector`, `Gate`, `Provider` and `Store` — and not on a single generator method. An earlier
draft of this section reduced the provider to one call taking the current text and returning a
candidate, which silently deleted `Selector` from the design: a conforming provider written
against it could not receive exemplars at all, and the exemplar path is not an optimisation
but the mechanism by which the rewrite is anchored to the author rather than to the model's
prior.

**A provider is given a request, not a string.** `RewriteRequest` carries the **assembled
prompt**, the profile and invocation identity the result is attributed to, and the
`--local-only` setting. An earlier draft of this paragraph said it carried the current segment
and the chosen exemplars separately; that contradicts the rule two paragraphs below, which is
the one that shipped — handing a provider the raw pieces would let it assemble a different
outbound prompt. That shape exists so ADR 0007's
boundary is expressible: **only the draft passage and a handful of exemplars are ever sent,
never the corpus.** A provider that received only a string could not honour that rule, because
it would have nothing to send but the passage — and a later change adding exemplars would have
no declared place to put them.

**Prompt assembly belongs to `rewrite`, not to its providers.** Corpus prose may itself read
as instructions, and a request carrying raw exemplar strings cannot enforce anything: every
provider implementation would have to remember to fence them, and one that forgot would have
no test that failed. So the request carries an **assembled prompt** built by this package, and
fencing is mechanical rather than conventional.

**And it carries nothing a provider could assemble a different prompt from.** Handing over the
raw exemplars alongside the prompt — for convenience, so a provider need not re-parse — would
restore the whole problem: a provider could ignore the assembled text and build its outbound
request from the unfenced strings, and the fence would be a convention again. The request
therefore holds the prompt and the non-prose identity and settings, and no raw prose at all.
A provider needing a structured split, a system and a user message say, is given **assembled**
parts rather than the ingredients.

**The fence is a line prefix, because a line prefix cannot be escaped out of.** A delimiter
pair can be broken by exemplar text that contains the delimiter, and escaping that text is a
second mechanism with its own failure modes. **Every** line of every exemplar is instead
emitted behind the prefix — blank lines included, since a blank line is where a delimiter-based
fence would be most tempting to close — so content that reads as an instruction stays behind
it exactly as ordinary content does.

**The passage carries its own marker, on the line immediately before it.** Distinguishing it
from the exemplars by position alone would be a convention again: a prompt that simply put the
passage first would satisfy "not fenced" while telling the model nothing about which text it
is being asked to rewrite. The marker is explicit, is not the fence prefix, and its adjacency
to the passage is part of the contract — a marker somewhere in the prompt marks nothing.

A blank exemplar line is emitted as the prefix alone, verbatim, so that "every line is fenced"
is checkable rather than inferred from the lines that happen to have content.

**Exemplars are selected once per invocation, not once per attempt.** ADR 0004 settles this
and this section had failed to carry it across: *exemplars are stable per profile and largely
cacheable, rather than recomputed per draft*. If they do not vary per draft they certainly do
not vary between attempts on one segment, and a loop that asked again each time would let a
stateful selector change the author's own writing underneath a running rewrite — so the second
attempt would be measured against an anchor the first never saw.

**Zero exemplars is not a configuration.** ADR 0007 permits the passage *and a handful of
exemplars*, and the exemplars are the anchor to the author's own prose rather than an
optional extra: a prompt without them asks a model to write in a style it has not been shown.
A non-positive count is refused rather than honoured, and the declared default is **three** —
a handful, and few enough to keep the boundary tight.

**Exemplars arrive exactly as requested, or not at all.** A selector returning fewer than
asked for is a silent reduction of the anchor to the author's own prose, and a substitution is
worse. The loop requires the requested count and refuses otherwise rather than proceeding with
a weaker prompt nobody chose.

`--local-only` is asserted by test, per ADR 0007: no cloud provider constructed, no credential
read, no dial outside loopback, no telemetry. That assertion belongs to the `llm` component, where a
provider exists to construct; this component's obligation is to carry the setting into every
request so a provider can honour it.

Everything in the loop except `Provider` is deterministic, which is what lets the acceptance
rule be tested exactly against fakes: a `Provider` returning scripted candidates, a `Selector`
returning fixed exemplars, and a `Gate` with scripted verdicts.

**What "retains before/after artifacts and rejection reasons" is allowed to mean.** The
component table promises retention; the store's privacy invariant forbids **any reversible
prose representation or textual derivative**, and says so for the database, its sidecars, any
export, and all log and diagnostic output. Those two sentences are in direct tension, and
resolving it by implementation would resolve it the wrong way — "auditable" is exactly the
word under which prose gets persisted.

The permitted audit record is therefore stated as a whitelist. For each attempt: the **span
reference** it applied to, **content hashes** of the current and candidate text, the
**distance and band** on each side, the **preserve verdict** and the identifiers of what it
found missing, the **tells comparison** result, the **rejection code**, and the profile,
provider and invocation identity. None of that is reversible to prose.

**Candidate text exists only in the output the user asked for.** It is never written to the
store, a log, a diagnostic dump or an export. A rejected candidate is not retained at all —
only its hash and the code that rejected it — because a rejected rewrite is precisely prose
the user never chose to keep.

**Reassembling a document is not in this component, and it has an owner.** The loop decides,
per segment, what the text should be; `assemble` turns those decisions into a document.
Splicing needs raw offsets and must preserve the excisions `text` removes from a leaf's token
view — a hazard of its own that would be hidden inside an acceptance loop rather than tested
on its own terms. Saying only that it is "separate" would have left `hapax rewrite draft.md`
with no component able to produce its output, which is not a boundary but a gap.

`assemble` takes the original bytes and a set of replacements, each naming a **raw byte
span** and its new text, and its contract is:

- **Spans are ordered and non-overlapping.** Overlapping replacements have no defined result,
  so they are refused rather than resolved by precedence.
- **Every untouched byte survives exactly**, including the excisions inside a replaced leaf's
  span and all material between spans. A replacement whose span contains an excision is
  refused: the excised bytes are not the leaf's prose and the loop never measured them, so
  overwriting them would discard content the rewrite never considered.
- **All or nothing.** A failure anywhere — an unordered span, an overlap, an excision inside a
  replacement — produces no output at all rather than a partially rewritten document. A file
  half in the author's voice and half in the model's is worse than an error.

**A replacement span must equal an included leaf's span exactly.** Not merely fall inside one.
Nothing in this system produces a verdict for an arbitrary byte range: `score` measures
paragraphs, the loop decides per paragraph, and a sub-span has no measurement attached to it —
so accepting one would mean splicing text no gate ever saw. It also disposes of grapheme
splitting for free, since leaf spans are already boundary-aligned. An excluded leaf — a
heading, a code block, a paragraph inside a block quote — has no included span and so cannot
be named at all.

**How restrictive the excision rule actually is, measured rather than assumed.** The rule reads
as though it would refuse most real prose, since Markdown paragraphs are full of inline syntax.
It does not. Against the real structure parser:

| construct | excisions | rewritable |
|---|---|---|
| plain paragraph, one line or wrapped | 0 | yes |
| `**bold**`, `[link](url)`, em-dashes, quotes | 0 | yes |
| `` `code` `` inline | 1 | **no** |
| footnote reference `[^1]` | 1 | **no** |
| heading, code block, block-quoted paragraph | — | excluded before this point |

So it bites on exactly two constructs, and in both the refusal is the point rather than a
limitation: a leaf's run text *drops* its excisions, so the paragraph
`A paragraph with `+"`code`"+` inline and a footnote.[^1]` reaches the loop as
`A paragraph with  inline and a footnote.` — the code span and the reference are simply not
there. Splicing a rewrite of that string over the leaf's raw span would delete the user's code
and their footnote, silently, and nothing downstream would notice. Refusing is the only
answer that does not lose content.

**A stripped BOM is put back.** `Admit` removes a leading UTF-8 BOM and reports it through
`HadBOM`, and every offset in the system is relative to the stripped bytes. Assembling from
those bytes alone would therefore rewrite the file's encoding preamble as a side effect of
editing its prose, on every BOM-carrying file, which is not a change the user asked for. If
the document had a BOM, the output starts with one — and the spans are still resolved against
the stripped bytes, because prepending first and splicing second puts every replacement three
bytes early.

**Offsets are byte offsets.** `Span` addresses raw bytes, not runes and not graphemes, and the
distinction is invisible in ASCII. A replacement is spliced by copying the original bytes up to
`Offset`, then the new text, then resuming at `Offset+Length` — always against the original
bytes, never against a buffer being built, or every span after the first lands wherever the
preceding replacement's change in length left it.

**Three refusals that are about the caller rather than the document.** Each would otherwise be
discovered by a user rather than declared:

- **Empty replacement text is refused.** It deletes a paragraph and leaves its blank lines
  behind, and the acceptance loop compares distances between real texts, so it can never
  propose one. An empty replacement is a caller error, not a decision to honour.
- **Invalid UTF-8 in a replacement is refused.** The output must be a document the tool can
  read back; writing bytes that `Admit` would reject means writing a file `hapax` cannot open.
- **The caller's slice is not modified.** Checking that spans are ordered by sorting them is
  the obvious implementation and silently reorders the caller's own data.

There is no version constant. Every other component here declares one because it binds an
artifact's identity; `assemble` returns bytes that carry no identity of their own, and the
document it produces is identified by hashing its content like any other input.

### `select`: representative of a pool, which is the only claim it makes

ADR 0004 separates scoring from retrieval: exemplars are segments representative of the
**author**, never nearest-neighbour to the draft, or the tool retrieves the author's most
AI-adjacent writing and teaches itself the defect it exists to remove.

**It owns its transform, and it is Train-only.** The obvious move — reuse the distance `d` —
fails twice. `d` measures distance to the reference *centre*, but a medoid minimizes distance
to actual segments, and in a bimodal population the centre sits in a sparse valley, so the
smallest-`d` paragraphs are representative of neither mode. And `deviation.BuildReference`
refuses any split but `calibrate`, so consuming a reference would give `select` an `eval`
dependency it does not declare. Instead: standardize against the profile, which needs no
reference, then rank-transform each feature against the Train candidates themselves, with the
same mid-rank plotting position and probit already used by `Reference.Transform` — including
its treatment of a feature that is constant across the pool, which yields a *defined* zero
rather than an undefined value.

**Both profile statistics are operative**, which is worth stating because ranking per feature
looks as though it should absorb them. It would, if standardization were a map shared by every
candidate — but the denominator is `sqrt(profileVariance + samplingVariance)` and sampling
variance is *per candidate*, so neither subtracting the mean nor dividing by the variance is a
common monotone transform, and both can reorder the population before the rank transform sees
it. Measured on the test fixture, where sampling variance spans 0 to 0.95: a mean of 1.5
changes the density record and a mean of 5 changes the selection; a variance of 1e-6 changes
the selection while 1 does not. An earlier draft of this note claimed the mean was
structurally inert on the strength of one fixture where it happened not to matter; it is not.

Self-referential ranking is sound *because this is not a gate* — nothing is validated, only
ordered. But it is population-relative: adding or removing a candidate can change every rank.
So the population is closed and content-addressed, and the claim is bounded to match —
**representative of this admitted Train pool**, not of the author in the abstract.

**Density is local; the centre is not consulted.** For each candidate, density is the mean
pairwise distance to its `k` nearest neighbours over valid pairs. Pairwise distance is the
same Burrows'-Delta form as `d` but between two segments, over features defined in both. A
pair is valid only above a shared-feature floor: without one, two segments that share three
agreeing features look closer than two that share thirty, and missingness manufactures
density.

**Declared cost limit.** `select` materializes the full symmetric `N × N` pairwise-distance
matrix, although it uses only its triangle. Its memory use is therefore `O(N²)` — roughly
400 MB at 5,000 candidates and 1.6 GB at 10,000 candidates. This implementation is intended
for the bounded fixture-scale pools it currently serves; a streaming k-nearest design is a
separate future change.

Every constant is fixed here, because "declared" without a number leaves two conforming
implementations free to disagree:

| | value |
|---|---|
| candidate unit | exactly the leaves `profile.ParagraphVectors` admits, under the same structure options and paragraph floor |
| canonical identity | the source document's content digest, then the leaf's raw `(offset, length)` — a text digest collides on duplicate leaves. It breaks every tie here, so it must be unique: a population containing the same identity twice is **refused**, since a non-unique tie-break is no tie-break |
| minimum population | `N ≥ max(30, 10n)`, else refuse |
| shared-feature floor | a pair is valid iff features defined in **both** `≥ ceil(0.5 × manifest size)` |
| `k` | `max(3, min(15, floor(sqrt(N))))` |
| minimum valid neighbours | a candidate with fewer than `k` valid pairs is ineligible |
| eligibility | of the candidates with at least `k` valid neighbours, sort ascending by density and keep the first `ceil(0.75 × N)`; if fewer qualify than that, keep all of them and let the `n` refusal decide. Boundary ties by canonical identity ascending |
| rank population | the `M` values **defined** in the pool for that feature; each candidate is transformed as a query against that distribution, plotting position `(lower + (upper−lower)/2 + 0.5) / (M+1)`, exactly as `Reference.Transform` does with the pool as its reference. The candidate is a member of the distribution — that is what self-referential means |
| medoid domain | summed valid pairwise distance to the **other** eligible candidates of its own stratum; self is excluded. A candidate needs at least one valid intra-stratum pair to be medoidable. Both this and the singleton rule are evaluated against the stratum's **remaining** candidates each round, so a stratum of two yields its second exemplar in a later round rather than being exhausted after the first: whenever exactly one eligible candidate remains, it is that round's medoid. A stratum with no medoidable candidate remaining is exhausted. Ties by canonical identity ascending |
| stratum key | `<role>\|<container>/<container>/…`, the leaf's `text.Role` then its container path in document order, joined by `/`. Ascending means byte-wise ascending on that string |
| allocation | strata ordered by descending eligible count then key ascending, then round-robin one slot each until `n` is filled, skipping exhausted strata. Each pick is the medoid of that stratum's *remaining* eligible candidates |
| caps | round-robin *is* the cap; there is no second mechanism |
| refusal | fewer than `n` eligible candidates in total refuses; it never returns fewer |

**Groups are structural strata, not clusters.** Feature-space clustering would need an
algorithm, a `k`, a linkage and a stopping rule, each requiring held-out evidence to tune that
does not exist yet. Strata are declared: a leaf's `text.Role` and its container path, both
already normalized vocabularies with no offsets or indexes in them. Each stratum contributes
its own medoid before any slot is filled, so representativeness is prior to diversity rather
than traded against it.

That leaves **register bimodality inside one named profile unhandled, deliberately** — ADR 0004
already answers it with explicit named profiles rather than inference, so a mixed-register
profile is a prompt to split it, not a case for this component to detect.

**`n` is part of the artifact, not a prefix of it.** `Exemplars(n)` cannot slice a pool and
still honour stratum allocation for arbitrary `n`, so the selection is built for the `n` asked
for and `n` is part of the cache key. `rewrite` requires exactly `n` back and defaults to 3.
If allocation cannot reach `n`, `select` refuses rather than quietly returning fewer or
substituting.

**Cache identity** covers the profile ID, every Train document's content digest, the text and
structure contract versions, the feature manifest digest, `n`, and **every constant in the
table above** — not a hand-maintained version string, which lets a changed threshold rehydrate
a stale selection under unchanged inputs. An upstream parser change yields different leaves
from the same document, so the document digest alone is not enough either. Failed rehydration
refuses rather than reseating the set. Persistence itself is the caller's: `select` computes
the identities, and the store that keys on them lives in `cli`.

**And it emits a certificate**, because "representative" is otherwise unfalsifiable: the
admitted population, per-candidate density, the eligibility decision, strata, medoid sums and
tie resolutions.

**Determinism is claimed for repeated runs, not across independent implementations.** The
distance `d` and the clustered bootstrap make the stronger claim because they are release
gates: their evidence has to be checkable by someone else. Exemplar selection is not a gate —
it fills a prompt — so pinning probit's last bit, floating-point summation order and
distance-equality across platforms would buy nothing the cache needs and would be a promise
this project has no way to test. What *is* claimed: the same binary on the same inputs
produces the same selection and the same certificate ID, which is exactly what makes the cache
sound. That is not automatic in Go: map iteration order is randomized per run, so **no map is
ever iterated** — candidates, features, neighbour lists, strata and every floating-point
reduction are traversed in the canonical orders declared here.

Within that, the encodings are still fixed rather than left to chance. The **selection ID** is
`identity.HashBytes` over `identity.Frame` of each chosen leaf's canonical identity in order,
each as `<document digest>:<offset>:<length>`. The **certificate ID** is
`identity.HashInputs` over exactly nine keys: `selection` (the selection ID), `population` (framed
canonical identities of every candidate, in canonical order), `eligible` (likewise, of the
eligible set), `density` (framed `<identity>=<numberID>` pairs in canonical order), `strata`
(framed `<identity>=<stratum key>` assignments in canonical identity order, so the certificate
binds candidates to strata rather than only counting them), `medoids` (framed
`<round>:<stratum key>:<identity>:<numberID sum>`), `binding` (framed `profile=<profile ID>`, `text=<text.ContractVersion>`,
`structure=<text.StructureVersion>`, `manifest=<feature manifest digest>` — without this the
certificate is blind to the profile and the parsers that produced its candidates, and a cache
keyed on it would serve a selection fitted to a different author), `ties`
(framed `<site>:<round>:<winner>`, where `site` is one of `eligibility`,
`medoid` or `stratum-order`, `round` is the allocation round or `-` where none applies, and an
entry appears **only** where two or more items actually compared equal — no tie, no entry.
An `eligibility` tie is recorded only where the cut itself falls inside a run of equal
densities; equal densities wholly inside or wholly outside the kept set change nothing and
are not ties. `stratum-order` uses round `-`), and
`config`, framed `<name>=<value>` over exactly these names in this order:
`n`, `k`, `min-population-absolute` (30), `min-population-multiple` (10),
`shared-feature-fraction` (0.5), `k-min` (3), `k-max` (15) and
`eligibility-fraction` (0.75). Naming each one is what stops a changed threshold from
rehydrating a stale selection behind an unchanged version string. `k` also appears on the
certificate directly, alongside a per-candidate valid-neighbour count, since both are the
evidence that density was computed rather than asserted. Floats use
`numberID`, which normalizes `-0`. That makes the *mechanical* claims checkable. Whether these exemplars produce
better rewrites is a held-out experiment nobody has run, so this is a declared proxy and is
called one.

### `score`, and two things it found underneath it

`score` measures a draft against a profile and emits, per paragraph, a calibrated band, the
distance behind it, and the per-feature deltas with their direction — or *insufficient
evidence*. Almost all of that is assembly: the deviation, the distance, the gates and the
release verdict already exist. Building it surfaced two defects in what it assembles.

**A draft belongs to no split, and the split vocabulary had no word for that.** Every
standardized segment records the split it came from, and only `train`, `calibrate` and
`test` were nameable. A draft is none of them: it is the thing the tool is *for*. Without a
word for it a draft has to claim one of the three, and the only survivable lie is `test` —
which is precisely the split the discrimination gate and the band floor draw their evidence
from. A scoring path that labelled drafts `test` would let the user's own unmeasured writing
into the gates that decide whether the profile can be trusted.

So `draft` joins the vocabulary, meaning **scored, never fitted, never evidence**. It is
never assigned by the corpus, which partitions only among the other three; a reference
refuses anything but `calibrate`; and both release gates require `test`. The new value can
therefore reach the scoring path and nothing else, which is a stronger guarantee than the
one it replaces rather than a looser one.

**A reference could not be stored.** The deviation reference held its per-feature
distributions in an unexported field, so an encoded and restored reference came back holding
nothing — and `Transform` then reported `reference-too-small` for every feature. `score` is
the first consumer that loads a reference rather than building one, and the failure it would
have hit is the worst shape available: not a corrupt artifact and not a crash, but *every
paragraph reporting insufficient evidence*, which is a legitimate verdict and would have
been read as one. The distributions are now part of the artifact.

That is the same defect the band calibration slice found in its own artifact, in a package
that had already been reviewed and merged. **Every artifact with a content-addressed identity must survive
its own encoding, and that property has to be tested, not assumed** — not every artifact has
one, since `document`, `node`, `feature_vector` and `rewrite_attempt` are keyed within a
parent instead — a round trip
through the encoding, with behaviour compared before and after.

**The report has one shape, not two.** ADR 0005 says an uncalibrated profile still emits the
raw distance and per-feature deltas, and only the band is withheld. That is not a second
report format: the band already carries its own definedness and reason, exactly as a distance
and a deviation do. A reader distinguishes the cases by asking whether the band is defined,
not by discovering a missing field.

**Paragraph admission is the profile's, not a second copy.** A draft is split into segments
by the same shared path the profile was fitted with and the same lexical-token floor.
Measuring against a profile fitted on a different notion of paragraph is the error the shared
path exists to prevent, and it would be invisible in the output.

**v1 scores Tier A only, because that is what the manifest declares.** ADR 0003 gives `score`
Tier A at paragraph scale and Tier B over rolling windows; Tier B has no features and the
window is not built. The tier set is read off the manifest, so the day a Tier B feature is
added the per-tier minimums and the partial-score rule are already in force — but the
windowing mechanism arrives with it, and until then every score is a paragraph-scale Tier A
score.

**Direction is reported beside every delta.** The transformed deviation is signed for exactly
this reason: `above` the author's usual, `below` it, or `typical` at zero. An undefined
deviation has no direction, since there is nothing to take a sign of.

### The discrimination gate

ADR 0005 names "a predeclared minimum AUC" and never declares one, nor says which direction
AUC runs, nor what happens to ties. Each of those omissions is a way to ship a number that
looks like a measurement.

**Orientation, stated because getting it backwards is silent.** `d` is a distance: *lower*
means closer to the author. So the quantity is

```
AUC = P(d_author < d_distractor) + ½ · P(d_author = d_distractor)
```

over all author×distractor pairs of held-out segments. An implementation computing the
conventional "probability the positive scores higher" against `d` reports 1 − AUC, and a
profile with excellent discrimination then reports 0.15 — a number low enough to look like a
failure and high enough not to look like a bug. Nothing in the arithmetic would object.

**Ties count as a half**, the Mann–Whitney convention. This is not a formality here: `d` is a
mean of rank-transformed deviations that are themselves capped by the reference size, over
six features, so exact ties are ordinary rather than rare. Dropping ties inflates AUC and
counting them as wins inflates it further.

**A bound, not a point estimate**, as everywhere else. The gate is on the **one-sided lower**
confidence bound of AUC, from the same clustered bootstrap, at the same 0.95. AUC is a paired
statistic, so each resample draws clusters from **both** classes — independently, from their
own streams — and recomputes AUC on the resampled pair. A closed-form AUC standard error
assumes independent segments, which is the assumption this whole section is built to avoid.

**And the same degeneracy, from the other end.** Perfect separation resamples to 1.0 every
time, so the bootstrap reports a lower bound of exactly 1.0 for a profile that has merely
never misordered a pair yet. The bound is therefore **the lesser of the bootstrap percentile
and 1 − 3/*c***, where *c* is min(author clusters, distractor clusters) — the rule of three
again, at the cluster level, mirrored. Never claim a tighter bound than a perfect sample of
this many independent units could support.

**The floor is 0.80, and it is a judgement rather than a derivation.** Unlike the band
minimums nothing implies it: it answers *how good is good enough*, which no arithmetic
settles. Two things inform the choice. It is the conventional threshold above which a
diagnostic measure is treated as actionable rather than merely better than chance. And this
tool's output drives **edits to the user's own writing** — ADR 0006's loop accepts a rewrite
whenever `d` improves, so a `d` that barely discriminates turns the loop into noise-driven
vandalism. The bar belongs above "better than a coin".

Declared before measurement and published with its measured outcome, like every other target
here. **It may well not be met by v1's six Tier A features at paragraph scale**, and that is
the intended behaviour rather than a number to tune down afterwards: the profile reports
`uncalibrated`, `score` emits the raw distance and per-feature deltas without a band, and
`rewrite` refuses. Section 2 already says where a target cannot be met the honest result is
refusal, never a quietly relaxed target.

**The floor implies its own cluster minimum**, as the band floor does. Since the bound cannot
exceed 1 − 3/*c*, clearing 0.80 needs 3/*c* ≤ 0.20, so **at least fifteen clusters in each
class**. That is less demanding than the band gate's thirty and sixty, so in practice the
band gate binds first — which is the right ordering, since a band makes a narrower claim than
the profile as a whole.

**The two gates compose in one direction only.** Discrimination is prior: below its floor no
band is emitted whatever the band-level evidence says, because a band is a label on a
distance that has been shown not to carry information. The reverse does not hold — individual
bands can fail while discrimination passes, and the profile stays usable for the bands that
hold. That ordering is structural rather than a convention a caller has to remember: the
release verdict owns the composition, and it is what `score` and `rewrite` are given.

### The band calibration floor

ADR 0005 requires each band to be backed by held-out evidence, and the earlier statement of
the test did not state one. "Its observed rate must fall inside its declared confidence
interval" is either vacuous — a point estimate always lies inside an interval computed from
it — or it refers to a pre-declared acceptable range that appears nowhere. Either way it
controlled nothing, which is the failure mode this section keeps finding in its own prose.

**Only two bands make a claim, and each is gated on the error of that claim.** `in range`
says *this is the author*, so its error is a **distractor** landing there, bounded by
`p_distractor`. `not you` says the opposite, so its error is the **author** landing there,
bounded by `p_author`. `drifting` asserts nothing, needs no evidence, and is therefore the
fallback rather than a gated band.

**The error rate is class-conditional, not the band's composition.** The quantity gated is
`P(distractor lands in range)`, over all held-out distractors — the same quantity the
threshold targets bound. The band's composition (what fraction of the segments in it came
from each class) depends on how many distractors the user happened to supply, so it is a
property of the pool rather than of the method; it is reported and not gated on.

**The gate runs on Test.** The thresholds were fitted on Calibrate to hit their targets
there. Re-measuring on Calibrate would ask whether the fit fits. Test asks the question that
matters: does the target hold out of sample.

**A bound, not a point estimate**, for the same reason as everywhere else in this section:
an observed zero on a handful of segments is not evidence of a small rate. The bound is a
**one-sided upper bound from the same clustered bootstrap** used for the threshold
intervals, at the same 0.95. A binomial interval would assume segments are independent,
which is the assumption the clustered bootstrap exists to avoid.

**But a bootstrap degenerates at zero**, and this gate lives at zero: a rate observed as
zero resamples to zero every time, so the bootstrap reports an upper bound of exactly 0 for
a band that has simply never been wrong yet. That is the most over-confident answer
available, and it is worst precisely where the evidence is thinnest.

The bound is therefore **the greater of the clustered bootstrap percentile and 3/*c***,
where *c* is the number of **clusters** in the class whose error is bounded. Stated plainly,
this is a declared conservatism rather than a theorem: **never claim a tighter bound than a
perfect sample of this many independent units could support.** The bootstrap contributes the
clustering; the floor contributes what the bootstrap cannot see.

The denominator is the cluster count and not the segment count, and the difference is not
cosmetic. A hundred error-free segments drawn from one document are one independent
observation, not a hundred, and 3/100 would claim a bound of 0.03 from evidence that
supports nothing of the sort. Applying the rule of three at the segment level would
contradict the entire reason this section resamples clusters — it would smuggle the
independence assumption back in through the floor after the bootstrap had been built to
avoid it.

**The minimum held-out count is then a consequence rather than a second gate, and it is a
demanding one.** Since the bound is at least 3/*c*, a band whose target is *p* cannot clear
it below *c* = ⌈3/*p*⌉ **clusters**: **60 held-out author documents for `not you` at
`p_author` = 0.05, and 30 distractor clusters for `in range` at `p_distractor` = 0.10.**
That is a real cost and it is stated rather than softened — a band is a claim about an error
rate, and there is no sample size below this at which such a claim can be made. The figure
is reported so a user knows what a band needs, and not tested separately, because one rule
that implies the other is better than two that could disagree.

**An unscoreable segment needs no cluster label.** It carries no distance, so it is excluded
before anything is clustered and is evidence about nothing. Requiring a label for it would
refuse a population over data the gate does not use.

**A class with no held-out segments at all bounds nothing**, and 3/0 is not a number. The
bound is reported as 1 — the widest a rate can be — which fails every target below it and
refuses the band. Reporting 0 there would be the same over-confidence the floor exists to
stop, arrived at from the other direction.

The count that matters is the **class whose error is being bounded**, not the band's
occupancy. Occupancy by the other class is not evidence about the rate the band
claims. Occupancy is reported for both classes because it tells a reader whether a label is
ever reached, but a band is not refused for being unvisited.

**Collapse is toward the fallback.** A failed claiming band reports `drifting`, which is the
adjacent band in the only ordering that matters here — from a claim to no claim. When both
claiming bands fail, everything would report `drifting`, which is not a band set but an
absence of one, so the profile reports `uncalibrated` instead of dressing the absence as a
result.

**A calibration is self-contained, like every other artifact here.** It carries the
boundaries it classifies with and the bindings it checks, rather than holding a reference to
the threshold artifact it came from. The failure this avoids is silent rather than loud: a
calibration that classified through state which did not survive storage would decode its
boundaries as zero and then place every distance above zero in `not you`, confidently and
with no error. An artifact that cannot be written and read back is not an artifact.

**Collapse is applied by the calibration, not left to the caller.** The threshold artifact
answers a geometric question — which side of the boundaries a distance falls on — and the
calibration answers the one that matters: which label may actually be emitted. A consumer
that had to read the band reports and then apply the thresholds itself could emit a label
the gate refused, which is the one outcome this whole section exists to prevent. So the
calibration carries the classification, and it is the only one `score` and `rewrite` are
given.

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
sample sizes, the weighting scheme and its version, missingness rules, seeds,
split identity, the threshold artifact, and the distractor-pool identity. Anything that can
change a score is in the key.

### Numerical targets are outputs, not inputs

`p_author`, `p_distractor`, the AUC floor, the noise/signal fraction, `k` and every
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
| `rewrite_attempt` | Invocation plus attempt index | The audit whitelist `rewrite.Attempt` declares: hashes, span reference, distances, bands, verdicts and a rejection code. **No prose** |

That last row was missing. `rewrite.Store.RecordAttempt` has existed since the rewrite slice
and the artifact table never named what it writes, which is exactly how an audit record ends
up holding whatever seemed useful at the time.

**And writing it down found that it already holds prose.** `rewrite.Attempt.Missing` carries
the preserve gate's *item text* — its own frozen test pins `["number:1979", "url:example.com"]`
— while the whitelist two sections above says the record holds "the identifiers of what it
found missing". The sequencing explains it without excusing it: `rewrite` was frozen before
`preserve` existed, `preserve` later introduced `Result.Identifiers()` explicitly for this
record, and nothing went back. So the field must carry identifiers before `store` persists
it, and that correction is its own slice rather than something the store works around —
a store that quietly categorised the prose away would leave the leak in memory, in logs and
in any diagnostic dump, all of which the invariant covers.

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

### `store`, and the eight questions a schema has to answer

**A codec per artifact, never a marshalled owner struct.** Persisting
`json.Marshal(profile)` would mean every field a future slice adds to `profile.Profile` is
persisted the moment it exists, including a prose-bearing one. Each artifact instead has an
explicit persistence struct in `store` with named columns, and a test asserts its field set is
exactly the declared allowlist. The allowlist becomes something a reviewer must widen on
purpose rather than something a struct literal widens by accident.

**Two kinds of identity, because not everything is content-addressed.** Most artifacts carry
an ID their owning component already computed — `store` never invents one. But `document`,
`node`, `feature_vector` and `rewrite_attempt` are identified by a composite key within a
parent, and "invocation plus attempt index" is an idempotency key rather than a digest. So the
rule is stated: writing a key that already exists **succeeds if the content is identical** and
returns `ErrConflict` if it is not. A retried write is safe; a changed one is corruption.

**Aggregates become visible at once.** A snapshot, its documents, their nodes and those nodes'
vectors are written in one transaction. There is no moment at which a reader sees half a
document tree, because a partial graph is indistinguishable from a small corpus.

**One writer, and locks that answer to the caller.** WAL mode, `BEGIN IMMEDIATE` for writes so
lock acquisition fails early rather than mid-transaction, a busy timeout bounded by the
caller's context rather than a constant, and a unique constraint on every identity so two
writers cannot silently overwrite one another.

**Migration is a transaction, not a version integer.** Each migration runs inside one
transaction that also writes its own row — version, a checksum of the migration itself, and
when it was applied — so the version transition is atomic and an interrupted migration leaves
no half-migrated schema. Opening refuses on three conditions: a recorded version newer than
the binary, a recorded checksum that disagrees with the migration of that version, and a
version with no recorded row. "Refuse" rather than "repair": a database is the user's evidence.

**Span references are structured, and rehydration opens the file once.** A reference carries
the snapshot ID, the document ID, the expected content hash and a raw byte range — not an
opaque string. Rehydration reads the whole file, hashes what it read, compares, and only then
slices. Stat-then-read or hash-then-reopen both leave a window in which the file is replaced
between the check and the use.

**Rehydration has a closed vocabulary**, because "it did not work" is not something a caller
can act on: `ok`, `missing`, `unreadable`, `content-changed`, `span-invalid`. All but the
first are ordinary states rather than errors — a user may edit, move or delete their own
writing at any time — and none of them permits substitution or silent reduction. A malformed
stored reference is deliberately **not** in this list; it is `ErrCorrupt`, for the reason
given below.

**Absence is not deletion.** The earlier rule, that reindexing evicts artifacts whose
documents are gone, is unsafe as written: an unmounted drive or a changed permission would
erase evidence. A document that cannot be read is marked **unavailable, with a timestamp**,
and nothing is removed. Removal is a separate, explicit, transactional `Prune` whose roots are
declared — the current snapshot of each profile, every `eval_result`, and every
`rewrite_attempt` — which removes only what is unreachable from them.

**Audit attempts outlive their spans.** A `rewrite_attempt` is retained even when the span it
references can no longer be rehydrated. An audit record whose evidence has disappeared is
precisely the case where it is worth having.

**"Identical content" is one definition, not one per caller.** Idempotent writes and
content-addressed identities both depend on it, so it is the codec's column values compared as
typed values after four normalisations: paths are corpus-root-relative with forward slashes,
floats use the `numberID` convention that normalises `-0`, timestamps are RFC 3339 in UTC to
the second, and an absent list is the empty list. Two implementations that disagree on any of
those disagree on whether a retry is safe.

**Aggregate integrity is a read rule as well as a write rule.** Foreign keys are enforced, and
a read of a snapshot's tree happens in one transaction, because a child that is missing,
duplicated or orphaned would otherwise present as a smaller valid corpus. The same closure
applies to the profile, exemplar, threshold and eval-result graphs, not only to ingestion.

**A concurrent idempotent write is decided by rereading, not by the collision.** The loser of
a unique-constraint race has not yet compared anything, so it rereads the winning row inside
its own transaction and succeeds only if the content is identical. Returning `ErrConflict` on
the collision itself would fail a safe retry.

**Four refusals, not one.** A database this binary cannot account for fails in one of four
distinguishable ways, because "schema mismatch" tells an operator nothing about what to do:
`ErrSchemaAhead` (a version newer than this binary), `ErrSchemaChecksum` (a version whose
recorded checksum differs from the migration this binary carries), `ErrSchemaIncomplete` (a
gap, or a ledger missing a version this binary has), and `ErrSchemaForeign` (a database with
tables but no ledger at all). Every one of them leaves the file and its sidecars byte-identical.

**The migration ledger is the only authority on version.** Versions are contiguous from zero;
the checksum is over the exact migration bytes the binary carries; and the ledger, not the
schema, answers "what version is this". Three disagreements are distinguished rather than
merged: a version newer than the binary knows, a checksum that differs from the binary's
migration of that version, and a schema with no ledger at all — the last being a
pre-ledger or externally-created database, which is refused rather than adopted.

**Rehydration is given a root.** Snapshot identity is deliberately location-independent, so
the reference cannot name a directory; the caller passes the corpus root it wants read. The
outcome vocabulary maps to causes explicitly: a path that does not resolve is `missing`; an OS
error opening or reading it is `unreadable`; bytes that hash differently are `content-changed`;
a range outside those bytes is `span-invalid`. `unavailable_at` is set when a document first
yields `missing` or `unreadable` and cleared the first time it reads back `ok`.

A **malformed stored reference is not in that vocabulary** — it is `ErrCorrupt`. The earlier
draft listed `reference-corrupt` as an ordinary state, which contradicted the rule below: a
user editing their own file is ordinary, a store that wrote a reference it cannot parse is not.

The line between the two runs through `span-invalid`, which is narrower than it looks. A
negative offset or length, a path or hash outside its grammar, or a node whose document and
snapshot disagree with where it was found, are all damage the store itself wrote: `ErrCorrupt`.
`span-invalid` is reserved for a **structurally valid range that cannot be sliced from the
bytes just read** — the user's edit shortened the file. And `content-changed` and
`span-invalid` both mean the file *was* read, so neither marks the document unavailable;
`unavailable_at` answers "could this be read at all", which only `missing` and `unreadable`
can make false.

**The schema's shape is part of the allowlist.** Column names alone would pass a schema whose
`content_hash` was an unconstrained `TEXT`, so declared types, `NOT NULL`, foreign keys with
`ON DELETE CASCADE`, uniqueness and `CHECK` constraints are all asserted, and there are no
virtual tables. Enforcement is a property of the connection rather than of the schema, and this slice exposes
no operation that could violate a foreign key — `PutSnapshot` validates before it inserts — so
what is asserted here is that the constraints are *declared*. Enforcement gets its observable
consequence with the cascade `Prune` relies on, and is tested there.
The migration payload is exported so a test can hash it independently — a checksum that is
merely *consistent* between two databases would be satisfied by a constant. Even then this is a **tripwire, not a proof**: the honest controls are the
column allowlist, the codec field-set tests, and review of anything added. A DDL substring
scan cannot rule out every reversible derivative and is not claimed to.

**The allowlist is columns, not prose.** "Holds" in the artifact table cannot drive a test, so
each artifact's columns are declared, and every textual column has a grammar: an identity or
digest is hex or a declared identifier form, a path is corpus-root-relative, an enum is one of
a closed set, and there is no free-text column anywhere. `rewrite_attempt` is the one to read
twice, since it is the record that already held prose once.

**`Prune` has a declared graph, and it is directed away from its roots.** An earlier draft
rooted it at the current snapshot while pointing the edge `profile → snapshot`, so nothing
reachable from a root included the profile, its reference, its thresholds or its exemplar
selections — a traversal that would have deleted the entire live profile graph. The roots are
therefore the artifacts a user still has, not the ones they are built on:

- **roots**: exactly the profile IDs `Prune` is *given*, plus every `eval_result` and every
  `rewrite_attempt`
- `profile` → its `snapshot`, its `reference`, its `threshold`s, its `exemplar_selection`s,
  its `profile_stat`s and its `profile_head`
- `snapshot` → its `document`s → their `node`s → their `feature_vector`s → their `feature_value`s
- `node` → **its `document`, and that document's `snapshot`**
- `reference` → its `reference_value`s
- `exemplar_selection` → its `exemplar_member`s and the `node`s it names
- `eval_result` → its `profile` and `reference`
- `rewrite_attempt` → its `profile`, its span's `node`, and its `rewrite_attempt_identifier`s

The `node → document → snapshot` edge is stated **on the node**, so it holds however the node
was reached, and it is what stops `Prune` destroying the evidence the rule below promises to
keep. `rewrite_attempt.node_id` cascades on delete, and a rewrite operates on *draft* nodes
that need not belong to the profile's own snapshot — so without this edge a snapshot reachable
from no root would be deleted, the cascade would take its documents and nodes, and the cascade
would then take the `rewrite_attempt` itself. It is redundant for a node reached through its
own profile's snapshot, which is the right way round: a graph that is complete on its own beats
one that is correct only if an invariant declared in another section holds.

Its cost is deliberate. `Prune` cannot reclaim a snapshot that a retained audit record points
into. Those rows are metadata — no prose, no vectors of consequence — and they are what makes
the audit record mean anything.

**`Prune` takes its roots as arguments**, so its result is a function of what it was told and
nothing else. "The current profile" is a policy question — which register, which of several
profiles, whether an older one is still wanted — and `store` should not be the thing deciding
it. `cli` passes the heads it wants kept.

The head itself is still a stored fact, because *something* has to answer "which profile does
`hapax score` use". `profile_head` maps a register to one profile ID and is updated in the same
transaction that writes the profile it points at, so there is no window in which a head names
a profile that does not exist. It is not itself a `Prune` root: a head that nobody passed is a
head `cli` chose not to keep.

A document marked unavailable keeps its whole artifact graph — that is the point of marking
rather than deleting. Marking and deletion are computed and committed in one write
transaction, so a concurrent reader never sees a half-pruned graph.

**The columns themselves.** `hex` is lower-case hexadecimal of a declared length, `rel` is a
corpus-root-relative forward-slash path, `enum` is one of a closed set named by the owning
package, and `num` is a float under the `numberID` convention. There is no free-text column
in this schema, which is the property the allowlist test asserts.

| artifact | key | columns |
|---|---|---|
| `snapshot` | `id` hex | `policy_digest` hex, `created_at` time |
| `document` | `document_id` = `HashInputs{snapshot, path}` | `snapshot_id` hex, `path` rel, `content_hash` hex, `register` enum, `split` enum, `admission` enum, `language` enum, `unavailable_at` time or null |
| `node` | `node_id` = `HashInputs{document, ordinal}` | `document_id` hex, `ordinal` int, `kind` enum, `role` enum, `containers` enum list, `offset` int, `length` int, `included` bool, `exclusion` enum |
| `feature_vector` | `node_id` + `manifest_digest` hex | `set_version` int, `tokens` int, `lexical_tokens` int, and per feature `value` num, `defined` bool, `sampling_variance` num, `sampling_variance_defined` bool |
| `profile` | `id` hex | `snapshot_id` hex, `register` enum, `unit` enum, `variance_convention` enum, `manifest_digest` hex, `min_paragraph_lexical_tokens` int, and per feature `n` int, `mean` num, `variance` num, `defined` bool, `variance_defined` bool, `min_observations` int |
| `profile_head` | `register` enum | `profile_id` hex, `updated_at` time |
| `reference` | `id` hex | `profile_id` hex, `split` enum, `min_segments` int, and per feature an ordered `num` distribution |
| `exemplar_selection` | `id` hex | `profile_id` hex, `n` int, `certificate_id` hex, and ordered member `node_id`s |
| `threshold` | `id` hex | `profile_id` hex, `reference_id` hex, `population_id` hex, `t_low` num, `t_high` num, achieved rates num, interval bounds num, `verdict` enum |
| `eval_result` | `id` hex | `profile_id` hex, `reference_id` hex, `auc` num, `lower_bound` num, `cap` num, cluster and segment counts int, `discriminates` bool, `calibrated` bool, `shippable` bool, `reason` enum |
| `rewrite_attempt` | `invocation_id` hex + `index` int | `profile_id` hex, `provider_id` enum, `node_id`, `current_hash` hex, `candidate_hash` hex, `current_distance` num, `candidate_distance` num, `current_band` enum, `candidate_band` enum, `preserved` bool, `preserve_identifiers` identifier list, `tells_comparison` int, `tells_comparable` bool, `accepted` bool, `rejection` enum |

The `snapshot` identity is **verified, not trusted**. `corpus` computes it, but `store` has to
be able to recompute it or the read-integrity rule is unenforceable for the one artifact
everything else hangs from: it is
`HashInputs{"policy": policyDigest, "documents": Frame(sorted "path=contentHash")}`, and a
snapshot whose membership does not hash to its stored ID is `ErrCorrupt`.

`document_id` and `node_id` are **derived**, not surrogate, and the preimages are named so a
test can compute them: `document_id` is `HashInputs{"snapshot": snapshotID, "path": path}` and
`node_id` is `HashInputs{"document": documentID, "ordinal": decimal ordinal}`. A foreign
reference is then a single stable column and two implementations produce the same value. An autoincrement key would be local to one database
and would make a span reference meaningless outside it.

Two columns of `corpus.Document` are deliberately **absent**. `rejection_detail` holds an
error message — today always `invalid UTF-8 at byte offset N`, which carries nothing, but a
string whose contents are whatever a future error type formats is not a column this schema
should own. `rejection_offset` is the persistable form of the same fact. And `register` is a
user-supplied label, so it is not free text either: it matches `[a-z0-9][a-z0-9-]{0,31}` and is
validated on write, which is what keeps the no-free-text claim true rather than nearly true.

Two of those are worth saying out loud. `provider_id` is an enum over the declared providers
rather than a label a caller chooses, and `invocation_id` is a digest rather than anything a
user names — both are strings that would otherwise be free text by another name.
`preserve_identifiers` is validated against `preserve.ValidIdentifier` on the way in, because
that column is the one that already held prose once.

**Every read is validated.** Unknown enum values, non-finite floats and rows whose `(kind, id)`
disagrees with where they were found are all `ErrCorrupt`, as is an artifact that names a
parent outside its own graph — a threshold or eval result combining one profile with another
profile's reference. Corruption must never be able to present itself as *insufficient
evidence*, which is a legitimate verdict this system emits and would therefore be believed.

**Recomputing an identity is possible only where its preimage is stored**, which is the
snapshot, the document and the node — and it is required of exactly those. This line
previously said a decoded content that no longer hashes to its stored ID is `ErrCorrupt` for
every content-addressed artifact, which no schema of this shape can satisfy: a profile ID
hashes the outlier algorithm and four build floors, and an exemplar certificate ID hashes the
density, medoid and tie records, none of which are columns here and none of which should be —
persisting a preimage so the ID can be rechecked would mean persisting the working records
the artifact table exists to keep out. Every other ID is **carried**, and what is checked of
it is its digest form and its referential closure. Widening the schema until an ID becomes
recomputable is a change that must make it verified.

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
- Reindexing marks artifacts whose documents cannot be read as **unavailable** and removes
  nothing. This line previously said it evicts them, which would let an unmounted drive erase
  evidence; removal is `Prune`'s job and `Prune` is explicit

### Schema migration

The store carries a schema version, migrated forward only. A migration that changes the
*meaning* of a stored artifact must also bump the relevant semantic contract version from
Section 2 — otherwise migrated artifacts are silently reinterpreted under new rules, which is
the stale-reuse failure the cache identity exists to prevent.
