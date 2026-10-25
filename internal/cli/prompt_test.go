package cli

import (
	"os"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A live, empty pipe must not keep --no-input waiting or authorize defaults.
func TestNoInputNeverWaitsForPromptAnswers(t *testing.T) {
	previousNoInput, previousAssumeYes := noInput, assumeYes
	SetNoInput(true)
	t.Cleanup(func() {
		SetNoInput(previousNoInput)
		SetAssumeYes(previousAssumeYes)
	})

	for _, tt := range []struct {
		name      string
		run       func() (any, error)
		want      any
		wantErr   error
		assumeYes bool
	}{
		{name: "optional confirmation declines a yes default", run: func() (any, error) { return ConfirmYesNo("Continue?", true), nil }, want: false},
		{name: "optional confirmation honors yes", run: func() (any, error) { return ConfirmYesNo("Continue?", false), nil }, want: true, assumeYes: true},
		{name: "required confirmation", run: func() (any, error) { return ConfirmYesNoRequired("Continue?", true, false) }, want: false, wantErr: ErrConfirmationRequired},
		{name: "required confirmation honors yes", run: func() (any, error) { return ConfirmYesNoRequired("Continue?", false, false) }, want: true, assumeYes: true},
		{name: "required confirmation honors force", run: func() (any, error) { return ConfirmYesNoRequired("Continue?", false, true) }, want: true},
		{name: "selection never guesses", run: func() (any, error) { return SelectOne("Choose", []string{"a", "b"}, 1) }, want: -1, wantErr: ErrNoInteractiveInput},
		{name: "required selection", run: func() (any, error) { return SelectOneRequired("Choose", []string{"a", "b"}, 0) }, want: -1, wantErr: ErrNoInteractiveInput},
		{name: "text input never guesses", run: func() (any, error) { return InputWithDefault("Name", "suggestion") }, want: "", wantErr: ErrNoInteractiveInput},
		{name: "typed uninstall rejects yes", run: func() (any, error) { return nil, ConfirmUninstall("repo", false) }, wantErr: ErrNoInteractiveInput, assumeYes: true},
		{name: "typed uninstall honors force", run: func() (any, error) { return nil, ConfirmUninstall("repo", true) }},
		{name: "typed operation rejects yes", run: func() (any, error) { return nil, ConfirmDangerousOperation("delete", "delete", false) }, wantErr: ErrNoInteractiveInput, assumeYes: true},
		{name: "typed operation honors force", run: func() (any, error) { return nil, ConfirmDangerousOperation("delete", "delete", true) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			SetAssumeYes(tt.assumeYes)
			r, w, err := os.Pipe()
			require.NoError(t, err)
			defer r.Close()
			defer w.Close()
			oldStdin := os.Stdin
			os.Stdin = r
			defer func() { os.Stdin = oldStdin }()
			type result struct {
				value any
				err   error
			}
			done := make(chan result, 1)
			go func() {
				value, runErr := tt.run()
				done <- result{value: value, err: runErr}
			}()
			select {
			case got := <-done:
				assert.Equal(t, tt.want, got.value)
				if tt.wantErr != nil {
					assert.ErrorIs(t, got.err, tt.wantErr)
				} else {
					assert.NoError(t, got.err)
				}
			case <-time.After(time.Second):
				// Release the reader before restoring process-wide stdin.
				_ = w.Close()
				<-done
				t.Fatal("--no-input waited for a prompt answer")
			}
		})
	}
}

// A pipe's final value must survive EOF even when it has no trailing newline.
func TestInputWithDefaultPreservesPipedValues(t *testing.T) {
	previousNoInteractive, previousNoInput := noInteractive, noInput
	SetNoInteractive(true)
	SetNoInput(false)
	t.Cleanup(func() {
		SetNoInteractive(previousNoInteractive)
		SetNoInput(previousNoInput)
	})
	for _, tt := range []struct {
		name, input, want string
	}{
		{"value at EOF", "alice@example.com", "alice@example.com"},
		{"value with newline", " alice@example.com \n", "alice@example.com"},
		{"empty EOF", "", "default@example.com"},
		{"blank line", "\n", "default@example.com"},
		{"whitespace at EOF", " \t", "default@example.com"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withStdin(t, tt.input, func() {
				value, err := InputWithDefault("Email", "default@example.com")
				require.NoError(t, err)
				assert.Equal(t, tt.want, value)
			})
		})
	}
	t.Run("read error", func(t *testing.T) {
		withStdin(t, "", func() {
			require.NoError(t, os.Stdin.Close())
			value, err := InputWithDefault("Email", "default@example.com")
			assert.ErrorIs(t, err, os.ErrClosed)
			assert.Empty(t, value)
		})
	})
}

// helper to create a KeyPressMsg for tests
func keyPress(code rune, mod tea.KeyMod, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: mod, Text: text})
}

