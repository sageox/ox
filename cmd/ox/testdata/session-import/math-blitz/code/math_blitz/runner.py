"""Terminal orchestration; the rules know nothing about this module."""
import argparse
import random
import time
from datetime import datetime, timezone
from .engine import Game
from .questions import ranked_questions
from .scores import (ScoreEntry, ScoreStore, ScoresReadOnly, default_path,
                     format_board, sanitize_name)
from .terminal import Terminal
from . import tui

NAME_PROMPT_SECONDS = 600


def run_is_ranked(game) -> bool:
    """Only a completed, timed run of a ranked-eligible game is recorded."""
    return (game.timed and not game.stopped and game.rounds == game.question_count
            and getattr(game, "ranked_eligible", True))


def offer_high_score(game, terminal, store, now=None):
    """Game-over screen: show the table, asking for a name only if the run makes it."""
    if not run_is_ranked(game):
        if not game.timed:
            print("Untimed practice is not eligible for the high-score table.")
        return
    mode = "ranked"
    rank = store.projected_rank(mode, game.score, game.best_streak)
    if store.notice:
        print(store.notice)
    highlight = None
    if rank is not None and not store.read_only:
        terminal.clear_pending()  # keys typed during the last question are not a name
        print("\nNew high score! You placed #{}. Enter your name: ".format(rank),
              end="", flush=True)
        try:
            reply = terminal.read(time.monotonic() + NAME_PROMPT_SECONDS)
        except KeyboardInterrupt:
            print("\nScore not saved.")
            reply = None
        else:
            now = now or datetime.now(timezone.utc)
            entry = ScoreEntry(mode, sanitize_name(reply.text or ""), game.score,
                               game.best_streak, now.strftime("%Y-%m-%dT%H:%M:%SZ"))
            try:
                highlight = store.add(entry)
            except (OSError, ScoresReadOnly) as error:
                print("Could not save your score: {}".format(error))
    board = store.board(mode)
    if board:
        print("\nHigh scores")
        print(format_board(board, highlight))


def parse_args(argv=None):
    parser = argparse.ArgumentParser(prog="math_game.py", description="Math Blitz")
    parser.add_argument("--plain", action="store_true",
                        help="line-based terminal mode instead of the full-screen UI")
    return parser.parse_args(argv)


def report(game, terminal):
    print("\nFinal score: {} | Correct: {}/{} | Best streak: {}".format(
        game.score, game.correct, game.rounds, game.best_streak))
    try:
        offer_high_score(game, terminal, ScoreStore(default_path()))
    except OSError as error:
        print("High scores unavailable: {}".format(error))


def play_full_screen():
    """Run the curses UI; None means it is unavailable and plain mode should run."""
    reason = tui.unavailable_reason()
    if reason is not None:
        if reason:
            print(reason)
        return None
    game = Game(ranked_questions(random.Random()), timed=True)
    try:
        tui.run(game)
    except tui.TuiUnavailable as error:
        print("Full-screen mode could not start ({}); using plain mode.".format(error))
        return None
    except KeyboardInterrupt:
        game.stop()
        print("Run ended.")
    return game


def main(argv=None):
    args = parse_args(argv)
    if not args.plain:
        game = play_full_screen()
        if game is not None:
            with Terminal() as terminal:
                report(game, terminal)
            return
    main_plain()


def main_plain():
    print("Math Blitz — 20 questions, one numeric attempt per question.")
    print("Type q to quit. Correct answers earn points for speed and streaks.")
    with Terminal() as terminal:
        if terminal.timed:
            print("You have 15 seconds per question.")
        else:
            print("Untimed practice: this input is not an interactive console.")
            print("Speed bonuses and ranked eligibility are disabled.")
        game = Game(ranked_questions(random.Random()), timed=terminal.timed)
        try:
            while not game.finished:
                terminal.clear_pending()
                question = game.questions[game.rounds]
                print("\n[{}/20] What is {}? ".format(game.rounds + 1, question.text),
                      end="", flush=True)
                game.start_question(time.monotonic())
                while True:
                    reply = terminal.read(game.deadline)
                    if reply.timed_out:
                        outcome = game.expire(question.id, reply.at)
                    elif reply.text is None:
                        game.stop()
                        break
                    else:
                        outcome = game.submit(question.id, reply.text, reply.at)
                    if outcome.kind == "invalid":
                        print("Enter a whole number (or q); {}: ".format(
                            "the timer keeps running" if terminal.timed else "try again"),
                            end="", flush=True)
                        continue
                    if outcome.kind == "quit":
                        break
                    message = {
                        "correct": "Correct! +{} points".format(outcome.points),
                        "wrong": "Not quite. Answer: {}".format(question.answer),
                        "timeout": "Too slow! Answer: {}".format(question.answer),
                    }[outcome.kind]
                    print("{} | Score: {} | Streak: {}".format(
                        message, game.score, game.streak))
                    break
        except KeyboardInterrupt:
            game.stop()
            print("\nRun ended.")
        report(game, terminal)
