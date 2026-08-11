# Scoring and report format

## Dimension score (0–10)

Each expert scores the subject on its own dimension. The score answers one
question: **how well does this hold up under my lens?** High is good — 10 means
the expert has no concerns in its lane.

| Score | Meaning |
|---|---|
| 9–10 | Strong. Nothing to fix in this lane. |
| 7–8 | Sound, with minor improvements worth making. |
| 5–6 | Workable, but with real gaps that will cost later. |
| 3–4 | Significant problems. Should not ship in this shape. |
| 0–2 | Blocking. Would cause damage as written. |

An expert whose lane is genuinely not engaged by the subject reports **N/A** and is
dropped from the weighting rather than scoring a hollow 8. Padding the panel with
irrelevant high scores inflates the composite and hides the scores that matter.

## Confidence

Each score carries `high`, `medium`, or `low`.

- **high** — verified against the code, with a reproducible failure scenario
- **medium** — strong inference from evidence, not directly exercised
- **low** — expert judgement, or blocked by something it could not inspect

Low confidence is not a weakness to hide. A confident wrong score costs the user
more than an honest uncertain one, and the confidence field is what lets them tell
the difference.

## Default weights

Start here and adjust to the subject. State the weights and the reason in one line
so the user can challenge the framing rather than just the conclusion.

| Expert | Default |
|---|---|
| `panel-agent-security` | 0.30 |
| `panel-llm-evals` | 0.22 |
| `panel-agent-experience` | 0.18 |
| `panel-oss-growth` | 0.17 |
| `panel-go-systems` | 0.13 |

The defaults encode where joshbot's risk actually sits: concentrated in security
and behavioural correctness, lightest in the Go craft the maintainer already
covers well. They are not a claim about which discipline matters more in general.

Adjust when the subject clearly leans:

- **Shell, skills, web, channel auth, or anything touching a trust boundary** —
  security to 0.40+
- **README, site, positioning, install flow, docs** — growth to 0.35+
- **Memory, context compression, providers, model routing** — evals to 0.35+
- **Config, onboarding, heartbeat, cron, message formatting** — experience to 0.30+
- **Concurrency, build tags, release pipeline, coverage** — Go systems to 0.30+

Renormalise so the active weights sum to 1.0 after any N/A experts are dropped.

## Composite

```
composite = Σ (weight × score) / Σ (weight)   over experts that scored
```

Report it to one decimal place, and pair it with the verdict band:

| Composite | Verdict |
|---|---|
| 8.0+ | **Ship** — proceed as designed |
| 6.5–7.9 | **Ship with fixes** — proceed once the listed items are addressed |
| 5.0–6.4 | **Revise** — the approach holds but needs rework before proceeding |
| < 5.0 | **Reconsider** — the approach itself is in question |

**Any blocking concern overrides the band.** A subject can average 8.4 and still be
blocked by one unresolved security finding. Say so explicitly rather than letting
the composite speak — a single fatal flaw is not something an average should
launder away.

## Report template

```markdown
# Panel Review: <subject>

**Verdict: <band> — composite <X.X>/10**
<one sentence: the single thing the user should take away>

Weights: <expert:weight, ...> — <one line on why>

## Blocking concerns
<numbered, each with expert, file:line, and the failure scenario. "None" if none.>

## Panel scores

| Expert | Score | Confidence | Headline |
|---|---|---|---|
| Security | X/10 | high | ... |
| Evals | X/10 | medium | ... |
| Experience | X/10 | high | ... |
| Growth | N/A | — | not engaged by this subject |
| Go systems | X/10 | high | ... |

## Findings
<grouped by expert, most severe first. Each: what, where, why it matters,
and what to do. Mark verified vs inferred.>

## What changed in debate
<scores that moved and what moved them; findings dropped because no expert would
defend them. This section is the value of running a panel instead of one review —
if nothing changed, say that too, and note whether that reflects real consensus or
a panel that failed to engage.>

## Open disagreements
<where experts still disagree, stated as the actual tradeoff the user must decide.
Do not resolve these by averaging.>

## Not checked
<what the panel could not verify and why — missing access, missing context,
or out of scope.>
```

## Reporting to the user

The user never sees subagent output. Whatever matters has to appear in the final
response: the verdict, the blocking concerns, the disagreements, and the specific
next actions. A summary that gestures at "several findings" throws away the entire
run.

Keep the panel's disagreements visible. The most valuable output of a five-expert
review is usually not the score — it is the one tradeoff the experts could not
settle, surfaced early enough for the user to decide it deliberately.