func TestSelectModel_Init(t *testing.T) {
	t.Parallel()

	m := selectModel{
		title:   "Choose:",
		options: []string{"a", "b", "c"},
	}

	cmd := m.Init()
	assert.Nil(t, cmd, "Init should return nil")
}

func TestSelectModel_Update_Navigation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		initial    int
		msg        tea.Msg
		wantCursor int
		wantDone   bool
	}{
		{"down moves cursor", 0, keyPress(tea.KeyDown, 0, ""), 1, false},
		{"j moves cursor down", 0, keyPress('j', 0, "j"), 1, false},
		{"up moves cursor", 2, keyPress(tea.KeyUp, 0, ""), 1, false},
		{"k moves cursor up", 2, keyPress('k', 0, "k"), 1, false},
		{"down at bottom stays", 2, keyPress(tea.KeyDown, 0, ""), 2, false},
		{"up at top stays", 0, keyPress(tea.KeyUp, 0, ""), 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := selectModel{
				title:   "Choose:",
				options: []string{"a", "b", "c"},
				cursor:  tt.initial,
			}

			result, _ := m.Update(tt.msg)
			rm := result.(selectModel)
			assert.Equal(t, tt.wantCursor, rm.cursor)
			assert.Equal(t, tt.wantDone, rm.done)
		})
	}
}

func TestSelectModel_Update_Selection(t *testing.T) {
	t.Parallel()

	m := selectModel{
		title:   "Choose:",
		options: []string{"a", "b", "c"},
		cursor:  1,
	}

	result, cmd := m.Update(keyPress(tea.KeyEnter, 0, ""))
	rm := result.(selectModel)

	assert.True(t, rm.done, "enter should mark as done")
	assert.Equal(t, 1, rm.selected, "selected should match cursor position")
	assert.False(t, rm.canceled)
	assert.NotNil(t, cmd, "enter should return quit command")
}

func TestSelectModel_Update_Space_Selects(t *testing.T) {
	t.Parallel()
	m := selectModel{
		title:   "Choose:",
		options: []string{"a", "b", "c"},
		cursor:  1,
	}

	result, cmd := m.Update(keyPress(tea.KeySpace, 0, "space"))
	rm := result.(selectModel)

	assert.True(t, rm.done)
	assert.False(t, rm.canceled)
	assert.Equal(t, 1, rm.selected)
	assert.NotNil(t, cmd)
}

func TestSelectModel_Update_Cancel(t *testing.T) {
	t.Parallel()

	cancelMsgs := []struct {
		name string
		msg  tea.Msg
	}{
		{"ctrl+c", keyPress('c', tea.ModCtrl, "")},
		{"q", keyPress('q', 0, "q")},
		{"esc", keyPress(tea.KeyEsc, 0, "")},
	}

	for _, tc := range cancelMsgs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := selectModel{
				title:   "Choose:",
				options: []string{"a", "b"},
				cursor:  0,
			}

			result, cmd := m.Update(tc.msg)
			rm := result.(selectModel)

			assert.True(t, rm.canceled, "%s should cancel", tc.name)
			assert.True(t, rm.done, "%s should mark done", tc.name)
			assert.NotNil(t, cmd, "%s should return quit command", tc.name)
		})
	}
}

func TestSelectModel_View_Done_ReturnsEmptyView(t *testing.T) {
	t.Parallel()

	m := selectModel{
		title:   "Pick:",
		options: []string{"a"},
		done:    true,
	}

	view := m.View()
	// when done, View returns NewView("") - the view's string representation is empty
	expected := tea.NewView("")
	assert.Equal(t, expected, view, "done view should be empty NewView")
}

func TestSelectOne_EmptyOptions(t *testing.T) {
	t.Parallel()

	idx, err := SelectOne("Title", []string{}, 0)
	assert.Error(t, err, "empty options should return an error")
	assert.Equal(t, -1, idx)
}

func TestSelectOneValue_EmptyOptions(t *testing.T) {
	t.Parallel()

	val, err := SelectOneValue("Title", []SelectOption[string]{}, 0)
	assert.NoError(t, err)
	assert.Empty(t, val, "empty options should return zero value")
}

func TestInputModel_Init(t *testing.T) {
	t.Parallel()

	m := inputModel{title: "Enter name:"}
	cmd := m.Init()
	assert.Nil(t, cmd)
}

func TestInputModel_Update_Typing(t *testing.T) {
	t.Parallel()

	m := inputModel{title: "Name:"}

	// type 'h' - single printable character
	result, _ := m.Update(keyPress('h', 0, "h"))
	rm := result.(inputModel)
	assert.Equal(t, "h", rm.value)

	// type 'i'
	result, _ = rm.Update(keyPress('i', 0, "i"))
	rm = result.(inputModel)
	assert.Equal(t, "hi", rm.value)
}

func TestInputModel_Update_Backspace(t *testing.T) {
	t.Parallel()

	m := inputModel{title: "Name:", value: "hello"}

	result, _ := m.Update(keyPress(tea.KeyBackspace, 0, ""))
	rm := result.(inputModel)
	assert.Equal(t, "hell", rm.value)
}

