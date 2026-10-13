# Session import fixture

The real Math Blitz recordings and sample repository are a shared public release
asset, rather than copies checked into test consumers. `artifact.json` pins its
immutable URL and SHA-256. The archive includes provenance and the MIT license.

`scripts/session_import_testbed.py create TARGET` downloads and verifies this pin
automatically. Go corpus tests use the same loader. The cache lives under
`${XDG_CACHE_HOME:-~/.cache}/sageox/test-fixtures/session-import/`, keyed by the
digest. Warm runs verify the cached bytes and need no network access.

To prime the cache separately:

```sh
python3 scripts/session_import_testbed.py fixture
```

A missing or changed artifact fails the test; it never silently skips recordings.
Production ox commands do not use this asset. `create --fixture DIR` still supports
an explicitly supplied local fixture for capture review and synthetic tests.

To change the corpus, capture and review a new fixture, publish it as a new release
asset, and update the pin. Keep an existing release asset unchanged so older
checkouts remain reproducible.
