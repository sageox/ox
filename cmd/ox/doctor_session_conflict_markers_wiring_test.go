//go:build !short

package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findCheckByName returns the first check with the given name across all categories.
func findCheckByName(categories []checkCategory, name string) (checkResult, bool) {
	for _, cat := range categories {
		for _, check := range cat.checks {
			if check.name == name {
				return check, true
			}
		}
	}
	return checkResult{}, false
}

// TestDoctorRun_SessionConflictMarkersIsInvoked drives the real doctor runner (what
// `ox doctor --fix-slug=session-conflict-markers` calls). The check was registered in
// DoctorCheckRegistry but the Sessions phase never ran it, so --fix-slug silently did nothing.
func TestDoctorRun_SessionConflictMarkersIsInvoked(t *testing.T) {
	sandboxDoctorEnv(t)
	ledger := newMarkedLedger(t, false)
	// the doctor's ledger-branch-status auto-fix pushes unpushed commits earlier in the run; a
	// remote that refuses pushes keeps the marker commit local, as on a wedged real ledger
	hook := filepath.Join(filepath.Dir(ledger), "remote.git", "hooks", "pre-receive")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755))

	project := testGitRepo(t)
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	require.NoError(t, os.Chdir(project))
	createFreshSageoxStructure(t, project)
	require.NoError(t, config.SaveLocalConfig(project, &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: ledger},
	}))

	categories, err := runDoctorChecks(context.Background(), doctorOptions{
		fix:      true,
		fixSlugs: []string{CheckSlugSessionConflictMarkers},
		forceYes: true,
	})
	require.NoError(t, err)

	check, found := findCheckByName(categories, conflictMarkersCheckName)
	require.True(t, found, "doctor output has no %q line", conflictMarkersCheckName)
	assert.True(t, check.passed, check.message)

	assert.Equal(t, cleanUpstreamMeta, ledgerFile(t, ledger, "HEAD:"+markedUpstreamMeta))
	subject, _ := runIsolatedGit(t, ledger, "log", "-1", "--format=%s")
	assert.Equal(t, "doctor: resolve committed conflict markers in 2 session files", subject)
}

// TestSessionsPhase_EveryRegisteredCheckIsInvoked fails when a check registered in the
// "Sessions" category is never invoked by checkSessionHealth. That phase calls checks
// explicitly (the registry only supplies metadata and --fix-slug validation), so a registration
// without a call site passes --fix-slug validation yet runs nothing. A check counts as invoked
// when doctor_session.go names its slug constant or calls a function its Run calls.
func TestSessionsPhase_EveryRegisteredCheckIsInvoked(t *testing.T) {
	files, err := filepath.Glob(repoPath("*.go"))
	require.NoError(t, err)

	type registration struct {
		file, slugConst string
		run             []string // callee names the Run function reaches
	}
	var registrations []registration
	phaseCalls := map[string]bool{}  // functions doctor_session.go calls
	phaseIdents := map[string]bool{} // identifiers named in doctor_session.go

	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err)
		isPhase := filepath.Base(file) == "doctor_session.go"
		ast.Inspect(parsed, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				if ident, ok := node.Fun.(*ast.Ident); ok && isPhase {
					phaseCalls[ident.Name] = true
				}
			case *ast.Ident:
				if isPhase {
					phaseIdents[node.Name] = true
				}
			case *ast.CompositeLit:
				reg := registration{file: file}
				inSessions := false
				for _, elt := range node.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, _ := kv.Key.(*ast.Ident)
					if key == nil {
						continue
					}
					switch value := kv.Value.(type) {
					case *ast.Ident:
						switch key.Name {
						case "Slug":
							reg.slugConst = value.Name
						case "Run":
							reg.run = []string{value.Name}
						}
					case *ast.FuncLit:
						if key.Name == "Run" {
							ast.Inspect(value, func(inner ast.Node) bool {
								if call, ok := inner.(*ast.CallExpr); ok {
									if ident, ok := call.Fun.(*ast.Ident); ok {
										reg.run = append(reg.run, ident.Name)
									}
								}
								return true
							})
						}
					case *ast.BasicLit:
						if key.Name == "Category" && value.Value == `"Sessions"` {
							inSessions = true
						}
					}
				}
				if inSessions && reg.slugConst != "" {
					registrations = append(registrations, reg)
				}
			}
			return true
		})
	}
	require.NotEmpty(t, registrations, "found no Sessions checks; the scan is broken")

	for _, reg := range registrations {
		invoked := phaseIdents[reg.slugConst]
		for _, callee := range reg.run {
			invoked = invoked || phaseCalls[callee]
		}
		assert.True(t, invoked, "%s registers %s in Sessions but checkSessionHealth never runs it, so --fix-slug does nothing", reg.file, reg.slugConst)
	}
}
