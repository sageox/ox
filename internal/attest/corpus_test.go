package attest

import (
	"os"
	"path/filepath"
	"testing"
)

// writeCorpus lays out a throwaway corpus: <root>/features/<domain>/<name>.
func writeCorpus(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, featuresSubdir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

func scan(t *testing.T, files map[string]string) *Corpus {
	t.Helper()
	root := writeCorpus(t, files)
	corpus, err := ScanCorpus(root, root)
	if err != nil {
		t.Fatalf("ScanCorpus: %v", err)
	}
	return corpus
}

func findCap(t *testing.T, c *Corpus, id string) Capability {
	t.Helper()
	for _, cap := range c.Capabilities {
		if cap.ID == id {
			return cap
		}
	}
	t.Fatalf("capability %q not found; have %v", id, capIDs(c))
	return Capability{}
}

func capIDs(c *Corpus) []string {
	out := make([]string, 0, len(c.Capabilities))
	for _, cap := range c.Capabilities {
		out = append(out, cap.ID)
	}
	return out
}

func TestScanCorpus_MultipleRulesBecomeSeparateCapabilities(t *testing.T) {
	c := scan(t, map[string]string{
		"sharing/share-links.feature": `Feature: Share links

  Rule: Revoked share links return a client error, never a server error
    Scenario: A revoked link 410s
      Given a revoked link

  Rule: Share links honor a max-uses cap
    Scenario: The cap is enforced
      Given a link with a cap
    Scenario: The cap is reported
      Given a link with a cap
`,
	})

	if got := len(c.Capabilities); got != 2 {
		t.Fatalf("capabilities = %d, want 2 (one per Rule); ids=%v", got, capIDs(c))
	}
	revoked := findCap(t, c, "sharing/share-links#revoked-share-links-return-a-client-error-never-a-server-error")
	if got := len(revoked.Scenarios); got != 1 {
		t.Errorf("first Rule scenarios = %d, want 1", got)
	}
	cap2 := findCap(t, c, "sharing/share-links#share-links-honor-a-max-uses-cap")
	if got := len(cap2.Scenarios); got != 2 {
		t.Errorf("second Rule scenarios = %d, want 2", got)
	}
	if revoked.Domain != "sharing" {
		t.Errorf("domain = %q, want %q", revoked.Domain, "sharing")
	}
}

// A scenario written above any Rule must survive. 100% of the SageOx corpus is
// ruled today, but a customer's need not be, and a vanished scenario is a wrong
// answer rather than a missing feature.
func TestScanCorpus_ScenarioAboveAnyRuleIsNotDropped(t *testing.T) {
	c := scan(t, map[string]string{
		"onboarding/signup.feature": `Feature: Signup

  Scenario: Someone signs up before any Rule is declared
    Given a browser

  Rule: An invitation can be accepted once
    Scenario: Accepting twice fails
      Given an invite
`,
	})

	if got := len(c.Capabilities); got != 2 {
		t.Fatalf("capabilities = %d, want 2 (synthetic + ruled); ids=%v", got, capIDs(c))
	}
	synthetic := findCap(t, c, "onboarding/signup")
	if synthetic.Rule != "" {
		t.Errorf("synthetic capability Rule = %q, want empty", synthetic.Rule)
	}
	if got := len(synthetic.Scenarios); got != 1 {
		t.Fatalf("synthetic scenarios = %d, want 1 — the ungrouped scenario vanished", got)
	}
	if synthetic.Title() == "" {
		t.Error("synthetic capability must still render a title")
	}
}

// `Examples:` is an Outline's data table, NOT a scenario. Counting it would
// inflate the authored total, which is precisely the over-claim this tool
// exists to prevent.
func TestScanCorpus_ExamplesTableIsNotAScenario(t *testing.T) {
	c := scan(t, map[string]string{
		"repository/browse.feature": `Feature: Browse

  Rule: A member can view their team's repository
    Scenario Outline: A member views <surface>
      Given a member
      Examples:
        | surface |
        | overview |
        | sessions |
`,
	})

	cap := findCap(t, c, "repository/browse#a-member-can-view-their-team-s-repository")
	if got := len(cap.Scenarios); got != 1 {
		t.Fatalf("scenarios = %d, want 1 — an Examples: table was counted as a scenario", got)
	}
	if cap.Scenarios[0].Name != "A member views <surface>" {
		t.Errorf("scenario name = %q", cap.Scenarios[0].Name)
	}
}

func TestScanCorpus_ExamplesTagsDoNotLeakIntoNextScenario(t *testing.T) {
	c := scan(t, map[string]string{
		"repository/browse.feature": `Feature: Browse

  Rule: A member can browse
    Scenario Outline: A member views <surface>
      Given a member
      @validated
      Examples: common surfaces
        | surface |
        | overview |

    Scenario: A member views settings
      Given a member
`,
	})

	cap := findCap(t, c, "repository/browse#a-member-can-browse")
	if got := len(cap.Scenarios); got != 2 {
		t.Fatalf("scenarios = %d, want 2", got)
	}
	if cap.Scenarios[1].HasTag(TagValidated) {
		t.Fatalf("Examples tag leaked into the following scenario: %v", cap.Scenarios[1].Tags)
	}
}

func TestScanCorpus_ScenarioIndexIsFeatureWideAcrossRules(t *testing.T) {
	c := scan(t, map[string]string{
		"devices/pairing.feature": `Feature: Pairing

  Rule: Phones pair
    Scenario: A device pairs
      Given a phone

  Rule: Tablets pair
    Scenario: A device pairs
      Given a tablet
`,
	})

	if got := c.Capabilities[0].Scenarios[0].Index; got != 0 {
		t.Errorf("first scenario index = %d, want 0", got)
	}
	if got := c.Capabilities[1].Scenarios[0].Index; got != 1 {
		t.Errorf("second scenario index = %d, want 1", got)
	}
}

// `@pending-migration` is NOT `@pending`. Exact equality, never prefix.
func TestScenario_HasTagIsExactNotPrefix(t *testing.T) {
	c := scan(t, map[string]string{
		"knowledge/kb.feature": `Feature: KB

  Rule: A bubble can be created
    @pending-migration
    Scenario: Still dispatches
      Given a bubble
    @pending
    Scenario: Genuinely switched off
      Given a bubble
`,
	})

	cap := findCap(t, c, "knowledge/kb#a-bubble-can-be-created")
	if len(cap.Scenarios) != 2 {
		t.Fatalf("scenarios = %d, want 2", len(cap.Scenarios))
	}
	migration, off := cap.Scenarios[0], cap.Scenarios[1]

	if migration.HasTag(TagPending) {
		t.Error("@pending-migration matched @pending — prefix matching would silently switch off a live scenario")
	}
	if !migration.HasTag("@pending-migration") {
		t.Error("@pending-migration should match itself")
	}
	if !off.HasTag(TagPending) {
		t.Error("@pending should match @pending")
	}
}

// Tags merge feature ∪ rule ∪ scenario, which is the set the compiler applies.
func TestScanCorpus_TagsInheritFromFeatureAndRule(t *testing.T) {
	c := scan(t, map[string]string{
		"security/auth.feature": `@security
Feature: Auth

  @slow
  Rule: A session expires
    @critical
    Scenario: It expires
      Given a session
`,
	})

	cap := findCap(t, c, "security/auth#a-session-expires")
	s := cap.Scenarios[0]
	for _, want := range []string{"@security", "@slow", "@critical"} {
		if !s.HasTag(want) {
			t.Errorf("missing inherited tag %s; got %v", want, s.Tags)
		}
	}
}

// A Rule whose text contains a colon must still slug cleanly and keep its text
// verbatim — the claim is displayed to humans and must not be truncated.
func TestScanCorpus_RuleTextWithColonSurvives(t *testing.T) {
	c := scan(t, map[string]string{
		"cli/doctor.feature": `Feature: Doctor

  Rule: Diagnostics report: authentication status
    Scenario: It reports
      Given a CLI
`,
	})

	cap := c.Capabilities[0]
	if cap.Rule != "Diagnostics report: authentication status" {
		t.Errorf("Rule = %q, want the full text including the colon", cap.Rule)
	}
	if cap.ID != "cli/doctor#diagnostics-report-authentication-status" {
		t.Errorf("ID = %q", cap.ID)
	}
}

// Two Rules with identical text in one file must not collide into one id.
func TestScanCorpus_DuplicateRuleTextGetsDistinctIDs(t *testing.T) {
	c := scan(t, map[string]string{
		"devices/pairing.feature": `Feature: Pairing

  Rule: A device pairs
    Scenario: First
      Given a device

  Rule: A device pairs
    Scenario: Second
      Given a device
`,
	})

	if len(c.Capabilities) != 2 {
		t.Fatalf("capabilities = %d, want 2", len(c.Capabilities))
	}
	if c.Capabilities[0].ID == c.Capabilities[1].ID {
		t.Fatalf("duplicate Rule text collided on id %q — one capability shadows the other", c.Capabilities[0].ID)
	}
	if want := "devices/pairing#a-device-pairs-2"; c.Capabilities[1].ID != want {
		t.Errorf("second id = %q, want %q", c.Capabilities[1].ID, want)
	}
}

func TestScanCorpus_MissingCorpusIsAnError(t *testing.T) {
	root := t.TempDir()
	if _, err := ScanCorpus(root, filepath.Join(root, "nope")); err == nil {
		t.Fatal("expected an error for a missing corpus, got nil")
	}
}
