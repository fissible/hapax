# hapax — project state

Rewrite AI-drafted prose into your own voice, measured against your own prior
writing. Apache-2.0, public.

This file is the stateless entry point: everything needed to resume work with no
prior context. Design lives in [`docs/DESIGN.md`](docs/DESIGN.md), decisions in
[`docs/adr/`](docs/adr/), and the adversarial review log in
[`docs/REVIEW.md`](docs/REVIEW.md).

---

## Build order and status

Components are numbered as in DESIGN Section 1 and listed leaves → roots. Each
was built with the `duet` process: tests written and adversarially reviewed
before any implementation, tests frozen by commit, implementation by a second
model, then review.

| # | Component | Status | Notes |
|---|---|---|---|
| 0 | `text` | **built** | admission + spans (2a-1), tokenization (2a), structural tree (2d), run tokens |
| 1 | `tells` | **built** | schema, regex matcher, screening model |
| 2 | `corpus` | **built** | walk, dedupe, split, snapshot identity |
| 3 | `features` | **built** | Tier A candidates |
| 4 | `profile` | **built** | paragraph-unit. Readiness withheld until the minimums are derived |
| 5 | `eval` | **built** | deviation, distance `d`, thresholds, clustered bootstrap, band floor, AUC gate |
| 6 | `score` | **built** | PR #32. Added the `draft` split; found a reference that could not be stored |
| 7 | `select` | **built** | PR #39, package `exemplar` — `select` is a Go keyword |
| 8 | `preserve` | **built** | PR #34 |
| 9 | `llm` | **built** | PR #41. Ollama + Anthropic, dial seam, AST egress guard |
| 10 | `rewrite` | **built** | PR #33; audit record corrected in PR #43 |
| 11 | `assemble` | **built** | PR #37 |
| 12 | `store` | **built** | PR #45 schema, #48 codecs, #49 rehydration and `Prune`, #52 the release |
| 13 | `ingest` | **built** | PR #54. Verified snapshot to a deterministic node/vector graph; one tree per call |
| 14 | `cli` | **built** | A1 #51, A2a `index`/`profile` #57, A2b `eval` #59, A2c `score` #66, B1 planning #67, B2a the credential boundary #73, B2b-1 execution #75, B2b-2 the command and publication #68. All six commands run |

Supporting: `fixtures` (vendored public-domain corpus), `ciconfig` + CI workflow.

---

## Sprint: what the codex review found in v0.1.0

An independent codex review of everything merged after `157617c` — eight slices that
Claude specified, Claude reviewed and Claude implemented — found three reproduced
production defects, one unrepresentable claim, and one false one. All five are in released
code.

**The class, which is the finding that matters most:** *representation mismatch across
components, hidden by calling several different byte sequences "the paragraph".* Every
Claude review asked whether its slice hashed the right bytes and each answered correctly
for itself. None asked whether the slices agreed on what a paragraph IS, because no review
ever saw more than one slice. A slice review CAN reach an interaction — it can read the
consumer's contract — and these reviews did not; that is what happened, and claiming the
stronger thing, that reviewing in isolation cannot find an interaction, is the same kind of
overclaim this sprint exists to strike.

### Dependency order

```
A  the false claims          no deps          do first, it is free and it is shipped
      |
B  publication identity      needs nothing    restores two guards currently bypassed
   (#132 + #134 + #135)                       establishes the canonical paragraph form
      |
C  the scoring invariant     needs B's form   publishes what it scored
   (#133)
```

**B split into B1 and B2 after the design pass.** The grouping was "one `rewrite_attempt`
rebuild rather than two", and that stopped applying: B1's fix is a NEW table and rebuilds
nothing, while `#135` needs its own column regardless. The replay-argument strike `#135` calls
for went with it into B2.

`#132` and `#134` are one question from two sides — *what bytes, from what evidence, count
as a published paragraph* — so they are one slice. Deriving the evidence from the assembled
bytes rather than the attempt table fixes the representation mismatch as a consequence,
because the assembled leaf span IS the canonical form. `#135` joins them if the fix needs a
`rewrite_attempt` column, so the table is rebuilt once rather than twice; if it does not, it
splits back out.

`#133` is independent of both but must adopt whatever canonical form B establishes, so it
goes after rather than in parallel.

