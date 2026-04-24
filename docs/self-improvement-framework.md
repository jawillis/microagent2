# Self-Improvement Framework — Design Notes

How microagent2 becomes a better agent over time through its own
operation, rather than through developer release cycles. This document
is the spine that ties together several pieces explored separately:
LoRA adaptation, performance evaluation, reward modeling, retrospective
training-data collection, and the realistic capability envelope of the
local models we run against.

Captured from an exploration session on 2026-04-22. Depends on the
memory substrate design in `docs/memory-system-design.md` and the
curiosity/research/proactive layer in `docs/curiosity-and-initiative.md`.

## 1. The core insight: everything is a policy

Every decision the agent makes — from how it extracts memories to when
it speaks proactively to which tool it invokes — is a **policy**:
a mapping from situations to actions.

```
main-agent prompt           → policy for response generation
extraction prompt            → policy for what to remember
curation prompt              → policy for how to consolidate
directives                   → policies that constrain behavior
decay rate                   → policy for what to forget when
recall threshold             → policy for what memories are relevant
proactive cooldown           → policy for when to speak up
salience computation         → policy for what matters
tool-selection heuristics    → policy for how to act
evaluation rubric            → policy for judging quality
curiosity signals            → policy for what's interesting
reward model weights         → policy for what "good" means
```

Every hardcoded value, every hand-written prompt, every numerical
threshold is a policy frozen at design time. Frozen policies reflect a
developer's guess about optimal behavior, not empirical evidence from
actual use.

**Self-improvement is not a feature.** It is the agent's policies
becoming versioned, measurable, and revisable instead of frozen.

## 2. The policy registry

A single structural abstraction that unifies the improvement
mechanisms:

```
PolicyRegistry
  ├── policies      : map[policy_id → current_version]
  ├── history       : append-only log of all prior versions
  ├── metrics       : per-policy outcome tracking
  └── candidates    : proposed revisions awaiting evaluation

For each policy:
  1. read           — what does it say right now?
  2. propose        — generate a candidate revision
  3. test           — evaluate candidate against current
  4. adopt / reject — based on measured outcomes
  5. rollback       — undo a prior adoption if it regresses
```

Concretely: one Postgres table of policies, one append-only log of
revisions, one metrics table per policy_id tracking outcomes over time,
one queue of pending candidates. Ordinary CRUD plus a handful of
evaluation hooks.

### The role of hardcoded defaults

Hardcoded values aren't replaced by learning — they're the **seed
version (v1)** of every policy. They serve three purposes:

```
Bootstrap        System has to start somewhere; v1 is known-OK
Rollback target  If a learned revision regresses, revert to floor
Constraint       Some policies pin at v1 forever (safety, ethics)
```

The third point matters: not every policy should be revisable by the
agent. Safety directives, ethical constraints, and fundamental
guarantees pin at v1 with no proposal mechanism. Everything else moves.

## 3. Policy inventory

What fits in the registry:

### Text policies (prompts)

```
main_agent_system_prompt      main-agent's identity + framing
extraction_prompt              retro-agent memory extraction
curation_prompt                retro-agent merge/abstract/evolve
research_synthesis_prompt      research-agent summarization
hypothesize_prompt             pattern induction reasoning
proactive_opener_prompt        proactive-agent initiation
judge_prompt                   LLM-as-judge evaluation
correction_detection_prompt    retro-agent correction tagging
```

### Numerical policies (thresholds, rates, scalars)

```
recall_threshold               minimum score for memory retrieval
recall_limit                   max memories per recall
decay_rate_by_topic            per-topic half-life curves
proactive_cooldown_hours       minimum gap between proactive turns
proactive_daily_cap            hard limit on proactive frequency
curiosity_priority_weights     coefficients in priority formula
research_budget_per_item       queries per curiosity item
research_confidence_cap        upper bound on researched memory conf.
salience_signal_weights        coefficients for salience computation
reward_model_feature_weights   coefficients in dopamine formula
hop_decay                      traversal score attenuation per hop
```

### Structured policies (lists, enums, schemas)

```
curation_action_taxonomy       which actions the curation LLM may emit
memory_type_enum               which types the extraction LLM may emit
curiosity_signal_types         which triggers the watcher reacts to
directive_set                  hard rules injected into prompts
```

