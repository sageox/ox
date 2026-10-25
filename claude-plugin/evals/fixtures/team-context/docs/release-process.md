---
title: Release process
description: Tagging, release notes, and the rollout order
visibility: indexed
when: cutting a release, writing release notes, or rolling back
---

# Release process

Tag from main, draft notes from the merged PR list, roll out to staging for one
hour, then production. Riley owns the notes; rollback is `acme deploy --rollback`.
