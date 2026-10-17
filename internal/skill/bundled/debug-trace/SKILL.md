---
name: debug-trace
description: Diagnose a crash, incorrect result, regression or failing test by tracing a concrete symptom to its cause; use for debugging rather than general exploration.
---
Capture the exact symptom and a minimal reproducer. Search for the error or failing symbol, then inspect the caller, state ownership and failure boundary. If Ripwire is installed, inspect its help for trace and caller queries; otherwise use bounded text searches.

Form a falsifiable hypothesis and run the smallest check that can distinguish it from competing causes. Preserve the original failing evidence. Fix the responsible boundary, not merely the visible symptom. Add a regression for the behavior when it is stable and meaningful. Report what reproduced, what changed and what remains unverified. Do not use production credentials or paid inference to make an incidental test pass.
