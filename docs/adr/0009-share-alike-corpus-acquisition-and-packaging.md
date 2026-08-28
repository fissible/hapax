# ADR 0009 — Acquisition and packaging of a share-alike distractor corpus

**Status:** Accepted (2026-08-27, during the issue #2 timebox)

## Context

Issue #2 needs a register-matched distractor corpus. The candidates that survive on
content — ParlaMint, Wikipedia Talk — are CC BY-SA, and this project ships Apache-2.0
(ADR 0008). The issue treated share-alike as broadly incompatible with that.

That framing was too broad. CC BY-SA 4.0 §3(b) applies its ShareAlike condition when we
share **Adapted Material** — material in which the licensed work is "translated, altered,
arranged, transformed, or otherwise modified". A collection may include CC-licensed
material without changing that material's licence or the licence of the other works
beside it. Apache's own third-party policy is *not* authority here: its CC BY-SA
allowance covers unmodified **media**, explicitly "not intended to mean inclusion in our
source code", and it binds ASF projects, which this is not.

A tempting next move was to ask whether the statistical outputs — discrimination AUC,
thresholds, band counts — are themselves adaptations, and treat the answer as the gate.
That is the wrong gate. Those are this project's own numerical outputs, not redistributed
prose and not a substantial copy of the corpus's selection or arrangement, and Creative
Commons distinguishes the use of facts and ideas from a copyright-regulated reuse of
database expression. Resting the decision there would require a global conclusion that
statistics never create obligations, which is broader than anything we need and broader
than anyone should assert.

The material risk is narrower and entirely within our control: **what acquisition retains,
derives and redistributes.**

## Decision

Three rules, by what the acquisition path actually does.

**1. If it downloads and retains or distributes original documents plus manifest
metadata** — treat the corpus as a **separately licensed data artifact**. Preserve
attribution and the CC BY-SA notice, and keep it outside the Apache-licensed code
distribution. Unmodified inclusion in a collection is not adaptation, so nothing here
reaches the code.

**2. If it normalizes, excerpts, recombines, or publishes a corpus-derived dataset** —
assume that artifact may be Adapted Material, or a database-rights case, and **publish it
under CC BY-SA 4.0** with its provenance and attribution. This remains available, but it
is a deliberate choice that constrains the acquisition design rather than a licensing
footnote discovered later.

**3. Published evaluation reports are aggregate-only.** Corpus identity and hash, source
and version, method, and figures. **Never prose, never per-document feature vectors, never
recoverable feature records.**

**Counsel reviews the final manifest and distribution layout before any CC BY-SA source
ships.** This ADR bounds the engineering decision; it does not substitute for that review.

## Consequences

- The acquisition design is now constrained up front. Whether hapax ships original files
  or a derived corpus is a licensing decision made before the code is written, not after.
- Rule 3 tightens, for published artifacts, an invariant DESIGN Section 3 already states
  for the local store. There, "feature vectors and span references are the permitted
  derived forms" — permitted *to persist locally*, on the author's own machine. That is
  not permission to publish them. The same prohibition now serves two purposes: the
  author's privacy, and not redistributing a licensed corpus in derived form.
- Issue #17's sufficiency demonstration — per register, per band, with clustered
  confidence intervals — is aggregate by construction and is unaffected.
- The decision does not depend on any general claim about statistics and copyright, so it
  survives being wrong about the edges of that question.
- If no source clears on the merits within the timebox, none of this is wasted: the same
  three rules govern a user's own `--distractors` directory, where the licences are
  whatever the user's own reading pile carries.
