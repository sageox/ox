package usagepane

import (
	"encoding/json"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/paths"
)

func TestModFS_HoldsTheMod(t *testing.T) {
	tests := []struct {
		file string
		want string
	}{
		{".claude-plugin/plugin.json", `"version": "{{VERSION}}"`},
		{".claude-plugin/marketplace.json", `"name": "ox-cli-brief"`},
		{"hooks/hooks.json", `"./register.tsx"`},
		{"hooks/register.tsx", `command: 'ox-brief'`},
		{"hooks/logic.ts", `'brief', '--json', '--recent', '0'`},
		{"types/index.d.ts", `'ox-cli-brief-pane'`},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			b, err := fs.ReadFile(ModFS(), path.Join(ModRoot, tt.file))
			if err != nil {
				t.Fatalf("read %s: %v", tt.file, err)
			}
			if !strings.Contains(string(b), tt.want) {
				t.Errorf("%s does not contain %q", tt.file, tt.want)
			}
		})
	}
}

func TestManifests_NameThePlugin(t *testing.T) {
	var plugin struct {
		Name string `json:"name"`
	}
	b, err := fs.ReadFile(ModFS(), path.Join(ModRoot, ".claude-plugin/plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &plugin); err != nil {
		t.Fatalf("plugin.json: %v", err)
	}
	if plugin.Name != PluginName {
		t.Errorf("plugin.json name = %q, want %q", plugin.Name, PluginName)
	}

	var market struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"plugins"`
	}
	b, err = fs.ReadFile(ModFS(), path.Join(ModRoot, ".claude-plugin/marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &market); err != nil {
		t.Fatalf("marketplace.json: %v", err)
	}
	if market.Name != MarketplaceName || len(market.Plugins) != 1 || market.Plugins[0].Name != PluginName || market.Plugins[0].Source != "./" {
		t.Errorf("marketplace.json = %+v, want %s listing %s at ./", market, MarketplaceName, PluginName)
	}
	if PluginRef != PluginName+"@"+MarketplaceName {
		t.Errorf("PluginRef = %q", PluginRef)
	}
}

func TestDir_IsUnderOxData(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"current", Dir(), filepath.Join(paths.DataDir(), "mods", "claude-code", "ox-cli-brief-pane")},
		{"legacy", LegacyDir(), filepath.Join(paths.DataDir(), "mods", "claude-code", "ox-cli-usage-pane")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("dir = %q, want %q", tt.got, tt.want)
			}
		})
	}
	if Dir() == LegacyDir() {
		t.Error("Dir and LegacyDir must differ, or the legacy cleanup would remove the current pane")
	}
}
