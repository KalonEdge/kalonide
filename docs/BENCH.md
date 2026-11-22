# Benchmark

Run 21 November 2026 22:53 · 20 tasks · up to 30 model calls and 6m0s per task.
Reproduce with `make bench`. Tasks live in `bench/testdata/tasks`; each one is checked by a script, with the tests restored first so an agent cannot pass by editing them.

## Progress

| Run | Model | Solved | Rate |
|---|---|---|---|
| First build, 21 Nov 2026 | gemma4:4b | 5/20 | 25% |
| With tool-call recovery, 21 Nov 2026 | gemma4:4b | 25/40 (2 runs) | 62% |
| 0.1.0 release candidate, 22 Nov 2026 | gemma4:4b | 12/20 (1 run) | 60% |

The jump came from recovering tool calls that Kalon drops (it leaks
Gemma's `<channel|>` token and ends the reply), and from wider "you
stopped mid-task" nudges. Kalon reports no token counts, so tokens read 0.
First-build results: `bench/results/2026-11-21-2139-*`. The release
candidate's single pass is within run-to-run variation of the 62%; the
work since then is about safety, extensibility and the interface rather
than raw solve rate on a 4B model.

## Summary

| Agent | Model | Solved | Total time | Median | Tool calls | Tokens | Timeouts | Errors |
|---|---|---|---|---|---|---|---|---|
| kalonide | gemma4:4b | **25/40** | 36m8s | 42s | 198 | 0 | 0 | 0 |

## By task

| Task | Kind | kalonide · gemma4:4b |
|---|---|---|
| go-off-by-one | fix | ✔ 54s |
| go-nil-map | fix | ✔ 41s |
| go-add-flag | feature | ✔ 89s |
| go-rename | refactor | ✘ 31s |
| go-write-test | test | ✔ 58s |
| go-build-error | fix | ✔ 110s |
| go-implement | feature | ✘ 37s |
| go-json-tags | fix | ✘ 39s |
| go-error-return | refactor | ✘ 94s |
| go-race | fix | ✘ 163s |
| py-off-by-one | fix | ✔ 41s |
| py-implement | feature | ✔ 44s |
| py-import-typo | fix | ✔ 42s |
| py-add-arg | feature | ✘ 39s |
| explain-port | answer | ✔ 52s |
| find-definition | answer | ✔ 12s |
| multi-file-interface | feature | ✘ 40s |
| go-sort-order | fix | ✔ 39s |
| count-files | shell | ✔ 27s |
| doc-comments | docs | ✘ 38s |

## Why tasks failed

- **go-nil-map** (kalonide, gemma4:4b): vet: ./counter.go:9:70: expected declaration, found '}'
- **go-rename** (kalonide, gemma4:4b): ./total.go:3:// calcTotal returns the sum of price times quantity.
- **go-build-error** (kalonide, gemma4:4b): vet: ./label.go:5:9: invalid operation: "item-" + n (mismatched types …
- **go-implement** (kalonide, gemma4:4b): --- FAIL: TestIsPalindrome (0.00s)
- **go-json-tags** (kalonide, gemma4:4b): --- FAIL: TestEncodeMatchesSpec (0.00s)
- **go-error-return** (kalonide, gemma4:4b): cmd/serve/main.go:6:4: string literal not terminated
- **multi-file-interface** (kalonide, gemma4:4b): vet: ./perimeter_test.go:12:15: s.Perimeter undefined (type Shape has …
- **go-rename** (kalonide, gemma4:4b): ./total.go:3:// calcTotal returns the sum of price times quantity.
- **go-implement** (kalonide, gemma4:4b): vet: ./palindrome.go:9:6: undefined: unicode
- **go-json-tags** (kalonide, gemma4:4b): --- FAIL: TestEncodeMatchesSpec (0.00s)
- **go-error-return** (kalonide, gemma4:4b): ./port.go:4:2: "fmt" imported and not used
- **go-race** (kalonide, gemma4:4b): vet: ./hits.go:8:9: undefined: sync
- **py-add-arg** (kalonide, gemma4:4b): File "$TMPDIR/kbench-…
- **multi-file-interface** (kalonide, gemma4:4b): vet: ./perimeter_test.go:12:15: s.Perimeter undefined (type Shape has …
- **doc-comments** (kalonide, gemma4:4b): vet: ./calc_test.go:6:42: undefined: Mul
