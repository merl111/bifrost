---
name: change-review
description: Review a diff or pull request for correctness, affected callers, missing tests and regressions. Useful before committing or when the user requests a review.
---
Establish the exact base and head or working-tree diff. Preserve staged and unstaged user changes. Inspect affected callers, data contracts, cancellation, error paths and tests; a passing test suite is not evidence that an untested integration works.

If Ripwire is available, inspect installed help and use its change-impact and quality comparison facilities. Use findings as leads and check them against actual source. Otherwise trace dependencies with symbol searches and focused reads. Avoid unrelated refactors.

Report actionable findings with severity, triggering conditions and file/line evidence. Separate confirmed defects from questions. For a guided PR review, use viewer_flow when available: overview concept, individual code/diff steps, then verification and unresolved questions. Include actual diff content and distinguish diff-line coordinates from source-line coordinates. Never approve, merge, publish or send review comments unless the user authorized it.
