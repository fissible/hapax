# ADR 0003 — Feature tiers; Burrows' Delta only at window scale

**Status:** Accepted (2026-08-26, from review round 1)

## Context

The original design scored individual paragraphs with Burrows' Delta. Adversarial review
established this is statistically invalid. Delta assumes stable relative frequencies over a
substantial sample; in a 60-word paragraph most function-word counts are 0–2, and
z-normalization amplifies sampling noise into a confident-looking number.

## Decision

Features are tiered by the sample size they require, and each feature declares its own
minimum.

- **Tier A** — dense, valid at paragraph scale: sentence-length mean and variance,
  punctuation rates, contraction rate, word-length distribution, clause-marker density.
- **Tier B** — sparse, requires a rolling window of several hundred tokens: function-word
  distribution, hapax ratio, sentence-opener distribution. Delta operates here only.

A paragraph is scored on Tier A directly and on Tier B via a rolling window centered on it.
When neither tier has sufficient sample, `score` returns `insufficient evidence` and no
number.

A normalization and tokenization contract (`text`) is a prerequisite component, since every
feature is determined by its choices.

## Consequences

- No score is emitted with more precision than its sample supports.
- The hapax ratio, which names the project, is a Tier B feature and is not assumed to be
  top-weighted. Whether it earns its place is an empirical question for the evaluation
  harness (ADR 0005). Ablation is post-v1.
