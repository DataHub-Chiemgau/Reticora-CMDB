# Defect tests of the Phase-1 acceptance audit

The Phase-1 acceptance audit (`audit/phase1-final/`) encoded every confirmed finding as a test that fails
until the finding is fixed. The assertions encode the requirement, not the observed behaviour; they are not
relaxed to make a test pass. The G1 remediation plan (`docs/plan/g1-korrektur/`) fixes the findings work
package by work package.

| Kind       | Where                                                                  | Run                                                                 |
| ---------- | ---------------------------------------------------------------------- | ------------------------------------------------------------------- |
| `go`       | test functions in `*_test.go` files with `//go:build auditdefect`      | `go test -tags auditdefect -run <Test> <package>`                   |
| `sh`       | cases of `tests/audit/*.test.sh`                                       | `bash tests/audit/<name>.test.sh`                                   |
| `vitest`   | `frontend/src/**/*.auditdefect.ts(x)`                                  | `cd frontend && npm run test:auditdefect`                           |
| `e2e-real` | `frontend/e2e-real/*.spec.ts` (real stack, no mocks)                   | `npx playwright test -c e2e-real/playwright.real.config.ts`         |

## Ratchet (WP-337)

`expected-failures.txt` lists every defect test that is red today, one line per test:

```text
art;ort;test;befund
```

- `go`: `ort` = module and package path (`backend/internal/discovery`), `test` = name of the test function.
- `sh`: `ort` = script path, `test` = case. A case is the text of an `ok   <text>` or `FAIL <text>` line up to
  the first `: ` (the whole text if there is none). Lines with the same case form one case; it is red as soon
  as one of its lines is `FAIL`.
- `vitest`: `ort` = file path, `test` = full test name (describe titles and title, separated by blanks).
- `e2e-real`: `ort` = `<project>:<spec file>` with the Playwright project of `playwright.real.config.ts`
  (`chromium`, `edge`, `firefox`, `webkit`, `mobile-chrome`, `mobile-safari`, WP-532) from `projectName` of the
  report, `test` = full title. A test that is red in two projects needs a line for each of them.
- `befund` = finding ID from `audit/phase1-final/PHASE1_FINDINGS.md`, from F-355 on from `audit/g1-reaudit/FINDINGS.md`.

`run-auditdefect.sh` runs the defect tests and compares each result with the list. The CI job `auditdefect`
(`.github/workflows/ci.yml`) compares `go`, `sh` and `vitest`; `install-smoke.yml` compares `e2e-real`
(WP-338): each job of the matrix `e2e-real` the lines of its project (`compare --projects <project>`), the job
`e2e-real-ratchet` the results of all projects together, where a line for a project that did not run is `verwaist`
(WP-532). The job fails on:

| Result                                         | Meaning                                                                   |
| ---------------------------------------------- | ------------------------------------------------------------------------- |
| `überführen`                                   | a listed test is green: move it into the regular suite, delete its line   |
| `Rückfall`                                     | an unlisted test is red: a fixed finding came back, or a new defect test  |
| `verwaist`                                     | a listed test was not run (renamed, moved, file deleted): delete the line |
| `nicht ausgeführt`                             | a listed test was skipped                                                 |
| `Abbruch`                                      | build failure, panic or missing setup (tool, variable, service)           |

A test that is listed and red is allowed. A green test that is not listed (helpers, controls) is fine.

Run locally like the CI job (PostgreSQL with all migrations applied, NATS JetStream, Redis):

```sh
TEST_DATABASE_URL=postgres://… TEST_NATS_URL=nats://localhost:4222 TEST_REDIS_URL=redis://localhost:6379/0 \
TEST_NATS_SERVER_BIN=/path/to/nats-server KUSTOMIZE=… TERRAFORM=… AMTOOL=… \
  ./tests/audit/run-auditdefect.sh --kinds go,sh,vitest --out /tmp/auditdefect
```

`run-auditdefect.test.sh` is the self-test of the comparison.

## Moving a fixed test into the regular suite

The work package that fixes a finding makes its defect test green and moves it in the same pull request:

- **Go:** move the test function into a regular `_test.go` file of the package (no build tag), or remove the
  `//go:build auditdefect` line once the file has no red test left. Tests that encoded the wrong behaviour as
  expected are rewritten.
- **Frontend:** rename `.auditdefect.tsx` to `.test.tsx` and `.auditdefect.ts` to `.test.ts` (for example
  `src/auth/session.auditdefect.ts`, `src/api/deprecatedPaths.auditdefect.ts`); a file with remaining red tests
  keeps them under the old name.
- **Shell:** call the script from the regular CI job that covers its area (or move it to `tests/`) once all
  its cases are green.
- **Real stack:** the spec stays in `frontend/e2e-real/`.

In every case delete the test's line from `expected-failures.txt`.
