package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShouldSkipFile(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     bool
	}{
		// exact matches against skipped prefixes
		{"agent exact", "ox_agent.md", true},
		{"daemon exact", "ox_daemon.md", true},
		{"config exact", "ox_config.md", true},
		{"version exact", "ox_version.md", true},
		{"coworker exact", "ox_coworker.md", true},
		{"integrate exact", "ox_integrate.md", true},
		{"view exact", "ox_view.md", true},

		// subcommands of skipped prefixes
		{"agent subcommand", "ox_agent_prime.md", true},
		{"agent deeply nested", "ox_agent_session_start.md", true},
		{"daemon subcommand", "ox_daemon_start.md", true},
		{"session commit", "ox_session_commit.md", true},
		{"session download", "ox_session_download.md", true},
		{"session export", "ox_session_export.md", true},
		{"session push-summary", "ox_session_push-summary.md", true},
		{"session redaction", "ox_session_redaction.md", true},
		{"session remove", "ox_session_remove.md", true},
		{"session upload", "ox_session_upload.md", true},

		// allowed commands
		{"root ox", "ox.md", false},
		{"session parent", "ox_session.md", false},
		{"session start", "ox_session_start.md", false},
		{"session stop", "ox_session_stop.md", false},
		{"login", "ox_login.md", false},
		{"init", "ox_init.md", false},
		{"doctor", "ox_doctor.md", false},
		{"status", "ox_status.md", false},
		{"teams", "ox_teams.md", false},

		// edge: no .md extension still works (strips .md first)
		{"no extension", "ox_agent", true},
		{"no extension allowed", "ox_login", false},

		// edge: prefix overlap that shouldn't match
		// "ox_configure" does NOT start with "ox_config_"
		{"configure not config subcommand", "ox_configure.md", false},
		{"view-something not view subcommand", "ox_view-something.md", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldSkipFile(tt.filename)
			if got != tt.want {
				t.Errorf("shouldSkipFile(%q) = %v, want %v", tt.filename, got, tt.want)
			}
		})
	}
}

