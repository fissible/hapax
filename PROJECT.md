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
| 0 | `text` | **built** | admission + spans (2a-1), tokenization (2a) |
| 1 | `tells` | **built** | schema, regex matcher, screening model |
| 2 | `corpus` | **built** | walk, dedupe, split, snapshot identity |
| 3 | `features` | **built** | Tier A candidates |
| 4 | `profile` | **built**, PR #13 | fenced: document-unit, not production-ready |
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

---

## Deferred slices, recorded so they are not forgotten

- **text 2b** — URL, email and file-path recognition plus terminal-punctuation
  peeling. Split out because "leaves a string still valid as that class" is only
  decidable once each class has a pinned grammar.
- **text 2c** — sentence segmentation. Needs a hand-annotated fixture and a
  published error rate; unblocks sentence-length features.
- **text 2d** — the structural tree. **Unblocks the paragraph unit**, which is
  what makes `profile` production-ready.
- **Contraction rate** — needs the contractible-opportunity denominator and a
  bidirectional lexicon.
- **Structural tell matchers** — triplet stacking, repeated openers, em-dash
  density. Schema admits them; loader rejects them as unimplemented.
- **Code-fence awareness** for tell suppression — needs the structural tree.
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

### 2026-08-26

**Completed this session.** The project went from an idea to seven built
components. Prior-art research, the full three-section design, eight ADRs, five
issues, CI, and then — in dependency order — `text` (admission, spans,
tokenization), `fixtures`, `features` Tier A, `corpus`, `tells`, and `profile`.
PRs #6–#12 merged; **PR #13 (`profile`) is open and green, awaiting merge**.

**Next task.** A deliberate fork, and worth choosing fresh rather than by
default:

- **text slice 2d — the structural tree** converts `profile` from fenced to
  real by supplying the paragraph unit, and unblocks the rest of Tier A. My
  recommendation: it turns an already-built component into a correct one rather
  than adding another fenced one. It is a large slice and shares fixture work
  with 2c.
- **`eval` (component 5)** is next in the numbering and its dependencies exist,
  but it is blocked in practice: it needs the distractor corpus from #2, and it
  would be calibrating a profile that declares itself not production-ready.

**Decisions made this session, with reasons.**

- **The profile is built on the TRAIN split only.** It is fitted state; building
  it on Calibrate or Test leaks into the figures `eval` reports.
- **Sample variance, not population.** The profile generalises to future
  writing; population variance is 20% too small at N=5.
- **Document-unit profiles are fenced, not deferred.** A document-level mean is
  a *different statistic* from a paragraph-level one — it averages away the
  within-document variation the design measures, so variances come out too
  small. `ProductionReady=false` with a reason; consumers must refuse.
- **`tells` ships no word list.** Banned-word lists are what every competitor
  ships, and issue #4 requires rules to be derived from paired data. Every rule
  declares provenance and category; the linter withholds verdicts it has not
  earned. A formatting rule is never evidence of machine authorship however well
  derived.
- **The corpus contamination hole is closed by source provenance and
  quarantine**, not by the linter. `tells` is a backstop.
- **ADR 0006's acceptance rule was wrong twice.** A scalar tell count lets one
  severe finding be traded for several minor ones; a componentwise "no severity
  may increase" rule inverts the question. It is now severity-lexicographic over
  derived, verdict-eligible findings only — which means **the gate is currently
  inert**, since every shipped rule is unvalidated.

**Blockers and known gaps.**

- `profile` is not production-ready by its own declaration until 2d lands.
- `corpus` still reports contamination screening as `NotPerformed`; wiring
  `tells` in is small, but per the reframe above it is not the real fix.
- Issue #2's binding seven-day timebox has **not started** — it begins at the
  first commit referencing that issue.
- No CLI exists. The README's command block is the planned interface.

**Process note.** The `duet` skill was written and revised this session. Its
freeze step now **commits** the tests rather than only hashing them, because a
mid-flight test amendment made the hash file stale and there was no way to tell
a legitimate amendment from an implementer edit. That change earned itself
twice in later slices.
