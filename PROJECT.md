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
| 13 | `cli` | **partial** | A1 shipped (#51). A2 blocked on #53; a 14th component, `ingest`, is proposed there |

Supporting: `fixtures` (vendored public-domain corpus), `ciconfig` + CI workflow.

---

## Open issues

| # | Title | Blocked on |
|---|---|---|
| [#1](https://github.com/fissible/hapax/issues/1) | Vendored public fixtures and end-to-end CI corpus | `score` and `eval` are built; actionable once `cli` can drive them |
| [#3](https://github.com/fissible/hapax/issues/3) | Incremental corpus indexing and derived-artifact cache | `store` is complete; needs `cli` to drive reindexing |
| [#53](https://github.com/fissible/hapax/issues/53) | Make `hapax index` possible — four gaps between corpus, profile and store | nothing — blocks all remaining `cli` work |
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