func TestIsSkippedSeeAlsoLink(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		// skipped links
		{
			"agent link",
			"* [ox agent](ox_agent.md) - Agent commands",
			true,
		},
		{
			"agent subcommand link",
			"* [ox agent prime](ox_agent_prime.md) - Prime the agent",
			true,
		},
		{
			"daemon link",
			"* [ox daemon](ox_daemon.md) - Daemon management",
			true,
		},
		{
			"version link",
			"* [ox version](ox_version.md) - Show version",
			true,
		},
		{
			"session commit link",
			"* [ox session commit](ox_session_commit.md) - Commit session",
			true,
		},

		// allowed links
		{
			"login link",
			"* [ox login](ox_login.md) - Log in to SageOx",
			false,
		},
		{
			"root link",
			"* [ox](ox.md) - CLI root",
			false,
		},
		{
			"session link",
			"* [ox session](ox_session.md) - Session commands",
			false,
		},
		{
			"session start link",
			"* [ox session start](ox_session_start.md) - Start a session",
			false,
		},

		// non-bullet lines
		{
			"plain text",
			"This is a paragraph mentioning ox_agent.md",
			false,
		},
		{
			"heading",
			"## SEE ALSO",
			false,
		},
		{
			"empty line",
			"",
			false,
		},

		// malformed links
		{
			"missing closing paren",
			"* [ox agent](ox_agent.md",
			false,
		},
		{
			"no link target",
			"* [ox agent]",
			false,
		},
		{
			"bullet without link",
			"* plain text bullet",
			false,
		},

		// indented bullet (still valid markdown)
		{
			"indented skipped link",
			"  * [ox agent](ox_agent.md) - Agent commands",
			true,
		},
		{
			"indented allowed link",
			"  * [ox login](ox_login.md) - Log in",
			false,
		},

		// transformed path format (no .md, slash-separated)
		{
			"transformed agent path",
			"* [ox agent](/agent) - Agent commands",
			true,
		},
		{
			"transformed agent subcommand",
			"* [ox agent prime](/agent/prime) - Prime",
			true,
		},
		{
			"transformed login path",
			"* [ox login](/login) - Log in",
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSkippedSeeAlsoLink(tt.line)
			if got != tt.want {
				t.Errorf("isSkippedSeeAlsoLink(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestComputeDestPath(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		allFiles []string
		want     string
		wantErr  bool
	}{
		{
			"root command becomes index",
			"ox.md",
			[]string{"ox.md", "ox_login.md"},
			"index.mdx",
			false,
		},
		{
			"leaf child of ox",
			"ox_login.md",
			[]string{"ox.md", "ox_login.md"},
			"login.mdx",
			false,
		},
		{
			"parent command with children becomes index",
			"ox_session.md",
			[]string{"ox.md", "ox_session.md", "ox_session_start.md", "ox_session_stop.md"},
			filepath.Join("session", "index.mdx"),
			false,
		},
		{
			"child of parent command",
			"ox_session_start.md",
			[]string{"ox.md", "ox_session.md", "ox_session_start.md"},
			filepath.Join("session", "start.mdx"),
			false,
		},
		{
			"deeply nested leaf",
			"ox_infra_tofu_upload.md",
			[]string{"ox.md", "ox_infra.md", "ox_infra_tofu.md", "ox_infra_tofu_upload.md"},
			filepath.Join("infra", "tofu", "upload.mdx"),
			false,
		},
		{
			"deeply nested parent",
			"ox_infra_tofu.md",
			[]string{"ox.md", "ox_infra_tofu.md", "ox_infra_tofu_upload.md"},
			filepath.Join("infra", "tofu", "index.mdx"),
			false,
		},
		{
			"leaf with no siblings",
			"ox_doctor.md",
			[]string{"ox.md", "ox_doctor.md"},
			"doctor.mdx",
			false,
		},
		{
			"not ox prefix errors",
			"notox_something.md",
			[]string{"notox_something.md"},
			"",
			true,
		},
		{
			"empty filename errors",
			".md",
			[]string{".md"},
			"",
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := computeDestPath(tt.filename, tt.allFiles)
			if (err != nil) != tt.wantErr {
				t.Errorf("computeDestPath(%q) error = %v, wantErr %v", tt.filename, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("computeDestPath(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestTransformLink(t *testing.T) {
	tests := []struct {
		name        string
		link        string
		currentFile string
		want        string
	}{
		{
			"simple subcommand link",
			"[ox login](ox_login.md)",
			"/docs/index.mdx",
			"[ox login](/login)",
		},
		{
			"nested subcommand link",
			"[ox agent prime](ox_agent_prime.md)",
			"/docs/index.mdx",
			"[ox agent prime](/agent/prime)",
		},
		{
			"root link",
			"[ox](ox.md)",
			"/docs/session/start.mdx",
			"[ox](/)",
		},
		{
			"deeply nested link",
			"[ox infra tofu upload](ox_infra_tofu_upload.md)",
			"/docs/index.mdx",
			"[ox infra tofu upload](/infra/tofu/upload)",
		},
		{
			"non-ox link unchanged",
			"[guide](guide.md)",
			"/docs/index.mdx",
			"[guide](guide.md)",
		},
		{
			"no match returns original",
			"plain text",
			"/docs/index.mdx",
			"plain text",
		},
		{
			"link text with parens",
			"[ox session (beta)](ox_session.md)",
			"/docs/index.mdx",
			// regex [^\]]+ matches parens in link text, so it transforms
			"[ox session (beta)](/session)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := transformLink(tt.link, tt.currentFile)
			if got != tt.want {
				t.Errorf("transformLink(%q, %q) = %q, want %q", tt.link, tt.currentFile, got, tt.want)
			}
		})
	}
}

func TestTransformFile(t *testing.T) {
	t.Run("directory path rejected", func(t *testing.T) {
		srcDir := t.TempDir()
		destPath := t.TempDir() + "/out.md"

		err := transformFile(srcDir, destPath, 1)
		if err == nil {
			t.Fatal("expected error for directory path, got nil")
		}
		if !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("expected 'not a regular file' error, got: %v", err)
		}
	})

	t.Run("frontmatter gets sidebar_position", func(t *testing.T) {
		srcDir := t.TempDir()
		srcPath := filepath.Join(srcDir, "ox_login.md")
		content := "---\ntitle: \"ox login\"\ndescription: \"CLI reference for ox login\"\n---\n\n## ox login\n\nLog in to SageOx.\n"
		os.WriteFile(srcPath, []byte(content), 0644)

		destPath := filepath.Join(t.TempDir(), "login.mdx")
		err := transformFile(srcPath, destPath, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		result, _ := os.ReadFile(destPath)
		output := string(result)

		if !strings.Contains(output, "sidebar_position: 3") {
			t.Errorf("expected sidebar_position: 3 in output, got:\n%s", output)
		}
		// sidebar_position should be before the closing ---
		idx := strings.Index(output, "sidebar_position: 3")
		closingIdx := strings.LastIndex(output, "---")
		if idx > closingIdx {
			t.Error("sidebar_position should appear before closing frontmatter delimiter")
		}
	})

	t.Run("links are transformed in body", func(t *testing.T) {
		srcDir := t.TempDir()
		srcPath := filepath.Join(srcDir, "ox.md")
		content := "---\ntitle: \"ox\"\n---\n\n## SEE ALSO\n\n* [ox login](ox_login.md) - Log in\n* [ox doctor](ox_doctor.md) - Diagnose\n"
		os.WriteFile(srcPath, []byte(content), 0644)

		destPath := filepath.Join(t.TempDir(), "index.mdx")
		err := transformFile(srcPath, destPath, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		result, _ := os.ReadFile(destPath)
		output := string(result)

		if !strings.Contains(output, "[ox login](/login)") {
			t.Errorf("expected transformed login link, got:\n%s", output)
		}
		if !strings.Contains(output, "[ox doctor](/doctor)") {
			t.Errorf("expected transformed doctor link, got:\n%s", output)
		}
	})

	t.Run("skipped see-also links are removed", func(t *testing.T) {
		srcDir := t.TempDir()
		srcPath := filepath.Join(srcDir, "ox.md")
		content := "---\ntitle: \"ox\"\n---\n\n## SEE ALSO\n\n* [ox login](ox_login.md) - Log in\n* [ox agent](ox_agent.md) - Agent commands\n* [ox daemon](ox_daemon.md) - Daemon\n"
		os.WriteFile(srcPath, []byte(content), 0644)

		destPath := filepath.Join(t.TempDir(), "index.mdx")
		err := transformFile(srcPath, destPath, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		result, _ := os.ReadFile(destPath)
		output := string(result)

		if !strings.Contains(output, "[ox login](/login)") {
			t.Errorf("expected login link to be preserved, got:\n%s", output)
		}
		if strings.Contains(output, "agent") {
			t.Errorf("expected agent link to be removed, got:\n%s", output)
		}
		if strings.Contains(output, "daemon") {
			t.Errorf("expected daemon link to be removed, got:\n%s", output)
		}
	})

	t.Run("no frontmatter passthrough", func(t *testing.T) {
		srcDir := t.TempDir()
		srcPath := filepath.Join(srcDir, "plain.md")
		content := "# Plain Markdown\n\nNo frontmatter here.\n"
		os.WriteFile(srcPath, []byte(content), 0644)

		destPath := filepath.Join(t.TempDir(), "plain.mdx")
		err := transformFile(srcPath, destPath, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		result, _ := os.ReadFile(destPath)
		output := string(result)

		// should pass through without adding sidebar_position
		if strings.Contains(output, "sidebar_position") {
			t.Errorf("should not inject sidebar_position without frontmatter, got:\n%s", output)
		}
		if !strings.Contains(output, "# Plain Markdown") {
			t.Errorf("content should be preserved, got:\n%s", output)
		}
	})

	t.Run("nonexistent source errors", func(t *testing.T) {
		err := transformFile("/nonexistent/file.md", t.TempDir()+"/out.mdx", 1)
		if err == nil {
			t.Fatal("expected error for nonexistent source file")
		}
	})

	t.Run("unwritable dest errors", func(t *testing.T) {
		srcDir := t.TempDir()
		srcPath := filepath.Join(srcDir, "test.md")
		os.WriteFile(srcPath, []byte("---\ntitle: test\n---\ncontent\n"), 0644)

		err := transformFile(srcPath, "/nonexistent/dir/out.mdx", 1)
		if err == nil {
			t.Fatal("expected error for unwritable destination")
		}
	})
}

func TestFilePrepender(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantIn   []string // substrings that must appear
	}{
		{
			"root command",
			"/docs/ox.md",
			[]string{`title: "ox"`, `description: "CLI reference for ox"`},
		},
		{
			"subcommand with underscores",
			"/docs/ox_agent_prime.md",
			[]string{`title: "ox agent prime"`, `description: "CLI reference for ox agent prime"`},
		},
		{
			"single subcommand",
			"/docs/ox_login.md",
			[]string{`title: "ox login"`, `description: "CLI reference for ox login"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filePrepender(tt.filename)

			// must have frontmatter delimiters
			if !strings.HasPrefix(got, "---\n") {
				t.Errorf("expected frontmatter to start with ---, got:\n%s", got)
			}
			if !strings.Contains(got, "\n---\n") {
				t.Errorf("expected closing frontmatter delimiter, got:\n%s", got)
			}

			for _, want := range tt.wantIn {
				if !strings.Contains(got, want) {
					t.Errorf("expected %q in output, got:\n%s", want, got)
				}
			}
		})
	}
}

func TestLinkHandler(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already has .md", "ox_login.md", "ox_login.md"},
		{"spaces become underscores", "ox agent prime.md", "ox_agent_prime.md"},
		{"no extension", "ox_login", "ox_login.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := linkHandler(tt.in)
			if got != tt.want {
				t.Errorf("linkHandler(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPostProcess(t *testing.T) {
	t.Run("end to end with file hierarchy", func(t *testing.T) {
		srcDir := t.TempDir()
		destDir := t.TempDir()

		// create source files simulating cobra output
		files := map[string]string{
			"ox.md":               "---\ntitle: \"ox\"\ndescription: \"CLI reference for ox\"\n---\n\n## ox\n\nRoot command.\n\n## SEE ALSO\n\n* [ox login](ox_login.md) - Log in\n* [ox session](ox_session.md) - Sessions\n* [ox agent](ox_agent.md) - Agent commands\n",
			"ox_login.md":         "---\ntitle: \"ox login\"\ndescription: \"CLI reference for ox login\"\n---\n\n## ox login\n\nLog in to SageOx.\n",
			"ox_session.md":       "---\ntitle: \"ox session\"\ndescription: \"CLI reference for ox session\"\n---\n\n## ox session\n\nManage sessions.\n",
			"ox_session_start.md": "---\ntitle: \"ox session start\"\ndescription: \"CLI reference for ox session start\"\n---\n\n## ox session start\n\nStart a session.\n\n## SEE ALSO\n\n* [ox session](ox_session.md) - Parent\n",
			"ox_session_stop.md":  "---\ntitle: \"ox session stop\"\ndescription: \"CLI reference for ox session stop\"\n---\n\n## ox session stop\n\nStop a session.\n",

			// skipped files that should be excluded
			"ox_agent.md":       "---\ntitle: \"ox agent\"\n---\n\nAgent.\n",
			"ox_agent_prime.md": "---\ntitle: \"ox agent prime\"\n---\n\nPrime.\n",
			"ox_daemon.md":      "---\ntitle: \"ox daemon\"\n---\n\nDaemon.\n",
			"ox_version.md":     "---\ntitle: \"ox version\"\n---\n\nVersion.\n",
		}

		for name, content := range files {
			os.WriteFile(filepath.Join(srcDir, name), []byte(content), 0644)
		}

		err := PostProcess(srcDir, destDir)
		if err != nil {
			t.Fatalf("PostProcess failed: %v", err)
		}

		// verify expected output files exist
		expectedFiles := []string{
			"index.mdx",                           // ox.md
			"login.mdx",                           // ox_login.md
			filepath.Join("session", "index.mdx"), // ox_session.md (parent)
			filepath.Join("session", "start.mdx"), // ox_session_start.md
			filepath.Join("session", "stop.mdx"),  // ox_session_stop.md
		}

		for _, f := range expectedFiles {
			path := filepath.Join(destDir, f)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Errorf("expected file %s to exist", f)
			}
		}

		// verify skipped files are NOT present
		skippedOutputs := []string{
			"agent.mdx",
			filepath.Join("agent", "index.mdx"),
			filepath.Join("agent", "prime.mdx"),
			"daemon.mdx",
			"version.mdx",
		}

		for _, f := range skippedOutputs {
			path := filepath.Join(destDir, f)
			if _, err := os.Stat(path); err == nil {
				t.Errorf("skipped file %s should not exist", f)
			}
		}

		// verify agent see-also link was stripped from root index
		rootContent, _ := os.ReadFile(filepath.Join(destDir, "index.mdx"))
		rootStr := string(rootContent)
		if strings.Contains(rootStr, "agent") {
			t.Errorf("root index should not contain agent link, got:\n%s", rootStr)
		}
		if !strings.Contains(rootStr, "[ox login](/login)") {
			t.Errorf("root index should contain transformed login link, got:\n%s", rootStr)
		}

		// verify sidebar_position is present in all files
		for _, f := range expectedFiles {
			content, _ := os.ReadFile(filepath.Join(destDir, f))
			if !strings.Contains(string(content), "sidebar_position:") {
				t.Errorf("file %s should contain sidebar_position", f)
			}
		}

		// verify cross-reference link is transformed in session/start.mdx
		startContent, _ := os.ReadFile(filepath.Join(destDir, "session", "start.mdx"))
		if !strings.Contains(string(startContent), "[ox session](/session)") {
			t.Errorf("session start should have transformed parent link, got:\n%s", string(startContent))
		}
	})

	t.Run("nonexistent source directory errors", func(t *testing.T) {
		err := PostProcess("/nonexistent/src", t.TempDir())
		if err == nil {
			t.Fatal("expected error for nonexistent source directory")
		}
	})

	t.Run("empty source directory produces no output", func(t *testing.T) {
		srcDir := t.TempDir()
		destDir := t.TempDir()

		err := PostProcess(srcDir, destDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		entries, _ := os.ReadDir(destDir)
		if len(entries) != 0 {
			t.Errorf("expected empty dest dir, got %d entries", len(entries))
		}
	})
}
