# ADR 0005 — Calibration is a release gate, not a report

**Status:** Accepted (2026-08-26, from review rounds 1–3)

## Context

A 0–100 "voice match" score is the category norm and is, as normally shipped,
unfalsifiable. Nothing establishes that the number corresponds to anything. Review held
that no score may be emitted before a protocol exists that gives it meaning.

## Decision

`eval` is a first-class component and a release gate.

- **Split unit is the whole source document**, held out *before* profiling. Paragraph-level
  splits leak: paragraphs from one document share topic, register and occasion.
- **Distractors are register-matched** writing by other authors. **`--distractors <dir>`
  is required for calibration; no distractor set ships.** Amended 2026-08-27 on the
  resolution of issue #2 — this clause previously promised a bundled permissively licensed
  set. No source surveyed cleared the requirements, and shipping one that did not would
  have produced a calibration figure measuring genre or era rather than authorship. Without
  `--distractors`, `eval` reports `uncalibrated`, `score` emits raw distance and per-feature
  deltas but no band, and `rewrite` refuses.
- **Two metrics.** *Discrimination:* pairwise ranking accuracy / AUC of held-out author
  segments against distractors. *Band calibration:* per band, the observed author-versus-
  distractor rate with confidence intervals. Discrimination alone cannot justify a label.
- **Two gates.** Below the discrimination floor the profile is `uncalibrated`: raw distance
  and feature deltas are still emitted, no band is, and `rewrite` refuses to run. A band
  failing its calibration test is not emitted and collapses to `drifting`; other bands
  remain usable.

  *Amended 2026-08-29, on scoping the band gate.* This clause previously said a band failed
  by missing "its minimum held-out count or its declared interval". Neither quantity existed:
  no minimum was ever declared, and "the observed rate must fall inside its declared
  confidence interval" is vacuous, since a point estimate always lies inside an interval
  computed from it. The test is now stated in Section 2 under "The band calibration floor" —
  a claiming band is emitted only when the one-sided upper bound on its own error rate,
  measured on Test, is at or below the target its threshold was built to respect. The minimum
  count is a consequence of that bound rather than a separate gate.
- **Provenance.** Every result is stamped with corpus hash, profile version and feature-set
  version.

Minimum corpus size, minimum segment size per tier, and measured intervals are published in
the README as numbers, not claims.

## Consequences

- v1 cannot ship a score until the harness and a distractor corpus exist.
- Feature-set ablation is explicitly post-v1; this is a calibration gate, not a research
  program.
