package adapterruntime

import "testing"

func TestValidateSessionID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "empty", id: "", wantErr: false},
		{name: "valid uuid", id: "550e8400-e29b-41d4-a716-446655440000", wantErr: false},
		{name: "valid alphanumeric", id: "session-abc-123", wantErr: false},
		{name: "path traversal", id: "../../../etc/passwd", wantErr: true},
		{name: "double dot", id: "foo..bar", wantErr: true},
		{name: "forward slash", id: "foo/bar", wantErr: true},
		{name: "backslash", id: "foo\\bar", wantErr: true},
		{name: "absolute path", id: "/etc/passwd", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSessionID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSessionID(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
			}
		})
	}
}
