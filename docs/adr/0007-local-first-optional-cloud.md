# ADR 0007 — Local-first, optional cloud, tested no-egress guarantee

**Status:** Accepted (2026-08-26)

## Context

The corpus is unusually sensitive: personal writing, often private, often predating the
author's public work. Both commercial competitors (Idiolect, Grammarly) are cloud-only.
Review argued for shipping Ollama-only to avoid provider lifecycle cost, then withdrew that
on the grounds that small local models are measurably weaker at style mimicry — which is
precisely the hard task.

## Decision

Ollama by default; Anthropic as the single named cloud provider, behind a provider
interface. Only the draft passage and a handful of exemplar sentences are ever sent —
never the corpus. Corpus text is fenced as untrusted data during prompt assembly, since it
may contain text that reads as instructions.

`--local-only` / `HAPAX_LOCAL_ONLY=1` is asserted **by test**, not by documentation: no
cloud provider constructed, no credential read from environment or config, **no connection
to any non-loopback address** attempted, no telemetry. The harness fails on any dial outside
loopback.

Amended 2026-08-29: this originally read "no outbound connection attempted", which
contradicted the same decision's default of Ollama over HTTP to localhost. The guarantee is
about destination, not silence.

A cloud failure is a hard error. There is never a silent downgrade to a local model, since
that would change output quality without the author knowing.

## Consequences

- Provider lifecycle work is real and accepted: credential handling, cancellation,
  request-size limits, integration tests. **Retries are not in v1** — a retry is a second
  charge and a second egress, and nothing yet requires one.
- Additional providers are deferred until one is proven.
