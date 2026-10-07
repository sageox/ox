# 0002: Adaptive difficulty for Math Blitz practice

Status: Accepted

Date: 2026-10-01

## Context

Players report that Math Blitz becomes too easy with extended play, while a
beginner can be overwhelmed by multiplication up to 12 x 12. The original
generator samples operands without adapting to performance.

This decision extends and aligns with [ADR-0001](0001-game-architecture.md):
adaptive practice remains separate from fixed-challenge rankings, the engine
remains deterministic, and generation receives an explicit random source.
It does not supersede the existing timing, scoring, or terminal rules.
<!-- SOURCE: sageox adr:docs/decisions/0001-game-architecture.md -->

An initial recommendation favored fixed levels for simplicity and forgiving
behavior. Reproducible synthetic evidence did not support the proposed tuning:
its long-run accuracy was about 69%, below the requested 75–80% range. We choose
the measured winner rather than retain that initial recommendation.

## Options and experiment

The comparison in [simulate_difficulty.py](../../scripts/simulate_difficulty.py)
uses seed **20261001**, **200 trials per player**, and **1,600 questions per
trial**. Each approach receives the same answer-uniform draws within a trial.
Answers are independent Bernoulli draws with
`P(correct) = logistic(true ability - true family difficulty)`.

The family catalog contains 33 difficulty values from -2 to 6 in increments of
0.25. All policies initially present the easiest family. Beginner, intermediate,
and advanced players have fixed abilities of 0, 2, and 4. A fourth player starts
at 4, declines linearly during questions 601–800, and remains at 2 afterward.

1. **Fixed levels:** promote after at least 8 correct in the last 10 attempts;
   demote after at least 3 misses in the last 5; clear history on an actual move.
   Move one family at a time, checking demotion before promotion.
2. **Staircase:** two consecutive correct answers move up one family; a miss
   moves down one family; reset the consecutive-correct counter on either move.
3. **Elo:** select a family with predicted success nearest 77.5%, using player
   update rate 0.15 and family update rate 0.01. Family priors equal true synthetic
   difficulty. Initial player rating is `-2 + logit(0.775)`.

### Metrics

Settling requires a 100-question rolling average of **true expected success
probability**, not observed answer accuracy, to remain in 75–80% for 100
consecutive windows. True probability is diagnostic information only; no policy
receives it. The reported time is the end of the first qualifying window, with
confirmation 99 questions later. Median time is conditional on settling within
the run; the percentage of runs settling must be read alongside it. For the
tiring player, times count after question 800 and use wholly post-fatigue windows.

Post-settling SD measures selected difficulty after confirmation, only in settled
trials with at least two remaining questions. Late metrics use the final 300
questions of **all trials**, including unsuccessful ones, to avoid survivor bias.
SD is in latent difficulty units; direction reversals skip questions on which
difficulty is unchanged. A run meeting the settling criterion can subsequently
leave the band: settling is not a guarantee of permanent stability.

### Results

The table below records the comparison rerun using production Elo with bounded
player ratings and anchored family ratings. The added edge safeguards leave
settling percentages, median times, and observed accuracy unchanged at displayed
precision; a few late expected-accuracy and oscillation metrics change slightly.

| Player | Policy | Settled % | Median questions | Late correct % | Late expected % | Post-settling SD | Late SD | Late reversals/100 | Late windows in band % |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Beginner | Levels | 28.5 | 662 | 69.4 | 69.4 | 0.318 | 0.297 | 6.0 | 9.3 |
| Beginner | Staircase | 29.0 | 738 | 70.5 | 70.5 | 0.443 | 0.442 | 31.3 | 12.9 |
| Beginner | Elo | 100.0 | 219 | 77.5 | 77.5 | 0.259 | 0.224 | 13.4 | 59.7 |
| Intermediate | Levels | 26.5 | 698 | 69.2 | 69.2 | 0.299 | 0.295 | 6.0 | 7.2 |
| Intermediate | Staircase | 29.0 | 760 | 70.6 | 70.6 | 0.456 | 0.455 | 31.2 | 13.9 |
| Intermediate | Elo | 98.0 | 334 | 77.5 | 77.5 | 0.258 | 0.228 | 13.8 | 57.2 |
| Advanced | Levels | 28.5 | 706 | 69.4 | 69.6 | 0.308 | 0.301 | 6.1 | 9.8 |
| Advanced | Staircase | 30.5 | 799 | 70.8 | 71.0 | 0.444 | 0.453 | 31.1 | 15.1 |
| Advanced | Elo | 99.5 | 405 | 77.6 | 77.7 | 0.258 | 0.227 | 14.1 | 55.6 |
| Tired: recovery | Levels | 17.0 | 382 | 69.6 | 69.6 | 0.266 | 0.301 | 6.0 | 11.1 |
| Tired: recovery | Staircase | 16.5 | 382 | 70.7 | 70.6 | 0.438 | 0.455 | 31.2 | 14.7 |
| Tired: recovery | Elo | 93.0 | 222 | 77.6 | 77.4 | 0.243 | 0.241 | 14.9 | 52.5 |

