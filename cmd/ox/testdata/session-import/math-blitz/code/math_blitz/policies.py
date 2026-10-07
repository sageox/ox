"""Scoring policy, independent of presentation."""
import math


def correct_points(elapsed: float, time_limit: float, previous_streak: int,
                   timed: bool = True) -> int:
    speed = 50 * max(0.0, min(time_limit, time_limit - elapsed)) / time_limit if timed else 0
    return 100 + math.floor(speed) + min(50, 10 * previous_streak)
