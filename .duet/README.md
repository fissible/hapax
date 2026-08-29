# duet freeze records

One pair of files per slice, named for the slice:

    <slice>.ref       the freeze commit — the last `test:` commit before implementation
    <slice>.sha256    `shasum -c` compatible, listing that slice's frozen test files

## Why per-slice

There were previously two shared files, `.duet-freeze-ref` and `.duet-tests.sha256`,
each holding the state of whichever slice was in progress. Two problems, both of which
actually bit:

- **Every pair of parallel slices conflicted.** Both are single-value files that each
  slice rewrites wholesale, so two branches cut from the same `main` could never merge
  cleanly. PR #33 and PR #34 hit exactly this.
- **Re-freezing destroyed the record.** Amending a frozen suite means regenerating the
  hash file, and `find … > .duet-tests.sha256` overwrites it. On the `preserve` branch
  that discarded the entries for six earlier slices. It was caught only because the
  merge conflict put the two versions side by side.

Per-slice files have neither failure mode: two slices touch disjoint paths, and
regenerating one cannot clobber another.

## Verifying a freeze

The hash file alone answers "have these tests changed since I recorded them". The
stronger question is whether the recorded hash and the recorded commit agree, which
catches a hash file regenerated against an implementer's edits:

    shasum -a 256 -c .duet/<slice>.sha256
    git diff --stat "$(cat .duet/<slice>.ref)" -- <test paths>     # must be empty
    git show "$(cat .duet/<slice>.ref):<path>" | shasum -a 256     # must match the record

All three agree for every slice recorded here.

## Freezing a new slice

    git add <test paths> && git commit -m "test: <what this covers> (RED)"
    mkdir -p .duet
    find <test paths> -type f | sort | xargs shasum -a 256 > .duet/<slice>.sha256
    git rev-parse HEAD > .duet/<slice>.ref

Write to `.duet/<slice>.*` and nothing else. Re-freezing after an amendment rewrites
only that slice's two files, which is the point.
