# ADR 0001 — Go, distributed as a single static binary

**Status:** Accepted (2026-08-25)

## Context

The tool is aimed at writers as well as developers. Every comparable OSS project in this
space is either a Python package or a `SKILL.md` locked to one agent. Install friction and
agent lock-in both cap adoption. The retrieval decision (ADR 0002) removes any need for an
ML runtime, which takes Python's main advantage off the table.

## Decision

Go, shipped as a single static binary with no runtime dependencies. Core logic lives in a
library package; the CLI is a thin shell over it. A Claude Code skill and an MCP server
will wrap the same core later.

## Consequences

- Trivial cross-compilation and a Homebrew formula; no interpreter, no virtualenv.
- Stylometric features are implemented directly rather than pulled from spaCy or NLTK.
  This is acceptable: the features are counting problems, and owning them makes the
  tokenisation contract (ADR 0003) explicit rather than inherited.
- The fissible org CI workflow is bash-oriented and will need a Go equivalent.
