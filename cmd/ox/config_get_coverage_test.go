package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContainsLevel_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		levels []ConfigLevel
		target ConfigLevel
		want   bool
	}{
		{
			name:   "all levels present, find user",
			levels: []ConfigLevel{ConfigLevelUser, ConfigLevelRepo, ConfigLevelTeam, ConfigLevelDefault},
			target: ConfigLevelUser,
			want:   true,
		},
		{
			name:   "all levels present, find default",
			levels: []ConfigLevel{ConfigLevelUser, ConfigLevelRepo, ConfigLevelTeam, ConfigLevelDefault},
			target: ConfigLevelDefault,
			want:   true,
		},
		{
			name:   "custom config level not in list",
			levels: []ConfigLevel{ConfigLevelUser},
			target: ConfigLevel("custom"),
			want:   false,
		},
		{
			name:   "empty string level",
			levels: []ConfigLevel{ConfigLevel("")},
			target: ConfigLevel(""),
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := containsLevel(tt.levels, tt.target)
			assert.Equal(t, tt.want, got)
		})
	}
}