func TestInputModel_Update_BackspaceOnEmpty(t *testing.T) {
	t.Parallel()

	m := inputModel{title: "Name:", value: ""}

	result, _ := m.Update(keyPress(tea.KeyBackspace, 0, ""))
	rm := result.(inputModel)
	assert.Empty(t, rm.value, "backspace on empty should stay empty")
}

func TestInputModel_Update_Enter(t *testing.T) {
	t.Parallel()

	m := inputModel{title: "Name:", value: "test"}

	result, cmd := m.Update(keyPress(tea.KeyEnter, 0, ""))
	rm := result.(inputModel)

	assert.True(t, rm.done)
	assert.False(t, rm.canceled)
	assert.Equal(t, "test", rm.value)
	assert.NotNil(t, cmd)
}

func TestInputModel_Update_Cancel(t *testing.T) {
	t.Parallel()

	cancelMsgs := []struct {
		name string
		msg  tea.Msg
	}{
		{"ctrl+c", keyPress('c', tea.ModCtrl, "")},
		{"esc", keyPress(tea.KeyEsc, 0, "")},
	}

	for _, tc := range cancelMsgs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := inputModel{title: "Name:", value: "partial"}

			result, cmd := m.Update(tc.msg)
			rm := result.(inputModel)

			assert.True(t, rm.canceled)
			assert.True(t, rm.done)
			assert.NotNil(t, cmd)
		})
	}
}

func TestInputModel_View_Done_ReturnsEmptyView(t *testing.T) {
	t.Parallel()

	m := inputModel{
		title: "Name:",
		done:  true,
	}

	view := m.View()
	expected := tea.NewView("")
	assert.Equal(t, expected, view, "done view should be empty NewView")
}

// TestSelectOneSimpleCore_StdinEOFHandling pins the three ways
// bufio.Reader.ReadString('\n') can end when fed from piped/redirected
// stdin: a full line, a final line with no trailing newline (io.EOF carries
// the data), and truly nothing at all (io.EOF with zero bytes). Only the
// last case may report explicit=false — the CLI equivalent of "no one was
// there to answer."
//
// Regression: `printf 2 | ox init` (no trailing newline) used to be treated
// identically to closed/empty stdin, silently discarding the piped "2" and
// falling back to defaultIdx — reintroducing exactly the "repo silently
// bound to the wrong team" failure this PR exists to close. `echo 2 | ox
// init` (which appends a newline) happened to work, masking the bug.
func TestSelectOneSimpleCore_StdinEOFHandling(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantIdx      int
		wantExplicit bool
	}{
		{
			name:         "no trailing newline (printf-style pipe)",
			input:        "2",
			wantIdx:      1,
			wantExplicit: true,
		},
		{
			name:         "trailing newline (echo-style pipe)",
			input:        "2\n",
			wantIdx:      1,
			wantExplicit: true,
		},
		{
			name:         "genuinely closed/empty stdin",
			input:        "",
			wantIdx:      0, // defaultIdx passed in below
			wantExplicit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withPipedStdin(t, tt.input)

			idx, explicit, err := selectOneSimpleCore("Pick:", []string{"a", "b", "c"}, 0)
			require.NoError(t, err)
			assert.Equal(t, tt.wantExplicit, explicit, "explicit")
			assert.Equal(t, tt.wantIdx, idx, "idx")
		})
	}
}

// TestSelectOneRequired_HonorsPipedSelectionWithoutTrailingNewline is the
// live check for the printf/EOF bug at the public-API level (in lieu of
// driving a full `ox init`, which needs a real team-selection flow):
// SelectOneRequired must return the piped choice, not ErrNoInteractiveInput.
// (TestSelectOneRequired_NoOneHome in select_required_test.go already pins
// the genuinely-empty-stdin side of this contract; this covers the other
// side the bug broke.)
func TestSelectOneRequired_HonorsPipedSelectionWithoutTrailingNewline(t *testing.T) {
	origInteractive := noInteractive
	SetNoInteractive(true)
	t.Cleanup(func() { noInteractive = origInteractive })

	withPipedStdin(t, "2")

	idx, err := SelectOneRequired("Team:", []string{"alpha", "beta", "gamma"}, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, idx, "piped '2' with no trailing newline must select index 1, not fall back to default")
}

// TestSelectOne_HonorsPipedSelectionWithoutTrailingNewline is the same live
// check against SelectOne (the silently-defaulting sibling of
// SelectOneRequired) to confirm the fix isn't limited to one API.
func TestSelectOne_HonorsPipedSelectionWithoutTrailingNewline(t *testing.T) {
	origInteractive := noInteractive
	SetNoInteractive(true)
	t.Cleanup(func() { noInteractive = origInteractive })

	withPipedStdin(t, "2")

	idx, err := SelectOne("Team:", []string{"alpha", "beta", "gamma"}, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, idx, "piped '2' with no trailing newline must select index 1, not fall back to default")
}
