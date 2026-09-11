---
title: On-call runbook
description: What to do when paged for the upload service
visibility: indexed
when: you are on call, a page fires, or an alert threshold needs changing
---

# On-call runbook

1. Check the artifact store status page.
2. If uploads fail with 503 during 02:00–03:00 UTC, it is the compaction window — retries cover it.
3. Anything else: page Devon (platform) for store problems, Avery for the listing API.
