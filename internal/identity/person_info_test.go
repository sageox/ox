package identity

import (
	"testing"

	"github.com/sageox/ox/internal/auth"
	"github.com/stretchr/testify/assert"
)

func TestNewPersonInfo(t *testing.T) {
	tests := []struct {
		name              string
		email             string
		fullName          string
		gitUsername       string
		configDisplayName string
		wantDisplayName   string
	}{
		{
			name:              "config display name wins over all",
			email:             "person.a@gmail.com",
			fullName:          "Person A",
			configDisplayName: "port8080",
			wantDisplayName:   "port8080",
		},
		{
			name:            "full name to first-last-initial",
			email:           "person.a@gmail.com",
			fullName:        "Person A",
			wantDisplayName: "Person A.",
		},
		{
			name:            "email local part with dot delimiter",
			email:           "person.a@gmail.com",
			wantDisplayName: "Person A.",
		},
		{
			name:            "email local part single token",
			email:           "persona@corp.com",
			wantDisplayName: "Persona",
		},
		{
			name:            "name only, no email",
			fullName:        "Person B",
			wantDisplayName: "Person B.",
		},
		{
			name:            "git username with hyphen delimiter",
			gitUsername:     "person-a",
			wantDisplayName: "Person A.",
		},
		{
			name:            "single first name only",
			fullName:        "Person",
			wantDisplayName: "Person",
		},
		{
			name:            "all empty returns Anonymous",
			wantDisplayName: "Anonymous",
		},
		{
			name:            "uppercase email preserved",
			email:           "PERSON.A@GMAIL.COM",
			wantDisplayName: "PERSON A.",
		},
		{
			name:            "email with underscore delimiter",
			email:           "person_a@example.com",
			wantDisplayName: "Person A.",
		},
		{
			name:            "git username with dots",
			gitUsername:     "person.b",
			wantDisplayName: "Person B.",
		},
		{
			name:            "three-part name uses last",
			fullName:        "Mary Jane Watson",
			wantDisplayName: "Mary W.",
		},
		{
			name:              "config display name wins over email",
			email:             "someone@example.com",
			configDisplayName: "cooldev",
			wantDisplayName:   "cooldev",
		},
		{
			name:              "whitespace-only config falls through to name",
			email:             "person@example.com",
			fullName:          "Person A",
			configDisplayName: "   ",
			wantDisplayName:   "Person A.",
		},
		{
			name:              "config with leading/trailing whitespace is trimmed",
			configDisplayName: "  port8080  ",
			wantDisplayName:   "port8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPersonInfo(tt.email, tt.fullName, tt.gitUsername, tt.configDisplayName)
			assert.Equal(t, tt.wantDisplayName, p.DisplayName)
			assert.Equal(t, tt.email, p.Email)
		})
	}
}

func TestNewPersonInfo_Determinism(t *testing.T) {
	for i := 0; i < 100; i++ {
		a := NewPersonInfo("person.a@gmail.com", "Person A", "persona", "")
		b := NewPersonInfo("person.a@gmail.com", "Person A", "persona", "")
		assert.Equal(t, a.DisplayName, b.DisplayName, "iteration %d: expected deterministic output", i)
	}
}

func TestNewPersonInfoFromAuth(t *testing.T) {
	info := auth.UserInfo{
		UserID: "user-123",
		Email:  "person.a@gmail.com",
		Name:   "Person A",
	}

	p := NewPersonInfoFromAuth(info, "")
	assert.Equal(t, "Person A.", p.DisplayName)
	assert.Equal(t, "person.a@gmail.com", p.Email)

	// config override via auth constructor
	p2 := NewPersonInfoFromAuth(info, "port8080")
	assert.Equal(t, "port8080", p2.DisplayName)
}

func TestPersonInfo_String(t *testing.T) {
	p := NewPersonInfo("test@example.com", "Test User", "", "")
	assert.Equal(t, "Test U.", p.String())

	var nilP *PersonInfo
	assert.Equal(t, "Anonymous", nilP.String())
}

func TestFormatNameAsDisplay(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Person A", "Person A."},
		{"Person", "Person"},
		{"", ""},
		{"  Person  A  ", "Person A."},
		{"PERSON A", "PERSON A."},
		{"person a", "Person A."},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, formatNameAsDisplay(tt.input))
		})
	}
}

func TestFormatIdentifierAsDisplay(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"person.a", "Person A."},
		{"person-a", "Person A."},
		{"person_a", "Person A."},
		{"persona", "Persona"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, formatIdentifierAsDisplay(tt.input))
		})
	}
}

func TestExtractLocalPart(t *testing.T) {
	assert.Equal(t, "person", extractLocalPart("person@example.com"))
	assert.Equal(t, "noemail", extractLocalPart("noemail"))
	assert.Equal(t, "", extractLocalPart("@bare"))
}

func TestSplitIdentifier(t *testing.T) {
	assert.Equal(t, []string{"person", "a"}, splitIdentifier("person.a"))
	assert.Equal(t, []string{"person", "a"}, splitIdentifier("person-a"))
	assert.Equal(t, []string{"person", "a"}, splitIdentifier("person_a"))
	assert.Equal(t, []string{"persona"}, splitIdentifier("persona"))
}

func TestCapitalize(t *testing.T) {
	assert.Equal(t, "Person", capitalize("person"))
	assert.Equal(t, "PERSON", capitalize("PERSON"))
	assert.Equal(t, "A", capitalize("a"))
	assert.Equal(t, "", capitalize(""))
}
