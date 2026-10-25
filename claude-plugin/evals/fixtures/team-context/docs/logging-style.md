---
title: Logging style
description: Single-line key=value logs, levels, and what never to log
visibility: indexed
when: adding a log line or choosing a log level
---

# Logging style

Single-line, `key=value`, no secrets, no request bodies. WARN is for degraded
but working; ERROR is for failed.
