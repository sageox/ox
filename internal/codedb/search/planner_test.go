package search

import "testing"

func TestPlanCodeBareSearch(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"foo"},
		Type:        SearchTypeCode,
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinBleveOnly {
		t.Errorf("strategy = %v, want BleveOnly", plan.Strategy)
	}
	if plan.BleveQuery != "foo" {
		t.Errorf("bleve query = %q", plan.BleveQuery)
	}
	if plan.BleveIndex != "code" {
		t.Errorf("bleve index = %q", plan.BleveIndex)
	}
	if plan.Limit != 20 {
		t.Errorf("limit = %d, want 20", plan.Limit)
	}
}

func TestPlanCodeWithFiltersIntersect(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"foo"},
		Type:        SearchTypeCode,
		Filters:     Filters{Lang: "go"},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinIntersect {
		t.Errorf("strategy = %v, want Intersect", plan.Strategy)
	}
}

func TestPlanCodeWithRepoFilter(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"bar"},
		Type:        SearchTypeCode,
		Filters:     Filters{Repo: "myrepo"},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinIntersect {
		t.Errorf("strategy = %v, want Intersect", plan.Strategy)
	}
}

func TestPlanCodeWithFileFilter(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"baz"},
		Type:        SearchTypeCode,
		Filters:     Filters{File: "*.go"},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinIntersect {
		t.Errorf("strategy = %v, want Intersect", plan.Strategy)
	}
}

func TestPlanCodeWithNegFilters(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"baz"},
		Type:        SearchTypeCode,
		Filters:     Filters{NegFile: "test"},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinIntersect {
		t.Errorf("strategy = %v, want Intersect", plan.Strategy)
	}
}

func TestPlanCodeWithRevFilter(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"baz"},
		Type:        SearchTypeCode,
		Filters:     Filters{Rev: "develop"},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinIntersect {
		t.Errorf("strategy = %v, want Intersect", plan.Strategy)
	}
}

func TestPlanCodeWithSelectFilter(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"baz"},
		Type:        SearchTypeCode,
		Filters:     Filters{Select: SelectFile},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinIntersect {
		t.Errorf("strategy = %v, want Intersect", plan.Strategy)
	}
}

func TestPlanCodeEmptyPatternError(t *testing.T) {
	q := &ParsedQuery{
		Type: SearchTypeCode,
	}
	_, err := Plan(q)
	if err == nil {
		t.Fatal("expected error for empty code search")
	}
}

func TestPlanCodeCustomCount(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"foo"},
		Type:        SearchTypeCode,
		Filters:     Filters{Count: 50},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Limit != 50 {
		t.Errorf("limit = %d, want 50", plan.Limit)
	}
}

func TestPlanCodeRegex(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{`fn\s+\w+`},
		Type:        SearchTypeCode,
		IsRegex:     true,
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsRegex {
		t.Error("expected IsRegex")
	}
}

func TestPlanDiffBareSearch(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"streaming"},
		Type:        SearchTypeDiff,
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinBleveOnly {
		t.Errorf("strategy = %v, want BleveOnly", plan.Strategy)
	}
	if plan.BleveIndex != "diff" {
		t.Errorf("bleve index = %q, want diff", plan.BleveIndex)
	}
}

func TestPlanDiffWithFiltersIntersect(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"streaming"},
		Type:        SearchTypeDiff,
		Filters:     Filters{Author: "alice"},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinIntersect {
		t.Errorf("strategy = %v, want Intersect", plan.Strategy)
	}
}

func TestPlanDiffEmptyPatternError(t *testing.T) {
	q := &ParsedQuery{
		Type: SearchTypeDiff,
	}
	_, err := Plan(q)
	if err == nil {
		t.Fatal("expected error for empty diff search")
	}
}

func TestPlanDiffWithDateFilters(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"fix"},
		Type:        SearchTypeDiff,
		Filters:     Filters{Before: "2026-01-01"},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinIntersect {
		t.Errorf("strategy = %v, want Intersect", plan.Strategy)
	}
}

func TestPlanCommitSQLOnly(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"refactor"},
		Type:        SearchTypeCommit,
		Filters:     Filters{Author: "bob"},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinSQLOnly {
		t.Errorf("strategy = %v, want SQLOnly", plan.Strategy)
	}
	if plan.SQL == "" {
		t.Error("expected non-empty SQL")
	}
}

func TestPlanSymbolSQLOnly(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"SFrame"},
		Type:        SearchTypeSymbol,
		Filters:     Filters{Lang: "rust"},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinSQLOnly {
		t.Errorf("strategy = %v, want SQLOnly", plan.Strategy)
	}
}

func TestPlanCallsSQLOnly(t *testing.T) {
	q := &ParsedQuery{
		Filters: Filters{Calls: "groupby"},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinSQLOnly {
		t.Errorf("strategy = %v, want SQLOnly", plan.Strategy)
	}
}

func TestPlanCalledBySQLOnly(t *testing.T) {
	q := &ParsedQuery{
		Filters: Filters{CalledBy: "process"},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinSQLOnly {
		t.Errorf("strategy = %v, want SQLOnly", plan.Strategy)
	}
}

func TestPlanReturnsSQLOnly(t *testing.T) {
	q := &ParsedQuery{
		Filters: Filters{Returns: "Iterator"},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinSQLOnly {
		t.Errorf("strategy = %v, want SQLOnly", plan.Strategy)
	}
}

func TestPlanCallsOverridesCodeType(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"foo"},
		Type:        SearchTypeCode,
		Filters:     Filters{Calls: "bar"},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinSQLOnly {
		t.Errorf("calls: should force SQLOnly, got %v", plan.Strategy)
	}
}

func TestJoinStrategyString(t *testing.T) {
	tests := []struct {
		s    JoinStrategy
		want string
	}{
		{JoinSQLOnly, "sql_only"},
		{JoinBleveOnly, "bleve_only"},
		{JoinIntersect, "intersect"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}