Elo meets the average accuracy target, settles much more often, and has the
smallest post-settling difficulty variation. Fixed levels change direction less
often, but at the wrong success rate. The two-right/one-miss staircase is also
below target. Occasional target-band visits do not establish sustained success
for those configurations.

Reproduce from the repository root:

```sh
python3 scripts/simulate_difficulty.py
python3 -m unittest discover -s tests -v
```

## Decision

Use `EloDifficulty` behind the `DifficultyPolicy` protocol in
`math_blitz/difficulty.py`. The small interface is:

```python
select(operation: str) -> int
update(operation: str, level: int, correct: bool) -> None
reset() -> None
```

Levels are zero-based family-catalog indices. Each operation has its own player
rating and family ratings, so successful addition cannot promote multiplication.
Selection is stable between updates. Policies perform no I/O, clock reads, or
random draws. A policy instance belongs to one player/session; instances are
fresh by default and persistence is outside this decision.

For player rating `a`, family rating `d`, and outcome `y` (1 correct, 0 miss):

```text
p = logistic(a - d)
a' = a + 0.15 * (y - p)
d' = d - 0.01 * (y - p)
select the family whose rating is nearest a - logit(0.775)
```

Compute both updates from the pre-answer probability. Use a stable logistic
calculation to handle extreme abilities. Bound player ratings and preserve
catalog order by clipping updated family ratings into fixed prior-centered cells,
strictly inside the midpoint to each neighboring prior. The boundary
test exposed the need for this guard: repeated misses otherwise raise the easiest
family's rating past its neighbor and can send a struggling player upward.
Selection always searches the finite catalog, keeping indices within bounds.

### Tuning knobs

These are natural-log logistic units, not chess rating points. The policy
constructor accepts the following values; the values are provisional synthetic
tuning, not a measured mapping from arithmetic operands to difficulty.

| Knob | Chosen value | Reason and tradeoff |
|---|---|---|
| `target` | 0.775 | Midpoint of the requested 75–80% success band. Higher targets select easier questions; lower targets select harder ones. Must be strictly between 0 and 1. |
| `family_ratings` | 33 priors from -2 to 6, step 0.25 | Matches the experiment, covers its abilities, and permits small moves. Catalog size sets selectable bounds (0–32); rating gaps control resolution. Priors must be finite and strictly increasing. Calibrated arithmetic catalogs should replace these abstract priors. |
| Initial ability | First prior + `logit(target)` = approximately -0.763237 | Starts each operation at its easiest family while predicting the target success rate there. Derived, rather than separately tuned. |
| `player_k` | 0.15 | Responds to recent performance while limiting each raw update to 0.15 units. Measured convergence and the 20-success lucky-streak test support this starting value. Bigger values react faster and oscillate more; smaller values recover more slowly. Require (0, 1] so learning cannot be accidentally disabled. |
| `item_k` | 0.01 | Families move 15 times more slowly than player ability so a player's recent streak mostly changes that player's estimate. Allowed [0, 1]; zero intentionally freezes family calibration. |
| `rating_margin` | 1.0 | Bounds ability to `[first prior + logit(target) - 1, last prior + logit(target) + 1]`, approximately [-1.763237, 8.236763]. One unit is four default family steps of headroom. Prevents accumulating unlimited rating drift at the edges; tests verify recovery within 40 opposite outcomes after 10,000 edge outcomes. Larger margins permit longer recovery delays. Must be finite and positive. |
| `item_drift` | 0.125 | Caps family ratings at initial prior +/- half a default family step, further restricted by neighboring prior midpoints. Anchors the question bank against one player's absence or streak; preserves order and stops family collapse. Smaller values trust calibration more; larger values still cannot cross midpoint cells. Must be finite and nonnegative. |
| Question allowance | 15 seconds in timed play | Existing engine rule, independent of Elo. Instant and slow correct answers produce identical difficulty updates. Timeout is one miss. Untimed practice has no expiry. |
| Starting/restart state | Fresh ratings and zero progress | Conservative practice restart; no partial save/resume or hidden carry-over. Configuration and operation schedule survive an in-process restart. |

Both raw Elo updates use the pre-answer probability; clamp each result afterward.
Family bounds also apply to the first and last families, which otherwise have no
outer neighbor. Bounds are finite and do not expand with observed answers.

