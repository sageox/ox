"""Deterministic adaptive-practice policies, independent of scoring and I/O."""
import math
from dataclasses import dataclass
from typing import Dict, List, Protocol, Sequence


class DifficultyPolicy(Protocol):
    """Select without consuming an answer; update once per resolved question.

    Levels are zero-based indices into the caller's question-family catalog.
    A policy owns separate state for each operation. Selection must be stable
    until update; neither method performs I/O, reads a clock, or draws randomness.
    """

    def select(self, operation: str) -> int:
        ...

    def update(self, operation: str, level: int, correct: bool) -> None:
        ...

    def reset(self) -> None:
        """Discard learned state while retaining configuration for a new run."""
        ...


@dataclass
class _Ratings:
    ability: float
    families: List[float]


class EloDifficulty:
    """Select the family nearest the player's target success probability.

    Ratings use natural-log logistic units, not chess's 400-point scale. Default
    priors match the simulation's 33 abstract families; they are not calibrated
    estimates for real arithmetic. Callers may supply a calibrated catalog.
    Create a fresh instance for each player/session; persistence is external.
    """

    def __init__(self, family_ratings: Sequence[float] = tuple(
            -2.0 + i * 0.25 for i in range(33)), *, target: float = 0.775,
            player_k: float = 0.15, item_k: float = 0.01,
            rating_margin: float = 1.0, item_drift: float = 0.125):
        priors = tuple(family_ratings)
        if (not priors or any(not math.isfinite(r) for r in priors)
                or any(a >= b for a, b in zip(priors, priors[1:]))):
            raise ValueError("Family ratings must be finite and strictly increasing")
        if not math.isfinite(target) or not 0 < target < 1:
            raise ValueError("Target must be strictly between zero and one")
        if not math.isfinite(player_k) or not 0 < player_k <= 1:
            raise ValueError("Player update rate must be finite and in (0, 1]")
        if not math.isfinite(item_k) or not 0 <= item_k <= 1:
            raise ValueError("Family update rate must be finite and in [0, 1]")
        if not math.isfinite(rating_margin) or rating_margin <= 0:
            raise ValueError("Rating margin must be finite and positive")
        if not math.isfinite(item_drift) or item_drift < 0:
            raise ValueError("Family drift must be finite and nonnegative")
        self.family_ratings = priors
        self.target, self.player_k, self.item_k = target, player_k, item_k
        self.rating_margin, self.item_drift = rating_margin, item_drift
        self._offset = math.log(target / (1 - target))
        self.ability_bounds = (priors[0] + self._offset - rating_margin,
                               priors[-1] + self._offset + rating_margin)
        bounds = []
        for i, prior in enumerate(priors):
            lower, upper = prior - item_drift, prior + item_drift
            if i:
                midpoint = priors[i - 1] / 2 + prior / 2
                lower = max(lower, math.nextafter(midpoint, prior))
            if i + 1 < len(priors):
                midpoint = prior / 2 + priors[i + 1] / 2
                upper = min(upper, math.nextafter(midpoint, prior))
            bounds.append((lower, upper))
        self.family_bounds = tuple(bounds)
        if any(not math.isfinite(x) for pair in
               (self.ability_bounds,) + self.family_bounds for x in pair):
            raise ValueError("Rating bounds must be finite")
        self._operations: Dict[str, _Ratings] = {}

    def reset(self) -> None:
        self._operations.clear()

    def _state(self, operation: str) -> _Ratings:
        if operation not in ("+", "-", "*", "/"):
            raise ValueError("Unsupported operation: " + operation)
        if operation not in self._operations:
            self._operations[operation] = _Ratings(
                self.family_ratings[0] + self._offset, list(self.family_ratings))
        return self._operations[operation]

    def select(self, operation: str) -> int:
        state = self._state(operation)
        desired = state.ability - self._offset
        # Searching a finite catalog bounds selection even at extreme abilities.
        # Index breaks ties reproducibly in favor of the initially easier family.
        return min(range(len(state.families)),
                   key=lambda i: (abs(state.families[i] - desired), i))

    def update(self, operation: str, level: int, correct: bool) -> None:
        if type(level) is not int or not 0 <= level < len(self.family_ratings):
            raise ValueError("Level must be an index in the question-family catalog")
        if type(correct) is not bool:
            raise ValueError("Correct must be a boolean")
        state = self._state(operation)
        difference = state.ability - state.families[level]
        # Stable logistic calculation also handles long runs at either boundary.
        if difference >= 0:
            predicted = 1 / (1 + math.exp(-difference))
        else:
            exponent = math.exp(difference)
            predicted = exponent / (1 + exponent)
        residual = int(correct) - predicted
        state.ability = max(self.ability_bounds[0], min(self.ability_bounds[1],
                            state.ability + self.player_k * residual))
        updated = state.families[level] - self.item_k * residual
        # A long run of misses must not reclassify the easiest arithmetic family
        # as harder than its neighbor and send a struggling player upward.
        # Fixed prior-centered cells preserve order and prevent a local player's
        # fatigue or perfect streak from moving the entire question bank.
        lower, upper = self.family_bounds[level]
        state.families[level] = max(lower, min(upper, updated))
