package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/api/kbconfig"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/paths"
	"github.com/spf13/cobra"
)

// kbConfigCmd is the parent for read-only KB config commands.
//
// `ox kb config get <key>` and `ox kb config list` resolve effective values
// across env / kb / user / default layers. No `set` verb in v1 — KB-layer
// writes go through the sageox-mono PATCH /config endpoint; user-layer writes
// go through the existing `ox config set` surface.
var kbConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect KB configuration values (read-only)",
	Long: `Inspect the effective value of a KB configuration key across precedence layers.

Layers (highest precedence first):
  env      OX_SESSION_RECORDING (session_recording.mode only)
  kb       .sageox/config.yaml inside the KB checkout
  user     ~/.config/sageox/config.yaml (or ~/.sageox/config/config.yaml)
  default  per-KB-type fallback

session_recording.mode has a safety inversion: a "disabled" on either kb OR
user layer wins regardless of precedence. ` + "`--show-origin`" + ` marks the layer
that triggered the inversion.`,
}

var kbConfigGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Print the effective value of a KB config key",
	Args:  cobra.ExactArgs(1),
	RunE:  runKBConfigGet,
}

var kbConfigListCmd = &cobra.Command{
	Use:   "list",
	Short: "List effective values for all known KB config keys",
	Args:  cobra.NoArgs,
	RunE:  runKBConfigList,
}

func init() {
	kbCmd.AddCommand(kbConfigCmd)
	kbConfigCmd.AddCommand(kbConfigGetCmd)
	kbConfigCmd.AddCommand(kbConfigListCmd)

	for _, c := range []*cobra.Command{kbConfigGetCmd, kbConfigListCmd} {
		c.Flags().String("kb", "", "Target KB by slug, #slug, or kb_id (default: resolve from cwd)")
		c.Flags().Bool("show-origin", false, "Print each layer's value with origin")
		c.Flags().Bool("json", false, "Emit JSON output")
	}
	kbConfigGetCmd.Flags().String("at", "", "Print only the value at this scope (env|kb|user|default)")
}

// kbConfigContext captures resolved inputs shared by get/list.
type kbConfigContext struct {
	kbID       string
	kbType     string // best-effort; "" when unknown (defaults to manual)
	kbConfig   *kbconfig.ConfigYAMLEnvelope
	kbCfgPath  string // absolute path of the kb-layer config.yaml (whether or not it exists)
	userCfg    *config.UserConfig
	userCfgSrc string // human label for the user layer
}

// kbConfigResolver lets tests stub the slug→kb_id resolution without standing
// up a real kb-API client. Production wiring calls resolveKBInputForCmd, which
// reuses the same dependency tree kb_path.go uses (see kb_resolve.go).
var kbConfigResolver = func(ctx context.Context, raw string) (string, error) {
	return resolveKBInputForCmd(ctx, raw)
}

type kbMetaSummary struct {
	Type string `json:"type"`
}

func loadKBConfigContext(cmd *cobra.Command) (*kbConfigContext, error) {
	stderr := cmd.ErrOrStderr()

	kbFlag, _ := cmd.Flags().GetString("kb")
	kbFlagRaw := kbFlag // preserve original form for error formatting (`#marketing` vs `marketing`).
	kbFlag = strings.TrimSpace(kb.NormalizeSlugArg(kbFlag))

	var kbID string
	if kbFlag != "" {
		// Accept kb_id, bare slug, or `#slug`. The shared resolver in
		// kb_resolve.go mirrors `ox kb path` semantics exactly so users get a
		// uniform --kb argument surface across every kb subcommand.
		if strings.HasPrefix(kbFlag, "kb_") {
			kbID = kbFlag
		} else {
			resolved, err := kbConfigResolver(cmd.Context(), kbFlagRaw)
			if err != nil {
				msg, retErr := formatKBInputError(kbFlagRaw, err)
				fmt.Fprint(stderr, msg)
				return nil, retErr
			}
			kbID = resolved
		}
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get cwd: %w", err)
		}
		binding, err := kb.ResolveCurrentKB(cwd)
		if err != nil {
			return nil, err
		}
		if binding == nil {
			fmt.Fprintln(stderr, "no current KB. Run from inside a .sageox/-mapped tree, or pass --kb=<slug>.")
			return nil, cli.ErrSilent
		}
		kbID = binding.KBID
	}

	ctx := &kbConfigContext{kbID: kbID}

	// KB-layer config.yaml lives at <KBDir>/.sageox/config.yaml.
	ctx.kbCfgPath = filepath.Join(paths.KBDir(kbID), kbconfig.ConfigYAMLPath)
	if data, err := os.ReadFile(ctx.kbCfgPath); err == nil {
		env, err := kbconfig.UnmarshalConfigYAML(data)
		if err != nil {
			fmt.Fprintf(stderr, "kb config: parse %s: %v\n", ctx.kbCfgPath, err)
			return nil, cli.ErrSilent
		}
		ctx.kbConfig = env
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", ctx.kbCfgPath, err)
	}

	// User layer.
	userCfg, _ := config.LoadUserConfig()
	if userCfg == nil {
		userCfg = &config.UserConfig{}
	}
	ctx.userCfg = userCfg
	if envPath := os.Getenv(config.EnvUserConfig); envPath != "" {
		ctx.userCfgSrc = envPath
	} else {
		ctx.userCfgSrc = filepath.Join(config.GetUserConfigDir(), "config.yaml")
	}

	// KBType: best-effort from the daemon-written local metadata. This keeps
	// `ox kb config` offline while still surfacing correct per-type defaults
	// for synced bubbles.
	ctx.kbType = loadKBTypeFromMeta(kbID)

	return ctx, nil
}

