# JevCI

Zero-history semantic test impact analysis for CI. JevCI answers one research question: can a fast decision model provide useful zero-history semantic test selection cheaply enough to run on every pull request?

Given `base..head`, JevCI computes which Go test packages to run. It applies deterministic static rules first, then scores the non-obvious candidates with Jev (TypeSafe's decision model), then maps every target to `RUN`, `RUN PACKAGE`, or `SKIP`.

## Status

v0.1. Go repositories only. The first benchmark (kubernetes-sigs/kueue, 12 PRs) is published below; its recall column is not yet discriminating.

## How it works

```
git diff base..head
        │
        ▼
ignore globs ──► file→package attribution (go list)
        │
        ▼
reverse import graph ──► static classification
        │
        ▼
┌────────────────────────────────────────────────┐
│ changed / changed-test / protected / direct    │──► deterministic RUN
│ transitive (or all, per config)                │──► Jev scores P(impact)
│ unrelated                                      │──► SKIP
└────────────────────────────────────────────────┘
        │
        ▼
policy ──► RUN / RUN PACKAGE / SKIP per test package
```

| Class | Meaning | Default action |
|---|---|---|
| changed | non-test file in the package changed | RUN |
| changed-test | `*_test.go` in the package changed | RUN |
| protected | matches `protected_tests` | RUN |
| direct | imports a changed package (distance 1) | RUN |
| transitive | imports a changed package (distance ≥ 2) | Jev-scored |
| unrelated | no import dependency on changes | SKIP, or Jev-scored with `candidates: all_unselected` |

### Architecture

```
cmd/jevci            CLI (plan, version)
cmd/jevci-bench      benchmark runner and report
internal/gitdiff     git plumbing, name-status and hunk parsing
internal/golang      go list wrapper, test discovery, changed-symbol extraction
internal/dependency  reverse import graph and BFS distance to changed packages
internal/config      .jevci.yaml defaults, validation, glob matching
internal/policy      pure decision function: class + score + config -> action
internal/semantic    Scorer interface, Candidate/Change types, mock scorer
internal/semantic/jev  HTTP client for POST /v1/systemone and batching
internal/planner     orchestration into a Plan
internal/output      human table, JSON, package list
benchmark/           metrics schema, go test -json parser, suite runner
```

The planner depends on `semantic.Scorer`, not on the Jev client. Tests use `semantic.MockScorer`; no test needs a live API key.

## Install

```sh
go install github.com/abdelrahmanmagdii/jevci/cmd/jevci@latest
export TYPESAFE_API_KEY=...   # required for the jevci strategy
```

## CLI usage

```sh
jevci plan --base origin/main            # human table
jevci plan --base origin/main --json     # machine-readable
jevci plan --base origin/main --packages # selected package list for go test
jevci plan --base origin/main --strategy static
jevci version
```

Flags: `--head` (default `HEAD`), `--repo` (default `.`), `--config` (default `.jevci.yaml`), `--strategy jevci|static|changed|full`, `--no-jev`, `--timeout`.

Real output from `jevci plan --base HEAD~2 --no-jev` on `github.com/prometheus/client_golang` (merge-base `d2f148ba`, head `9bf26f79`):

```
JevCI

Changed packages:
  ./api/prometheus/v1

15 test targets discovered.

TEST TARGET                                        SOURCE     RELEVANCE  ACTION
./api                                              unrelated  0%         SKIP
./api/prometheus/v1                                changed    100%       RUN
./internal/github.com/golang/gddo/httputil         unrelated  0%         SKIP
./internal/github.com/golang/gddo/httputil/header  unrelated  0%         SKIP
./prometheus                                       unrelated  0%         SKIP
./prometheus/collectors                            unrelated  0%         SKIP
./prometheus/collectors/version                    unrelated  0%         SKIP
./prometheus/graphite                              unrelated  0%         SKIP
./prometheus/internal                              unrelated  0%         SKIP
./prometheus/promauto                              unrelated  0%         SKIP
./prometheus/promhttp                              unrelated  0%         SKIP
./prometheus/promhttp/zstd                         unrelated  0%         SKIP
./prometheus/push                                  unrelated  0%         SKIP
./prometheus/testutil                              unrelated  0%         SKIP
./prometheus/testutil/promlint                     unrelated  0%         SKIP

Selected: 1 / 15 targets
Reduction: 93.3%
```

Larger repos show the tiering more clearly. Illustrative output:

```
JevCI

Changed packages:
  ./pkg/controller
  ./pkg/cache

184 test targets discovered.

TEST TARGET                     SOURCE       RELEVANCE     ACTION
pkg/controller                  static       100%          RUN
pkg/scheduler                   Jev           83%          RUN
pkg/admission                   Jev           41%          RUN PACKAGE
pkg/metrics                     Jev            7%          SKIP

Selected: 52 / 184 targets
Reduction: 71.7%
Jev planning time: 1.4s
```

## JSON output

`--json` emits a stable schema (version 1). Trimmed real output from the same client_golang run:

```json
{
  "version": 1,
  "base": "HEAD~2",
  "head": "9bf26f79511fed7c5143fb78443f5590ec6836cd",
  "merge_base": "d2f148ba5de4e4e1d072740ced923f869f46f4a1",
  "strategy": "jevci",
  "changed_files": ["api/prometheus/v1/api.go", "api/prometheus/v1/api_test.go"],
  "changed_packages": ["./api/prometheus/v1"],
  "selected_packages": ["./api/prometheus/v1"],
  "unattributed_files": [],
  "nested_module_files": [],
  "targets_total": 15,
  "targets_selected": 1,
  "reduction_percent": 93.3,
  "jev": {"enabled": false, "model": "", "requests": 0,
          "input_tokens": 0, "candidates": 0, "latency_ms": 0,
          "cost_usd": 0, "error": ""},
  "targets": [
    {"package": "./api/prometheus/v1",
     "import_path": "github.com/prometheus/client_golang/api/prometheus/v1",
     "class": "changed", "distance": 0, "via": [],
     "action": "RUN", "source": "changed", "relevance": 1,
     "reason": "package changed"}
  ]
}
```

GitHub Actions:

```yaml
- uses: actions/checkout@v4
  with:
    fetch-depth: 0
- uses: actions/setup-go@v5
- run: go install github.com/abdelrahmanmagdii/jevci/cmd/jevci@latest
- name: Plan
  env:
    TYPESAFE_API_KEY: ${{ secrets.TYPESAFE_API_KEY }}
  run: jevci plan --base origin/${{ github.base_ref }} --json > jevci.json
- uses: actions/upload-artifact@v4
  with:
    name: jevci
    path: jevci.json
- name: Test selected packages
  run: |
    if [ "$(jq '.targets_selected' jevci.json)" -gt 0 ]; then
      go test $(jq -r '.selected_packages[]' jevci.json)
    fi
```

See `examples/github-actions.yml` for the full workflow.

## Configuration

`.jevci.yaml` controls thresholds, batching, safety rules, and ignore globs. Every key has a default; a missing file means defaults. See `examples/.jevci.yaml` for the annotated full config. `jevci plan` resolves a default `--config` inside `--repo` when it is absent from the working directory.

## Safety model

JevCI fails open. These rules apply in order:

- Always run changed packages, changed tests, protected packages, and direct dependents (each rule is configurable).
- Jev score ≥ `run_threshold` → `RUN`. Between `uncertain_threshold` and `run_threshold` → `RUN PACKAGE`. Below → `SKIP`.
- Jev error → fail-open `RUN PACKAGE` for every affected candidate; `fail_open: false` turns a Jev error into `SKIP`. A missing API key or disabled Jev always yields `RUN PACKAGE` for every candidate.
- `go.mod`, `go.sum`, `go.work`, `go.work.sum`, `vendor/modules.txt`, or an unattributed non-ignored file → run everything.
- `RUN` and `RUN PACKAGE` both execute `go test <pkg>` in v0.1. The distinction reserves room for per-test `-run` narrowing.

## How Jev is used

Jev scores only the non-obvious candidates: transitive dependents by default, or all unselected packages with `candidates: all_unselected`. It prunes within the static set; it does not add tests.

- One request per batch of `batch_size` candidates, `concurrency` requests in parallel.
- State per request: the budgeted diff, changed files, changed symbols, and per-candidate test names, test files, package doc, and the import chain to the changed package.
- One Noul question per candidate, keyed `candidate_i`, referencing `candidates[i]` in the state. Noul returns P(yes) directly, so no extra model call is needed for calibration.
- jev-1.13 limits: 32k tokens for state plus the longest question, 64k total per request. JevCI estimates tokens as chars/4 and keeps state under `max_state_chars` by shrinking the diff, then halving the batch.
- Input price: $0.042 per million input tokens. `--json` reports `cost_usd` per run.
- Retries with exponential backoff on 429/529/5xx and network errors, honouring `Retry-After`. 401 and 422 fail without retry.

## Limitations

- Single Go module only; `go.work` multi-module workspaces are not modelled.
- `go list` reads the working tree, so `--head` must be checked out.
- Selection granularity is the package, not the test.
- Couplings outside the import graph (reflection, config files, code generation) are invisible to static analysis. Jev sees them only with `candidates: all_unselected`.
- Build tags and cgo are not modelled.
- Deleted packages fall back to run-all.
- Any `go.mod`/`go.sum` change runs everything, including dependency bumps.
- Files in nested modules are ignored by default (`nested_modules: run_all` to run everything).
- Jev uses no historical test-failure data, by design.

## Benchmark methodology

For each historical PR: check out head, compute base via merge-base, plan every strategy, and run the suite with `go test -json` to get per-package results and durations.

The tested universe is configurable per suite: `packages` gives `go list` patterns (default `["./..."]`) and `exclude_package_regex` filters the resolved list. Kueue's suite uses `exclude_package_regex: "/test/"`, which mirrors its own `make test`. Selections are restricted to the tested universe before metrics are computed.

Strategies: `full`, `changed`, `static`, `jevci`, and `jevci:no-direct`. The last variant is `jevci` with `always_run_direct_dependents: false`, so Jev also scores direct dependents.

Ground truth comes from the revert oracle (`oracle: revert`): the PR's non-test source files are restored to base (`_test.go`, `vendor/`, and `testdata/` excluded), the suite runs again, and packages that fail on revert but pass at head form the impact set. Failures that also fail at head are noise and are subtracted. `oracle: head` is the fallback: packages failing at head.

Metrics per strategy (`benchmark/metrics.go`): targets total, selected, reduction %, selected-suite runtime (the serial sum of selected package durations), runtime reduction %, failed detected/missed, recall against the oracle's impact set, plan time, Jev latency, Jev tokens and cost.

## Benchmark results: kubernetes-sigs/kueue, 12 PRs (2026-09-18)

**On this suite Jev cut the selected unit-test set from 60% (static) to 79% reduction with no recall loss, but the oracle found zero cross-package regressions, so recall does not yet separate the strategies.**

Setup: 12 merged PRs (`benchmark/suites/kueue.yaml`), unit-test universe of 113 packages (`go list ./... | grep -v /test/`, kueue's own `make test` set), revert oracle, model `jev-1.13.0`. Full unit suite: 359 s serial mean per PR. Raw data: `benchmark/results/kueue-2026-09-18.jsonl`; rendered report: `benchmark/results/kueue-2026-09-18.md`.

| Strategy | Mean target reduction | Mean runtime reduction | Oracle failures detected / total | Recall | Mean Jev latency per PR | Jev cost, 12 PRs |
|---|---|---|---|---|---|---|
| full | 0.0% | 0.0% | 24 / 24 | 1.00 | – | – |
| changed | 97.9% | 93.6% | 24 / 24 | 1.00 | – | – |
| static | 60.0% | 48.2% | 24 / 24 | 1.00 | – | – |
| jevci | 78.7% | 67.1% | 24 / 24 | 1.00 | 3.5 s | $0.034 |
| jevci:no-direct | 87.0% | 77.3% | 24 / 24 | 1.00 | 3.2 s | $0.066 |

What the numbers say:

- Jev adds 18.7 points of target reduction and 18.9 points of runtime reduction over static dependency selection, at about $0.003 per PR and 3.5 s of planning time (807k input tokens over 12 PRs).
- Every one of the 24 oracle failures sits inside a package the PR itself changed. No PR produced a cross-package unit-test failure when its source was reverted. That is why `changed` also scores 1.00: on this suite the oracle cannot tell a safe pruner from a reckless one.
- In this unit-test benchmark, transitive candidates scored 0.05–0.50. No Jev score reached 0.70. The changed-package calibration below produces higher scores, but it does not establish that low transitive scores are safe.
- This run excludes Kueue's integration suites (`test/integration/...`, envtest). Those suites are the next candidates for finding cross-package oracle failures. Integration testing does not guarantee such failures.

Do not read the reduction percentages as safe-to-skip percentages. They are upper bounds on savings; the recall column is the one that must hold, and it has not yet been stress-tested.

## Changed-package calibration: 12 PRs (2026-09-19)

**The current prompt passes a positive-case sanity check. This result does not prove that semantic selection improves cross-package recall.**

This measured run scores 28 changed-package cases across the same 12 PRs. It bypasses static selection for scoring only. Production safety rules, the Jev question, and thresholds stay unchanged.

The recorded revert oracle identifies 24 of those cases as positives. A case means one package in one PR. The other four cases are not confirmed negatives. Oracle labels are not sent to Jev.

| Measurement | Result |
|---|---|
| Resolved model | `jev-1.13.0` |
| Oracle-positive cases scored | 24 / 24 |
| Positive score mean / median | 0.8404 / 0.90 |
| Positive score range | 0.49–0.94 |
| Positive scores ≥ 0.70 | 21 |
| Positive scores ≥ 0.30 and < 0.70 | 3 |
| Positive scores < 0.30 | 0 |
| All changed-package cases scored | 28 |
| Successful API requests | 13 |
| Reported input tokens | 111,969 |
| Cost at $0.042 per million input tokens | $0.004703 |

The three lower positive scores are:

| PR | Package | Score |
|---|---|---|
| 15773 | `./pkg/controller/jobs/trainjob` | 0.55 |
| 15284 | `./pkg/controller/concurrentadmission` | 0.49 |
| 15284 | `./pkg/dra` | 0.67 |

All 24 positives exceed the 0.30 selection threshold. These are score-only comparisons, not new production decisions. Static rules already protect changed packages.

The prompt can produce high scores for direct changes. Low transitive scores still need an independent test. This positive-case check cannot measure probability calibration or safe-skip precision. Changed test code in the diff can also make these cases easier than unchanged cross-package tests.

Raw data: `benchmark/results/kueue-calibration-2026-09-19.jsonl`. Each record keeps the input change, candidates, configuration, oracle labels, raw probabilities, model, and usage. Errors remain errors; they are not zero scores.

To repeat the check, set the API key environment variable and use a new output path:

```sh
go run ./cmd/jevci-bench calibrate \
  --suite benchmark/suites/kueue.yaml \
  --oracle benchmark/results/kueue-2026-09-18.jsonl \
  --out benchmark/results/kueue-calibration-new.jsonl
```

The command requires a clean existing checkout and pinned commit IDs. It restores the original checkout on normal completion or a handled error. It does not fetch, rerun tests, or rerun the oracle. It refuses to overwrite an existing output file. An abrupt process termination can leave the last PR checked out.

## Integration pilot: Kueue PR 15651 (2026-09-19)

**Both suites pass at head and after the source revert. This measured pilot cannot establish a recall advantage for Jev.**

The pilot uses two integration suites: TAS and scheduler. PR 15651 changes scheduler source and unit tests, but no integration test files. The oracle restores only `pkg/scheduler/scheduler.go` to base.

Environment: macOS arm64, Go 1.26.6, Kubernetes 1.37.0, etcd 3.7.0, and Ginkgo seed 15651. Packages run sequentially without the race detector. Envtest starts local control planes; it does not use an existing cluster.

| Integration suite | Specs passed at head | Specs passed after revert | Failed or skipped specs in either phase |
|---|---|---|---|
| TAS | 147 | 147 | 0 |
| Scheduler | 132 | 132 | 0 |
| Total | 279 | 279 | 0 |

| Strategy | Selected suites | Oracle recall |
|---|---|---|
| full | 2 / 2 | n/a |
| changed | 0 / 2 | n/a |
| static | 2 / 2 | n/a |
| jevci | 2 / 2 | n/a |
| jevci:no-direct | 2 / 2 | n/a |

The oracle finds zero impact packages. Recall is undefined, not 1.00. Jev selects the same two suites as static analysis, so this pilot shows no selection saving either. The `changed` baseline skips both suites, but the absence of oracle failures does not establish safe skipping.

This targeted pilot validates the test setup and complete-result checks. It does not represent all Kueue integration tests. A full 12-PR integration run has not started. First establish an independent cross-package positive before spending more on an expanded benchmark. Keep any synthetic fault-injection study separate from historical-PR results.

Raw benchmark record: `benchmark/results/kueue-integration-pilot-2026-09-19.jsonl`. Evidence snapshot: `benchmark/results/kueue-integration-pilot-2026-09-19-evidence.json`. The snapshot records spec counts, package durations, environment, and SHA-256 hashes of the raw files. Full test events remain local under `.bench/kueue-integration-pilot-2026-09-19-artifacts/pr-15651/`.

The captured record preserves an older oracle-runtime bug: `oracle.runtime_ms` counts only failing packages and is zero here. The evidence snapshot contains the actual package durations. New runs count passing packages too. Planner usage in the record includes candidates outside the two tested suites; it is not a cost measurement limited to those suites.

The suite file is `benchmark/suites/kueue-integration-pilot.yaml`. `require_head_pass: true` prevents an oracle run after a failed or skipped head package. `artifacts_dir` keeps raw stdout and stderr. The runner rejects interrupted or incomplete results and refuses to overwrite result files or raw logs.

## Controlled source-only replay: Kueue PR 15365 (2026-09-19)

**This measured replay exposes a cross-package failure that changed-only selection misses. Jev still shows no advantage over static selection in this case.**

The historical bug drops field selectors when listing pods for a workload slice. The replay holds all tests and other source at the fixed commit. It restores only `pkg/workloadslicing/pods.go` to the buggy version. The predeclared test universe contains workloadslicing, TAS controller, and core controller.

This is not recall measured on the unmodified historical PR. That PR adds the TAS regression cases, so the changed-test rule selects TAS for the original PR. Only the controlled replay treats those tests as fixed. Reports label this experiment `source_replay` and keep its aggregates separate from `historical_pr` results.

| Package | Fixed source | Buggy source | Relationship to changed source |
|---|---|---|---|
| workloadslicing | pass | fail | changed package |
| TAS controller | pass | fail | direct dependent |
| core controller | pass | pass | direct dependent |

| Strategy | Selected packages | Oracle packages detected | Recall |
|---|---|---|---|
| full | 3 / 3 | 2 / 2 | 1.00 |
| changed | 1 / 3 | 1 / 2 | 0.50 |
| static | 3 / 3 | 2 / 2 | 1.00 |
| jevci | 3 / 3 | 2 / 2 | 1.00 |
| jevci:no-direct | 3 / 3 | 2 / 2 | 1.00 |

The TAS failures are two `TestNodeFailureReconciler` cases. An unrelated running pod incorrectly prevents node replacement. Both assertions expect an unhealthy-node entry but receive none. These are assertion failures, not build failures.

A separate confirmation runs `TestNodeFailureReconciler` with `-count=3` in each phase. Each regression case passes 3/3 times at head, fails 3/3 after the source revert, and passes 3/3 after restoration. Test files remain unchanged throughout.

Default JevCI retains TAS through its direct-dependent safeguard. It does not score TAS in this mode. Without that safeguard, model `jev-1.13.0` scores TAS at 0.55 and core at 0.66. Both exceed the unchanged 0.30 selection threshold, so both packages still run. The core package passes this replay; that result does not prove it is safe to skip for other changes.

The next useful cases need transitive positives to exercise Jev's default scoring path. Do not tune thresholds from this single, targeted replay. No production safeguard or Jev prompt changed.

Suite: `benchmark/suites/kueue-source-replay.yaml`. Raw record: `benchmark/results/kueue-source-replay-2026-09-19.jsonl`. Evidence: `benchmark/results/kueue-source-replay-2026-09-19-evidence.json`. The evidence records the exact failing cases, confirmation outcomes, raw probabilities, and artifact hashes. Plans are saved before tests run. Full plans and test events remain local under `.bench/kueue-source-replay-2026-09-19-artifacts/pr-15365/`.

Replay mode requires pinned commits, explicit modified non-test Go source files, a passing head baseline, and raw artifacts. Historical mode rejects `source_files`. To repeat the experiment, use a new `artifacts_dir` and a new output path. Planner usage includes candidates outside the three tested packages; it is not a cost measurement limited to that test universe.

## Roadmap

- Build a declared replay set with transitive positives before claiming a semantic-selection advantage. Keep controlled replay and historical-PR results separate.
- Measure cross-package recall before changing the prompt or thresholds. The positive-case sanity check above is complete.
- Per-test granularity (`RUN` vs `RUN PACKAGE` becomes real `-run` narrowing).
- Multi-module `go.work` support.
- Additional languages beyond Go.
- More CI providers beyond GitHub Actions.
- Packaged GitHub Action.
- Attribute go.mod dependency bumps to the packages that import the bumped module.
- Model version pinning and reproducibility.
