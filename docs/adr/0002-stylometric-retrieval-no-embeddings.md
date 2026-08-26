# ADR 0002 — Stylometric retrieval, not semantic embeddings

**Status:** Accepted (2026-08-25)

## Context

Exemplar selection needs to find passages of the author's writing to condition the model
on. The obvious implementation is a vector database over sentence embeddings.

## Decision

No neural embeddings. Selection operates in the same stylometric feature space used for
measurement: function-word distributions, punctuation and rhythm signals, structural
features.

## Consequences

- **Correct axis.** Semantic embeddings match on *topic*. Two passages about databases
  cluster together regardless of who wrote them. Style is the axis that matters here, and
  optimizing topic similarity would actively select the wrong exemplars.
- No model download, no ONNX runtime, no vector store. Fully offline, deterministic,
  auditable, and fast — which is what makes ADR 0001 viable.
- Register and topic matching, where wanted, is handled explicitly (ADR 0004) rather than
  falling out of an embedding space as an uncontrolled side effect.
