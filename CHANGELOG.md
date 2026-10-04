# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/).
## [0.3.0] - 2026-10-03

### Added
- A candidate may not exceed 1.5x the ORIGINAL paragraph's lexical tokens, refused as the new
  `expanded` rejection code (#143). One-sided — nothing is refused for being shorter — and
  anchored on the original rather than the advancing current, so accepted passes cannot
  compound to 1.5^N. Reported after `not-preserved` and before both language refusals and
  `not-improved`. The multiplier is declared, not derived; `ExpansionCeilingDerived` says so,
  and #148 carries the measurement that would justify a value.

### Changed
- Migration 14 widens `rewrite_attempt.rejection` to admit `expanded` and adds
  `original_lexical_tokens`, `candidate_lexical_tokens` and `expansion_ceiling`, rebuilding
  the attempt table and its three child tables so their foreign keys survive. Historical rows
  backfill to zero, which reads as "written before the policy existed" rather than as a policy
  value. The database refuses an `expanded` row whose counts fall within the bound THE ROW
  RECORDS, never the current constant, so a later change to the multiplier cannot make old
  rows look contradictory.
- The two lexical counts are recorded on every attempt that admits exactly one candidate
  segment, including refusals that never reach the gate, and are zero when it does not;
  `expansion_ceiling` records the policy in force either way.
- A script is named for growth only when its count AND its share both rise (#136). The arm it
  replaces fired on any rise in absolute count, so it refused rewrites that DILUTED a script —
  measured, 10 Latin + 1 Greek to 23 + 2 takes Greek from 0.0909 to 0.0800 and was named. The
  change only ever permits more: the new arm implies the old one, so no rewrite accepted before
  this release becomes refused. Proportional scaling is exempt, which was the contract #136
  asked for. No migration.
- `Epsilon` is recorded as a declared tolerance rather than a bound below the score's
  resolution (#137), with `EpsilonDerived = false`. The rationale it replaces computed
  2.5/((n+1)*k), which approximates a single feature's rank step, where `d` is a mean over k of
  them — a witness through the real transform improves by 1.3e-10 and is rejected. What
  Epsilon actually does is reject ties: acceptance is `candidate <= current - Epsilon`, so at a
  zero tolerance a tie would be accepted and `current` would advance without improving.
  Behaviour unchanged; the value is still 1e-9.

### Fixed
- Acceptance now covers the computed float64 threshold at more than one value of `current`
  (#137). Two comparison errors passed the whole test suite before: turning the tolerance
  strict, and rewriting the rule as a subtraction, which disagree only where the threshold
  rounds — at `current = 1.0` the threshold is an improvement of 9.99999972e-10, under Epsilon
  and accepted.

### Internal
- `internal/workflow` builds its prepared fixtures once per package run and copies them per
  test (#126), taking the package from 425.3s to 326.9-345.4s under `-race` on two runs. The
  ticket's diagnosis was stale: the corpus indexing it blamed was already cached, and the cost
  was the per-fixture disposition check, 84% of it.

### Generated from the commit subjects

- feat: refuse a candidate more than 1.5x the original's length
- fix: exempt proportional growth and dilution from the script guard
- fix: record epsilon as a declared tolerance, not the score's resolution
- perf: build the workflow fixtures once per package run (#126)

## [0.2.0] - 2026-10-02

### Fixed
- Refuse publication when the assembled document does not reproduce the last accepted candidates' interpretation and measurements or changes an untouched included leaf (#133). Check shifted and BOM-adjusted spans, retain local attempt acceptance, and report all mismatched node IDs without publishable bytes or publication evidence. No migration is added.
- Rewrite publication identity now hashes each changed target's included leaf in the re-admitted final document, accounting for whitespace, BOMs and earlier replacements' offset shifts (#132).
- Both tool-output screens now use atomic publication evidence recorded after successful file publication, rather than accepted attempt hashes. Only the last accepted candidate per changed target is recorded; attempt hashes still describe the exact provider response (#134).
- A recording failure after publication exits 3 with a diagnostic and no stdout result, in human and JSON modes.

### Changed
- Migration 12 creates publication evidence without backfilling history. Paragraphs published before this upgrade stop being screened; stores with earlier accepted attempts disclose a persistent `publication-evidence-gap` on index. This deliberately fails to exclude unrecorded output rather than risk excluding the author's own prose based on an attempt that was never published.
- Retain each rewrite attempt's splice verdict independently of rejection precedence (#135).
  Migration 13 leaves historical verdicts empty without inference; database, write, and read
  validation reject contradictory evidence, and immutable writes compare the verdict strictly.
  Gate-skipping rejections (`not-one-segment`, `candidate-unscoreable`, `uncalibrated`,
  `different-features`) require an empty verdict at all three boundaries. `unscoreable`
  returns `TerminalNotEntered` before any attempt exists and is excluded from this rule.
- Replace the rewrite comments' replay claim with the text-local precedence rationale:
  audit rows store hashes, so they cannot supply the prose needed to rerun the gates.

### Generated from the commit subjects

- feat: give a published paragraph one canonical identity (#132, #134)
- feat: record the splice verdict on every attempt (#135)
- feat: publish only what was scored (#133)
- ci: make go test's own timeout explicit

## [0.1.0] - 2026-09-29

### Added
- Admission and normalization-safe span boundaries (GREEN)
- Vendored public-domain corpus with pinned identity
- Tokenization slice 2a (GREEN)
- Tier A candidate extraction (GREEN)
- Snapshot walk, admission, dedupe, split and identity (GREEN)
- Rule schema, regex matcher and screening model (GREEN)
- Author profile from train split (GREEN)
- The structural tree, slice 2d (GREEN)
- The paragraph unit, and text.RunTokens (GREEN)
- Snapshot roles and the author/distractor overlap screen (GREEN)
- The held-out segment population (GREEN)
- Per-feature sampling variance at the observed segment length
- The manifest digest covers the whole manifest
- Length-aware standardization and the rank transform
- The distance d, with tier availability and partial scores
- Band thresholds from ordered quantiles
- Clustered bootstrap confidence intervals on the band thresholds
- The band calibration floor, ADR 0005's second release gate
- The AUC discrimination gate, and the release verdict
- Measure a draft against a profile
- Preserve, the deterministic semantic-preservation gate
- The monotonic acceptance loop
- Assemble, splicing accepted replacements into the original bytes
- Select, author-representative exemplar selection
- Llm, the provider seam and the tested no-egress guarantee
- Store, the SQLite artifact schema and the snapshot aggregate
- Store slice 2a, the remaining artifacts and their codecs
- Store slice 2b, rehydration, unavailability and Prune
- Cli A1, the command surface, exit codes and output document
- Store slice 3, eval_result holds a release
- Identify band reports by band, and require an empty class to be empty
- Index the graph and persist what the packages actually produce
- Hapax index and hapax profile
- Hapax score
- Draft planning, node-keyed attempts, and the exemplar seam
- A fresh store is a copy, not a replay of the migration chain
- The credential boundary is a signature
- Execution — freshness, the loop, and assembled bytes
- Publication — the only code that can destroy a user's file
- Hapax rewrite — the command, and publication
- A tell ruleset that catches what a person actually complains about
- Hapax --help
- Explicit paragraph targets rewrite an uncalibrated profile
- Report the scripts a passage uses, and what a candidate introduces
- Refuse a candidate that introduces a script (#91)
- Refuse a candidate that grows a script out of proportion (#107)
- Disclose that the acceptance gate cannot reject (#117)
- Refuse to re-target a paragraph this tool wrote (#111)
- Screen this tool's own prose out of the corpus (#109)

### Changed
- One prune, one copy of each write, one migration transaction

### Fixed
- The audit record holds preserve identifiers, not its item text
- Call the seam instead of reflecting around it
- Close the union, and let the type carry what the tests were carrying
- A rejected document has no split, and index no longer dies on one
- An accepted attempt may carry no band at all
- A malformed preserve verdict is still a hard error
- Score's payload does not restate the envelope's refusal
- A refused plan states no selection and no claim
- Low is the author boundary and High the distractor one
- The boundary rule has a diagnostic of its own
- An entity is watched by name, not by the case it carries
- Ten lexical tokens before a paragraph is a measurement
- Report which paragraphs were skipped, not just how many
- Find figcaption boundaries in the raw bytes, not a lowercased copy
- Anchor preserve on the original paragraph (#116)
- Refuse a candidate that would not splice back in place (#115)

### Ci
- Go workflow with executable configuration invariants
- Give the test job room for a suite that measures 9m24s (#126)

### Merge
- Integrate rewrite (#33) into the preserve slice

### Tools
- Keep the select oracle rather than losing it with the session

