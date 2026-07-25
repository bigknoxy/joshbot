---
name: panel-review
description: Convene a five-expert review panel (agent security, OSS growth, LLM evals, agent experience, Go systems) that independently analyses, then debates, then scores whatever context it is given. Use this whenever the user asks for a panel, a review board, multiple expert opinions, a red-team or adversarial read, a "what would experts say" take, or a scored assessment of a design, PR, roadmap, feature idea, incident, or architecture decision in the joshbot repo. Also use it when the user is about to commit to a significant or hard-to-reverse direction and would benefit from structured dissent, even if they do not use the word "panel".
---

# Panel Review

Five standing experts review the context you give them, argue with each other, and
produce a scored verdict. The panel exists because joshbot is a one-maintainer
project whose risks cluster in areas the maintainer does not work in daily — a
solo review tends to be strongest exactly where the project is already strong.

The panel is scoped to this repository. Every expert is grounded in joshbot's
actual architecture and known gaps, and every claim must be checked against the
code rather than asserted from the charter.

## The panel

| Expert | Lens |
|---|---|
| `panel-agent-security` | Prompt injection, tool abuse, trust boundaries, the shell/skills/web attack surface |
| `panel-llm-evals` | Does the agent behaviour actually work — memory, context, providers, regressions |
| `panel-agent-experience` | Onboarding, config surface, over-blocking, proactive behaviour design |
| `panel-oss-growth` | Adoption, positioning, and whether anyone will ever find this |
| `panel-go-systems` | Go correctness, concurrency, daemon hardening, release integrity |

**Charters live in `references/experts.md` and that file is the source of truth.**
Read it before Phase 2 — it is what makes the reviews specific rather than
generic, and it also defines the output contract every expert shares.

### Spawning experts portably

The charters deliberately do not depend on any one harness's agent registry, so
the panel runs anywhere that can spawn a subagent. Use whichever applies:

- **Claude Code** — `.claude/agents/panel-*.md` register these as agent types, so
  spawn with `subagent_type: panel-agent-security` and so on. Note that the
  registry is read at session start: if the agent files were only just created,
  they will not resolve until a new session, and you should fall back to the
  option below rather than treating that as a failure.
- **Any harness** — spawn a general-purpose subagent and tell it to read the
  `## panel-agent-security` section of
  `.claude/skills/panel-review/references/experts.md`, or paste that section
  inline if the agent has no filesystem access.

Both routes produce the same review because both read the same charter. Prefer
pointing at the file over pasting: it keeps one copy authoritative.

The scoring rubric and the report template are in `references/scoring.md`. Read it
before Phase 4.

## Workflow

### Phase 1 — Frame the context

Establish what is actually under review before spending five agents on it. The
context can be anything: a diff, a PR, a design doc, a roadmap, a feature idea, a
bug, an incident, a file, or a plain question.

Write a short framing block and keep it — every expert receives it verbatim, so
they are all reviewing the same thing:

```
SUBJECT:    <one line — what is being reviewed>
ARTIFACTS:  <files, diffs, PR numbers, commands to inspect it>
DECISION:   <what the user will do with the verdict — ship it, redesign, prioritise>
CONSTRAINTS: <anything fixed — deadline, compatibility, "must stay a single binary">
```

If the subject is genuinely ambiguous — if two readings would send the panel in
different directions — ask one clarifying question. Otherwise proceed; a panel
that stalls on process is worth less than one that reviews the obvious reading and
says which reading it assumed.

Then set the weights. Different subjects deserve different panels: a shell tool
change is mostly a security question, a README rewrite is mostly a growth
question. Start from the defaults in `references/scoring.md` and adjust, then
state the weights you chose and why in one line. Weighting is a judgement call the
user should be able to see and challenge.

### Phase 2 — Independent analysis

Spawn all five experts **in a single message so they run concurrently**, and give
each one only the framing block plus a pointer to its own charter section. Do not
show an expert what the others are thinking at this stage.

The isolation is the point. Experts who see each other's drafts converge on the
first plausible reading, and a panel that agrees too early is just one opinion
with five signatures. Independent drafts are what make Phase 3 productive.

Each expert returns:

- **Findings** — each with a concrete failure scenario and a file:line or command
  that backs it up
- **A dimension score** (0–10) and a confidence level
- **Blocking concerns** — anything that should stop the decision outright
- **What it could not check** — missing information, rather than a guess

An expert that finds nothing in its lane should say so plainly and score
accordingly. A panel where all five manufacture concerns to look useful is worse
than one where two say "not my problem" — false findings cost the user real time
to chase down.

### Phase 3 — Cross-examination

Give every expert the other four reports and require each to do two things:

1. **Challenge at least one** specific finding from another expert — dispute the
   severity, the evidence, or the proposed fix.
2. **Concede or hold** on every challenge made against it, with a reason.

Debate matters here because the experts have genuinely competing incentives. The
security expert wants the shell tool sandboxed; the agent-experience expert knows
a sandbox that breaks the assistant's usefulness will get switched off. The growth
expert wants shipping speed; the evals expert wants a regression suite first.
Surfacing that tension is more useful to the user than a smoothed-over consensus,
so do not resolve disagreements by splitting the difference. Where experts still
disagree after cross-examination, record it as an open disagreement in the report
and let the user decide.

Watch for findings that no expert will defend under challenge — those are the ones
to drop, and dropping them is a success of the process, not a failure.

### Phase 4 — Score

Collect each expert's post-debate score (it may have moved — note it if so) and
compute the weighted composite using `references/scoring.md`. Scores that changed
during debate are the most informative signal in the whole exercise: say what
changed the expert's mind.

### Phase 5 — Report

Use the template in `references/scoring.md`. Lead with the verdict and the
blocking concerns, because that is what the user acts on. Put the per-expert
detail below it, and the open disagreements at the end where they read as
unresolved questions rather than noise.

Relay the substance in your own response — the user does not see subagent output,
so a summary that says "the panel found several issues" wastes the entire run.

## Scaling the panel

Five agents on a one-line question is waste. Judge by the size of the decision:

- **A focused question in one lane** (is this deny-list rule sound?) — run the one
  or two relevant experts and say which you skipped and why.
- **A normal change** (a PR, a feature) — the full five, one round of debate.
- **A direction-setting decision** (architecture, roadmap, a security posture
  change) — the full five, and consider a second debate round if the first
  produced new findings rather than converging.

## Grounding rules

These apply to every expert and are worth restating in each spawn prompt:

- **Verify before asserting.** Read the file, run the command, check the coverage
  number. joshbot's own docs have drifted from the code before; a finding built on
  a stale doc is a false finding.
- **Cite location.** `internal/tools/shell.go:152` is actionable; "the shell tool
  is unsafe" is not.
- **Give a failure scenario.** Concrete inputs and state leading to a concrete bad
  outcome. If you cannot construct one, the finding is a hypothesis — label it.
- **Separate what you verified from what you inferred.** Both are useful; conflating
  them is what makes review output untrustworthy.
