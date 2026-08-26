# ADR 0008 — Apache-2.0, public from the first commit, voice fidelity not evasion

**Status:** Accepted (2026-08-26)

## Context

Three viable paths: OSS reputation asset, paid product, or private tool embedded in
fissible products. The paid path competes with Grammarly and Idiolect on distribution
rather than technology, and a local CLI is not a subscription business. The existing OSS
field is shallow — banned-word lists and single-file agent skills — so a rigorous
implementation is disproportionately visible there.

## Decision

Public from the first commit, Apache-2.0. The patent grant is worth having, and permissive
licensing keeps the door open to embedding the core in Station without the question
`setec-voiceprint`'s GPL would raise. No open-core: against free single-file competitors, a
crippled core loses adoption for negligible revenue.

Commercial upside, if pursued, is a Station CMS module — editorial voice consistency for
multi-author publishing — where a real buyer exists. The OSS project is what makes that
feature credible, not the reverse.

**Positioning is explicit and load-bearing: voice fidelity, not detector evasion.** The
adjacent vocabulary ("humanizer", "undetectable", "watermark remover") carries an
academic-dishonesty association that would make the project unsafe to cite professionally
and unsellable into a CMS. The README states plainly that the tool is not for defeating AI
detectors and cannot make someone else's writing sound like you.

That constraint is honest rather than decorative: the tool is inert without a substantial
corpus of the author's own writing.

## Consequences

- README leads with the calibration honesty and the no-API-key demo path.
- Design and review history are committed publicly, including the objections that changed
  the design.
