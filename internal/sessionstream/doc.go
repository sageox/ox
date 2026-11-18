// Package sessionstream is the wire contract for real-time session capture
// (ADR-247): a client streams one append-only byte stream per conversation
// layer to api-go, which stores it in the S3 session store before answering.
//
// The package is dependency-free on purpose. api-go, the Temporal worker, the
// session-capture twin and the reference client all import it, so the shapes
// they exchange cannot drift. Everything a client and server must agree on
// byte-for-byte lives here: routes, signatures, the stream hash, the batch
// rules, object keys and the JSON bodies.
//
// Offsets are an ingest cursor only. Durable references into a session stay
// turn-relative (ADR-057); nothing outside this contract should persist a byte
// offset as an anchor.
package sessionstream
