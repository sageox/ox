// Package markers holds SageOx repo-marker filenames that must be referenced
// from packages which cannot import each other without an import cycle.
//
// The doctor package owns marker semantics (set/clear/check), but the daemon's
// agentwork producer also needs the .needs-doctor-agent filename to bridge the
// marker into an agent task — and agentwork cannot import doctor (that cycles:
// doctor → daemon → agentwork). This leaf package (no imports) is the single
// source of truth both sides reference, so a rename can never silently diverge.
package markers

// NeedsDoctorAgent is the .sageox/ marker dropped when an agent session ends
// with incomplete artifacts, signaling that the next live coworker should run
// doctor's finalize/recover flow.
const NeedsDoctorAgent = ".needs-doctor-agent"