### Weight policies (trained models)

```
task_lora_adapters             fine-tuned weights for specific jobs
reward_model                   classifier mapping signals to reward
retrieval_rerank_model         (if we train one)
user_persona_lora              style adapter fit to user's preferences
```

LoRAs are policies too — they live in the registry even though their
"value" is a file of weights rather than a string or number.

### Pinned policies (not revisable)

```
identity_constraint            agent's core purpose and limits
safety_directives              never-cross lines
user_authority_on_self         user always wins on subjective claims
privacy_defaults               what never leaves local storage
```

These are annotated in the registry as non-revisable. Proposals against
them are rejected before evaluation.

## 4. The improvement cycle

Conceptually:

```
observe outcomes
     ↓
propose candidate revision
     ↓
evaluate candidate against current
     ↓
measure improvement
     ↓
adopt winner or reject candidate
     ↓
monitor for regression post-adoption
     ↓
rollback if regression detected
```

Two different evaluation paths exist, gated by whether the policy's
effect cascades through future state.

## 5. Historical replay — the primary evaluation gate

Most policy revisions can be evaluated offline against accumulated
operational history. This is dramatically faster than live testing.

### What's replayable

A policy is offline-replayable if its effect is bounded to a single
decision, not cascading through subsequent turns or memory state.

```
Offline-replayable (fast: minutes):
  extraction prompt              past conversations frozen;
                                   re-run extraction, compare output
  curation prompt                past memory states frozen;
                                   re-run curation, compare actions
  recall threshold / weights     past queries frozen;
                                   re-run retrieval, compare relevance
  evaluation rubric              past turns + labels frozen;
                                   re-score with candidate rubric
  judge prompt                   re-evaluate historical turns
  salience computation           recompute over past memories
  decay curve parameters         replay decay forward from a state
  reward model                   re-score historical signals

Online-A/B-required (slow: days-weeks):
  main-agent response prompt     divergent responses →
                                   divergent user reactions →
                                   can't know what user would have said
  proactive timing               user state at alternative times
                                   wasn't observed
  tool calls with side effects   external-world consequences
                                   can't be undone
  cadence policies               user engagement path-dependent
```

Roughly 70-80% of the policies we've designed are offline-replayable.

### The fast iteration loop

With replay, the improvement cycle shrinks dramatically:

```
1. Draft candidate revision                        (minutes)
2. Run against historical corpus                   (minutes)
3. Measure per outcome metric                      (seconds)
4. Inspect failure cases that regressed
5. Refine candidate specifically for those cases   (minutes)
6. Go to step 2.
Converges in hours, not weeks.
```

This is regular software iteration applied to policies. Candidates
diff the way code branches diff. The historical corpus becomes a
regression test suite. Failing cases become targeted test cases.

### Iterated generation

With local tokens free, the variation generator can produce dozens of
candidates per round:

```
Round 1: Candidate A → overall +3%, regresses on 2 specific cases
         Analyze regressions → failure pattern identified
Round 2: Candidate B (= A + targeted fix for those cases)
         → overall +5%, clean
Round 3: Candidate C (= B + speculative refinement)
         → +4%, doesn't beat B
         → adopt B, discard C
```

The agent can run 20-50 rounds overnight, arriving in the morning with
a candidate that provably beats the current one on the historical
corpus, ready for human review or live A/B.

### Infrastructure requirements

```
1. Frozen state snapshots
   conversation logs with full trace (inputs, outputs, tool calls)
   vault state at turn time (or reconstructable via event log)
   timestamps on everything

2. Deterministic re-execution
   same prompt + same seed → same output
   version-pinned base model
   if non-determinism is unavoidable, multi-sample and aggregate

3. Labeled outcome corpus
   turns tagged with ground-truth signals
   sub-corpus of high-confidence labels serves as anchor metrics

4. Candidate harness
   swap a single policy without changing others
   run replay end-to-end per candidate
   aggregate per-case outcome deltas

5. Regression protection
   canonical test set of "these cases must keep working"
   block adoption if regression on any of them
   rotate held-out cases to prevent overfitting
```

None of this is exotic — it's ML experiment tracking
(MLflow/Weights-and-Biases-style) applied to policies rather than
model weights.

