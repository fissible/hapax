# hapax

**Rewrite AI-drafted prose into your own voice, measured against your own prior writing.**

`hapax` builds a stylometric profile from writing you did yourself, scores a draft against
it, and rewrites only the passages that miss — verifying after every change that the prose
moved *toward you* rather than merely away from the model.

The name is from *hapax legomenon*: a word appearing exactly once in a corpus.

> **Status: v0.1.0, the first tagged release.** All six commands run and the
> library is complete. The version says the shape has settled, not that the
> work has: the number is below one because the qualification checks a corpus
> would need before anyone should trust a band are still unimplemented, and
> `index` says so in every result it emits.
>
> What is out of reach for most people is **calibration**. A band claim needs
> roughly 600 documents of your own writing plus a distractor corpus, so until
> you have that, `score` reports distances without a band and `rewrite` needs
> you to name paragraphs yourself. See [PROJECT.md](PROJECT.md) for the
> component-by-component state.

---

## What makes this different

Most tools in this space do one of two things. Either they strip "AI-sounding" words
against a banned list, which produces a generic human voice rather than yours, or they
measure style and decline to change anything. `hapax` closes the loop:

```
your corpus → measured profile → score the draft → rewrite what misses → rescore → repeat
```

Every rewrite is gated. A pass is kept only if it measurably moves closer to your profile,
preserves every name, number and quotation the draft carried, introduces no new writing
system, and splices back as the paragraph it replaced. **On the distance, the output is
never worse than the input** — that is a property of the acceptance rule, not an
aspiration.

One gate does not currently function, and the tool says so rather than letting you assume
otherwise. The AI-tell comparison counts only rules validated against a corpus, and none of
the twenty-two shipped rules is: validating them was attempted and the corpus cannot
support it. So the gate accepts everything, and every rewrite result carries
`tells_inactive_reason: no-validated-rule` until that changes. The other four gates are
real.

## It tells you when it doesn't know

Stylometric measures need sample size. Burrows' Delta, the standard authorship distance,
assumes stable word frequencies over hundreds of words; run it on a single paragraph and
most counts are zero or one, and the result is noise wearing a number.

So `hapax` tiers its features by the sample each one needs, scores short passages only on
the measures that survive at that length, and returns **`insufficient evidence`** rather
than a fabricated score when the sample is too small.

It applies per paragraph too. A paragraph below the profile's lexical floor is not
scored at all, and `score` names it — its location in your file, its token count, and
the floor it failed — rather than reporting a number nobody should act on. A one-word
paragraph once scored as the *most* deviant passage in a document, which is how that
floor came to exist.

The same honesty applies to the profile as a whole. `hapax eval` holds out whole documents
from your corpus, tests whether the profile can actually distinguish your writing from
other people's, and publishes the number. Below a predeclared floor, the profile is marked
`uncalibrated` and `hapax` refuses to emit a score band at all.

If you want a tool that always gives you a confident number, there are many. This is not
one of them.

## Try it without an API key

`score`, `tells` and `eval` make no network calls and require no model:

```bash
hapax tells draft.md                     # deterministic AI-tell linter, needs nothing
hapax index ./writing --profile essays   # build a profile from your own work
hapax eval --profile essays --distractors ./others   # can it actually distinguish you?
hapax score draft.md --profile essays    # per-paragraph distance, and what was skipped
```

Only `hapax rewrite` needs a model. It uses a local Ollama model by default; set an API key
to use a stronger one.

## Your corpus stays on your machine

The profile is built locally and never leaves. When `rewrite` calls a cloud model it sends
the draft passage and a handful of exemplar sentences — never the corpus. `--local-only`
(or `HAPAX_LOCAL_ONLY=1`) is a hard guarantee, verified by a test that fails on any dial
outside loopback: no cloud provider is constructed, no credential is read, no telemetry is
emitted. Loopback, because the default provider is Ollama on localhost — the guarantee is
about where bytes go, not whether a socket opens. A cloud failure is an error, never a silent downgrade.

## It will not learn from itself

A tool that rewrites your prose and then measures you against its own output is measuring
itself. `hapax` closes that loop in both directions.

`index` screens your corpus for paragraphs it published and refuses them as
`rejected-tool-output`, so a rewrite you saved into your writing directory stops counting
as your style. In the corpus this was found in, one file contributed a paragraph that held
63 of the 64 Han letters anywhere in the corpus — the tool's own output, from a rewrite
that had gone wrong, about to be learned from as though it were the author's.

`rewrite` refuses to re-target a paragraph it wrote. Every guard anchors on the paragraph
the run started from, so rewriting in place and running again would have anchored the
second run on the first run's output, and each step would have been individually
admissible while the composition was not.

Neither catches a paragraph you edited after the tool wrote it, and that is deliberate:
anything you touched is yours.

> Both are detected by content hash, so the first `index` after upgrading may report fewer
> eligible documents than the last one. It also re-IDs every snapshot — the screen's
> version is part of snapshot identity — which means new profile IDs and, once the old
> profile is pruned, the loss of any calibration built on it.

## What this is not

**This is not an AI-detector evasion tool.** It requires a substantial corpus of writing
you did yourself, ideally from before you used AI assistance, because it has nothing to
work from otherwise. It cannot make someone else's writing sound like you, and it is not
built to help anyone pass off work as their own.

The problem it solves is voice fidelity: you drafted with a model, the result is
serviceable and doesn't sound like you, and you would like your own register back.

## Documentation

- [`docs/DESIGN.md`](docs/DESIGN.md) — architecture and dependency order
- [`docs/adr/`](docs/adr/) — architecture decision records
- [`docs/REVIEW.md`](docs/REVIEW.md) — adversarial design review log

## License

Apache-2.0. See [LICENSE](LICENSE).
