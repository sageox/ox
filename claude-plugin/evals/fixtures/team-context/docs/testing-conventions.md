---
title: Testing conventions
description: Table-driven tests, fake clocks, and the flake policy
visibility: indexed
when: writing or fixing tests, or a test is flaky
---

# Testing conventions

Table-driven tests; inject clocks, never sleep; a test that flakes twice in a
week is fixed or deleted the same week. `go test -count=200` is the bar for
"fixed" on a flake.
