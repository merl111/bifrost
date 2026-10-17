---
name: code-walkthrough
description: Explain a concept, code flow, architecture or pull request as navigable steps with explanations and relevant code or diffs.
---
Inspect the relevant source and tests before composing the explanation. Start from the user's concrete question and build a short causal sequence: context, entry point, important transformations, output and edge cases. Each step should explain what happens and why it matters, not repeat the code.

When viewer_flow is available, publish one complete ordered walkthrough. Use titled steps with explanation; kind code with a workspace-relative path and source start_line/end_line, kind concept for context without a file, or kind diff with the actual unified diff in content. Diff highlight coordinates index the displayed diff, not the original file. Keep to 1–64 steps, 16 KiB explanation per step and 128 KiB total. The user controls Back/Next; do not repeatedly replace their presentation while they navigate. Leave edits and staging to separate explicit requests.

When no viewer tool is available, provide numbered steps with code references in chat and state that interactive viewer controls are unavailable with this backend. Do not claim to have opened the viewer. For PR reviews, distinguish behavior changes, confirmed issues, and open questions, and identify which base/head you reviewed.