func loadKBTypeFromMeta(kbID string) string {
	if kbID == "" {
		return ""
	}
	metaPath := filepath.Join(paths.KBDir(kbID), ".sageox", "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return ""
	}
	var meta kbMetaSummary
	if err := json.Unmarshal(data, &meta); err != nil {
		return ""
	}
	return meta.Type
}

// extractLayerValue returns the value for a known key at a single layer.
// Empty string means "not set at this layer".
func extractLayerValue(key, scope string, ctx *kbConfigContext) string {
	switch key {
	case kb.KeySessionRecordingMode:
		switch scope {
		case kb.LayerScopeEnv:
			return os.Getenv(config.EnvSessionRecording)
		case kb.LayerScopeKB:
			if ctx.kbConfig == nil {
				return ""
			}
			m := ctx.kbConfig.Features.SessionRecording.Mode
			if m == nil {
				return ""
			}
			return *m
		case kb.LayerScopeUser:
			if ctx.userCfg == nil || ctx.userCfg.Sessions == nil {
				return ""
			}
			mode := ctx.userCfg.Sessions.GetMode()
			if mode == "none" {
				// "none" is the SessionsConfig zero-value, not an opt-out.
				return ""
			}
			return mode
		case kb.LayerScopeDefault:
			return kbconfig.DefaultSessionRecordingMode(ctx.kbType)
		}
	case kb.KeyMurmursEnabled:
		switch scope {
		case kb.LayerScopeKB:
			if ctx.kbConfig == nil || ctx.kbConfig.Features.Murmurs.Enabled == nil {
				return ""
			}
			if *ctx.kbConfig.Features.Murmurs.Enabled {
				return "true"
			}
			return "false"
		}
		return ""
	case kb.KeyVisibility:
		switch scope {
		case kb.LayerScopeKB:
			if ctx.kbConfig == nil {
				return ""
			}
			return ctx.kbConfig.Features.Visibility
		case kb.LayerScopeDefault:
			return kbconfig.VisibilityPrivate
		}
		return ""
	}
	return ""
}

// labelSource returns the human-readable source string for a layer.
func (ctx *kbConfigContext) labelSource(key, scope string) string {
	switch scope {
	case kb.LayerScopeEnv:
		if key == kb.KeySessionRecordingMode {
			return config.EnvSessionRecording
		}
		return ""
	case kb.LayerScopeKB:
		return ctx.kbCfgPath
	case kb.LayerScopeUser:
		return ctx.userCfgSrc
	case kb.LayerScopeDefault:
		return "built-in default"
	}
	return ""
}

func resolveOneKey(key string, ctx *kbConfigContext) kb.EffectiveValue {
	envVal := extractLayerValue(key, kb.LayerScopeEnv, ctx)
	kbVal := extractLayerValue(key, kb.LayerScopeKB, ctx)
	userVal := extractLayerValue(key, kb.LayerScopeUser, ctx)
	defaultVal := extractLayerValue(key, kb.LayerScopeDefault, ctx)

	ev := kb.ResolveEffective(key, envVal, kbVal, userVal, defaultVal)
	// Stamp human source labels.
	for i := range ev.Layers {
		ev.Layers[i].Source = ctx.labelSource(key, ev.Layers[i].Scope)
	}
	return ev
}

