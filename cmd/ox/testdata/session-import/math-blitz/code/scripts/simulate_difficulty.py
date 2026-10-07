#!/usr/bin/env python3
"""Compare adaptive policies using the production Elo policy (no game I/O).

Run: python3 scripts/simulate_difficulty.py

Model: one skill/operation, P(correct) = logistic(ability - difficulty),
independent Bernoulli answers. Difficulty families span -2..6 in steps of .25.
All policies start by presenting difficulty -2 and share random answer draws.
These abstract families are NOT calibrated arithmetic questions. There are no
learning, typing, timing, or operation-switching effects in this model.

Levels implement the previously proposed 8/10 promotion, 3/5 demotion rules,
clearing history on a move. Staircase implements exactly two consecutive right
answers up, one miss down, resetting its counter on either move. Elo selects
the family nearest its predicted .775 success target, updating both learner
and family ratings. Family priors equal simulated true difficulty (an optimistic
calibration assumption); learner prior matches initial difficulty + logit(.775).
Production Elo bounds player ratings and anchors family ratings to prior-centered
cells. The ADR records the rerun with these safeguards, including small changes
to late oscillation metrics compared with the original unbounded experiment.

Settling is the first completed 100-question expected-probability window in
[.75, .80], sustained for 100 consecutive rolling windows. It is confirmed 99
questions later. Expected probability is diagnostic ground truth, NEVER policy
input. Median settling time includes only settled trials; always read its rate.
Post-settling SD uses questions after confirmation, only for settled trials with
at least two remaining questions. Late metrics use the last 300 questions of
EVERY trial, including unsettled ones, avoiding survivor bias. SD measures
selected difficulty; reversals skip holds.

The tired player declines from ability 4 to 2 over questions 601..800. Its row
measures recovery after question 800, including only wholly post-fatigue windows.
This tests chosen configurations, not universal superiority of an algorithm.
Python 3.9+, standard library only.
"""

import argparse
import csv
import math
import random
import statistics
import sys
from collections import deque
from dataclasses import dataclass
from pathlib import Path
from typing import Optional

# Allow the documented direct script invocation from any working directory.
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from math_blitz.difficulty import EloDifficulty


@dataclass(frozen=True)
class Config:
    seed: int = 20261001
    trials: int = 200
    questions: int = 1600
    step: float = 0.25
    player_k: float = 0.15
    item_k: float = 0.01
    rating_margin: float = 1.0
    item_drift: float = 0.125
    target: float = 0.775
    window: int = 100
    sustain: int = 100
    tail: int = 300


def logistic(value):
    return 1.0 / (1.0 + math.exp(-value))


class Levels:
    def __init__(self, families, config):
        self.families = families
        self.index = 0
        self.history = deque(maxlen=10)

    def select(self):
        return self.index

    def update(self, index, correct):
        self.history.append(correct)
        recent = list(self.history)
        move = 0
        if len(recent) >= 5 and sum(recent[-5:]) <= 2:
            move = -1
        elif len(recent) == 10 and sum(recent) >= 8:
            move = 1
        next_index = max(0, min(len(self.families) - 1, self.index + move))
        if next_index != self.index:
            self.index = next_index
            self.history.clear()


class Staircase:
    def __init__(self, families, config):
        self.families = families
        self.index = 0
        self.right = 0

    def select(self):
        return self.index

    def update(self, index, correct):
        if correct:
            self.right += 1
            if self.right == 2:
                self.index = min(len(self.families) - 1, self.index + 1)
                self.right = 0
        else:
            self.index = max(0, self.index - 1)
            self.right = 0


class Elo:
    def __init__(self, families, config):
        self.policy = EloDifficulty(families, target=config.target,
                                    player_k=config.player_k, item_k=config.item_k,
                                    rating_margin=config.rating_margin,
                                    item_drift=config.item_drift)

    def select(self):
        return self.policy.select("*")

    def update(self, index, correct):
        self.policy.update("*", index, correct)


PLAYERS = (("Beginner", 0.0), ("Intermediate", 2.0),
           ("Advanced", 4.0), ("Tired (recovery)", 4.0))
POLICIES = (("Levels", Levels), ("Staircase", Staircase), ("Elo", Elo))


def ability_at(name, baseline, question):
    if name == "Tired (recovery)":
        return baseline - 2.0 * max(0.0, min(1.0, (question - 600) / 200.0))
    return baseline


def settling_time(probabilities, start, config) -> Optional[int]:
    window = deque()
    total = 0.0
    consecutive = 0
    first = None
    for question, probability in enumerate(probabilities[start:], 1):
        window.append(probability)
        total += probability
        if len(window) > config.window:
            total -= window.popleft()
        if len(window) < config.window:
            continue
        if 0.75 <= total / config.window <= 0.80:
            if consecutive == 0:
                first = question
            consecutive += 1
            if consecutive >= config.sustain:
                return first
        else:
            consecutive = 0
    return None


def reversals_per_100(difficulties):
    previous_direction = 0
    reversals = 0
    for before, after in zip(difficulties, difficulties[1:]):
        direction = (after > before) - (after < before)
        if direction:
            if previous_direction and direction != previous_direction:
                reversals += 1
            previous_direction = direction
    return 100.0 * reversals / (len(difficulties) - 1)


