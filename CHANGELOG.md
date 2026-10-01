# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/).
## [Unreleased]

### Fixed
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
