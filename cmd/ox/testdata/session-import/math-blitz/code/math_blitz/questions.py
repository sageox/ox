"""Question data and reproducible generation."""
from dataclasses import dataclass
from random import Random
from typing import List, Optional


@dataclass(frozen=True)
class QuestionRequest:
    """Engine-selected family; generation uses an external random source."""
    id: int
    operation: str
    level: int

@dataclass(frozen=True)
class Question:
    id: int
    text: str
    answer: int
    operation: str
    level: Optional[int] = None


def make_question(question_id: int, operation: str, rng: Random) -> Question:
    if operation == "/":
        divisor, quotient = rng.randint(1, 12), rng.randint(1, 12)
        a, b, answer = divisor * quotient, divisor, quotient
    elif operation == "*":
        a, b = rng.randint(1, 12), rng.randint(1, 12)
        answer = a * b
    elif operation in ("+", "-"):
        a, b = rng.randint(1, 50), rng.randint(1, 50)
        if operation == "-":
            a, b = max(a, b), min(a, b)
        answer = a + b if operation == "+" else a - b
    else:
        raise ValueError("Unsupported operation: " + operation)
    return Question(question_id, "{} {} {}".format(a, operation, b), answer, operation)


def ranked_questions(rng: Random) -> List[Question]:
    operations = list("+-*/") * 5
    rng.shuffle(operations)
    return [make_question(i, op, rng) for i, op in enumerate(operations, 1)]