def simulate(config):
    count = round(8.0 / config.step)
    families = [-2.0 + i * config.step for i in range(count + 1)]
    rows = []
    for player_index, (name, baseline) in enumerate(PLAYERS):
        samples = {label: [] for label, _ in POLICIES}
        for trial in range(config.trials):
            rng = random.Random(config.seed + player_index * 1000003 + trial)
            draws = [rng.random() for _ in range(config.questions)]
            for label, policy_class in POLICIES:
                policy = policy_class(families, config)
                probabilities, answers, difficulties = [], [], []
                for question, draw in enumerate(draws, 1):
                    index = policy.select()
                    difficulty = families[index]
                    probability = logistic(ability_at(name, baseline, question)
                                           - difficulty)
                    correct = draw < probability
                    policy.update(index, correct)
                    probabilities.append(probability)
                    answers.append(correct)
                    difficulties.append(difficulty)
                start = 800 if name == "Tired (recovery)" else 0
                tail_p = probabilities[-config.tail:]
                tail_d = difficulties[-config.tail:]
                settled_at = settling_time(probabilities, start, config)
                post_d = (difficulties[start + settled_at + config.sustain - 1:]
                          if settled_at is not None else [])
                samples[label].append({
                    "settle": settled_at,
                    "post_sd": statistics.pstdev(post_d) if len(post_d) >= 2 else None,
                    "accuracy": 100.0 * statistics.mean(answers[-config.tail:]),
                    "expected": 100.0 * statistics.mean(tail_p),
                    "sd": statistics.pstdev(tail_d),
                    "reversals": reversals_per_100(tail_d),
                    "in_band": 100.0 * statistics.mean(
                        0.75 <= statistics.mean(tail_p[i:i + config.window]) <= 0.80
                        for i in range(len(tail_p) - config.window + 1)),
                })
        for label, _ in POLICIES:
            results = samples[label]
            settled = [r["settle"] for r in results if r["settle"] is not None]
            row = {"player": name, "policy": label,
                   "settled_pct": 100.0 * len(settled) / config.trials,
                   "median_questions": statistics.median(settled) if settled else None}
            post_sd = [r["post_sd"] for r in results if r["post_sd"] is not None]
            row["post_sd"] = statistics.mean(post_sd) if post_sd else None
            for metric in ("accuracy", "expected", "sd", "reversals", "in_band"):
                row[metric] = statistics.mean(r[metric] for r in results)
            rows.append(row)
    return rows


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--seed", type=int, default=20261001)
    parser.add_argument("--trials", type=int, default=200)
    parser.add_argument("--questions", type=int, default=1600)
    parser.add_argument("--step", type=float, default=0.25)
    parser.add_argument("--player-k", type=float, default=0.15)
    parser.add_argument("--item-k", type=float, default=0.01)
    parser.add_argument("--rating-margin", type=float, default=1.0)
    parser.add_argument("--item-drift", type=float, default=0.125)
    parser.add_argument("--csv", help="Optional per-scenario aggregate CSV output path")
    args = parser.parse_args()
    if args.trials < 1 or args.questions < 1100:
        parser.error("Use at least 1 trial and 1100 questions (including fatigue recovery).")
    if (not math.isfinite(args.step) or not 0.01 <= args.step <= 1.0
            or not math.isclose(8 / args.step, round(8 / args.step))):
        parser.error("Step must divide the -2..6 range evenly and lie in [.01, 1].")
    if any(not math.isfinite(k) or not 0 <= k <= 1
           for k in (args.player_k, args.item_k)) or args.player_k == 0:
        parser.error("K values must be finite: player in (0, 1], item in [0, 1].")
    if not math.isfinite(args.rating_margin) or args.rating_margin <= 0:
        parser.error("Rating margin must be finite and positive.")
    if not math.isfinite(args.item_drift) or args.item_drift < 0:
        parser.error("Item drift must be finite and nonnegative.")
    config = Config(seed=args.seed, trials=args.trials, questions=args.questions,
                    step=args.step, player_k=args.player_k, item_k=args.item_k,
                    rating_margin=args.rating_margin, item_drift=args.item_drift)
    rows = simulate(config)
    print("Seed={seed}; trials={trials}; questions={questions}; step={step}; "
          "Elo K(player/item)={player_k}/{item_k}; rating margin={rating_margin}; "
          "item drift={item_drift}".format(**vars(config)))
    print("Settling: 100-question mean true P(correct) in 75–80% for 100 "
          "consecutive windows; median first qualifying window end, conditional "
          "on settling. Confirmation takes 99 more questions.")
    print("Tail: final 300 questions, all trials. Tired times count after q800. "
          "Post SD: after settling confirmation, settled trials only. "
          "SD in latent difficulty units; rev = direction reversals per 100 questions.")
    print("| Player | Policy | Settled % | Median q | Tail correct % | Tail P % "
          "| Post SD | Tail SD | Rev/100 | Tail windows in band % |")
    print("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|")
    for row in rows:
        median = "—" if row["median_questions"] is None else str(round(row["median_questions"]))
        post = "—" if row["post_sd"] is None else "{:.3f}".format(row["post_sd"])
        print("| {player} | {policy} | {settled_pct:.1f} | ".format(**row)
              + median + " | {accuracy:.1f} | {expected:.1f} | ".format(**row)
              + post + " | {sd:.3f} "
              "| {reversals:.1f} | {in_band:.1f} |".format(**row))
    if args.csv:
        with open(args.csv, "w", newline="", encoding="utf-8") as output:
            writer = csv.DictWriter(output, fieldnames=list(rows[0]))
            writer.writeheader()
            writer.writerows(rows)


if __name__ == "__main__":
    main()
