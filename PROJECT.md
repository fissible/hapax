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
| 5 | `eval` | not started | blocked in practice — see below |
| 6 | `score` | not started | needs `eval` thresholds |
| 7 | `select` | not started | needs `profile` |
| 8 | `preserve` | not started | needs `text` |
| 9 | `llm` | not started | leaf; Ollama + one cloud provider |
| 10 | `rewrite` | not started | needs 6–9 |
| 11 | `cli` | not started | intended early as a thin shell; deferred while there was nothing to expose |

Supporting: `fixtures` (vendored public-domain corpus), `ciconfig` + CI workflow.

---

## Open issues

| # | Title | Blocked on |
|---|---|---|
| [#1](https://github.com/fissible/hapax/issues/1) | Vendored public fixtures and end-to-end CI corpus | partially delivered; rest needs `score`, `eval` |
| [#2](https://github.com/fissible/hapax/issues/2) | Select and licence-verify a register-matched distractor corpus | nothing — actionable, blocks release |
| [#3](https://github.com/fissible/hapax/issues/3) | Incremental corpus indexing and derived-artifact cache | `text` contract versioning exists; actionable |
| [#4](https://github.com/fissible/hapax/issues/4) | Golden set — matched-brief triplets | needs maintainer-authored triplets |
| [#5](https://github.com/fissible/hapax/issues/5) | Author-specific orthographic profile | needs `profile` (now built) |

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
