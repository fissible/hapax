# hapax

**Rewrite AI-drafted prose into your own voice, measured against your own prior writing.**

`hapax` builds a stylometric profile from writing you did yourself, scores a draft against
it, and rewrites only the passages that miss — verifying after every change that the prose
moved *toward you* rather than merely away from the model.

The name is from *hapax legomenon*: a word appearing exactly once in a corpus.

> **Status: pre-release.** No version is tagged yet. All six commands below
> run; fourteen of fifteen library components are built and tested, and the
> CLI that was the last of them is now complete.
>
> What is still out of reach for most people is **calibration**. A band claim
> needs roughly 600 documents of your own writing plus a distractor corpus, so
> until you have that, `score` reports distances without a band and `rewrite`
> needs you to name paragraphs yourself. See [PROJECT.md](PROJECT.md) for the
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
preserves the draft's meaning, and does not increase AI tells. **The output is never worse
than the input** — that is a property of the acceptance rule, not an aspiration.

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
