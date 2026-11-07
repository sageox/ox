# Brief: the SageOx flow lane

## Who

A coworker running Claude Code in the Claude desktop app with SageOx installed. They are reading
the transcript while an AI coworker works: prompts, tool rows, answers. They did not ask to see
SageOx; they want to know, at a glance and without reading, whether SageOx put anything in front
of the model on this turn, how much, and whether anything went back out.

## The moment

- **A tool row runs an `ox` call** (`ox query`, `ox agent prime`, a SageOx MCP tool, `ox murmur`).
  The row itself already says tool, digest, duration and status (Claude Code's own tool row). The
  call takes 0.2–3s, sometimes 10s+ (a cold `ox query`). It can fail (exit 1), be interrupted
  (Escape), or come back with nothing.
- **A prompt is submitted** and hooks attach team context (prime, murmurs, a ledger recall) before
  the model sees it. That happens in well under a second; nobody waits on it.
- What happens next: the model answers using that context. The lane is evidence, after the fact,
  of what reached it.

## What the system knows

One `FlowEvent` per call or per prompt's attachments: lane (team context, ledger, murmurs, code,
cloud), direction (in = SageOx to session, out = session to SageOx), a past-tense label
("searched team"), a present-participle step ("searching team discussions"), an estimated token
count, optionally an item count and noun ("5 discussions"), start and end times, failed or
interrupted. Nothing else: no exit code, no command text, no per-item names.

## Success, as behaviour

- They notice data arriving in the corner of their eye without stopping to read, and can tell a
  big load (prime, ~15k) from a small one (~80) by weight alone.
- They can tell in from out by shape, not only colour: filled dots arrive, hollow rings leave.
- While a call is open they read what it is doing ("searching team discussions"), not a spinner.
- When it failed they see it ended and nothing came back, in words.
- A screen-reader user hears the same sentence: "searched team: ~1.2k tokens from 5 discussions, 0.8s".
- Scrolling back through a long transcript shows settled lanes, nothing moving, nothing replaying.

## Constraints

- Drawn by the desktop as a plain `<img>` from an SVG source string: no script, no hover, no
  external refs; SMIL runs, CSS `<style>` and media queries work inside the image.
- Every redraw re-creates the image (scroll, on-screen changes, tool resolution). The SVG must be a
  pure function of (event, now) and resume in phase.
- One text line tall (24px), row width (assume 640, the host scales).
- SageOx design system (sageox-design/DESIGN.md): sage on crater/dust, silt neutrals, danger only
  for failure; rules 2 (motion layers, loops stop), 3 (colour + shape), 17 (label directly),
  18 (non-visual channel), 19 (name the step).
- Source size under 128 KiB, degrading rather than overflowing.

## Assumptions (not asked)

- The tool row above already shows the command; the lane does not repeat it, it repeats the
  outcome ("searched team · ~1.2k from 5 discussions · 0.8s").
- The host cannot tell us `prefers-reduced-motion` reliably inside an `<img>` for SMIL, so the
  caller may pass `reducedMotion` and the static frame of every animation is its end state.

## Direction

Brainstormed answers to "what could this show?":

1. A Tufte progress bar per call (20x1, value/max). Rejected: there is no max; tokens are not progress.
2. A sparkline of tokens per turn across the session. Rejected: belongs in the pane, not per row.
3. A chip with the token count only. Rejected: no direction, no motion, duplicates the pane.
4. **A lane from SageOx to the session along which context travels as dots.** Chosen: it carries
   direction, weight and state in one line, reads at a glance, and uses DESIGN.md's own marks
   (filled dot vs hollow ring, hairline / edge / thick-edge strokes).
5. A breathing live dot on the SageOx node while a call is open. Runner-up: needs an indefinite
   loop, which rule 2 forbids, and redraws would restart it.

Signatures:
- **Visual:** the lane itself, solid sage where it flowed, dotted with a ring where it is open, a
  faint stub where it ended with nothing, its stroke weight the residue of how much moved.
- **Motion:** context arrives as dots that converge into the receiving node, once, staggered.