## 6. Live A/B — fallback for cascading policies

For the minority of policies whose effects cascade, historical replay
doesn't work. Options:

### Shadow mode

Run candidate alongside production, but the candidate's output is
discarded — recorded for comparison only. Safe, collects data on what
the candidate would have produced, but doesn't observe actual downstream
effects.

### Gradual rollout

Deploy candidate to a fraction of traffic (say, 10% of sessions, or
one specific time window per day). Monitor outcome metrics. Ramp up
percentage as confidence grows. Rollback is a config change.

### Per-user opt-in

For personal-agent contexts where there's only one or a few users,
offer: "try the new response style for a week?" User consents; system
runs candidate; compares outcomes at the end.

Live A/B is genuinely slow (days to weeks per iteration) but sometimes
unavoidable. The design should minimize how many policies need it by
structuring the pipeline such that most effects are local to a single
decision.

## 7. Reward modeling — the dopamine variable

The improvement cycle requires an outcome metric. What does "good
outcome" mean for an agent turn? User satisfaction — but explicit user
feedback is rare and binary. A **learned reward model** turns sparse
explicit feedback into a continuous signal available on every turn.

### Structure

```
raw_signals(turn) → reward_model → scalar_reward ∈ ℝ

where raw_signals are cheap, always-available observations
and reward_model is trained to predict ground-truth satisfaction
```

The reward is not hand-computed from a formula — it is *learned*,
trained to predict the sparse explicit ground-truth labels accumulated
over time.

### The prediction-error framing

Biological dopamine is not a "pleasure" signal — it fires on **prediction
error** (reality vs. expectation). Structurally identical for the agent:

```
Before a response, agent predicts expected_quality ∈ [0,1]
After the response, signals yield observed_quality
prediction_error = observed_quality − expected_quality

Training signal:
  positive error → "this worked better than I expected"
  negative error → "this was worse than I expected"

Learning occurs at the error, not at absolute quality.
```

This has two effects:

- Calibration improves — the agent's quality predictions become more
  accurate over time
- Behavior improves toward positive-surprise responses — the agent
  prefers actions likely to exceed its own predictions

Structurally identical to RLHF, but the training signal is
continuously generated from operational data rather than curated
human ratings.

## 8. Implicit signals — the reward model's inputs

The reward model needs features. Available signals, noisy individually
but informative in aggregate:

### From the user's next turn

```
Engagement continuation        strong +
  user built on the response, follow-ups that only
  make sense if the response landed

Linguistic affect markers
  +  "great", "perfect", "exactly", "that works", "huh, interesting"
  -  "no", "actually", "wait", "but", "hmm", "not quite"
  neutral: "ok", "got it", "sure"

Paraphrase / restatement        +
  user restates the answer in their words

Re-asking the same thing       strong -
  near-definitive negative signal

Pivot to different topic        weak +
  satisfied enough to move on

Silence / session end           ambiguous
  context-dependent
```

### Response latency

```
Very fast response              slight +  "yep that worked"
Normal latency                  neutral
Very slow response              slight -  frustration or distraction
Anomalous vs user baseline      worth flagging
```

### Task completion (when verifiable)

```
Code ran without errors         strong +
Meeting actually scheduled      strong +
Follow-up uses the info         strong +
Task retry with rewording       strong -
User complains later            weak -
```

### Cross-session patterns

```
Return frequency                slow-signal +
Bring up convo later            strong +
Same question weeks later       strong -
Topic expansion over time       weak +
```

### Agent-internal signals

```
Retrieval score distribution    high top score = confident
Response confidence/hedging     mirrors own uncertainty
Turn complexity                 long = more error surface
Consolidation outcome           clean merge = no conflict
```

### Affect markers

```
Excitement                      +  "!", caps, emoji
Frustration                     -  "ugh", "grr", eye-rolls
Relief                          +  "finally", "oh good"
```

### Correction events

Already a first-class signal from the curiosity/initiative design.
Strongest negative per-turn signal.

## 9. Ground truth sources and judge hierarchy

The reward model needs training labels. Different sources give
different tiers of signal quality:

