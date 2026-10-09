"""The full-screen game loop. It knows no curses, only the `screen` protocol:

    size() -> (rows, cols)    draw(view)    key(timeout_ms) -> int
    flush_input()             resized()

so tests drive it with a scripted screen and a fake clock. All state changes go
through `ui_model`; all rules stay in the engine.
"""
import time

from .engine import Outcome
from .ui_model import (EOF, FLASH_SECONDS, KEY_RESIZE, RESIZE, SUBMIT, TICK_MS,
                       AnswerEditor, final_view, key_wait_ms, outcome_view, question_view,
                       too_small, with_buffer, with_clock, with_invalid)


def play(screen, game, clock=time.monotonic) -> None:
    """Drive `game` to its end. Quit/EOF stop it; Ctrl-C raises KeyboardInterrupt."""
    while not game.finished:
        question = game.questions[game.rounds]
        view = question_view(game, question)
        _wait_for_room(screen, view)
        screen.flush_input()
        screen.draw(view)
        game.start_question(clock())
        outcome, view = _answer(screen, game, question, view, clock)
        if outcome.kind == "quit":
            return
        _flash(screen, outcome_view(view, game, question, outcome), clock)
    screen.flush_input()
    _wait_for_key(screen, final_view(game))


def _wait_for_room(screen, view) -> None:
    """Hold before a question starts: the clock must not run in a tiny window."""
    while too_small(*screen.size()):
        screen.draw(view)
        if screen.key(100) == KEY_RESIZE:
            screen.resized()


def _answer(screen, game, question, view, clock):
    """Run one question; returns (outcome, last view)."""
    editor = AnswerEditor()
    while True:
        now = clock()
        remaining = game.deadline - now
        if game.timed:
            if remaining <= 0:
                return game.expire(question.id, now), with_buffer(view, editor.text)
            view = with_clock(view, remaining, game.time_limit)
        screen.draw(with_buffer(view, editor.text))
        key = screen.key(key_wait_ms(remaining, game.timed))
        at = clock()  # stamp the keystroke before doing anything else with it
        edit = editor.feed(key)
        if edit.action == RESIZE:
            screen.resized()
        elif edit.action == EOF:
            game.stop()
            return Outcome("quit"), view
        elif edit.action == SUBMIT:
            outcome = game.submit(question.id, edit.text, at)
            if outcome.kind != "invalid":
                return outcome, with_buffer(view, edit.text)
            view = with_invalid(view)


def _flash(screen, view, clock) -> None:
    until = clock() + FLASH_SECONDS[view.flash]
    while clock() < until:
        screen.draw(view)
        if screen.key(TICK_MS) == KEY_RESIZE:
            screen.resized()  # other keys are dropped; they belong to no prompt


def _wait_for_key(screen, view) -> None:
    while True:
        screen.draw(view)
        key = screen.key(100)
        if key == KEY_RESIZE:
            screen.resized()
        elif key != -1:
            return
