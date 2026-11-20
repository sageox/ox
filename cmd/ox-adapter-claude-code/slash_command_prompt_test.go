package main

import "testing"

// A skill run with a request is the person's prompt; a bare control command is
// not. Without this, a session driven only by slash commands never went live.
func TestClassifyTextContentSlashCommands(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantText  string
		wantClass userContentClass
	}{
		{
			name:      "skill with a request is the prompt",
			text:      "<command-message>review is running…</command-message>\n<command-name>/review</command-name>\n<command-args>the auth change</command-args>",
			wantText:  "/review the auth change",
			wantClass: userContentUser,
		},
		{
			name:      "name without a leading slash gets one",
			text:      "<command-name>status-board</command-name><command-args>show threads</command-args>",
			wantText:  "/status-board show threads",
			wantClass: userContentUser,
		},
		{
			name:      "multi-line args are kept",
			text:      "<command-name>/goal</command-name><command-args>make sure\nthis works</command-args>",
			wantText:  "/goal make sure\nthis works",
			wantClass: userContentUser,
		},
		{
			name:      "bare control command stays system",
			text:      "<command-name>/clear</command-name><command-message>clear</command-message><command-args></command-args>",
			wantClass: userContentSystem,
		},
		{
			name:      "whitespace-only args stay system",
			text:      "<command-name>/compact</command-name><command-args>   </command-args>",
			wantClass: userContentSystem,
		},
		{
			name:      "typed prompt is unchanged",
			text:      "explain the retry logic",
			wantText:  "explain the retry logic",
			wantClass: userContentUser,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, class := classifyTextContent(tt.text)
			if class != tt.wantClass {
				t.Fatalf("class = %v, want %v (text %q)", class, tt.wantClass, got)
			}
			if tt.wantClass == userContentUser && got != tt.wantText {
				t.Fatalf("text = %q, want %q", got, tt.wantText)
			}
		})
	}
}