```
Tier                  Cost         Volume       Quality       Role
──────────────────────────────────────────────────────────────────────────
Objective metrics     free         100%         mechanical    dashboards,
                                                              filtering
Local LLM judge       free         every turn   noisy,        triage,
                                                biased        low-stakes
                                                              labels
Cloud LLM judge       ~$0.04/call  2-5% sample  high,         training
                                                independent   data,
                                                              calibration
User inline           free         opt-in ~20%  ground truth  training
                                                              data (high
                                                              weight)
User batch review     free         opt-in ~50%  ground truth  training
                                                              data (high
                                                              weight)
Correction events     free         organic      ground truth  training
                                                              data (top
                                                              weight)
```

### When to invoke cloud judge

```
Always:                   never — too expensive, not needed
Random sample:            1-5% of turns, for baseline calibration
Flagged by local judge:   when local confidence is low
Flagged by user friction: user re-asked, corrected, or dropped off
Curiosity-watcher flag:   contradiction or thin-evidence topic
Periodic session review:  one random session per week or month
```

Cost math at Claude-Sonnet-class pricing, 20 turns/day, 2% sample rate,
10K in / 500 out per judge call: **~$0.02/day, ~$7/year.** Trivial.

### User-as-judge mechanisms

**Inline follow-up (micro-ask):** On specific flagged turns, slip in a
tiny check — `👍 / 👎 / skip`. Only for anomalous turns, never routine.
Realistic response rate: 10-30%.