func runKBConfigGet(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	key := args[0]
	if !kb.IsKnownKey(key) {
		fmt.Fprintf(stderr, "error: unknown key %q. Known keys: %s\n", key, strings.Join(kb.KnownKeys, ", "))
		return cli.ErrSilent
	}

	ctx, err := loadKBConfigContext(cmd)
	if err != nil {
		return err
	}

	ev := resolveOneKey(key, ctx)

	at, _ := cmd.Flags().GetString("at")
	showOrigin, _ := cmd.Flags().GetBool("show-origin")
	jsonMode, _ := cmd.Flags().GetBool("json")

	if at != "" {
		var atVal string
		var matched bool
		for _, l := range ev.Layers {
			if l.Scope == at {
				atVal = l.Value
				matched = true
				break
			}
		}
		if !matched {
			fmt.Fprintf(stderr, "error: unknown scope %q. Valid: env, kb, user, default\n", at)
			return cli.ErrSilent
		}
		if jsonMode {
			return writeJSON(stdout, map[string]any{
				"key":   key,
				"scope": at,
				"value": atVal,
				"kb_id": ctx.kbID,
			})
		}
		fmt.Fprintln(stdout, atVal)
		return nil
	}

	if jsonMode {
		return writeJSON(stdout, jsonEffective(ctx.kbID, ev))
	}

	if showOrigin {
		writeShowOrigin(stdout, ev)
		return nil
	}

	fmt.Fprintln(stdout, ev.Effective)
	return nil
}

func runKBConfigList(cmd *cobra.Command, args []string) error {
	stdout := cmd.OutOrStdout()

	ctx, err := loadKBConfigContext(cmd)
	if err != nil {
		return err
	}

	keys := append([]string(nil), kb.KnownKeys...)
	sort.Strings(keys)

	showOrigin, _ := cmd.Flags().GetBool("show-origin")
	jsonMode, _ := cmd.Flags().GetBool("json")

	results := make([]kb.EffectiveValue, 0, len(keys))
	for _, k := range keys {
		results = append(results, resolveOneKey(k, ctx))
	}

	if jsonMode {
		out := map[string]any{
			"kb_id": ctx.kbID,
			"keys":  make([]any, 0, len(results)),
		}
		arr := out["keys"].([]any)
		for _, ev := range results {
			arr = append(arr, jsonEffective("", ev))
		}
		out["keys"] = arr
		return writeJSON(stdout, out)
	}

	for i, ev := range results {
		if showOrigin {
			if i > 0 {
				fmt.Fprintln(stdout)
			}
			writeShowOrigin(stdout, ev)
			continue
		}
		fmt.Fprintf(stdout, "%s=%s\n", ev.Key, ev.Effective)
	}
	return nil
}

func writeShowOrigin(w io.Writer, ev kb.EffectiveValue) {
	fmt.Fprintf(w, "%s=%s\n", ev.Key, ev.Effective)
	// Determine winning scope (first layer whose value equals Effective and is non-empty).
	winnerIdx := -1
	for i, l := range ev.Layers {
		if l.Value != "" && l.Value == ev.Effective {
			winnerIdx = i
			break
		}
	}
	// If safety inversion fired and no non-empty layer matches (e.g. effective
	// came purely from kbconfig.ResolveEffectiveMode logic), fall back to the
	// trigger layer.
	if winnerIdx == -1 {
		for i, l := range ev.Layers {
			if l.Trigger {
				winnerIdx = i
				break
			}
		}
	}
	for i, l := range ev.Layers {
		annot := ""
		switch {
		case l.Trigger:
			annot = " [trigger]"
		case i == winnerIdx:
			annot = " [winner]"
		}
		val := l.Value
		if val == "" {
			val = "(unset)"
		}
		src := l.Source
		if src == "" {
			src = "-"
		}
		fmt.Fprintf(w, "  %-7s %s  (%s)%s\n", l.Scope, val, src, annot)
	}
	if ev.SafetyInversion {
		fmt.Fprintln(w, "  note: safety inversion — \"disabled\" on either kb or user layer vetoes the other.")
	}
}

func jsonEffective(kbID string, ev kb.EffectiveValue) map[string]any {
	layers := make([]map[string]any, 0, len(ev.Layers))
	for _, l := range ev.Layers {
		layer := map[string]any{
			"scope":  l.Scope,
			"source": l.Source,
			"value":  l.Value,
		}
		if l.Trigger {
			layer["trigger"] = true
		}
		layers = append(layers, layer)
	}
	out := map[string]any{
		"key":              ev.Key,
		"effective":        ev.Effective,
		"layers":           layers,
		"safety_inversion": ev.SafetyInversion,
	}
	if kbID != "" {
		out["kb_id"] = kbID
	}
	return out
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
