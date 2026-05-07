package main

import (
	"reflect"
	"testing"
)

func TestRewriteFormatAlias(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "no format flag passes through",
			in:   []string{"session", "list"},
			want: []string{"session", "list"},
		},
		{
			name: "format=json equals form",
			in:   []string{"session", "list", "--format=json"},
			want: []string{"session", "list", "--json"},
		},
		{
			name: "format json space form",
			in:   []string{"session", "list", "--format", "json"},
			want: []string{"session", "list", "--json"},
		},
		{
			name: "format=text equals form",
			in:   []string{"session", "list", "--format=text"},
			want: []string{"session", "list", "--json=false"},
		},
		{
			name: "format text space form",
			in:   []string{"session", "list", "--format", "text"},
			want: []string{"session", "list", "--json=false"},
		},
		{
			name: "unknown format value passes through (cobra/friction handles)",
			in:   []string{"session", "list", "--format=yaml"},
			want: []string{"session", "list", "--format=yaml"},
		},
		{
			name: "format with unknown value space form passes through",
			in:   []string{"session", "list", "--format", "yaml"},
			want: []string{"session", "list", "--format", "yaml"},
		},
		{
			name: "format at end with no value passes through",
			in:   []string{"session", "list", "--format"},
			want: []string{"session", "list", "--format"},
		},
		{
			name: "format followed by another flag does not consume",
			in:   []string{"session", "list", "--format", "--limit", "5"},
			want: []string{"session", "list", "--format", "--limit", "5"},
		},
		{
			name: "format followed by short flag does not consume",
			in:   []string{"session", "list", "--format", "-v"},
			want: []string{"session", "list", "--format", "-v"},
		},
		{
			name: "format=json mixed with other flags",
			in:   []string{"session", "list", "--limit", "20", "--format=json"},
			want: []string{"session", "list", "--limit", "20", "--json"},
		},
		{
			name: "multiple format aliases in one invocation (last wins via cobra)",
			in:   []string{"--format=text", "session", "list", "--format=json"},
			want: []string{"--json=false", "session", "list", "--json"},
		},
		{
			name: "empty args",
			in:   []string{},
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteFormatAlias(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("rewriteFormatAlias(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
