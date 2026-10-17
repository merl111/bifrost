---
name: code-explore
description: Map an unfamiliar codebase, locate a feature, trace callers and dependencies, or plan a change using source evidence. Supports Ripwire when available.
---
Start with repository instructions, Git status and a file map. Identify the entry point and service boundary relevant to the question before reading whole files.

If Ripwire is available, inspect its installed help and use bounded structural queries for symbols, callers and dependencies. CLI versions differ: do not invent commands. If it is absent, use file search, symbol search and focused source reads; do not install dependencies merely to explore.

Trace one concrete path from input to output, including error/cancellation paths and relevant tests. Distinguish a static call relationship from demonstrated runtime behavior. Before changing an interface, inspect its callers and reuse nearby implementations. Report source locations and remaining unknowns rather than a generic architecture inventory.