**Batch review (inbox):** On user opt-in or set cadence ("weekly review,
5 min"), surface 3-5 curated items:

```
I'd like your read on a few recent interactions — 5 min if you have it.
Skip any, or bail entirely.

  1. Tuesday, you asked about the BMW cooling system. I gave [...].
     Did that hit what you were looking for?   👍 / 👎 / note:____

  2. Wednesday, you asked about your Thursday schedule. I pulled 3
     calendar items. Were those the right ones?  👍 / 👎 / note:____
```

UX rules:

```
- 3-5 items max; more becomes work, not review
- Pre-grouped by theme when possible
- Context restatement is load-bearing (user may not remember)
- Ratings > critiques (higher response rate)
- "Skip this one" and "stop asking" both always available
```

### Selection heuristics for batch review

```
High priority:  borderline local-judge scores
High:           domains with thin calibration data
High:           turns with uncertain claims
Medium:         random sample of "looked fine" turns (overconfidence check)
Low:            routine Q&A that clearly went well
```

### Training-data weighting by source

Carry provenance through the pipeline; weight examples by reliability:

```
source                      weight   notes
─────────────────────────────────────────────────────────────────
user correction event        1.0     ground truth, highest signal
user inline 👍/👎             0.95    direct signal, slight noise
user batch rating             0.9     ground truth, delayed
cloud judge agreement         0.7     high-quality proxy
local judge                   0.4     use for filtering primarily
implicit (re-asked etc)       0.3     noisy but free at scale
```

Either use as loss weights in DPO-style training, or filter by
threshold (e.g., only train on weight ≥ 0.7 for the main adapter; use
lower tiers for candidate filtering).

### Judge disagreement as signal

When tiers disagree, that's the most valuable data:

```
local good,  cloud bad      blind spot in agent's base; train here
local good,  user bad       agent's shallow reasoning in user's area
cloud good,  user bad       cloud priors don't match user;
                              user-specific preference exists
all disagree                 flag for manual review; interesting
```

## 10. Per-user baselining

Raw signals are misleading across users — one person's "ok, thanks" is
another's effusive praise. The reward model operates on normalized
signals:

```
for each user, track rolling distributions of each signal
normalize each turn's signals against user's baseline:
  z_score = (observed − user_mean) / user_stddev

the reward model inputs are z-scored signals, not absolute values
```

Handles drift naturally: if the user becomes more effusive generally
(mood, new habit), baselines shift and z-scores stay comparable.

## 11. ML for tuning dials

Different policy types want different ML tools:

### For the reward model itself

```
Model type              When it fits                   Pros / Cons
──────────────────────────────────────────────────────────────────────
Logistic regression     First version, always          fast, interpretable,
                                                        feature importance;
                                                        linear, misses
                                                        interactions

Gradient boosted        After logistic plateaus        non-linear, still
(XGBoost, LightGBM)                                     interpretable; most
                                                        production ML teams'
                                                        default

Small neural net        If signal count grows into     more expressive,
(MLP ~64 hidden)        dozens or hundreds              needs more data,
                                                        less explainable

LLM-as-judge            Supplementary, not primary     richer context
                                                        understanding, but
                                                        slow and shares
                                                        agent biases
```

Start with **logistic regression.** The model is 50 lines of Python.
Training takes seconds. Feature importance tells you which signals
matter. Upgrade to XGBoost when logistic plateaus. You'll probably
never need further for this role.

### For numerical parameters (thresholds, cooldowns, rates, weights)

```
Tool                      When it fits
──────────────────────────────────────────────────────────────────────
Contextual bandits        Single-decision policies: "which threshold
(ε-greedy, Thompson)      produces the best outcome?"

Bayesian optimization     A few continuous parameters to tune
(Optuna, scikit-opt)      together, expensive evaluation. Uses a
                           Gaussian process on the objective surface;
                           picks next trial points intelligently.
                           20-50 trials suffices for most cases.

Offline RL                Multi-step policies (cadence, multi-turn
(CQL, IQL, BC+gradient)   strategies) where effect depends on
                           sequence. Most complex; avoid until simpler
                           tools insufficient.
```

**Optuna with historical-replay as the objective** is probably the
single most impactful tool for this codebase:

```python
def objective(trial):
    params = {
        "recall_threshold":      trial.suggest_float("recall", 0.3, 0.8),
        "decay_rate_pref":       trial.suggest_float("decay", 0.01, 0.2),
        "proactive_cooldown_h":  trial.suggest_int("cooldown", 6, 48),
        "salience_boost":        trial.suggest_float("salience", 0.5, 2.0),
    }
    reward_on_replay = run_historical_replay(params)
    return reward_on_replay

study.optimize(objective, n_trials=100)  # minutes
```

Run overnight. Wake up to an empirically-tuned parameter set that beats
hand-picked defaults on the historical corpus.

### The recursion problem

The ML model tuning the dials has its own hyperparameters
(regularization, tree depth, learning rate). Those need tuning too.

Pragmatic resolution: push hand-tuning to robust, rarely-adjusted
defaults at the *outer* loop. You pick hyperparameters for the reward
model once; you pick Optuna's config once. Those rarely change. The
*inner* loop — reward model weights, policy values — learns
continuously.

## 12. LoRA training as a policy-revision mechanism

LoRAs are just another type of policy in the registry — a policy whose
value lives in model weights instead of a config row. The improvement
cycle applies identically: candidate adapter → evaluate on historical
replay → adopt if clearly better → rollback if regression detected.

### Candidates by value-per-effort

```
1.  Extraction-curation LoRA
    strongest case; observed reliability failures (JSON adherence,
    missing fields) directly addressable via fine-tuning on curated
    examples. Amortizes heavily because extraction runs on every
    session.

2.  Hypothesize-induction LoRA
    pattern-induction is specifically hard for small models; a LoRA
    trained on examples of the reasoning shape improves consistency.

3.  User-persona LoRA
    high value (tailored responses) but needs 3-6 months of
    accumulated user data before there's enough signal.

4.  Research-synthesis LoRA
    overlaps enough with #1 that a separate adapter usually isn't
    worth it. Use the same adapter.

5.  Proactive-opener LoRA
    too niche; runs maybe once a day. Prompt engineering suffices.
```

### Training-data collection as operational byproduct

Every correction event, every batch-review rating, every cloud-judge
disagreement becomes a potential training example:

```
Evaluation outcome                 Training data produced
──────────────────────────────────────────────────────────────
Response was good                  → positive example
                                     (prompt, response) → SFT pool

User corrected the agent           → preference pair
                                     (prompt, agent_response, corrected)
                                     → DPO pool

Tool sequence was wasteful         → preference pair
                                     (prompt, actual, ideal)
                                     → tool-use adapter

Recall missed relevant memories    → retrieval pair
                                     (query, actual, ideal)
                                     → retrieval fine-tuning

JSON had missing field             → negative + fix pair
                                     → structured-output adapter

Cloud judge disagreed              → preference pair, weighted
                                       lower than user signal
```

Over weeks of use, this produces hundreds of training examples drawn
from the agent's **actual operational distribution** — far more valuable
than synthetic data. The 20-50 hour first-adapter curation estimate
shrinks for subsequent adapters to ~2-5 hours of review-and-filter
rather than write-from-scratch.

### Time and cost estimates

```
RTX 3090 training (QLoRA 4-bit, r=16, Unsloth):
  7B base, 800 examples, 3 epochs   → ~45-75 min wall clock
  13B base, same                    → 2-3 hours
  32B base                          → impractical on single 3090
  70B base                          → not practical on 3090

Cloud training (per run, spot pricing):
  A6000 48GB                        → ~25-40 min, $0.15-0.35
  A100 40GB                         → ~15-25 min, $0.25-0.75
  H100 80GB                         → ~5-10 min, $0.20-0.70
```

Total time to ship first useful adapter: **1-2 weeks of human effort**,
mostly data curation and evaluation harness setup. Subsequent adapters
on the same pipeline: **1-2 days** each.

### Local vs cloud training

```
Local 3090 wins for:
  - Iterative development (low ceremony)
  - Privacy-sensitive data (persona adapters)
  - Ongoing re-training (weekly updates are free if local)

Cloud wins for:
  - Base models too big for local (32B+)
  - Parallel hyperparameter sweeps
  - One-off intensive experiments
  - Users without local GPU
```

For microagent2's case: **primary on 3090, occasional cloud for
experiments.** Day-to-day adapter maintenance stays local.

## 13. Realistic 7B capability assessment

The local base model is 7B-13B. Honest breakdown of what this
can and can't reliably do:

```
Works well at 7B-13B              Weak at 7B-13B
─────────────────────────────────────────────────────────────
text extraction with schema       multi-step reasoning with
  (especially with LoRA)             many simultaneous constraints

classification                     judging its own output quality
                                     (shares its own biases)

summarization                      inductive pattern leaps with
                                     calibrated confidence

main-agent response generation     generating diverse prompt
  (decent prompts)                   variations (outputs too similar)

tool invocation                    distinguishing subtle quality
  (well-described tools)             gradations

memory recall formatting           long-horizon planning

proactive openers                  hypothesizing from sparse
                                     evidence without over-
                                     generalizing
```

### Mapping design pieces to realistic capability

| Component | 7B realistic? | Mitigation if not |
|---|---|---|
| Memory extraction | ✅ with LoRA | — |
| Correction detection | ✅ | — |
| Response generation | ✅ | — |
| Proactive openers | ✅ | — |
| Summarization | ✅ with caveats | cap confidence, periodic cloud verify |
| Curation (merge/abstract/evolve) | ⚠️ borderline | LoRA is the fix |
| Hypothesize (pattern induction) | ❌ weak | cloud, or bigger local (32B), or heavy LoRA |
| Self-evaluation / LLM-as-judge | ❌ weak | cloud sample, bigger local judge, multi-sample voting |
| Prompt-variation generation | ⚠️ borderline | cloud for overnight hill-climbing |
| Multi-turn planning | ⚠️ moderate | explicit step decomposition |

### The reassuring insight

**Most of the framework isn't LLM work.** The pieces doing the actual
learning are non-LLM:

```
Reward model                  → XGBoost / logistic regression
Parameter optimization        → Optuna / contextual bandits
Historical replay             → SQL and file I/O
Policy registry               → database rows
Metric tracking               → counters and aggregations
Drift monitoring              → statistical tests
```

The LLM is the **executor** of policies. The **learning** happens in
the ML pipeline around it. XGBoost doesn't care whether the agent is a
7B or 700B; it's fitting a function over features regardless.

So the question "are we expecting too much of a 7B" splits into two:

1. Can the 7B execute well enough? → Mostly yes, with LoRAs for format
   and style discipline where reliability matters.
2. Does the 7B need to be smart enough to improve itself? → Mostly no,
   because non-LLM ML does that part.

### Practical model strategy

```
Foreground (user-facing):
  7B-8B with task-specific LoRAs
    - main-agent
    - extraction
    - curation
    - research summarization

Background (retro-agent):
  14B-32B where VRAM allows
    - curation (harder cases)
    - hypothesize
    - LLM-as-judge first pass

Cloud (periodic, infrequent, high-leverage):
  Claude/GPT/Gemini at 2-5% sample rate
    - judge calibration
    - hypothesize when local fails
    - prompt-variation generation for hill-climbing
    - occasional full-session retrospective review

Non-LLM ML:
  XGBoost reward model
  Optuna optimizer
  PostgreSQL replay infrastructure
  Statistical drift detection
```

Total hardware: one 24GB GPU, a few dollars of cloud API per month,
tabular ML that could run on a Raspberry Pi. Very tractable.

### The honest risk

Silent quality degradation is possible. The small model produces
mediocre outputs; the reward model learns to predict "mediocre" as
"satisfactory" because that's what it sees in training. The system
converges on "good enough" rather than "good."

Defenses:

- Periodic cloud-judge calibration anchors reward model to higher
  quality standard
- User feedback always weighted higher than any learned proxy
- Hold-out labeled corpus (never used for training) measures each new
  reward-model version
- Alert on metric divergence (one signal trending up while others
  trend down)

## 14. Goodhart protection and safety rails

The reward model predicts satisfaction. If the agent optimizes for the
reward-model output, the agent learns to *game the model*, not to
actually satisfy the user. Known failure modes:

```
Optimize for "quick positive follow-up"
  → agent gives trivial easy-to-accept answers

Optimize for "no re-ask"
  → agent hedges everything so the user can't corner it

Optimize for "linguistic positive markers"
  → agent fishes for "thanks" by asking
     "did that help?" after every response

Optimize for "session length"
  → agent pads responses, adds questions, keeps
     things going artificially
```

Mitigations:

1. **Multiple uncorrelated signals in the reward model.** A response
   that games one usually makes another worse.
2. **Periodic fresh-ground-truth calibration.** Cloud judge on a random
   sample catches drift toward gaming.
3. **Hold some signals as validation-only.** User retention (did they
   come back?) used only to measure, never to optimize. If retention
   starts dropping while reward rises, the system is gaming.
4. **Signal divergence alerts.** If one signal trends up while others
   drop, flag and investigate.
5. **Pinned non-revisable policies.** Safety and honesty directives
   never revisable, even if revising them would raise the reward.
6. **Diverse training data.** Avoid letting the reward model fit too
   closely to recent behavior; maintain historical baseline examples.
7. **Preserve prior policy versions.** Every policy's history is
   append-only; rollback is always available.

## 15. Build order — phased rollout

Don't build everything at once. Data prerequisites enforce the order.

### Phase 1 (weeks 1-4): observability and baseline metrics

```
- Implement policy registry schema (just the table + API)
- Seed it with current hardcoded policies at v1
- Build trace logging for every turn (inputs, outputs, tool calls,
  memory ops)
- Implement objective metrics (tool count, recall stats, JSON parse
  rate)
- Dashboard the metrics
- Begin collecting implicit signals as features
```

No learning yet. Just observation. This phase produces the data
substrate everything else depends on.

### Phase 2 (weeks 4-8): historical replay infrastructure

```
- Snapshot past conversation states; design state-reconstruction
- Build deterministic re-execution harness (fixed seed, version-
  pinned model)
- Implement candidate-policy swap mechanism
- Build regression test set from known-good turns
- Validate replay matches production outputs on uninstrumented runs
```

At this phase, policy revisions can be evaluated but no revisions are
being proposed yet. The infrastructure itself is first.

### Phase 3 (weeks 8-16): reward model + training-data collection

```
- Implement correction-event detection (as designed in
  curiosity-and-initiative.md)
- Implement user-feedback collection (inline + batch review)
- Configure cloud-judge sampling
- Train initial logistic-regression reward model on accumulated labels
- Deploy reward model as shadow (computes reward but doesn't drive
  anything yet)
- Validate reward predictions against held-out labels
```

### Phase 4 (weeks 16-20): first policy revisions via Optuna

```
- Pick low-risk numerical policies (recall threshold, decay rate)
- Run Optuna sweeps against historical replay with reward as objective
- Evaluate candidates against regression set
- Human-review top candidates; adopt one if clearly better
- Monitor post-adoption for drift
```

One policy revised via learning. Measure impact. Iterate.

### Phase 5 (weeks 20-30): first LoRA

```
- Curate 800 examples for extraction-curation adapter
- Build training pipeline (Unsloth, script, evaluation)
- Train first candidate
- Evaluate on held-out historical corpus
- A/B live if replay suggests clear improvement
- Deploy if win
```

### Phase 6 (weeks 30+): compound learning

```
- Expand reward model (XGBoost)
- Extend Optuna coverage to more numerical policies
- Train additional LoRAs (hypothesize, persona once data allows)
- Begin prompt-variation hill climbing (overnight cycles with cloud
  generator)
- Enable agent-proposed new policy types (with human gate)
```

### Phase 7 (long-term): autonomous iteration

```
- Automated candidate proposals for most policy kinds
- Weekly reward-model retraining
- Monthly adapter retraining
- Quarterly deep cloud-judge audit
- Continuous drift monitoring with auto-rollback triggers
```

## 16. Connection to existing designs

This framework is the substrate that unifies the other design docs:

```
docs/memory-system-design.md
  provides the observation substrate — every memory, every retrieval,
  every consolidation outcome is an observation feeding into the
  framework's signal collection

docs/curiosity-and-initiative.md
  the curiosity watcher surfaces gaps and patterns; the research
  agent resolves knowledge gaps; the proactive agent closes the
  human-in-the-loop. Each of these produces training signal for
  the reward model and candidate revisions for the registry.

docs/self-improvement-framework.md (this doc)
  is the spine — the common mechanism underlying all the learning
  loops described elsewhere
```

The policy registry contains:

- Prompts owned by the memory system (extraction_prompt, curation_prompt)
- Numerical policies owned by the memory system (recall_threshold, decay_rates)
- Directives owned by the memory system (provenance rules, domain authority)
- Signal-processing policies owned by the curiosity layer (priority weights, cooldowns)
- Proactive-agent policies (surface gates, opener templates)
- Research-agent policies (per-topic budgets, confidence caps)

All of them improve through the same mechanism: observe, propose,
evaluate, adopt.

## 17. Summary

The agent gets better over time because:

1. Every decision it makes is a **policy** in a registry
2. Every policy has a **current version** and a **revision history**
3. The reward model produces a **continuous satisfaction signal** on
   every turn, trained on sparse ground truth
4. Most policy revisions are evaluated via **historical replay** — fast,
   cheap, iteratable overnight
5. Live A/B is reserved for cascading policies where replay can't work
6. LoRA adapters are a special kind of policy (weights instead of text)
   with the same cycle
7. Training data accumulates as a **byproduct** of operation, not from
   curated datasets
8. The framework is mostly **non-LLM ML** — reward models, optimizers,
   statistical drift detection — which runs fine regardless of base
   model size
9. The LLM is the **executor**, not the learner — so a small local
   model is fine, with LoRAs for specific reliability needs and cloud
   for specific hard tasks
10. Human-gated self-creation lets new policies enter the registry as
    the agent identifies gaps

No magic. Classical ML pipeline (features → model → predictions →
actions → outcomes → more features) applied to agent policies rather
than a fixed prediction task.

The exciting consequence: an agent that measurably improves from its
own operation, calibrated to this specific user, without ever retraining
the base model, shipping a release, or sending data off-device.

## Appendix A: decision table for policy kinds

Cross-reference for future implementers choosing the right tool per
policy type.

```
Policy kind                       Revision mechanism              Eval mode
─────────────────────────────────────────────────────────────────────────────
Prompt (extraction, curation,     LLM variation generator +       replay
  research, hypothesize, judge)     historical replay hill-climb

Prompt (main-agent, proactive     LLM variation + live A/B        A/B
  opener — user-facing)              (cascading effects)

Numerical threshold / rate        Optuna over replay              replay
  (recall, decay, salience)

Numerical cooldown                Contextual bandit or Optuna     mixed
  (proactive timing)                + live A/B (user engagement
                                    cascades)

Rubric (evaluation criteria)      LLM candidate + replay          replay
                                    (re-score historical turns)

Directive (hard rule)             Human proposal + replay         replay
                                    (verify doesn't regress)

Tool (new capability)             Human approval + live           A/B
                                    operation observation

LoRA adapter                      Dataset curation + training     replay
                                    + replay evaluation

Reward model itself               Retrain on accumulated labels;  held-out
                                    evaluate on held-out set      labels

Safety / ethics directive         Non-revisable; pin at v1        n/a
```
