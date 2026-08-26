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
- **Distractors are register-matched** writing by other authors. A bundled permissively
  licensed set ships; `--distractors <dir>` overrides.
- **Two metrics.** *Discrimination:* pairwise ranking accuracy / AUC of held-out author
  segments against distractors. *Band calibration:* per band, the observed author-versus-
  distractor rate with confidence intervals. Discrimination alone cannot justify a label.
- **Two gates.** Below the discrimination floor the profile is `uncalibrated`: raw distance
  and feature deltas are still emitted, no band is, and `rewrite` refuses to run. A band
  failing its minimum held-out count or its declared interval is not emitted and collapses
  to the adjacent wider band; other bands remain usable.
- **Provenance.** Every result is stamped with corpus hash, profile version and feature-set
  version.

Minimum corpus size, minimum segment size per tier, and measured intervals are published in
the README as numbers, not claims.

## Consequences

- v1 cannot ship a score until the harness and a distractor corpus exist.
- Feature-set ablation is explicitly post-v1; this is a calibration gate, not a research
  programme.