| Slice | Issues | Effort | Deps | Status |
|---|---|---|---|---|
| A — strike the false claims | finding 5, no issue | XS | none | **merged** ([#138](https://github.com/fissible/hapax/pull/138)) |
| B1 — publication identity and evidence | [#132](https://github.com/fissible/hapax/issues/132), [#134](https://github.com/fissible/hapax/issues/134) | M–L | A | **merged** ([#139](https://github.com/fissible/hapax/pull/139)) |
| B2 — the splice verdict | [#135](https://github.com/fissible/hapax/issues/135) | S | none | **PR [#140](https://github.com/fissible/hapax/pull/140)** |
| C — publish what was scored | [#133](https://github.com/fissible/hapax/issues/133) | M–L | B1 | **merged** ([#141](https://github.com/fissible/hapax/pull/141)) |

Reviewer for this sprint is **codex**. What the history establishes is that eight
consecutive Claude reviews did not find these defects; it does not isolate model identity as
the cause, and saying "the defects exist because the same model reviewed them" would be a
causal claim the evidence does not carry. The decision to change reviewers follows from the
observed miss, not from a demonstrated mechanism. A release should follow B and
C. Released as v0.2.0: minor rather than patch, because the repairs added two
tables, a column, a refusal code and envelope fields, and `feat:` is a minor bump under the org
rule even when the intent is a fix.

## Open issues

| # | Title | Blocked on |
|---|---|---|
| [#1](https://github.com/fissible/hapax/issues/1) | Vendored public fixtures and end-to-end CI corpus | `score` and `eval` are built; actionable once `cli` can drive them |
| [#3](https://github.com/fissible/hapax/issues/3) | Incremental corpus indexing and derived-artifact cache | `store` is complete; needs `cli` to drive reindexing |
| [#55](https://github.com/fissible/hapax/issues/55) | One verified read and one tree per document across an index | nothing — **held deliberately**, see below |
| [#56](https://github.com/fissible/hapax/issues/56) | One corpus, two snapshot identities | nothing; load-bearing in three places now |
| [#58](https://github.com/fissible/hapax/issues/58) | Say why nothing ships, instead of reporting a bound of zero | nothing |
| [#62](https://github.com/fissible/hapax/issues/62) | `hapax profile` reports an arbitrary reference | nothing; `score` does not inherit it |
| [#63](https://github.com/fissible/hapax/issues/63) | The distractor pool cannot get its author-clustering protection | needs a schema decision, not just a workflow one |
| [#65](https://github.com/fissible/hapax/issues/65) | The human renderer keeps growing empty members | best done once with `rewrite`'s line in view. #98 added three more |
| [#76](https://github.com/fissible/hapax/issues/76) | No durable evidence that a rewrite invocation happened | nothing |
| [#84](https://github.com/fissible/hapax/issues/84) | A corpus built from assistant transcripts is mostly the assistant's prose | nothing; the extractor is fixed, the guard is not |
| [#87](https://github.com/fissible/hapax/issues/87) | The calibrated-implies-ordered rule is enforced in Go but not in the schema | nothing |
| [#88](https://github.com/fissible/hapax/issues/88) | No fixture reaches eval's calibrated threshold-write branch | nothing |
| [#89](https://github.com/fissible/hapax/issues/89) | `Thresholds.Separated` is redundant with the boundary ordering | nothing |
| [#91](https://github.com/fissible/hapax/issues/91) | An accepted rewrite regressed tells and switched language mid-paragraph | overlaps #94 |
| [#94](https://github.com/fissible/hapax/issues/94) | A language is a register: per-language profiles | a register decision, not only a workflow one |
| [#95](https://github.com/fissible/hapax/issues/95) | A candidate returned as more than one paragraph is rejected as not-one-segment | nothing |
| [#97](https://github.com/fissible/hapax/issues/97) | Three floor consumers are unguarded: eval, execute rescoring, reference construction | nothing; #98 guarded score and workflow only |
| [#100](https://github.com/fissible/hapax/issues/100) | `rewrite` cannot read stdin or write stdout | nothing |
| [#101](https://github.com/fissible/hapax/issues/101) | `score` and `rewrite` disagree about what counts as a draft | nothing |
| [#102](https://github.com/fissible/hapax/issues/102) | A figure caption is truncated when its text lowercases to fewer bytes | nothing; found by #98's test review |
| [#4](https://github.com/fissible/hapax/issues/4) | Golden set — matched-brief triplets | needs maintainer-authored triplets |
| [#5](https://github.com/fissible/hapax/issues/5) | Author-specific orthographic profile | `profile` is built; actionable |
| [#17](https://github.com/fissible/hapax/issues/17) | Distractor sufficiency per register and per band | a user-supplied `--distractors <dir>`; #2 settled that v1 bundles none |
| [#18](https://github.com/fissible/hapax/issues/18) | Rewrite-quality figures with the contamination caveat | needs `cli` and a user-supplied distractor set |
| [#22](https://github.com/fissible/hapax/issues/22) | Editorial-normalisation stress test against ParlaMint-GB | the source is chosen (ParlaMint-GB 5.0, CC BY 4.0); needs `cli` to drive it |

**Why #17 and #18 exist separately.** Issue #2 is now closed — resolved on the
fallback, four days inside its timebox: **v1 ships no bundled distractor set**,
`--distractors <dir>` is required, and without it `eval` reports `uncalibrated`,
`score` withholds the band, and `rewrite` refuses. It was rescoped so its timebox could
govern it. Two of its acceptance criteria could not be evaluated until
components 5 and 10 existed, so the fallback could be adopted on day seven and
the issue would still have stayed open — the forcing function guaranteed
nothing. Distractor sufficiency became #17 and the rewrite-quality contamination
caveat became #18; neither blocks the licence decision, and what remains in #2
is exactly what seven days is meant to settle. (Both components are now built,
so #17 and #18 are gated only on a real corpus.)

## Issue #2 — resolved on the fallback

**Closed 2026-08-27**, four days inside its seven-day timebox, on evidence rather
than expiry. **v1 ships no bundled distractor set.** `--distractors <dir>` is
required for calibration; without it `eval` reports `uncalibrated`, `score`
emits raw distance and per-feature deltas but no band, and `rewrite` refuses.

ADR 0005 and DESIGN Section 2 both promised a bundled set and have been amended.

Nothing in the openly licensed field cleared all six requirements. The sources
with clean licences are institutional, edited, or single-register, and a figure
calibrated against them would measure era or house style rather than authorship.
That is not a failure of the search; it is the search doing its job. A false
calibration claim is worse than a documented absence.

The two investigations are preserved as **rejected-for-calibration** decisions
in #2 rather than deleted, because the reasoning is what stops them being
reopened:

- **ParlaMint-GB 5.0** — CC BY 4.0, native English, 2015-2022, ~1,951 speakers
  with `@who` labels. Rejected on requirement 3: its source, Hansard, is
  *substantially verbatim*, edited to a house style that removes repetitions and
  redundancies and corrects obvious mistakes — precisely the between-speaker
  variation stylometry measures. Retained in the backlog as an
  **editorial-normalisation stress test**, a negative control asking what the
  tool does with prose normalised toward a house style. Not calibration
  evidence, and not a published figure.
- **English Wikimedia Talk / Village Pump** — CC BY-SA 4.0, account-level
  labels, buildable as retained added spans per revision. Viable as a future
  artifact, not a six-day deliverable: copied and imported material, revert
  attribution, bot history and public-identity exposure need a measured pilot
  and legal review.

Acquisition and packaging, if a licensed source is ever adopted, are governed by
[ADR 0009](docs/adr/0009-share-alike-corpus-acquisition-and-packaging.md).

---

## Deferred slices, recorded so they are not forgotten

- **text 2b** — URL, email and file-path recognition plus terminal-punctuation
  peeling. Split out because "leaves a string still valid as that class" is only
  decidable once each class has a pinned grammar.
- **text 2c** — sentence segmentation. Needs a hand-annotated fixture and a
  published error rate; unblocks sentence-length features.
- **Contraction rate** — needs the contractible-opportunity denominator and a
  bidirectional lexicon.
- **Structural tell matchers** — triplet stacking, repeated openers, em-dash
  density. Schema admits them; loader rejects them as unimplemented.
- **Code-fence awareness** for tell suppression — the structural tree now
  exists, so this is actionable.
- **Git-date provenance** in `corpus` — a per-file `git log` shell-out is a
  performance decision worth its own slice.
- **CI guard forcing a `SetVersion` bump** when feature computation changes.
  Recorded as the only real enforcement; pinned golden values only make a change
  visible, they cannot compel the bump.

---

## Standing constraints

- The maintainer's own writing is **never** in the repository and is **never**
  required to run the test suite. A fresh clone runs everything offline with no
  credentials.
- `FISSIBLE_PAT` is deliberately unused: repository secrets are unavailable to
  pull requests from forks, and requiring one would break every outside
  contributor's CI.
- Positioning is **voice fidelity, not detector evasion**. See ADR 0008.

---

## Session handoff notes

### 2026-10-04 (#149 slice 1 — interruption cannot orphan a publication)

Branch `fix/graceful-interruption`, duet, frozen at `f8292f6`
(`.duet/interruption.{ref,sha256}`). Tests mine, implementation codex's; both agree slice 1 is
done. **#149 stays OPEN for slice 2.**

**The defect, reproduced rather than described.** The tests fail against main with the real
diagnostic: `published .../draft.md.revised but could not record publication evidence: context
canceled`. The cancelled context was forwarded straight to `RecordPublication`, making the
evidence write the one step guaranteed to fail — and the existing code handled that failure
gracefully, which is exactly why it stayed invisible. A context cancelled before the run even
started still published, so there was no admission boundary at all.

**The fix.** An admission boundary immediately before publication; once admitted, the
publication and its required evidence proceed under `context.WithoutCancel(ctx)`, confined to
one call inside a new `publishAndRecord` whose four-state result lets the caller tell
publication failure from evidence failure from completion. The loop checks cancellation before
every attempt and returns the ZERO outcome with a `context.Canceled` error — not a new terminal
(the vocabulary is closed and none of its three values means interrupted) and not a refusal
(which would claim the candidate was judged). Exit 3, with three distinct truthful diagnostics,
and `interrupted` decided ONCE before rendering.

**Two claims of mine that codex refuted.** That a signal-cancelled context alone would fix
this — it would CAUSE the bad case, since `publish` takes no context while the evidence write
uses the caller's. And that the residual exposure was only SIGKILL: `RecordPublication` can
fail normally, and a publisher can rename then fail on directory sync, after which this command
records nothing. Also that `publication-evidence-gap` covers it — it does not, it is a
migration marker and a fresh store can hit this with the flag false.

**Process.** Phase 1 took FIVE rounds; codex reproduced eighteen implementations that passed an
earlier draft. The costliest lesson: the same `--out`-only asymmetry hid three separate
mutants, so every interrupted case now runs over both destinations. The sharpest: my
`cancellingService` embedded `*rewriteService` and so inherited the NO-OP recorder, making the
rewrite-time rows observe nothing at all. And `recorded` is appended only after the context
check, so "nothing recorded" could not distinguish a wrongly-called recorder from an unasked
one — the fake now counts calls separately.

**Slice 2, scoped by codex:** wire `signal.NotifyContext` into the composition root; unregister
promptly after the first signal so a subsequent one can terminate a stuck shutdown, and clean
up on normal completion too since a defer in `main` will not run through `os.Exit`; subprocess
tests with readiness handshakes over both signals, before publication and after admission,
verifying exit 3, stderr's actual publication state, no stdout result, evidence completion and
this invocation's staging cleanup; and prove second-signal termination while documenting that
it can interrupt the orderly completion.

**Deliberately still out of scope:** hard-kill staging leftovers, failures after filesystem
publication, and durable crash recovery. Slice 2 establishes graceful signal handling without
claiming filesystem or database atomicity.

**Also this session:** v0.3.0 released; #134 closed (fixed by #139, never auto-closed); #148
filed for #143's measurement; #130 designed and ready to implement (its premise is false for
the shipped binary — no `os/signal` anywhere — so the fix is internal consistency, option C:
wrap `ctx.Err()` for classification, keep the response error as diagnostic text, do not also
match `ErrProvider`).

**Next:** #130 (designed, XS), then #149 slice 2, then #123, #122, #118.

### 2026-10-02 (#143 — the expansion ceiling)

Branch `feat/expansion-ceiling`, duet, frozen at `6d3454f`
(`.duet/expansion.{ref,sha256}`, seven files). Tests mine, implementation codex's; both agree
it is done.

**I got the central mechanism wrong first, and codex caught it.** I measured that duplicating
a paragraph changes every feature value by exactly zero and concluded `d` was length-invariant.
That measured the WRONG LAYER: `Standardize` divides by `sqrt(V + S(n))` and the sampling term
falls with length, so an unchanged rate gives a different z at a different n — which three
existing tests already pinned. Redone by reading the distances the loop actually RECORDED:

    duplicating the ORIGINAL          duplicating an IMPROVING candidate
      2.0x  1.338914001  refused        1.9x  0.686365334  accepted
      5.0x  1.553020197  refused       19.3x  1.002000883  accepted
    100.0x  1.553020197  refused      386.2x  1.002000883  accepted

**The real mechanism is SATURATION.** Expansion does move `d`, monotonically worse — but past
roughly 19x the value is constant to nine decimal places through 386x, because
`Reference.Transform` ranks against a finite reference and the insertion positions stop
moving. So the distance guard prices expansion only up to saturation and is exactly
indifferent past it, and whether an expansion is refused turns on whether the SATURATED
distance still beats the original. That is a property of the candidate, not a policy.

**The decision was the maintainer's.** Codex recommended the gate but left the multiplier
explicitly open; put to them with the measured alternatives, they chose 1.5 — the stricter of
the two I had flagged as evidence-indistinguishable. One-sided, lexical tokens, anchored on
the ORIGINAL so N passes cannot compound to 1.5^N, own rejection code, counts and bound
persisted.

**Verified end to end** through the real Runner afterwards: `lengthensOne` at 1.1x still
accepted as the suite requires; 1.9x, 19.3x and 386.2x all refused as `expanded`.

**Process.** Phase 1 took SIX rounds and codex found twenty implementations that passed an
earlier draft. The ones worth remembering: it removed `requireDispositions`-style evidence by
moving three assignments inside the gate block (early refusals then recorded 0 -> 0); it added
`break` after an expansion refusal (every over-bound fixture offered its candidate last); it
restricted the gate to candidates that already improve (an over-bound tie then reported
`not-improved`); and it left `Recorder.RecordAttempt` unwired while the columns and codec were
done, which every other test tolerated. Seven test files ended up in the freeze because the
`Attempt` field count guard cascades into six allowlists and fixtures.

**Still open, deliberately:** the multiplier's tradeoffs — rewrites lost against expansions
caught — need real provider candidates and author judgments made without showing scores.
And whether saturation is itself a scoring defect is a separate question; codex argued, and I
accept, that a continuous scorer would price expansion past 19x but still would not answer
whether replacing thirty words with three thousand is an acceptable rewrite.

**Next:** #130, #123, #122, #118, #113, #112, #110.

### 2026-10-01 (#126 — the workflow fixture cost)

Branch `perf/workflow-fixture-cost`, duet, frozen at `80db20d`
(`.duet/fixture-cache.{ref,sha256}`). Tests mine, implementation codex's; both agree done.

**The ticket's diagnosis was stale, and the measurement had to be redone.** It blames
"walks a corpus, indexes it" — already cached, `variedTemplate` is a `sync.OnceValues`. The
real per-fixture cost under `-race` is ~2.6s, of which `requireDispositions` is 2.20s (84%)
and goes unmentioned, because it runs a FULL PLAN to validate the fixture's shape. Helper
calls instrumented rather than counted by eye: targetStore 101, settledStore 6,
installRelease 114, requireDispositions 107 — the ticket says "roughly forty".

**The prize is smaller than the ticket implies, and I measured it instead of projecting.**
My first projection was 293s, wrong by 3x: 204 of 222 tests are parallel at GOMAXPROCS 10, so
summed latency is not wall time. Stubbing `requireDispositions` to a no-op measured the
ceiling at 425.3s → 338.5s. Delivered: 326.9s (my run) and 345.4s (codex's), so **80–98s,
roughly a fifth**, with a 4.3% run-to-run spread. The other ~75% is the tests themselves and
is not this issue's.

**Two beliefs I had to discard.** That `store: conflict` blocked the obvious fix — a second
`PutRelease(AdvanceHead)` returns nil, so whatever I hit in an earlier session was not this.
And that copies at different absolute paths would get different identities — `corpus.go:382`
uses `filepath.Rel`, and two copies give byte-identical profile and reference ids. That last
is the premise the whole design rests on, so it was measured rather than reasoned.

**Design, simplified against codex's recommendation on measurement.** It wanted three
templates including an unplanned one for the direct `installRelease` callers; those are 7
invocations worth 2.6s summed, so they were left untouched entirely — which satisfies its
concern (they keep exactly today's state) more simply. Two prepared templates, built once per
package run under a `TestMain`-owned directory, copied per test.

**Known limitation, agreed with codex rather than fixed:** the frozen suite verifies
`checkPreparedCopy` rejects correctly but does not detect removal of the validation CALLS
from `copyOfPreparedTemplate`; their presence was verified by source review.

**Process note.** Phase 1 took FIVE rounds and codex broke sixteen of my attempted
implementations. The one that mattered: it removed `requireDispositions` entirely and all my
tests passed, because that helper PERSISTS a draft snapshot and exemplar selection and I was
only checking the selection — whose identity derives from corpus exemplars, not the draft, so
my stated reason for the check was false. Cross-copy comparison also cannot see UNIFORM
damage, which is why the corpus is compared against an oracle built from
`writeVariedCorpusInto` directly.

**Next:** #143 (the expansion-guard decision this sprint's #136 split out), then #130, #123,
#122, #118, #113, #112, #110.

### 2026-10-01 (#136 — the script growth arm is conjunctive)

Branch `fix/script-proportional-growth`, duet, frozen at `4caef57`
(`.duet/proportional.{ref,sha256}`). Tests mine, implementation codex's; both agree it is done.

**The ticket asked one question and the answer required finding two.** #136 asked whether a
rewrite that scales a paragraph without changing its script mix should be exempt. Scoping it
showed the old arm — name a script whenever its absolute COUNT rose — was incoherent in both
directions: it refused rewrites that DILUTED a script (10 Latin + 1 Greek to 23 + 2 takes
Greek from 0.0909 to 0.0800 and was named), and it permitted a paragraph shrinking around a
script until the share reached 0.25 (90 + 10 to 45 + 10 is an 82% rise, permitted). A real
witness was already sitting in `exceeding_test.go`'s own fixtures.

**The decision was the maintainer's, and it went against codex's recommendation.** Codex
argued for RETAIN: neither rule dominates, the share arm removes a real protection, and no
representative evidence exists to price the tradeoff. It also refuted my first proposal — a
pure share arm refuses shortening the prose around an unchanged Greek quotation, which
`Exceeding` documents as tolerated. The hybrid it offered as its own point 5 is what shipped:
`(count grew AND share grew) OR share >= established`. Put to the maintainer with both
arguments; they chose the hybrid.

**Why the hybrid is safe to ship on unmeasured rates.** It only ever PERMITS more — the new
arm implies the old one and the other conditions are untouched, so no rewrite accepted today
becomes refused. Proof, plus zero newly named candidates measured over 42,806,400 pairs.

**The cost is recorded, not hidden.** Absolute growth at constant share is permitted without
bound and nothing refuses it: #143, filed. The exposure is wider rather than new — growth
below the ceiling and established scripts were already exempt.

**Still open under #136:** the rejection rate on legitimate rewrites and the escape rate on
incidents, both blocked on corpus acquisition. The corpus carries two non-Latin paragraphs of
1959, both below the ceiling, so the affected band cannot be sampled. I also corrected #136's
body: its "separation, stated correctly" section was built on the superseded 20.93% figure
that slice A replaced with 18.5841%, so 4.19x, 2.093%, 2.832% and 7.39x were all wrong.

**Process note.** Phase 1 took FIVE rounds, and three of them turned on the same defect
class: a fixture self-check that a useless fixture still satisfies. Codex broke six of my
fixtures by editing only the fixture — one passed with the candidate identical to the
original, "doubling" claim and all. Recorded in `CLAUDE.local.md`.

**Next:** #126 (`internal/workflow` fixture cost, 993 ms per build; `store: conflict` blocks
the obvious fix), then #143 if the expansion decision is wanted soon, then #130, #123, #122,
#118, #113, #112, #110.

### 2026-10-01 (#137 — epsilon is a declared tolerance)

Branch `fix/epsilon-resolution`, duet, frozen at `a8172a6` (`.duet/epsilon.{ref,sha256}`).
Tests mine, implementation codex's; both agree it is done.

**What was wrong.** `Epsilon = 1e-9` shipped claiming it stayed "below the resolution of a
score". The old argument used `2.5/((n+1)*k)` — 0.0134 at n=30, k=6 — but that approximates a
SINGLE feature's rank step and `d` is a mean over k of them, so changes cancel and the total
can be far finer. A witness through the real `Transform`/`Distance` improves by 1.31e-10 and
is rejected: 1.02e8 times finer than the refuted figure.

**What replaced it.** Epsilon's real job, which is narrower and which it does do: acceptance
is `candidate <= current - Epsilon`, so the comparison is NOT strict and at a zero tolerance a
tie would be accepted and `current` would advance without improving. The existing exact-tie row
proves that — an `Epsilon = 0.0` mutant dies on it.

**Two mutations that had been passing the whole package.** `>` -> `>=` at the boundary, and
rewriting the rule as `current-candidate < Epsilon`. Both are closed by testing the boundary at
TWO values of current, because acceptance compares against a ROUNDED threshold rather than
subtracting: at current = 1.0 the threshold is an improvement of 9.99999971718e-10, under
Epsilon and accepted. So "rejects improvements smaller than Epsilon" is false as a universal,
and the docs no longer say it.

**#137 stays OPEN deliberately** — `rewrite.go` and the frozen test both say so. The false
claim is corrected; what is still unmeasured is how often a real rewrite lands inside the
tolerance, which needs the maintainer's corpus and a provider. `EpsilonDerived = false` marks
the value as declared, following `ScriptCeilingDerived`'s precedent.

**Next:** #136 (the script ceiling's missing measurement), then #126 (`internal/workflow`
fixture cost, 993 ms per build; `store: conflict` blocks the obvious fix), then #130, #123,
#122, #118, #113, #112, #110.

### 2026-10-01 (v0.2.0 — the sprint closed)

All four slices merged: A (#138), B1 (#139), B2 (#140), C (#141). Tagged v0.2.0.

**Why 0.2.0 and not 0.1.1.** The three repairs landed as `feat:` commits, which the org rule
makes a MINOR bump, and the substance agrees: two new tables, one new column, a new value in the
refusal vocabulary consumers parse, new envelope fields, and one disclosed behaviour change.
The intent was defect repair, so 0.1.1 was the instinct; a patch would have understated what a
consumer has to handle.

**The upgrade consequence, stated in the README.** Paragraphs published by v0.1.0 are not
screened by v0.2.0. The migration cannot know what those runs published, and deriving it from
`accepted=1` would import the exact equation #134 refutes — so `index` discloses a
`publication-evidence-gap` instead. The failure direction is deliberate: fail to EXCLUDE this
tool's output rather than risk excluding the author's own prose.

**What the sprint was.** An independent codex review of everything merged after `157617c` found
three reproduced production defects in released code plus two claim defects. All five are closed.
Every defect was the same class: this tool's own output re-entering the measurement it is judged
against, by a path no single slice's review had looked at.

### Next: #137

`Epsilon = 1e-9` is declared with a rationale that is false: it says the value stays below the
score's resolution, and the score admits positive improvements about 10^8 times finer. Verified
through the real transform — a witness improving by 1.31e-10, which the rule rejects.

**The priority argument written here first was wrong, and it was mine.** It said this issue
"silently loses the user's work", ranking it above everything else on that basis. It does not: a
tolerance rejecting improvements smaller than itself is defensible design, and #137 is about the
RATIONALE, not the behaviour. Effort XS for the honesty. Whether the value should change needs a
measurement nobody has done — how often a real rewrite lands inside the tolerance — which needs
the maintainer's corpus and a provider, so the issue stays open for that after the claims are
corrected.

Then #136 (the script ceiling's missing measurement, which slice A's struck claims were standing
in for) and #126 (internal/workflow's fixture cost — now with numbers: 993 ms per build, and
sharing a corpus fails on `store: conflict` because `executingRunner` injects a constant
invocation id).

### 2026-10-01 (slices B1 and B2)

**B1 (#132 + #134) is merged** as PR #139. **B2 (#135) is PR #140**, CI green on ubuntu with
macOS running at the time of writing. Slice A is merged as #138.

B1's own handoff note was LOST in #139's merge: it conflicted with slice A's edit to this
section and the resolution took A's side. What follows restores the parts a later session needs,
because the roadmap is supposed to be readable with no prior context.

**What B1 shipped.** A published paragraph's identity is `H(admit(assembled).Raw()[leaf.Span])` —
the leaf span of the RE-ADMITTED FINAL document, which is what the plan (#111) and the corpus
screen (#109) already computed. `candidate_hash` stays the raw provider string, because the audit
record answers what was RETURNED. Both screens read
`published_paragraph(invocation_id, node_id, paragraph_hash)`, written by `cli` AFTER `Publisher`
succeeds; no foreign keys, because the next `index` re-derives every node id. Migration 12
creates it empty and records a one-row marker when the store already held accepted attempts.

**What B2 ships.** `rewrite_attempt.splice` — `''`, `intact`, `not-intact` — assigned after the
splice gate returns and before the precedence switch, so the verdict survives whichever rejection
wins. Migration 13, an ADD COLUMN rather than a sixth rebuild, measured against the driver first.
Three contradictions refused at three layers from one shared validator.

### Three things a later session will otherwise rediscover

**Two slices can claim the same migration index and neither suite can see it.** B1 and B2 both
wrote migration 12. Each was correct alone; the collision only existed once one merged, and it
surfaced on REBASE rather than in any test run. Read main's migration count at the start of a
slice rather than assuming the next index — and expect a rebase, not a test failure, to be what
tells you.

**`internal/workflow` is the expensive package and it is near a limit again.** Nearly every test
builds a corpus, indexes it and installs a release, measured at **993 ms** each, and B1 added
about twenty-five. Under `-race` on one machine the package went 300.9s to 362.8s, +20.6%, which
crossed Go's DEFAULT 10m per-package timeout on CI — the job timeout had been raised before and
`go test`'s own never had been. `ci.yml` now passes `-timeout 20m` with the job at 30m. Both
runners measure about 12m40s. That buys room; it does not change the trajectory.

**Sharing one corpus across a table's rows does not work**, which is the obvious way to cut that
cost. `executingRunner` injects a constant invocation id, so a second run against the same store
fails on `rewrite_attempt`'s primary key with `store: conflict`. Per-row invocation ids inside
frozen tests is a real change and belongs in its own slice.

### Next: slice C — #133

Publish what was scored. The splice gate publishes a paragraph it never scored: the loop scores
each candidate in isolation, and `assemble` splices the accepted text back, so what reaches the
file can differ from what the distance was measured on. C adopts the canonical paragraph form B1
established — the re-admitted assembled leaf — which is why it was sequenced after B1 rather
than in parallel.

A release should follow C, and did: v0.2.0, minor rather than patch, because the repairs added two
tables, a column, a refusal code and envelope fields, and `feat:` is a minor bump under the org
rule even when the intent is a fix.

### 2026-09-30 (codex sprint, slice A — the false claims)

Slice A is committed. It changes no behavior: seven files of comments and prose, struck
or scoped where they claimed more than the measurement.

**What was wrong, by class.**

- **The ceiling rationale's arithmetic was taken from the wrong fixture.** `rewrite.go`
  said #91's incident was 18 Han letters of 86 (20.93%). The pair `scripts_test.go`
  actually asserts is 52 Latin to 92 Latin + 21 Han — 18.5841%. The 20.93% belongs to an
  incident-DERIVED paraphrase in `exceeding_test.go`, which adds a Han letter to the
  original so #91's introduction guard is disarmed. Recomputed: 13.05x above the larger
  observed corpus use (unchanged), 3.72x below the incident (was 4.19x), tenfold both ways
  needs >= 3.831% and <= 1.858% at once (was <= 2.093%), symmetric at the geometric mean
  2.668% giving 6.96x each way (was 2.832% / 7.39x). The decision is unchanged; the numbers
  supporting it were not the ones claimed. Every fixture built on that paraphrase now says
  incident-DERIVED and names the reported pair alongside.
- **Predicate summaries that the predicate contradicts.** `Exceeding` has three
  conditions and the prose kept collapsing them into one. Struck: growth "refuses a
  crossing whether the script was present or not" (74L+4G crosses and is not named); "the
  rule also requires the count to have grown" (the takeover arm names an unchanged count);
  "the count arm keeps a SHORTENING rewrite admissible" (80L+20G -> 30L+15G names Greek on
  a count that FELL); `Overgrown` names scripts "whose count grew".
- **Claims true only at the shipped parameter ordering.** "A rewrite may never climb to
  established" holds only while ceiling < established — at established = ceiling = 0.25,
  80L+20G -> 60L+20G lands on 0.2500 and is admitted. Same shape in
  `execution_gate_internal_test.go`: "anything over the ceiling is refused long before 25%"
  is false on that test's own fixtures, which admit Han at 10% against the banked anchor.
- **Fixture comments generalized past their fixture.** "A paragraph mixing scripts in the
  band cannot grow either of them" (60L+20G+20H -> 960L+21G+21H grows both and names
  neither); "a longer rewrite is admissible" (96L+4G -> 96+6 names Greek at 0.0588); "a
  bilingual paragraph may be rewritten in either of its languages"; "no order statistic
  sees magnitude" (an ordered count vector does); "two percent becomes the paragraph" (1 of
  22 is 4.55%); "the low end sits a thousandth above the ceiling" (0.0455 is 0.0045 BELOW
  0.05); and the monoscript row credited to "the two proportional designs", which is the
  one case the paragraph-share bound admits.
- **A coverage claim with no test behind it.** `growth_test.go` said "the tests below hold
  both" for two non-containment examples; those tests drive fake verdicts. Added
  `TestNeitherGuardContainsTheOtherWithProductionAnchors` in `internal/text`, which asserts
  both directions against the real `ScriptSet` with production's anchors — including the
  reverse witness that was missing entirely (growth refusing what introduction permits).
- **The band floor's impossibility was a property of the chosen rule.** `3/c` inverted
  gives 60 and 30 clusters; the exact one-sided binomial bound on independent observations
  with zero errors, 1 - 0.05^(1/n), reaches 9.814% at n = 29 and 4.951% at n = 59 — one
  cluster below each minimum. It is not available here (these are clusters of dependent
  paragraphs, which is why the bootstrap exists), but "there is no sample size below this"
  was false as stated.

**The generator, not the instances.** Codex's diagnosis, now in `CLAUDE.local.md`: these
headers are written as ARGUMENTS for why the chosen design had to win, and an argument
wants a strong conclusion, so "this alternative failed this example" becomes "this class
cannot work". Corrections then APPEND an exception rather than replacing the block, so a
block accumulates a slogan, a predicate, a history and review qualifications — and the
predicate can be correct while the slogan is false. The remedy is to write the block as a
record: Contract / Evidence / Consequence / Decision / Unresolved, used where they earn
their place, writing from evidence toward a bounded claim, one authoritative home per
claim.

**Nine review rounds, and the last one says why.** Findings fell 7 -> 3 -> 3 -> 2 -> 1 but
never reached zero, and codex's answer to the direct question was that the process had
become churn and the contradictions should have been consolidated earlier. Worth knowing
before the next prose slice: re-reading the same paragraphs finds one more thing every
time, and that is not convergence.

**Scope grew from four files to seven, deliberately.** Grepping for other homes of the
incident numbers found the same false labels in `execution_gate_internal_test.go`. Fixing
them pulled that file into review, which produced three more rounds. Left alone: the ~60
"an earlier version..." notes across 32 other files — codex's position is that the note
itself is not the problem, only a false opening claim with its correction appended.

### Next, as slice A left it: slice B

Superseded by the 2026-10-01 note above, which records what B actually became. Kept only so
this section is not read as current.

### 2026-09-29 (v0.1.0, and closing the self-contamination loop)

Merged #124, #127, #129, #131. Closed #109, #111, #117, #125. Filed #122, #123, #125,
#126, #128, #130. Tagged v0.1.0.

**Three slices, one story.** This tool's own prose was re-entering its measurement of the
author by three separate paths, and all three are now closed.

- **#117** — the tells gate counts only `derived` rules and none of the 22 shipped rules
  is derived, so it accepted everything. Validation was attempted first, on instruction,
  and the corpus cannot support it: `not-just-but` fires in 11.36% of distractors, 97 of
  854 documents of other people's human writing. So the interim is disclosure —
  `tells_inactive_reason` in every rewrite result, and `Runner.Tells` makes the rule set a
  seam so the register bind is observable at all.
- **#111** — every guard anchors on the paragraph the run started from, and `--in-place`
  makes that the previous run's output. Refused by content hash at plan time. The measured
  ladder was 4.55% Han to 24.00% in two runs, each step admissible.
- **#109** — `index` screens the corpus for paragraphs this tool published. Of the five
  files the issue named, one matched — and it held 63 of the corpus's 64 Han letters, all
  in the one paragraph the screen catches.

**#109's first design was wrong and review killed it.** It refused to publish into a
corpus root; four shipped smoke tests assert the rewrite SHOULD land there, it was a no-op
for `--store` users, and it repaired nothing already present. The exclusion route only
became the cheaper fix once #111 shipped `ProducedByRewrite`.

**What the release number means.** v0.1.0 says the shape has settled. It is below one
because the five qualification checks are still `not-performed` and the tells gate is
inert — both now stated in the README rather than implied.

**Upgrade consequence, deliberately taken.** The tool-output check version is a snapshot
identity input, so the first `index` after this re-IDs every snapshot, every profile and
every `profile_head`; `eval_result.profile_id` cascades, so a calibration built on the old
profile is lost when it is pruned. Every other `*-version` key in that map charges the
same price.

**Next.** #112, #113, #118 and #110 are the substantive bugs left; #122, #123, #126, #128
and #130 are housekeeping these slices generated. #84 is the interesting one — "a corpus
built from assistant transcripts is mostly the assistant's prose" is the same
contamination a level up, and `ProducedByRewrite` plus the tool-output screen are the
machinery to measure it.

### 2026-09-23 (the paragraph floor, and what twenty-five review rounds bought)

Merged #85, #90, #96, #99, #103. Closed #64, #81, #83, #92, #93, #98. Filed #102.

**The floor.** A one-token paragraph — "Yes." — scored 1.5316 against a real 50-document
profile, the HIGHEST distance in its document, so `rewrite` offered it as the most
promising target. `MinParagraphLexicalTokens` was 1. It is now 10, recorded as a declared
interim bound with a statable rationale rather than a derived one: for a fixed denominator
N >= 10 a one-count change moves a rate by at most 0.1. That is a chosen resolution
constraint and guarantees nothing for any feature, which is why `ParagraphFloorDerived`
stays false. Section 2's per-tier measurement is still not done.

**Raising it broke the thing it fixed.** At a floor of one a scored index was the paragraph
a reader counted in their file. At ten it is not, and `score` reported only filtered indices
and a count. #98 is the bridge back: every segment carries its source span, every skipped
paragraph is named.

**What the review rounds actually found.** Twenty-five, every finding proven by applying the
mutation and showing the suite stayed green. The classes that would have shipped broken:
spans over a normalized copy rather than raw file bytes (NFC, CRLF) and over unrebased
coordinates (BOM, front matter, leading blank lines, no trailing newline); wrapped
paragraphs truncated at their first newline, which is most real prose; the entire default
human-output path unguarded, because every assertion had been written against `--json`;
admitted leaves that are not paragraphs (definition descriptions, figure captions,
referenced footnotes); numbers counted as lexical tokens; excisions deciding admission
rather than only the count; absent JSON fields decoding to a plausible zero.

**The reframing is the transferable part.** "Which shapes are untested" ran out after four
rounds. "Which transformations sit between the bytes on disk and a reported span" found six
more. "Where do a measurement rule and a span rule disagree" found the rest. Each question
found things the previous one could not, and the last one yielded the counting contract —
tokens that are lexical, wholly contained, overlapping no excision — which is now in the
test file with a map of which fixture covers which condition.

**A parser bug fell out of it.** #102: `htmlLeafNodes` finds `<figcaption>` boundaries by
searching a `bytes.ToLower` copy and applies those offsets to the original bytes.
`bytes.ToLower` is not length-preserving — U+0130 is two bytes and lowercases to one — so
the caption is truncated, a word straddles the span end, containment drops it, and the
caption measures one lexical token instead of two. It changes measurements, not only
reporting. Found while answering whether the containment condition was reachable at all.

**My own failures this session, named.** Three rounds running, the defects were in my PROSE
rather than my assertions: a comment claiming a fixture crossed the floor when nothing did,
a coverage map crediting a fixture with a guard it did not provide, a reproduction citing an
offset its own input does not produce. A weak test is eventually caught by a mutation; a
confident comment never is. Also: a scripted `str.replace` whose anchor stopped matching
silently did nothing, and the `crlf` subtest it was meant to create re-ran the LF case under
a different name for a full round. Both are in CLAUDE.local.md.

**And a tooling misdiagnosis worth not repeating.** `agentrun start` forks and returns; I
also backgrounded it through the harness, so every "completed" notification described the
launcher rather than the run. I narrated that as a harness quirk for twenty turns instead of
reading the thirty-line script, and hand-rolled polling loops around a tool that ships a
`wait` subcommand.

### What the duet process caught this session, and what it cost

Four slices, four merged PRs, and it is worth recording which findings the process produced
rather than the code, because that is the argument for keeping it.

**Things nothing else would have found.** #70's *consensus gate* found a resource leak — two
temporary directories per fresh store open, 21,598 accumulated over one session — after the
freeze verified, the suite was green, three mutations passed and the CI gain was measured. I
had concluded it was done. B2a's boundary reappeared one level down in the implementation with
every test green. Two defects in B1 were found at design time, before a line was written:
attempts colliding across paragraphs, and a draft that had never been in the store at all.

**A shipped bug a fixture found.** #69: `joinContainers` joins with a pipe and the column's
grammar admitted only lower case and hyphen, so every leaf with two or more containers was
unstorable — every paragraph inside a list, a quote, a table cell or a footnote. `hapax index`
failed on most real Markdown. Twelve slices missed it because every corpus fixture here is
plain top-level paragraphs.

**My own recurring failures, named so they stop recurring.** Assertions a correct
implementation satisfies that an incorrect one also satisfies — ten rounds of #70 were one
habit wearing different clothes. Bulk textual refactors, fixed by making them compiler-checked.
Comments that overstate what a test proves, which cost a channel in B2a. Verifying with
`go test -c`, which never runs the assertion. And three performance hypotheses wrong before
measurement, one of which nearly reopened a settled durability decision on a number that did
not bear on it.

**What it cost.** Ten review rounds on #70, six on B2a, eight on B1. Most rounds found
something real; the marginal ones were about how a broken implementation reports itself rather
than whether it is caught. The stopping rule that works: freeze when what remains needs the
implementer to *deliberately construct* an escape rather than make a plausible mistake.

### 2026-08-31 (B2a: the credential boundary, and what a boundary actually is)

`--local-only` is the one guarantee DESIGN calls tested rather than documented, and until this
slice nothing exercised it: no command had ever constructed a provider. PR #74.

**Two designs were rejected before the one that shipped, and the reasons are the useful part.**
Two unexported types in `workflow`, one holding a credential factory and one not, is not a
boundary — both can name the type and both can call a constructor a later edit widens.
Separate `ollama`/`anthropic` packages, the local one unable to *name* a credential, is not one
either. I checked that premise rather than asserting it, and it is false: a named function type
accepts a matching literal, so a package can populate a credential-typed field without
importing the package that defines it. The import guard would have passed while the boundary
was absent.

What holds is the signature. `NewLocal(LocalConfig, DialFunc, *x509.CertPool)` has nowhere to
put a credential, at every call site, now and after any later edit. **Two of the four verifying
mutations are build failures rather than test failures**, which is the distinction the slice
was for: a test says an implementation does not do the wrong thing, a compile error says none
can.

**The boundary did not propagate inward on its own.** The first implementation passed
`CloudDeps{Dial, RootCAs}` from the local path into the shared constructor — a struct with a
credential field, left nil. The rejected design, reappearing one level down, where the
guarantee degrades from *cannot* to *happens not to*. Every test was green and would have
stayed green if a later edit set it. Found by reading the implementation.

**Six review rounds, and every finding was a channel rather than a behaviour.** A type hidden
behind an allowed field name; what the local path puts on the wire; *where* it sends it, since
a credential travels as well in `?api_key=` as in a header; a package-level setter; an exported
method, declared or promoted from an embedding, reachable structurally through the returned
interface. Auditing what tests assert does not find these, because assertions describe
behaviour and capabilities describe access.

**Three properties nearly went missing relocating tests**, which is the risk that refactor
always had: the nil-dialer test stopped supplying a nil dialer, the configured-model test
asserted a model it no longer set — an implementation hard-coding that string would have passed
— and an obsolete default-config test survived against fields that no longer exist. All three
were found in review, none by me. The 48 call sites themselves were converted compiler-checked
rather than textually, so the wrong arm failed to build.

**And a failure in my own method.** I verified five rounds with `go test -c`, which compiles
without running, so a wrong expected value was invisible to my entire verification step. A
sorting bug survived until the reviewer ran the tests. Verification now runs what can be run.

### 2026-08-31 (B2 split into three, on the publication boundary)

`rewrite` was one issue, then two, then three, and each cut was the reviewer's:

- **#73 B2a** — provider construction and the credential boundary. Merged.
- **#75 B2b-1** — execution: freshness, the loop, assembled bytes. **It cannot write anywhere.**
- **#68 B2b-2** — the command, and the only slice with filesystem publication authority.

The last cut is the best of the three. Everything in B2b-1 is testable without a destination
existing, so the slice that can overwrite a user's file is small and reviewed alone.

Three decisions from those rounds that shaped the remaining work. `rewrite.Outcome` gains its
own closed **terminal-reason** enum rather than extending `RejectionCode` — three situations are
currently indistinguishable (empty provider response, attempts exhausted, loop never entered),
and per-attempt reasons and whole-loop outcomes are different vocabularies. The freshness
refusal is **`stale-draft`** and deliberately not `stale-exemplars`: the exemplars are fine, the
draft moved. And exit 1 is a *completed decision that did not improve* — including an empty
response, exhaustion and ordinary gate rejections — while exit 3 is a component error or a
planned target now unscoreable, because that violates a precondition rather than being a result.

**Next: #75, then #68.** Both issue bodies carry the settled design in full and are the spec.

### 2026-08-31 (#70: a fresh store stops replaying the migration chain)

Done before B2 because B2 adds more store-backed tests to a critical path B1 had just grown
by 73%.

**A brand-new database was spending 92% of its open replaying the chain** — 11.7ms of 12.7ms,
against 0.97ms to reopen — on migrations whose later steps exist to move data that is not
there. A fresh store is now a copy of a template the chain itself produced, published by
staging beside the destination and linking with a primitive that fails rather than overwrites.

**Copying rather than replaying was the reviewer's call and it was right.** I had proposed
replaying `sqlite_master` into a fresh file; that claims a projection of the chain's output is
faithful, and after a table rebuild the catalogue holds `CREATE` text no migration contains.
Copying claims nothing, because the bytes are the output — and it carries forward for free
anything a future migration does that a schema comparison would miss.

**It also closes an ownership race rather than inheriting one.** `open` has always stat-ed a
path before opening it, so two processes could create the same absent path at once. Staged
no-clobber publication means the loser opens the winner's database instead of replacing it.

| | before | after |
|---|---|---|
| fresh `store.Open` | 11.9ms | 8.98ms |
| `internal/store` under race | 242s | **92s** |
| whole suite under race | 4m09s | 3m13s |

**The two numbers disagree, and the reason is worth keeping.** The staging fsync is 5.15ms of
an 8.98ms open — two thirds of the per-open gain — but it is I/O wait, which `-race` does not
amplify, while the chain's CPU is amplified about twelvefold. So the sync costs almost nothing
where CI cost is decided. I nearly reopened the durability decision on the strength of the 25%
per-open figure, which does not bear on the question at all.

**Ten review rounds, and every finding was one habit:** an assertion a correct implementation
satisfies, where an incorrect one satisfies it too. Rows counted not compared; links counted
not bound to a destination; a template named not read; a sync logged not identified; a file
matched by pathname not inode; a reader opened not drained; a build counted sequentially not
concurrently; a barrier inside the build rather than at the decision that leads to one. The
seams now observe effects — bytes moving through a reader, inodes compared by `SameFile`,
migrations actually executed — and every remaining claim is cross-checked against one.

**The critical path has moved to `internal/workflow`, 186s.** That is B1's +57s, and it is
test count rather than migration cost — twenty-one tests that each build a real store. A
different problem, and not one to solve by trimming tests that each found something.

### 2026-08-31 (B1: everything `rewrite` decides before it spends anything)

`rewrite` split into #67 and #68. B1 ships no command — `Plan` is on the runner and
deliberately not on `Service` — because a command that reports targets it cannot rewrite
promises an action it cannot perform.

**Two defects were found at design time rather than by running the binary,** which is a first
for this project. `rewrite.Loop.Rewrite` is per-segment and numbers attempts from zero within
each, so one invocation over two paragraphs wrote one `rewrite_attempt` key twice; every test
that reached that table used a single node, so the whole suite passed. And a draft had never
been in the store at all, so `PutRewriteAttempt` would have refused every attempt as an
invalid artifact — after the provider had been paid. DESIGN anticipated the second and nothing
implemented it.

**A third was found by a test fixture, and it had shipped: #69.** `joinContainers` joins with
a pipe; the column's grammar admitted only lower case and hyphen. Every leaf with two or more
containers was unstorable — every paragraph inside a list, a quote, a table cell, a footnote
or a definition list — so `hapax index` failed on most real Markdown with a constraint error
rather than a refusal. Twelve slices missed it because every corpus fixture in this repository
is plain top-level paragraphs: one container, no separator. The fixture that caught it was
added at the reviewer's insistence, on the grounds that inline code alone "can pass with a
syntax-specific shortcut".

**Eight review rounds before the freeze, and the last four each found a hole in a fix for the
previous one.** Counting rows is not "nothing was written" — an implementation that moves a
head leaves every count where it was, so the census hashes each table's contents. Node/span
consistency is not identity — a same-shaped snapshot from a previous run satisfied it. The
pattern is mine: I closed the instance the reviewer named instead of the class. Generalising
first would have cost fewer rounds.

**Both frozen-test amendments were my errors.** I froze `vocabulary_test.go`, which describes
the schema this slice changes — the same category as `allowlist_test.go`, which I had
deliberately excluded for exactly that reason. And I wrote a test asserting that a profile
built directly and the same profile as indexed share an identity, which is #56 and is open.
Each was agreed before it was made, committed on its own, and the freeze renewed, so the diff
between consecutive freeze commits is exactly the amendment. The implementer stopped and
reported both times rather than editing a frozen file, which is the whole argument for the
process.

**CI cost is real and filed rather than hidden: #70.** `internal/store` under race goes 140s
to 242s, +102s on the critical path, because two rebuild migrations run on every `store.Open`
including a brand-new empty database. That is #61's named follow-up — materialising a fresh
store's head schema, derived from the chain rather than hand-written. Not done here; B1
already carries two migrations, one of them unplanned.

**A measurement that corrected six rounds of my own worry.** `requireBoundariesProduce`, which
I flagged repeatedly as a CI risk, costs nothing: three fixtures and twelve score calls run in
0.43s. B1's +57s on `internal/workflow` is 21 tests at ~0.19s each amplified about 12x by the
race detector, with no hot spot. Third time this session a performance hypothesis of mine was
wrong before measurement.

**Next: B2, #68.** The provider seam with type-separated local and cloud factories, the loop,
assembly, the atomic destination write, `--local-endpoint` so the smoke test can drive the
real binary against a loopback server, and #65's human-line builder. Three things B1 leaves
pinned only by agreement and not by test, all recorded on #68: `Selection` is populated but
unasserted, `plan_state` and `state` must be separate envelope members, and an in-range
paragraph with excisions stays in-range.

### 2026-08-31 (ingest, and the CLI down to one command)

Five slices merged: #54 `ingest` and the four gaps under it, #57 `index` and
`profile`, #59 `eval`, #61 the CI-time work, #66 `score`. **Only `rewrite`
remains.** Every one of them was `duet`, and the pattern that decided this run
is worth stating plainly:

**Almost every defect that mattered was found by running the binary, not by the
suite.** Twenty distractors collapsing to one cluster behind a perfect AUC of
1.000; `hapax eval` unable to discover its own store; a deviation-internal error
escaping as an operational failure; `Document.valid` rejecting a payload its own
workflow produced; `hapax score` looking up the empty register. Three of those
live in the gap between a fake service and the real composition root — a gap
every `internal/cli` test sits on one side of. `internal/cli/smoke_test.go`
closes it: a real corpus, a real database, the real binary, run from a working
directory rather than handed `--store`, because passing the path everywhere is
exactly how the missing discovery went unnoticed.

**The arithmetic nobody had written down.** A shippable release needs sixty
held-out author documents and thirty distractors — `ceil(3/target)` clusters per
band — which is a corpus of about seven hundred. DESIGN stated ⌈1/p⌉, twenty and
ten, but that is the minimum number of *distances* for a quantile to exist, not
the *clusters* the bootstrap needs. Both are real; only one was documented. This
is why no test asserts a shippable release end to end and why
`internal/eval/evaltest` exists.

**Two constraints met in letter.** A2b's `deviation.Standardize` was asked to
take `profile.Fitted` and became generic over `profile.Fitted | *profile.Profile`
— every test passed, because every test passed a `Fitted`. Same shape as the
reflection that got around the ingest seam in #53. Closed in A2c with a guard
that reads the *signature*, since compilation cannot catch a union coming back.
And an implementation raised the default paragraph floor from 1 to 3 so one test
would pass — a product-wide change to what counts as a paragraph. Reverted;
exactly one test failed, which is the definition of special-casing a fixture.

**My own recurring failure was mechanical.** Five or six bulk regex edits across
many call sites silently matched nothing or split an argument containing a
comma, and codex stopped on each rather than working around it. Two habits fixed
it: verify every edit landed before moving on, and sweep the whole repository for
call sites rather than discovering them one package per round trip.

**#55 is held deliberately.** Its correctness rationale was spent when A2a closed
the floor divergence, and doing it would make a real cross-check tautological:
`profile.Build` and `ingest` currently count the admitted population
independently, and the floor test compares one against the other.

**Next: B, `rewrite`.** It is the only command that touches a provider, so it is
the first to exercise `--local-only`, the credential factory and the refusal
`local-only-forbids-provider` end to end. #65 should be done with its rendering
in view rather than after it.

### 2026-08-30 (store slice 2b — component 12 complete)

Six design rounds before a test was written, then eleven on the tests. The
design rounds paid for themselves twice over:

- **Rehydration hashed the wrong bytes.** DESIGN said "hashes what it read".
  `corpus` stores a hash of `text.Admit(raw).Raw()`, and `Admit` strips a
  leading UTF-8 BOM — so for any BOM-carrying file the stored hash covers three
  fewer bytes than the file and every stored offset is shifted by three. An
  implementation following DESIGN literally would have reported
  `content-changed` for a document nobody touched.
- **`span-invalid` was unreachable** and is deleted. A matching admitted-byte
  hash means the buffer is byte-for-byte the one indexed, so a range that fitted
  then still fits. The only way to reach it was a stored span inconsistent with
  its own document, which is `ErrCorrupt`. The vocabulary is four.
- **`Prune`'s edge list would have destroyed audit evidence.** `rewrite_attempt`
  cascades on its node, and a rewrite operates on draft nodes that need not
  belong to the profile's snapshot — so a snapshot reachable from no root would
  be deleted and the cascade would take the audit record. The edge is now stated
  ON THE NODE so it holds however the node was reached.

**The test rounds were mostly about my own cheats**, and the pattern is worth
keeping: every structural harness got verified by mutation before it was
frozen. The row-fault driver was proved to land mid-stream rather than at EOF;
the commit fault was proved to roll back; the fault aim was anchored on table
names after a fragment-of-SQL aim turned out to be dodgeable by renaming a
query; and the whole thing rests on a checked premise — no views — rather than
an assumed one.

**Two defects in already-merged code were found by aiming at them.** `vectorFrom`
reported an interrupted row read as `ErrCorrupt`, telling a user their evidence
was damaged when a read was merely interrupted. And slice 2a's known
`rows.Err()` gap is now closed under test rather than on argument.

**Carried forward, none blocking:** `Prune`'s fixpoint re-scans the whole UNION
until it adds nothing, which is the thing to revisit if it ever gets slow; the
`Nodes`/`Documents` counters are expressed via unreachable snapshots and are
equivalent to document-reachability only under the general closure, which a
reader has to work out; and nothing tests WAL/journal sidecars, which this slice
neither promises nor changes.

**Next: `cli`** (component 13), and nothing blocks it. It is the composition
root, and several obligations other components deliberately pushed outward land
there together: reading `HAPAX_LOCAL_ONLY`, constructing the credential factory
only on the cloud path, choosing which profile is current (since `Prune` takes
roots as arguments and refuses to decide), and the "no LLM, no network"
guarantee for `score` and `tells` — testable the way `llm`'s was, with a dial
function that fails the test if called.

### 2026-08-30 (preserve, assemble, select, llm, the audit fix, store slice 1)

**Merged since the last note:** `preserve` (#34), `assemble` (#37), `select`
(#39, package `exemplar`), `llm` (#41), the audit-record privacy fix (#43), and
`store` slice 1 (#45). Components 0–11 are complete; 12 is half.

**Next, in order:**

1. **`store` slice 2b** (issue #44) — rehydration, the unavailability
   marking, and `Prune`. Slice 2a shipped the artifact codecs, so every table
   now has typed operations and the graph they hang from is closed. What
   remains is the part that touches the user's files: open once, hash what was
   read, then slice; the closed outcome vocabulary `ok` / `missing` /
   `unreadable` / `content-changed` — four, not five; `span-invalid` was
   unreachable and is gone — with a malformed *stored* reference being
   `ErrCorrupt` rather than an outcome; `unavailable_at` set on
   the first `missing` or `unreadable` and cleared on the first `ok`; and
   `Prune` over the roots DESIGN declares. `Prune`'s tests were drafted during
   slice 1 and deliberately **not** kept: the API moved underneath them, and
   the six design rounds behind `Prune` are recorded in DESIGN, which is the
   part worth having. Write them fresh from the declared roots and edges.
2. **`cli`** (component 13) — the composition root. Exit codes, mode resolution
   and the command surface are settled in DESIGN; what remains is wiring and the
   output schema. Several obligations other components deliberately pushed
   outward land here together: reading `HAPAX_LOCAL_ONLY`, constructing the
   credential factory only on the cloud path, choosing which profile is current
   (since `Prune` takes roots as arguments and refuses to decide), and the
   `no LLM, no network` guarantee for `score` and `tells` — testable the way
   `llm`'s was, with a dial function that fails the test if called.

**The design round is now the highest-value part of the process.** Reviewing a
design before writing any test has caught, across these slices: `select`
reusing a distance that measures the wrong thing; the local-only guarantee
contradicting itself across five documents; `Prune` traversing away from what
it meant to keep; and a prose leak that had been sitting in merged code for two
slices. None of those would have been found by testing the thing I was about to
build.

**Three defects were found by guards written for something else.** `ciconfig`'s
Go-version check caught `go get` bumping `go.mod` past what CI installs; the
race detector caught `store` mutating its caller's slice; CI's `go mod tidy`
check caught thirty missing `go.sum` entries. Keep writing that kind of test.

**Where a claim cannot be made mechanical, narrow it rather than implying it.**
`llm`'s AST guard is a structural backstop and says so; `store`'s foreign-key
enforcement is declared but not exercised in slice 1 and says so; the process
barrier forces contention without proving all eight writers blocked and says
so. Codex accepted all three and overruled a fourth — migration atomicity —
correctly, because an *internal* seam is not the public test-only API worth
refusing.

### 2026-08-29 (rewrite)

**Completed: the monotonic acceptance loop** (PR #33, all three checks green).
The last component the release gates existed to protect.

**Epsilon was the wrong shape.** An absolute value compares a constant against a
quantity whose resolution moves with the corpus: `d`'s finest expressible change
is about 2.5/((n+1)·k), so 0.01 accepts a single-rank improvement at a reference
of thirty and rejects the identical improvement past about seventy — the tool
growing *less* willing to improve as its evidence improved. It is a tolerance;
churn is bounded by the cap, which counts attempts rather than acceptances.

**Reviewing the design before writing tests paid for itself.** Four defects
found with no code in existence: the provider contract had deleted `Selector`,
reassembly had no owner, the audit record conflicted with the store's privacy
invariant, and the cap's obvious reading did not terminate. Do this again.

**Process notes, and this slice has the sharpest ones yet.**

The fencing assertions took four review rounds because each of my fixes was
**satisfiable without doing the thing it named**: a prefix that appeared
anywhere, a marker that appeared anywhere, a blank line satisfied by another
line's prefix, a duplicate hidden by `ReplaceAll`. When an assertion is about a
structural property, ask what else could make it true.

**Two corrections went in opposite directions, and both are instructive.**
Codex reported a compile-blocking redeclaration; I showed the cited line was a
comment and that the package compiles cleanly under a signature stub, and it
withdrew — its own `go test` had failed at "no non-test Go files" before
type-checking anything. Then it reversed its own agreement that per-attempt
exemplar selection was fine, and **it was right**: ADR 0004 settles it in one
sentence and I had argued from ADR 0007's silence. *A claim about what the
design permits is worth exactly as much as the reading behind it, and "the ADR I
checked does not say" is not "no ADR says".*

**Three defects arrived at the consensus gate**, all real, all amended in the
tests first rather than fixed silently: zero exemplars was an accepted
configuration and `DefaultOptions` shipped it; exemplars were selected per
attempt; and the request handed providers the raw exemplars alongside the
assembled prompt, which a test of mine had blessed on a convenience argument
against a safety property.

**A standing rule earned twice now:** an assertion that can be satisfied by
something other than the behaviour is not an assertion. And verify an edit
landed — two of mine silently did not apply, and one left a reference to a field
the same edit removed.

### 2026-08-29 (score)

**Completed: `score`** (PR #32, all three checks green). Per paragraph: a
calibrated band, the distance behind it, per-feature deltas with direction, or
insufficient evidence.

**Building it found two defects underneath, and both are the interesting part.**

*A draft belongs to no split.* Only train, calibrate and test were nameable, so
a draft had to claim one — and the only survivable lie is `test`, the split both
release gates draw their evidence from. `corpus.Draft` means scored, never
fitted, never evidence. The vocabulary got **stricter**, not looser.

*A reference could not be stored.* Its distributions were unexported, so a
restored reference held nothing and `Transform` reported `reference-too-small`
for every feature. `score` is the first consumer that loads a reference rather
than building one. The failure shape is the lesson: not a crash, not a corrupt
artifact, but **every paragraph reporting insufficient evidence** — a legitimate
verdict, indistinguishable from a real one. Second artifact in this design found
unable to survive storage, both in already-merged packages.

**Standing rule from that:** when adding an artifact type, round-trip it through
JSON in a test and compare behaviour before and after. Do not assume.

**Next: `rewrite`** — the last component, and the one every gate exists to
protect. ADR 0006 is unusually complete for it, so read that first: `current`
begins as the input; a candidate is accepted iff `d(candidate) <= d(current) - e`
AND `preserve` passes AND `tells(candidate)` is no worse as a
severity-lexicographic vector; ties inside epsilon are rejections; passes are
capped; and if `d` is unavailable on either side the segment is passed through
untouched.

What does not exist yet:

- **`preserve`** — deterministic: numbers, named entities, negations, URLs and
  quoted strings must survive an edit. Nothing implements it.
- **The `tells` vector comparison.** `tells` exists but ADR 0006's gate compares
  a severity-lexicographic vector of DERIVED findings only, from the same
  rule-set digest with suppression disabled on both sides. Note ADR 0006 already
  admits this gate is **inert** while every shipped rule is unvalidated — state
  that plainly rather than implying it works.
- **`epsilon` and the pass cap have no declared values.** Both are judgements
  like the AUC floor, not derivations. Settle them before writing tests.
- **The LLM boundary.** `rewrite` is the only component that touches one. The
  candidate generator should be an interface with a deterministic fake in tests,
  or the suite cannot be frozen at all.

Comparability is already handled: a segment carries its `deviation.Distance`,
which carries the contributing feature set, so `rewrite` can refuse to compare
two distances built on different features rather than accepting a rewrite that
only moved the denominator.

**Carried forward, agreed with codex rather than fixed:** nothing exercises a
draft that `text.Admit` refuses. The behaviour propagates correctly; the
assertion is missing. Worth adding when `score` is next touched, not worth
reopening a freeze for.

**Process notes.** Codex stopped rather than editing frozen tests for the sixth
time and was right again — both were my fixture bugs, one of them a literal ID
where the real artifact is content-addressed.

Two failures of my own worth naming, both caught from outside: an edit that
**silently did not apply** because an earlier regex had already changed the text
it matched on, leaving a test whose name no longer described it; and duplicated
comment sentences left by another. Verify replacements landed rather than
trusting the tool reported success.

And codex caught that my first `score` API took its own paragraph floor — with a
test of mine explicitly scoring at 500 against a profile fitted at 5. That is
precisely the error the shared admission path exists to prevent, blessed by the
suite meant to protect it. The parameter is gone.

### 2026-08-29 (the discrimination gate)

**Completed: ADR 0005's third and last release gate** (PR #31, all three checks
green). **All three gates are now done**, which was the standing condition on
emitting a score at all. `score` and `rewrite` are buildable.

**Three omissions filled, one of them a trap.** "A predeclared minimum AUC"
declared no minimum, no orientation and no tie rule. Orientation is the
dangerous one: `d` is a *distance*, so discrimination is
`P(d_author < d_distractor)`, and an implementation reaching for the
conventional "probability the positive scores higher" reports `1 - AUC` — 0.15
for a profile that separates perfectly. Low enough to read as failure, high
enough not to read as a bug, and no arithmetic objects. Ties count as a half.

**The floor is 0.80 and is labelled a judgement**, not a derivation — the first
declared value in this design that nothing implies. What informs it: the output
drives edits to the user's own writing, and ADR 0006's loop accepts a rewrite
whenever `d` improves, so a barely-discriminating `d` turns that loop into
noise-driven vandalism. **v1's six Tier A features may well not clear it**, and
that is the designed behaviour rather than a number to relax later.

**The band floor's degeneracy, mirrored.** Perfect separation resamples to 1.0
every time, so the bound is capped at `1 - 3/c` over the smaller class. That
implies fifteen clusters per class, less demanding than the band gate's thirty
and sixty, so the band gate binds first.

**Next: `score`**, then `rewrite`. Both were blocked on the gates and are not any
more.

`score` per DESIGN's component table: Tier A at paragraph scale, Tier B over
rolling windows, emitting a calibrated band plus per-feature deltas plus
direction, or insufficient evidence — and requiring an explicit `--profile`.
Most of that now exists; what does not:

- **Tier B has no features and no rolling-window mechanism.** ADR 0003 puts the
  function-word distribution, hapax ratio and sentence-opener distribution
  there, all needing several hundred tokens. The tier machinery is built and
  derives its tier set from the manifest, so adding them is additive — but the
  windowing is not built at all.
- **Per-feature deltas and direction** are the reporting half of `score` and
  have no artifact yet. The signed deviation exists and was deliberately kept
  signed for exactly this.
- **`score` consumes a `Release`**, which is the type that composes both gates.
  Do not let it reach for `Calibration.Band` or `Thresholds.Band` instead.

Open question worth settling before `score`: **what a report looks like when the
profile is uncalibrated.** DESIGN says raw distance and per-feature deltas are
still emitted with no band, which means the report has two shapes and the
difference must be legible rather than an absent field.

**Deferred with a reason, not a TODO:** `auc()` is O(n_a x n_d) inside the
resample loop — nothing on the fixtures, four billion comparisons on a corpus of
2000 author and 1000 distractor segments. The rank-based Mann-Whitney form is
O(n log n) and the frozen exact values would catch any tie-handling mistake in
the substitution. Codex and I agreed to defer until a real corpus establishes
the cost. Do it when someone measures, not before.

**Process notes.** Six review rounds. Two worth carrying:

I **pushed back on a finding and was accepted** — codex wanted
`Calibration.Band` unexported to close a bypass; I argued that `Thresholds.Band`
already sets the precedent for public lower-level classification and that the
real protection is type-level, since `score` and `rewrite` are handed a
`Release`. Worth remembering that the reviewer is not automatically right.

And **one of my own assertions was simply false**: I claimed a fixture held
cluster membership identical while changing only the clustering mode. It does
not — the membership record includes each member's author. Following the
correction through inverted the conclusion: the mode is a *function* of the
distractor membership, so no test can isolate it and hashing it separately is
redundant rather than load-bearing. Checking a reviewer's correction can change
the design, not just the test.

### 2026-08-29 (the band calibration floor)

**Completed: ADR 0005's second release gate** (PR #30, all three checks green).

**The rule had to be given something to do first.** "The observed rate must fall
inside its declared confidence interval" is vacuous — a point estimate always
lies inside an interval computed from it — and the other reading names a range
declared nowhere. That is the *third* rule in this project that read as a control
and controlled nothing, after the band-crossing rule and `z_max`. Replaced with a
bound against a target, measured on Test.

**The finding that cost the most.** I floored the bound at 3/n over *segments*,
guarding against a bootstrap's upper bound on a zero-observed rate being exactly
zero. Codex caught that this smuggles the independence assumption back in: a
hundred error-free segments from one document are one independent observation.
**The floor counts clusters.** The consequence is demanding and published: a band
needs **60 held-out author documents** or **30 distractor clusters** at the v1
targets. That is what a claim about an error rate costs.

**Also settled:** only `in range` and `not you` claim anything, so only they are
gated; `drifting` is the fallback; both failing is `uncalibrated` rather than a
band set where everything lands in the band that means nothing. The gated rate is
class-conditional, not band composition. The calibration classifies, rather than
leaving a consumer to apply thresholds itself and possibly emit a refused label.

**Next: the AUC discrimination gate**, the last of ADR 0005's three. Then the
gates are done and `score` and `rewrite` become buildable.

Open questions to settle first, the way every previous slice's were:

- **The AUC floor has no declared value.** Unlike the band minimums it probably
  cannot be derived — it is a statement about how good is good enough, which is a
  judgement. Expect to declare it as a stand-in with the reasoning recorded.
- **AUC needs a paired, clustered treatment.** A naive AUC standard error assumes
  independent segments and would repeat exactly the mistake the clustered
  bootstrap exists to avoid. The bootstrap machinery is already there and should
  be reused rather than a closed-form variance introduced.
- **Ties in `d`.** AUC is defined over pairwise comparisons and `d` can tie,
  particularly at small reference sizes where the deviation is capped. The tie
  convention (count as half) must be declared, not inherited from whichever
  formula gets written.

**Process notes.** Five review rounds, then one defect after the freeze.

The post-freeze defect is the one to carry: **`Calibration` classified through an
unexported field.** Unexported fields do not survive encoding, so a calibration
read back from the artifact store would decode its boundaries as zero and place
every distance above zero in `not you` — confidently, with no error. Silent
wrongness on a persisted artifact. Codex then improved my proposed fix: the test
is a real `encoding/json` round trip rather than a reconstruction from exported
fields, because that tests the persistence mechanism instead of Go visibility.

**Worth generalising:** every artifact in this design is content-addressed so it
can be stored and reused, and that only holds if it is self-contained. When
adding an artifact type, round-trip it through JSON in a test before believing it.

And for the third slice running, codex caught **a guard written on one side of a
symmetric pair** in my own tests. It is my most reliable defect. Check both sides
before handing tests over.

### 2026-08-29 (clustered bootstrap intervals)

**Completed: confidence intervals on both band thresholds** (PR #29, all three
checks green). First of ADR 0005's three release gates.

**Settled:** confidence 0.95 percentile, 2000 resamples, a fixed recorded seed.
**Actionability turned out not to need a declared width at all** — ADR 0005's
"too wide to be actionable" is answered by geometry: the two intervals must not
overlap, or the data does not resolve `t_low` from `t_high`.

**A distinction the spec had been hiding.** "Clustered bootstrap by document and
author" does not name one unit — it differs by class. The author's own distances
all come from one author, so clustering them by author collapses the class and
leaves nothing to resample. Author side clusters by document; distractor side by
author. Issue #2 left no per-author distractor labels, so the fallback is
recorded and flagged: document-only clustering *understates* uncertainty.

**Round 9's minimum is not a shipping minimum.** `ceil(1/p)` is where a
threshold exists; at that size it rests on one tail observation and any resample
duplicating it qualifies nothing. Measured: 20 author distances qualify ~58% of
resamples, 60 reach ~98%, 100 reach 100% — and the figure barely moves when
every distance gets its own document. The tail is short, not the cluster count.
No second minimum declared; the 90% qualification floor enforces it against the
population actually supplied.

**The technique worth reusing.** Codex found the seed could be recorded, hashed,
and never used. Fixing it properly meant specifying the draw as a pure function
— SplitMix64 per class, clusters ordered lexicographically, index modulo cluster
count — which then let me **write an independent implementation in Python and
derive every expected value from it** rather than reading them out of the
package. All matched on first run. When a slice has a numeric contract, build
the oracle outside the implementation.

**Next: the remaining two release gates**, in order.

1. **The band calibration floor** (ADR 0005). Per band, a minimum count of
   held-out segments and an observed author-versus-distractor rate inside its
   declared interval. A band failing either is not emitted and collapses to the
   adjacent wider band; other bands stay usable. Depends on this slice.
2. **The discrimination gate** — AUC of held-out author segments against
   distractors against a predeclared floor. Below it the profile is
   `uncalibrated`: raw distance and feature deltas still emitted, no band, and
   `rewrite` refuses.

Open questions to settle first, the way `z_max`, the crossing rule and the
bootstrap parameters were: the **AUC floor** has no declared value; the
**per-band minimum held-out count** has no value; and the **band-rate confidence
interval** needs its own level, which may or may not be the 0.95 used here.

Also worth deciding early: the AUC gate needs a *paired* treatment, since author
and distractor segments are clustered — a naive AUC standard error would repeat
the mistake the clustered bootstrap exists to avoid.

**Process notes.** Five review rounds before the freeze, then one defect after.

The post-freeze defect was mine and is the same class as last time: **the
interval identity covered the cluster counts but not the partition.** The same
distances grouped round-robin versus in contiguous blocks share a threshold ID
and cluster counts, produce different intervals, and shared an interval ID —
a shipping decision served from the wrong evidence. Amended by consensus, then
fixed.

One finding went the other way and is worth recording: codex derived that a thin
fixture qualified 9.375% of resamples and called it flaky. Exhaustive
enumeration of all 256 draws gives 56.25% — a resample omitting the document
holding the largest value still qualifies on the next value down. **Check a
reviewer's arithmetic the same way you check your own**; the fixture changed
anyway, for a better reason.

### 2026-08-29 (thresholds and bands)

**Completed: band thresholds and band assignment** (PR #28, all three checks
green, branched from main after #27 landed). `Calibrate` produces `t_low` and
`t_high` from the two declared error targets; `Band` assigns one of three labels
or refuses.

**The crossing rule was backwards, and this is the one to remember.** Section 2
assigned the two quantiles unconditionally and declared the targets jointly
unsatisfiable when `t_low >= t_high`. But that inequality is what
*well-separated* distributions produce. Measured on synthetic populations at the
v1 targets, the refusal fired on clean separation and stayed silent on heavy
overlap: as specified, the profile that discriminates best emitted no bands.

The fix is to order the pair — `t_low = min(A, D)`, `t_high = max(A, D)`. Both
targets still hold by monotonicity, the overlap case is unchanged, and in the
separated case `drifting` spans the gap where neither population has mass. The
unsatisfiable case does not exist. REVIEW Round 9.

This rule survived three earlier review rounds because it was checked for
internal consistency and never against a population. It is exactly what Section
2's own summary warns about. **When a rule is about error rates, test it against
error rates.**

**Also settled:** `p_author` = 0.05 and `p_distractor` = 0.10, declared
stand-ins, asymmetric. The minimum sample sizes are *derived* rather than
declared — the only derived minimum in the design — because a threshold meeting
target *p* exists only where 1/*n* <= *p*, forcing 20 author and 10 distractor
distances at the v1 targets.

**Two bindings that cannot be read off the distances** are now named at
calibration: the declared distractor pool (Section 2 has always required figures
per `(profile, distractor pool)` pair) and the calibration cohort (the Calibrate
split is a role, not the identity of the documents in it).

**Next: ADR 0005's release gates**, which is what stands between here and a
score anyone should trust. Three pieces, in dependency order:

1. **Clustered bootstrap confidence intervals** by document and author, on both
   thresholds. Section 2 says a threshold whose interval is too wide is not
   shipped, and nothing computes an interval yet. This is the prerequisite for
   the band calibration floor.
2. **The band calibration floor** — per band, a minimum count of held-out
   segments and an observed author-versus-distractor rate inside its declared
   interval. A band failing either is not emitted and collapses to the adjacent
   wider band.
3. **The discrimination gate** — AUC of held-out author segments against
   distractors, against a predeclared floor. Below it the profile is
   `uncalibrated`: raw distance and feature deltas still emitted, no band, and
   `rewrite` refuses.

The open questions to settle before those tests, the way `z_max` and the
crossing rule were settled first: the AUC floor has no declared value; the
per-band minimum held-out count has no value; and the confidence level and
bootstrap resample count are both unstated. All three are declared-not-derived
quantities and need stand-ins with a stated derivation path.

**Process notes.** Five review rounds before the freeze, then two defects caught
after it.

The first was mine and is worth carrying: `Band` refused any distance not from
the Calibrate split, which makes the scoring path unusable, and no test caught
it because the `scored()` helper always set Calibrate. A fixture that
under-supplies is the defect class that keeps recurring in my own tests. It was
fixed by the documented route — consensus, a separate test commit, re-freeze,
then the implementation.

The second was the **same negative-zero blind spot as the previous slice**. I
argued the negative-value guard made `-0` unreachable; `math.Copysign(0,-1) < 0`
is false in Go. Twice now a reachability argument of mine has been wrong at a
boundary. Check boundaries by running them, not by reasoning about them.

### 2026-08-29 (later)

**Completed: the distance `d`** (PR #27, all three checks green). A uniformly
weighted mean of absolute transformed deviations over the features a segment
makes available in the tiers that met their minimum.

**`z_max` is struck**, which was the open question. Winsorization was specified
when deviations were unbounded; the rank transform bounds them per feature. A
conventional `z_max` = 3 does not bind until a feature carries 370 reference
values against an illustrative size of thirty, and one low enough to bind
discards evidence the reference supports — with a flat constant, where the
existing bound already scales with per-feature evidence. Recorded in REVIEW
Round 8 with the same reasoning that struck `λ`.

**Two further gaps closed in the same round.** "Neither tier meets its minimum"
named a quantity that had never been stated — it is now a majority of the tier's
manifest features, expressed as a proportion so it does not weaken as the
manifest grows. And `d` now carries its contributing feature set, because ADR
0006's acceptance loop compares two distances and a mean over one feature set is
not comparable to a mean over another.

**A correction worth remembering.** The first pass proposed declaring an empty
`TierB` and building the tier machinery against it. Wrong, and caught before any
test existed: an empty tier can never meet its minimum, so every v1 score would
be flagged partial against something that does not exist. The tier set is read
off the manifest instead — one tier today, two the day a Tier B feature lands,
no code change, and the manifest digest moves at the same moment so no threshold
artifact crosses.

**Next: thresholds and bands**, per DESIGN Section 2. `d` exists and is
calibratable now. The band logic is where REVIEW's Section 2 summary says the
most instructive defects were found — "three times running, arithmetic that was
internally consistent and controlled nothing" — so the frozen tests should be
written against the error rates themselves, not against the arithmetic.

The open questions to settle before those tests, the way `z_max` and the weights
were settled first:

- `p_author` and `p_distractor` are declared quantile targets with no values.
  DESIGN says they are declared before measurement and published with their
  measured outcomes, so they need stand-in values and a stated derivation path.
- The minimum Calibrate reference size is named a published figure throughout
  and has no number. Note the interaction found this session: the reference size
  caps `|deviation|` at 1.69 for ten values and 2.14 for thirty, so this minimum
  sets the ceiling on `d` itself.
- Bands need their own thresholds per scored tier subset. In v1 there is one
  subset, but the artifact has to be keyed for more.

**Process notes.** Six review rounds on the distance tests before APPROVE. The
recurring value is that codex attacks the fixtures rather than the prose: every
numeric fixture was at most 1.5, so a still-winsorizing implementation would
have passed the entire suite, and the tier derivation was only ever tested
against a manifest that made hardcoding indistinguishable from deriving. Both
needed a seam, not an argument.

Also: this slice added a fourth near-identical manifest-shape validator, which
is the same defect codex caught on the previous slice about three of them. Four
copies of one rule is where the fifth diverges. They are now one generic
`manifestMap` with five callers.

### 2026-08-29

**Merged first.** PRs #24 (recovery) then #25 (sampling variance), in that
order, and verified on `main` rather than trusted: 10 packages present,
`internal/eval` and the sampling-variance fields both there. The stranding
incident of the previous session is why this is now checked rather than read
off the PR list.

**Two design decisions settled, both by the repo owner.**

*Weights.* Section 2 had asserted `w` was "learned, not asserted", fitted on
Train against author-versus-distractor separation — without stating the
objective, regularization, constraints or missing-feature rule, and without the
preconditions holding. **v1 declares uniform weights**, records the scheme and
its version in the scoring cache identity, and leaves fitting as the intended
destination. Two reasons, both internal to the design: there is no distractor
pool with author diversity to separate against (issue #2 closed on the
no-bundled-corpus fallback), and fitting 150+ weights on a personal corpus's
Train split is the over-parameterization the same section rejects Mahalanobis
for. Recorded in REVIEW Round 6.

*`λ` is struck.* It was named as Train-fitted in three places and defined in
none. Neither reading survives the uniform choice: as a regularization strength
it has nothing left to restrain, and as a Tier A/B blend it duplicates the
availability rules and `d_A`'s separate threshold artifact. A future
fitted-weights slice reintroduces it with a definition.

**Completed: the deviation slice** (PR #26, all three checks green). DESIGN
Section 2's two corrections, composed in the order Round 5 fixed —
length-aware standardization, then the empirical-CDF rank transform of *that*
quantity. Calibrate-only reference, content-addressed and per-feature.

Round 7 settled what the transform returns, which had never been stated: an
ECDF rank is a percentile, `z_max` winsorization is vacuous on a percentile,
and `d` averages |z|. The rank is therefore mapped back through the normal
quantile function. The plotting position is declared because it is visible in
the output — it caps |deviation| at `Phi^-1(1-1/2m)`, **1.69 at ten reference
values**, so at small reference sizes the cap and not `z_max` is the operative
limit.

**Next: the distance `d`.** Everything it needs now exists. Per DESIGN Section 2
"The distance `d`" as amended in Round 6:

- a weighted robust mean of transformed deviations, Manhattan in transformed
  space, with **uniform weights** over whichever features a segment makes
  available
- deviations winsorized at `z_max`, which is fixed on Train and shipped with a
  sensitivity analysis over its value — note that the reference cap already
  bounds |deviation| below `z_max` at small reference sizes, so the interaction
  needs stating
- Tier-A-only scores get their own threshold artifact and are reported as such;
  neither tier meeting its minimum is **insufficient evidence** — no `d`, no
  band, and `rewrite` passes the segment through untouched

`z_max` is the open question to settle before writing those tests, the way the
weights question was settled before this slice: it is declared fixed on Train,
but nothing says what value or how the sensitivity analysis is reported.

**Process notes worth carrying.** Codex has now stopped rather than edited a
frozen test seven times on this project and has been right every time. It found
six defects in these tests across four review rounds, and one in my own
reasoning at the consensus gate: I dismissed a negative-zero hazard in the
reference identity on a reachability argument that only covered
`value == mean` with both positive, missing that IEEE `(-0) - (+0)` is `-0`.
Verify numeric claims against a real Go run, not against reasoning — the same
lesson as the tokenizer counts.

### 2026-08-27

**Completed, two slices.**

**text slice 2d — the structural tree** (merged, PR #15). Markdown parses into
containers and leaf text runs, each leaf carrying a role, an inclusion verdict,
a machine-readable exclusion reason and the evidence the verdict came from.
Every row of DESIGN Section 3's leaf-role table is implemented. goldmark with
the table, footnote and definition-list extensions, parsing the raw admitted
bytes so every offset is already a raw offset.

**`profile` rewired onto the paragraph unit** (branch
`feat/profile-paragraph-unit`), plus the primitive it needed: `text.RunTokens`,
the document's own tokens inside a leaf's span and outside its excisions. The
fence 2d existed to remove is gone.

**Next task.** `eval` (component 5) is next in the numbering and its
dependencies now exist in the right shape, but it remains blocked in practice on
the register-matched distractor corpus in **issue #2**, whose binding seven-day
timebox has **still not started** — it begins at the first commit referencing
that issue. Two candidates could reasonably go first:

- **Derive the minimums.** The per-feature minimum sample sizes and the
  paragraph size floor are both declared stand-ins, and they are the only reason
  the profile still withholds readiness. Section 2 specifies the derivation.
- **Issue #5, the author-specific orthographic profile**, which needed
  `profile` and is now unblocked.

**Decisions made, with reasons.**

- **Structural parsing runs over the raw admitted bytes.** Section 3 required a
  normalized-to-raw offset map; that is only needed for a parser consuming the
  normalized form, so the map would always be the identity — a place for a bug
  to hide. Amended; logged as Section 3 Round 4 in REVIEW.md.
- **A run with no words left after excision is outside the population**,
  wherever it sits: admitting it adds a paragraph observation carrying no
  measurement. Only a role exclusion outranks it.
- **Empty blocks emit no leaf.** "Non-included leaves are recorded" governs text
  runs excluded by policy, not blocks with no run to record.
- **Sententiality is a declared heuristic with a published error rate.** A
  proper per-item prose decision needs a finite-verb test, which needs POS
  tagging, which ADR 0001 rules out. The rule is `(EndsTerminal AND Words >= 4)
  OR Words >= 8`, closers peeled first, measured against a 30-item hand-annotated
  fixture: **13.3% error against a declared 20% ceiling**, with both misses and
  both false positives recorded in the fixture.
- **Paragraphs pool unweighted.** One paragraph is one observation, which
  estimates "a randomly chosen paragraph by this author" — what `score`
  measures, so estimator and target match. Document weighting would estimate a
  different quantity and inflate short documents' influence.
- **Readiness stays withheld**, because Section 2 requires derived minimums and
  none is derived. The reason changed, not the answer: from "the unit is wrong",
  a defect in the statistic, to "the minimums are declared, not derived".
- **Split assignment stays at document level.** A paragraph inherits its
  document's split and never crosses one.

**Known limitations, all deliberate.**

- One `panic` remains in `text`'s leaf constructor, on an internal invariant. It
  is a consequence of `Structure` having no error return. A 28,818-input sweep
  no longer reaches it, but it is **not** claimed unreachable — that claim was
  made once and disproved.
- A container's span is the enclosure of its descendant leaves, not its own
  source extent, so quote and list markers are not represented.
- `Profile.Documents` counts eligible train documents READ, not documents that
  contributed a retained paragraph.
- `Stats.Undefined` is forward-compatible accounting: every current feature is
  defined whenever a paragraph has one lexical token, and the floor guarantees
  that, so the tally is always zero today.
- `text.Node` carries no document provenance, so a node from another document
  with a coincidentally valid span is undetectable. Closing it means reopening
  2d's frozen Node schema.

**Performance note, worth remembering.** `Document.Tokens()` returns a *copy* of
the token slice. Calling it once per leaf made `Structure()` quadratic in
allocation: on the 1.1 MB Federalist fixture, 3.39 s and 20.5 GB, with a
profile pass adding as much again. An internal cached-token accessor plus a
binary search bounding each scan to the run brought it to 282 ms / 73.6 MB and
5.5 ms / 36.5 MB. The defect entered in 2d when a per-leaf `Admit()` was
replaced by a per-leaf `Tokens()` without anyone measuring that `Tokens()`
copies.

**Process note.** Both slices used the `duet` process: tests written and
adversarially reviewed before any implementation existed, frozen by commit,
implemented by a second model, then reviewed. The implementer stopped rather
than editing a frozen test **five times across the two slices** and was right
every time — a wrong NFC expectation, two sententiality expectations the
declared rule contradicts, and two fixtures that silently deduped. Every
amendment was made by consensus, committed on its own and re-frozen, so the
history distinguishes an agreed amendment from an implementer edit.

The other lesson is that frozen tests are not enough on their own.
Adversarial *input* sweeps found five defect classes in 2d that thirteen review
rounds had missed, and a measurement found a 20 GB allocation that no test would
ever have failed on.
