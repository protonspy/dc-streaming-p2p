# CLAUDE.md

Spec-driven development, scaffolded and checked by `scc`. The methodology lives in `.claude/rules/` — never inline it here.

## Rules — `.claude/rules/<name>.md`

Claude Code loads `.claude/rules/` at session start: they are in front of you, nothing to open.
The triggers below say *when* each governs — the failure is not a rule you never read,
it is one you had all along and applied at the wrong moment, or not at all.

`caveman.md` is always on: the register you answer in. The rest, by where you are:

- `autonomy.md` — at kickoff, before writing anything
- `routing.md` — work arrives and needs a vehicle: a spec, or a plan
- `methodology.md` — starting a task: which cycle, what to run first
- `verification.md` — code is written and you think it is done
- `delivery.md` — last task done: branch, review, PR

Triggered by what you are about to touch:

- `project.md` — **before any build, test, lint, or format command.** This project's
  commands exist nowhere else, and a guessed test command that exits 0 looks exactly
  like a passing suite.
- `code-search.md` — before going looking for code you have not read
- `artifacts.md` — before opening a plan or a spec
- `specs.md` — writing requirements, design, or tasks for a spec
- `tasks.md` — working through a spec's task list
- `knowledge-base.md` — something was learned, or a decision was made

## Ask the index before you read the file

**Code** — `scc graph query|explore <symbol>`, or `codegraph_explore` where registered.
Read the source when you are about to change it, not to find it.

**Plans and specs** — a plan is a header and a checklist: `map brief <plan>` once, then
`map tasks <plan> --next` per task; **never open the plan**. Also `scc map` and `map
show <artifact> <address>`. An address is a name, never a line number: `1.2` `#risks`.

**Changing one** — `scc patch check <artifact> 1.2`, plus `task` `add` `append` `fm`. Not
an editor: it resolves the address, re-validates, and rolls back an edit that adds a
finding — so you need not read a plan to change one line of it.

## Layout

```
specs/<feature>/    requirements.md · design.md · tasks.md
plans/<name>.md     structure, plus a checklist and/or spec references
docs/               knowledge base — wiki, adr, codewiki, glossary, stack
.claude/rules/      the methodology above
.claude/skills/     authoring each part of docs/, and running a plan group by group
.claude/commands/   the same skills on demand: /scc-plan-run, /scc-wiki, /scc-adr, …
```

## Checking your work

`scc validate` — or `npx @protonspy/scc validate` if not installed (`@<version>` pins for CI).
`scc update` brings a newer scc's rules and agents in: it shows the plan, then asks.

Exit `0` ok · `1` could not run · `2` ran and found something. A finding is an answer, not a crash.
`scc` checks artifact *shape* only; it never reads source, so whether the code honors it is on you.

<!-- scc:codegraph-instructions v1 -->
## CodeGraph
Ask the symbol graph before reading files. "Who calls this", "what breaks if I change it",
"where does this concept live" are one command here and a dozen reads otherwise.

- `scc graph explore "<question>"` — the relevant symbols' source plus the call paths between them. Start here.
- `scc graph query <name> [--kind function|class] [--limit N]` — find a symbol by name.
- `scc graph status` — what the graph holds. `--check` exits 2 when there is none.
- `scc graph sync` — re-index after you have written code you then need to search.
- `scc graph build [--force]` — first index, or a full rebuild when the graph has gone wrong.

`scc launch` indexes before the session starts, so the graph is current at turn one.
It goes stale as you edit: sync before searching for something you just wrote.
The graph is CodeGraph's — never edit `.codegraph/`, and never commit it.
<!-- /scc:codegraph-instructions -->

<!-- rtk-instructions v2 -->
## RTK
Prefix EVERY command with `rtk`, including each link in a `&&` chain (`rtk git add . && rtk git commit -m "x"`).
No dedicated filter means it passes through unchanged — always safe.

Covered:
- cargo build/check/clippy/test, go test, tsc, lint, prettier, next build
- jest, vitest, playwright, pytest, rspec, rake test, test `<cmd>`
- git (all subcommands)
- gh pr view/checks, gh run list, gh issue list, gh api
- pnpm, npm run, npx, prisma, uv run
- ls, read, grep, find
- err, log, json, deps, env, summary, diff
- docker, kubectl, curl, wget

Meta: `rtk gain [--history]`, `rtk discover`, `rtk proxy <cmd>` (no filtering), `rtk init [--global]`
Caveat: `rtk grep` with `-c -l -L -o -Z` runs raw.
<!-- /rtk-instructions -->
