package viz

import "strings"

// PRMedium is the smallest truthful delivery medium for a reviewer question.
type PRMedium string

const (
	PRMediumNone    PRMedium = "none"
	PRMediumMermaid PRMedium = "github-mermaid"
	PRMediumRich    PRMedium = "rich"
)

// PRDecision is deterministic guidance for an AI teammate. Rich is never a
// default simply because a prompt asks to "show" something.
type PRDecision struct {
	Medium           PRMedium        `json:"medium"`
	Primary          string          `json:"primary,omitempty"`
	Variant          string          `json:"variant,omitempty"`
	Reason           string          `json:"reason"`
	RequiredEvidence string          `json:"required_evidence"`
	VisualContract   *VisualContract `json:"visual_contract,omitempty"`
}

func DecidePRMedium(intent string) PRDecision {
	q := normalize(intent)
	if anyPhrase(q, "one sentence", "simple summary", "summarize", "short summary", "rename", "test updates", "changelog") {
		return PRDecision{Medium: PRMediumNone, Reason: "the requested conclusion fits concise prose", RequiredEvidence: "one review-relevant sentence"}
	}
	if anyPhrase(q, "cpu core", "cpu cores", "concurrency", "parallel execution", "thread") {
		return richDecision("execution-trace", "the question needs simultaneous work on a shared clock", "named lanes, timing, wait or handoff state")
	}
	if anyPhrase(q, "event stream", "replay", "idempotency", "independent consumer", "durable topic") {
		return richDecision("event-stream", "the question needs durable fan-out or delivery semantics", "producer, topic, named consumers, and replay/idempotency semantics")
	}
	if anyPhrase(q, "p99", "latency", "cpu saturation", "deploy marker", "correlated signals") {
		return richDecision("operational-time-series", "the question needs measured change across time", "units, scale, observations, threshold, and causal marker")
	}
	if anyPhrase(q, "coverage matrix", "across agents", "across adapters", "compatibility matrix", "conformance suite") {
		return richDecision("coverage-matrix", "the question needs dense, comparable evidence across implementations", "named behaviors, implementations, explicit statuses, and the evidence standard for proven")
	}
	if anyPhrase(q, "pause resume lifecycle", "session lifecycle", "recovery transitions", "orphan recovery", "state lifecycle") {
		return richDecision("state-machine", "the question needs a guarded lifecycle with recovery or terminal distinctions", "observable states, labeled transitions, happy path, recovery path, and terminal states")
	}
	if anyPhrase(q, "multi-component architecture", "external adapters", "ownership zones", "trust boundary", "runtime boundaries") {
		return richDecision("architecture", "the question needs responsibility zones and meaningful boundary crossings", "named zones, essential components, consequential relationships, and the changed boundary")
	}
	if anyPhrase(q, "failure recovery sequence", "multi-writer recovery", "multi writer recovery", "push failure recovery", "crash recovery sequence") {
		return richDecision("sequence-diagram", "the question needs ordered failure and recovery behavior across several owners", "participants, ordered messages, the decisive safety step, and at most one recovery branch")
	}
	if anyPhrase(q, "multi-stage data flow", "streaming data flow", "sources transformations consumers", "changing data contract") {
		return richDecision("data-flow", "the question needs source, transformation, and consumer responsibilities around a changing data contract", "named sources, transformations, consumers, and the changed contract")
	}
	if anyPhrase(q, "work queue", "claim lease", "lease expiry", "atomic claim") {
		return richDecision("sequence-diagram", "the question needs ordered claim, lease, dispatch, and recovery behavior", "participants, ordered messages, the decisive atomic claim, and at most one reclaim branch")
	}
	if anyPhrase(q, "durable fan out", "consumer groups") {
		return richDecision("event-stream", "the question needs durable fan-out and independent consumer ownership", "producer, durable topic, named consumer groups, and replay/idempotency semantics")
	}
	if anyPhrase(q, "small multiples", "compare series") {
		return richDecision("small-multiples", "the question needs comparable repeated quantitative facets", "three to six equal-scale facets and one meaningful outlier")
	}
	if anyPhrase(q, "trend", "time series", "by day", "over time") {
		return richDecision("line-chart", "the question needs a quantitative trajectory", "unit, scale, observations, and threshold or baseline")
	}
	return PRDecision{Medium: PRMediumMermaid, Reason: "a compact causal or topology flow is clearest as GitHub-safe Mermaid", RequiredEvidence: "two to five named nodes and direct labeled edges"}
}

func richDecision(primary, reason, evidence string) PRDecision {
	contract, _ := VisualContractByID(primary)
	return PRDecision{
		Medium: PRMediumRich, Primary: primary, Variant: "standard",
		Reason: reason, RequiredEvidence: evidence, VisualContract: contract,
	}
}

func anyPhrase(q string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(" "+q+" ", " "+normalize(phrase)+" ") {
			return true
		}
	}
	return false
}