The simulation exposes `--player-k`, `--item-k`, `--step`, `--rating-margin`, and
`--item-drift` for sensitivity experiments, plus seed/trial/run-length controls.
It fixes target at 0.775. Its 100-question window, 100-window persistence, and
300-question tail are diagnostic settings, not production policy knobs.

### Edges and restart

- **Everything correct instantly:** ratings and selection rise gradually, then
  saturate at the hardest family. Response speed changes points, not difficulty.
  After 10,000 consecutive successes, ten timeouts move selection off that edge
  with default tuning. There is no impossible attempt to force 77.5% accuracy
  when the catalog cannot challenge the player further.
- **Walk away:** each active prompt expires once and trains one miss, with no
  double penalty from repeated expiry calls. Difficulty falls to the easiest
  family; bounded ratings prevent a long absence from creating an indefinitely
  negative player estimate. After 10,000 timeouts, 25 correct answers move off
  that edge. Expiry still requires orchestration to call the engine; the engine
  neither reads time nor runs a background timer. A finite schedule ends normally.
- **Restart mid-question:** `AdaptiveGame.restart()` calls policy `reset()`,
  discards active/cached questions without training a miss, and resets score,
  streaks, counters, deadlines, and stopped state. It preserves tuning, timing
  mode, allowance, and the operation schedule. Question IDs continue increasing
  within the same engine instance so pre-restart answer/timeout events are
  ignored even after the first new prompt starts. A process restart creates fresh
  objects and ratings; there is no persistence or partial-game resume.

Staying at an endpoint while behavior remains all-correct or all-timeout is
intentional saturation, not a stuck policy. Tests reverse behavior to distinguish
the two. The numerical recovery counts apply to the default catalog and tuning;
they are observations, not guarantees for arbitrary configuration.

`AdaptiveGame` extends the existing fixed-question engine with a caller-supplied
operation schedule. `request_question()` returns the next unique ID, operation,
and selected family level. The caller generates a matching `Question` externally,
labels it with `level`, renders it, then passes it to
`start_question(now, question)`. The engine validates all three metadata fields
and records policy feedback exactly once when that question resolves.

Correct answers train as successes; wrong numeric answers and timeouts train as
misses. Invalid text, pending expiry checks, stale/duplicate events, quit, EOF,
and stopping do not train. Speed and scoring streak bonuses do not enter the
rating update. Existing scoring, absolute deadlines, one-attempt rules, and
duplicate-event protection are retained. Adaptive games are explicitly ineligible
for ranking; fixed `Game` and the existing terminal flow retain their behavior.

The simulation imports the production Elo policy so comparison evidence and
shipping policy cannot silently diverge. Levels and staircase remain simulation
alternatives, not production implementations.

## Validation

- Engine-driven synthetic trials at all three stationary abilities converge
  into the 75–80% band for aggregate late expected and observed accuracy, moving
  closer to the target than their initial 100 questions.
- Ten thousand misses keep selection at the easy boundary. Ten thousand correct
  answers reach the hard boundary without exceeding the catalog.
- Twenty consecutive lucky successes from a fresh beginner state select at most
  family 4 of 32, rather than the hardest family. This pins the default learning
  rates; it is not a claim that an arbitrarily long perfect streak never promotes.
- Tests exercise policy substitution, operation independence, matching question
  metadata, timeout feedback, no feedback on invalid/stale/quit events, and fixed
  versus adaptive deadline and scoring parity.
- Tests additionally bound internal ratings during 10,000 instant answers or
  timeouts, verify recovery on behavior reversal, compare instant versus slow
  difficulty updates, and exercise restart during active/requested/stopped/finished
  games with stale-event rejection and configuration preservation.
- The complete test suite passes: 40 tests, including existing game and terminal
  regression checks.

## Consequences and limits

The evidence supports this configuration under this model, not universal Elo
superiority. Initial family ratings are perfectly calibrated in the experiment.
Real arithmetic families must be mapped to levels and calibrated separately;
the default -2..6 priors are abstract model values, not measured difficulty for
12 x 12. This change provides the engine API; the terminal CLI does not yet expose
an adaptive mode or invent an arithmetic-to-family mapping.

The simulation excludes correlated streaks, learning, input errors, timing,
operation switching, and multiple players jointly calibrating a question bank.
Even Elo's late rolling windows occupy the narrow target band only 52.5–59.7% of
the time. There is no guarantee of 75–80% correctness for every short session.
Settling generally takes hundreds of attempts at one operation, beyond a single
20-question mixed run.

All feedback updates local session ratings, including family ratings. Fatigue
can therefore affect both ability and inferred difficulty. Persistent profiles,
shared item calibration, arithmetic family definitions, longer practice sessions,
and empirical player validation remain separate follow-ups. Custom targets,
catalogs, and update rates require fresh simulation evidence before claiming
these results apply to them.
