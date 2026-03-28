package carts

import (
	"testing"
	"time"
)

func TestGenerateID(t *testing.T) {
	now := time.Now()
	id1 := GenerateID("Test title", "description", now)
	id2 := GenerateID("Test title", "description", now)
	id3 := GenerateID("Different title", "description", now)

	if id1 != id2 {
		t.Errorf("same inputs should produce same hash: %s != %s", id1, id2)
	}
	if id1 == id3 {
		t.Errorf("different inputs should produce different hash")
	}
	if len(id1) != 64 {
		t.Errorf("hash should be 64 hex chars, got %d", len(id1))
	}
}

func TestStatusIsValid(t *testing.T) {
	valid := []Status{StatusOpen, StatusInProgress, StatusClosed}
	for _, s := range valid {
		if !s.IsValid() {
			t.Errorf("expected %s to be valid", s)
		}
	}
	if Status("invalid").IsValid() {
		t.Error("expected 'invalid' status to be invalid")
	}
}

func TestIssueTypeIsValid(t *testing.T) {
	valid := []IssueType{TypeBug, TypeFeature, TypeTask, TypeEpic, TypeChore}
	for _, it := range valid {
		if !it.IsValid() {
			t.Errorf("expected %s to be valid", it)
		}
	}
	if IssueType("invalid").IsValid() {
		t.Error("expected 'invalid' type to be invalid")
	}
}

func TestDependencyTypeIsValid(t *testing.T) {
	valid := []DependencyType{DepBlocks, DepRelated, DepDiscoveredFrom}
	for _, d := range valid {
		if !d.IsValid() {
			t.Errorf("expected %s to be valid", d)
		}
	}
	if DependencyType("invalid").IsValid() {
		t.Error("expected 'invalid' dep type to be invalid")
	}
}

func TestIssueValidate(t *testing.T) {
	tests := []struct {
		name    string
		issue   Issue
		wantErr bool
	}{
		{
			name:    "empty title",
			issue:   Issue{Title: "", Status: StatusOpen, IssueType: TypeTask},
			wantErr: true,
		},
		{
			name:    "invalid priority",
			issue:   Issue{Title: "test", Priority: 5, Status: StatusOpen, IssueType: TypeTask},
			wantErr: true,
		},
		{
			name:    "valid issue",
			issue:   Issue{Title: "test", Priority: 2, Status: StatusOpen, IssueType: TypeTask},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.issue.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSetDefaults(t *testing.T) {
	i := &Issue{}
	i.SetDefaults()

	if i.Status != StatusOpen {
		t.Errorf("default status should be open, got %s", i.Status)
	}
	if i.IssueType != TypeTask {
		t.Errorf("default issue type should be task, got %s", i.IssueType)
	}
	if i.Source != "cli" {
		t.Errorf("default source should be cli, got %s", i.Source)
	}
}
