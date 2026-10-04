`prototype-443.jsonl.gz` derives from the local prototype capture described in
"Completed Claude OTel Session Profile" (2026-09-19). It contains the same 177
OTLP envelopes, 443 spans, and two-session interleaving.

All native session IDs are synthetic UUIDs; trace/span IDs are deterministic
synthetic hashes. Arbitrary string values (including prompts, identities,
request IDs, tool-call IDs, paths, models, and errors) become `fixture-value`.
Only schema keys, the five built-in Claude span names, and numeric timestamps
and counters are retained. Identity attribute keys remain with synthetic values
so the golden test can verify removal and exact scrub counts. This is a protocol
and materialization fixture, not a copy suitable for profiling the original work.
