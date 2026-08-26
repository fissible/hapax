# ADR 0004 — Scoring and exemplar selection are separate objectives

**Status:** Accepted (2026-08-26, from review round 1)

## Context

The original design reused one nearest-neighbour search for both scoring a draft segment
and retrieving exemplars, presented as an elegance. Review identified it as degenerate.

Retrieving corpus segments *nearest to a failing draft* retrieves the author's most
AI-adjacent writing, then presents it to the model as evidence of how the author writes.
The tool would teach itself the defect it exists to remove.

## Decision

The two share a feature *extractor* and nothing else.

- **`score`** measures a segment against the profile.
- **`select`** picks segments *representative of the author* — medoids and high-density
  regions of the named profile — diversified by structure. Never nearest-neighbour to the
  draft.

Register is handled by explicit named profiles (`--profile essays`), required whenever more
than one exists. There is no register classifier in v1: inferring which of an author's
voices a draft is reaching for is a research problem, and asking is one flag.

## Consequences

- Exemplars are stable per profile and largely cacheable, rather than recomputed per draft.
- Draft-similarity as a bounded secondary ranker remains possible later, but only with
  held-out evidence that it helps.
