# Benchmark

Run 21 November 2026 21:39 · 20 tasks · up to 30 model calls and 6m0s per task.
Reproduce with `make bench`. Tasks live in `bench/testdata/tasks`; each one is checked by a script, with the tests restored first so an agent cannot pass by editing them.

## Summary

| Agent | Model | Solved | Total time | Median | Tool calls | Tokens | Timeouts | Errors |
|---|---|---|---|---|---|---|---|---|
| kalonide | gemma4:4b | **5/20** | 7m45s | 14s | 48 | 0 | 0 | 0 |

## By task

| Task | Kind | kalonide · gemma4:4b |
|---|---|---|
| go-off-by-one | fix | ✘ 20s |
| go-nil-map | fix | ✘ 5s |
| go-add-flag | feature | ✘ 13s |
| go-rename | refactor | ✘ 6s |
| go-write-test | test | ✘ 7s |
| go-build-error | fix | ✔ 59s |
| go-implement | feature | ✘ 23s |
| go-json-tags | fix | ✘ 12s |
| go-error-return | refactor | ✘ 8s |
| go-race | fix | ✘ 5s |
| py-off-by-one | fix | ✔ 45s |
| py-implement | feature | ✘ 29s |
| py-import-typo | fix | ✘ 11s |
| py-add-arg | feature | ✘ 60s |
| explain-port | answer | ✘ 12s |
| find-definition | answer | ✔ 11s |
| multi-file-interface | feature | ✘ 22s |
| go-sort-order | fix | ✔ 28s |
| count-files | shell | ✔ 15s |
| doc-comments | docs | ✘ 75s |

## Why tasks failed

- **go-off-by-one** (kalonide, gemma4:4b): --- FAIL: TestAverage (0.00s)
- **go-nil-map** (kalonide, gemma4:4b): --- FAIL: TestCounter (0.00s)
- **go-add-flag** (kalonide, gemma4:4b): flag provided but not defined: -upper
- **go-rename** (kalonide, gemma4:4b): ./receipt.go:11:	return s + fmt.Sprintf("total: %.2f\n", calcTotal(ite…
- **go-write-test** (kalonide, gemma4:4b): no test file
- **go-implement** (kalonide, gemma4:4b): vet: ./palindrome.go:9:6: expected '(', found IsPalindrome
- **go-json-tags** (kalonide, gemma4:4b): --- FAIL: TestEncodeMatchesSpec (0.00s)
- **go-error-return** (kalonide, gemma4:4b): vet: ./port_test.go:6:15: assignment mismatch: 2 variables but ParsePo…
- **go-race** (kalonide, gemma4:4b): ==================
- **py-implement** (kalonide, gemma4:4b): ======================================================================
- **py-import-typo** (kalonide, gemma4:4b): Traceback (most recent call last):
- **py-add-arg** (kalonide, gemma4:4b): count 3: Hello, Ada!
- **explain-port** (kalonide, gemma4:4b): answer lacks 8087
- **multi-file-interface** (kalonide, gemma4:4b): vet: ./perimeter_test.go:9:31: cannot use Circle{…} (value of struct…
- **doc-comments** (kalonide, gemma4:4b): missing doc comments: Mul, Div, Clamp
