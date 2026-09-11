---
title: CI pipeline
description: What runs on every PR and how caching works
visibility: indexed
when: a PR build is slow or failing in CI but passing locally
---

# CI pipeline

Lint → unit → integration. Module and build caches are keyed on go.sum.
