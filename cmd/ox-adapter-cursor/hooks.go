package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

const (
	cursorHooksVersion = 1
	cursorHookTimeout  = 10 // Cursor version-1 hook timeout is measured in seconds.
)

var cursorHookEvents = []string{
	"sessionStart",
	"beforeSubmitPrompt",
	"postToolUse",
	"postToolUseFailure",
	"afterAgentResponse",
	"stop",
	"sessionEnd",
	"preCompact",
}

type cursorHooksDocument struct {
	root  map[string]json.RawMessage
	hooks map[string]json.RawMessage
}

type cursorHookEntry struct {
	raw     json.RawMessage
	object  map[string]json.RawMessage
	command string
	timeout *float64
}

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	if err := validateCursorHookScope(p); err != nil {
		return nil, err
	}
	executable, err := currentCursorExecutable()
	if err != nil {
		return nil, err
	}
	path, changed, err := installCursorHooks(p.RepoRoot, executable)
	if err != nil {
		return nil, err
	}
	response := &adapterprotocol.InstallHooksResponse{
		Installed: true,
		Hooks:     append([]string(nil), cursorHookEvents...),
	}
	if changed {
		response.FilesWritten = []string{path}
	}
	return response, nil
}

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	if err := validateCursorHookScope(p); err != nil {
		return nil, err
	}
	executable, err := currentCursorExecutable()
	if err != nil {
		return nil, err
	}
	path, installed, err := checkCursorHooks(p.RepoRoot, executable)
	if err != nil {
		return nil, err
	}
	return &adapterprotocol.CheckHooksResponse{
		Installed: installed,
		Scope:     p.Scope,
		HookFiles: []string{path},
	}, nil
}

func handleUninstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error) {
	if err := validateCursorHookScope(p); err != nil {
		return nil, err
	}
	executable, err := currentCursorExecutable()
	if err != nil {
		return nil, err
	}
	path, changed, err := uninstallCursorHooks(p.RepoRoot, executable)
	if err != nil {
		return nil, err
	}
	response := &adapterprotocol.UninstallHooksResponse{Uninstalled: true}
	if changed {
		response.FilesModified = []string{path}
	}
	return response, nil
}

func validateCursorHookScope(p adapterprotocol.HookParams) error {
	if p.Scope != "project" {
		return fmt.Errorf("cursor hooks: unsupported scope %q (only project scope is supported)", p.Scope)
	}
	if p.RepoRoot == "" {
		return errors.New("cursor hooks: repo_root is required")
	}
	return nil
}

func currentCursorExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cursor hooks: resolve adapter executable: %w", err)
	}
	if !filepath.IsAbs(executable) {
		executable, err = filepath.Abs(executable)
		if err != nil {
			return "", fmt.Errorf("cursor hooks: make adapter executable absolute: %w", err)
		}
	}
	if err := validateCursorExecutable(executable); err != nil {
		return "", err
	}
	return filepath.Clean(executable), nil
}

func validateCursorExecutable(executable string) error {
	if !filepath.IsAbs(executable) {
		return fmt.Errorf("cursor hooks: adapter executable must be absolute: %q", executable)
	}
	info, err := os.Stat(executable)
	if err != nil {
		return fmt.Errorf("cursor hooks: adapter executable unavailable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("cursor hooks: adapter executable is not a regular file: %q", executable)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("cursor hooks: adapter executable is not executable: %q", executable)
	}
	return nil
}

func installCursorHooks(repoRoot, executable string) (string, bool, error) {
	if err := validateCursorExecutable(executable); err != nil {
		return "", false, err
	}
	path, err := cursorHooksPath(repoRoot)
	if err != nil {
		return "", false, err
	}
	changed := false
	err = fileutil.WithFileLock(context.Background(), path, func() error {
		if err := ensureCursorHooksParent(path, true); err != nil {
			return err
		}
		document, exists, err := readCursorHooksDocument(path)
		if err != nil {
			return err
		}
		if !exists {
			document = newCursorHooksDocument()
		}

		for _, event := range cursorHookEvents {
			entries, err := document.entries(event)
			if err != nil {
				return err
			}
			updated, eventChanged, err := upsertCursorHook(entries, executable, event)
			if err != nil {
				return err
			}
			if eventChanged {
				document.setEntries(event, updated)
				changed = true
			}
		}
		if !changed {
			return nil
		}
		return writeCursorHooksDocument(path, document)
	})
	if err != nil {
		return path, false, err
	}
	return path, changed, nil
}

func checkCursorHooks(repoRoot, executable string) (string, bool, error) {
	if err := validateCursorExecutable(executable); err != nil {
		return "", false, err
	}
	path, err := cursorHooksPath(repoRoot)
	if err != nil {
		return "", false, err
	}
	if err := ensureCursorHooksParent(path, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, false, nil
		}
		return path, false, err
	}
	document, exists, err := readCursorHooksDocument(path)
	if err != nil {
		return path, false, err
	}
	if !exists {
		return path, false, nil
	}
	for _, event := range cursorHookEvents {
		entries, err := document.entries(event)
		if err != nil {
			return path, false, err
		}
		owned := 0
		correct := false
		wantCommand := cursorHookCommand(executable, event)
		for _, entry := range entries {
			if !isOwnedCursorHook(entry.command, event, wantCommand) {
				continue
			}
			owned++
			correct = entry.command == wantCommand && entry.timeout != nil && *entry.timeout == cursorHookTimeout
		}
		if owned != 1 || !correct {
			return path, false, nil
		}
	}
	return path, true, nil
}

func uninstallCursorHooks(repoRoot, executable string) (string, bool, error) {
	if err := validateCursorExecutable(executable); err != nil {
		return "", false, err
	}
	path, err := cursorHooksPath(repoRoot)
	if err != nil {
		return "", false, err
	}
	changed := false
	err = fileutil.WithFileLock(context.Background(), path, func() error {
		if err := ensureCursorHooksParent(path, false); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		document, exists, err := readCursorHooksDocument(path)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}

		for _, event := range cursorHookEvents {
			entries, err := document.entries(event)
			if err != nil {
				return err
			}
			wantCommand := cursorHookCommand(executable, event)
			kept := make([]cursorHookEntry, 0, len(entries))
			for _, entry := range entries {
				if isOwnedCursorHook(entry.command, event, wantCommand) {
					changed = true
					continue
				}
				kept = append(kept, entry)
			}
			if len(kept) == len(entries) {
				continue
			}
			if len(kept) == 0 {
				delete(document.hooks, event)
			} else {
				document.setEntries(event, kept)
			}
		}
		if !changed {
			return nil
		}
		return writeCursorHooksDocument(path, document)
	})
	if err != nil {
		return path, false, err
	}
	return path, changed, nil
}

func cursorHooksPath(repoRoot string) (string, error) {
	if !filepath.IsAbs(repoRoot) {
		return "", fmt.Errorf("cursor hooks: repo_root must be absolute: %q", repoRoot)
	}
	canonicalRoot, err := filepath.EvalSymlinks(filepath.Clean(repoRoot))
	if err != nil {
		return "", fmt.Errorf("cursor hooks: resolve repo_root: %w", err)
	}
	info, err := os.Stat(canonicalRoot)
	if err != nil {
		return "", fmt.Errorf("cursor hooks: stat repo_root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cursor hooks: repo_root is not a directory: %q", repoRoot)
	}
	return filepath.Join(canonicalRoot, ".cursor", "hooks.json"), nil
}

func ensureCursorHooksParent(path string, create bool) error {
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil {
		if !os.IsNotExist(err) || !create {
			return err
		}
		if err := os.Mkdir(directory, 0o755); err != nil && !os.IsExist(err) {
			return fmt.Errorf("cursor hooks: create .cursor directory: %w", err)
		}
		info, err = os.Lstat(directory)
		if err != nil {
			return fmt.Errorf("cursor hooks: verify .cursor directory: %w", err)
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cursor hooks: .cursor directory must not be a symlink: %q", directory)
	}
	if !info.IsDir() {
		return fmt.Errorf("cursor hooks: .cursor path is not a directory: %q", directory)
	}

	leaf, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("cursor hooks: inspect hooks.json: %w", err)
	}
	if leaf.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cursor hooks: hooks.json must not be a symlink: %q", path)
	}
	if !leaf.Mode().IsRegular() {
		return fmt.Errorf("cursor hooks: hooks.json is not a regular file: %q", path)
	}
	return nil
}

func newCursorHooksDocument() *cursorHooksDocument {
	return &cursorHooksDocument{
		root: map[string]json.RawMessage{
			"version": json.RawMessage("1"),
		},
		hooks: make(map[string]json.RawMessage),
	}
}

func readCursorHooksDocument(path string) (*cursorHooksDocument, bool, error) {
	cursorRoot, err := openCursorConfigRoot(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = cursorRoot.Close() }()
	leaf, err := cursorRoot.Lstat("hooks.json")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("cursor hooks: inspect hooks.json: %w", err)
	}
	if leaf.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("cursor hooks: hooks.json must not be a symlink")
	}
	if !leaf.Mode().IsRegular() {
		return nil, false, errors.New("cursor hooks: hooks.json is not a regular file")
	}
	data, err := cursorRoot.ReadFile("hooks.json")
	if err != nil {
		return nil, false, fmt.Errorf("cursor hooks: read hooks.json: %w", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, false, fmt.Errorf("cursor hooks: malformed hooks.json: %w", err)
	}
	if root == nil {
		return nil, false, errors.New("cursor hooks: malformed hooks.json: root must be an object")
	}
	versionRaw, ok := root["version"]
	if !ok {
		return nil, false, errors.New("cursor hooks: malformed hooks.json: version is required")
	}
	var version int
	if err := json.Unmarshal(versionRaw, &version); err != nil {
		return nil, false, errors.New("cursor hooks: malformed hooks.json: version must be an integer")
	}
	if version != cursorHooksVersion {
		return nil, false, fmt.Errorf("cursor hooks: unsupported hooks.json version %d", version)
	}

	hooks := make(map[string]json.RawMessage)
	if hooksRaw, present := root["hooks"]; present {
		if err := json.Unmarshal(hooksRaw, &hooks); err != nil || hooks == nil {
			return nil, false, errors.New("cursor hooks: malformed hooks.json: hooks must be an object")
		}
	}
	document := &cursorHooksDocument{root: root, hooks: hooks}
	for event := range hooks {
		if _, err := document.entries(event); err != nil {
			return nil, false, err
		}
	}
	return document, true, nil
}

func (d *cursorHooksDocument) entries(event string) ([]cursorHookEntry, error) {
	raw, present := d.hooks[event]
	if !present {
		return nil, nil
	}
	var rawEntries []json.RawMessage
	if err := json.Unmarshal(raw, &rawEntries); err != nil || rawEntries == nil {
		return nil, fmt.Errorf("cursor hooks: malformed hooks.%s: expected an array", event)
	}
	entries := make([]cursorHookEntry, 0, len(rawEntries))
	for index, rawEntry := range rawEntries {
		entry, err := decodeCursorHookEntry(rawEntry)
		if err != nil {
			return nil, fmt.Errorf("cursor hooks: malformed hooks.%s[%d]: %w", event, index, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func decodeCursorHookEntry(raw json.RawMessage) (cursorHookEntry, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return cursorHookEntry{}, errors.New("hook entry must be an object")
	}
	entryType := ""
	if typeRaw, present := object["type"]; present {
		if err := json.Unmarshal(typeRaw, &entryType); err != nil || entryType == "" {
			return cursorHookEntry{}, errors.New("type must be a nonempty string")
		}
	}
	commandRaw, present := object["command"]
	command := ""
	if present {
		if err := json.Unmarshal(commandRaw, &command); err != nil || command == "" {
			return cursorHookEntry{}, errors.New("command must be a nonempty string")
		}
		if entryType != "" && entryType != "command" {
			return cursorHookEntry{}, errors.New("command hook type must be command")
		}
	} else if entryType == "prompt" {
		promptRaw, ok := object["prompt"]
		if !ok {
			return cursorHookEntry{}, errors.New("prompt is required for a prompt hook")
		}
		var prompt string
		if err := json.Unmarshal(promptRaw, &prompt); err != nil || prompt == "" {
			return cursorHookEntry{}, errors.New("prompt must be a nonempty string")
		}
	} else {
		return cursorHookEntry{}, errors.New("command is required")
	}

	var timeout *float64
	if timeoutRaw, present := object["timeout"]; present {
		decoder := json.NewDecoder(bytes.NewReader(timeoutRaw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return cursorHookEntry{}, errors.New("timeout must be a positive number")
		}
		number, ok := value.(json.Number)
		if !ok {
			return cursorHookEntry{}, errors.New("timeout must be a positive number")
		}
		parsed, err := number.Float64()
		if err != nil || parsed <= 0 || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
			return cursorHookEntry{}, errors.New("timeout must be a positive number")
		}
		timeout = &parsed
	}
	return cursorHookEntry{raw: raw, object: object, command: command, timeout: timeout}, nil
}

func (d *cursorHooksDocument) setEntries(event string, entries []cursorHookEntry) {
	raw := make([]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		raw = append(raw, entry.raw)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		panic(err) // json.RawMessage values were already validated JSON.
	}
	d.hooks[event] = encoded
}

func upsertCursorHook(entries []cursorHookEntry, executable, event string) ([]cursorHookEntry, bool, error) {
	wantCommand := cursorHookCommand(executable, event)
	firstOwned := -1
	changed := false
	updated := make([]cursorHookEntry, 0, len(entries))
	for _, entry := range entries {
		if !isOwnedCursorHook(entry.command, event, wantCommand) {
			updated = append(updated, entry)
			continue
		}
		if firstOwned >= 0 {
			changed = true
			continue
		}
		firstOwned = len(updated)
		owned, entryChanged, err := makeCursorHookEntry(wantCommand, entry.object)
		if err != nil {
			return nil, false, err
		}
		updated = append(updated, owned)
		changed = changed || entryChanged
	}
	if firstOwned < 0 {
		owned, _, err := makeCursorHookEntry(wantCommand, nil)
		if err != nil {
			return nil, false, err
		}
		updated = append(updated, owned)
		changed = true
	}
	return updated, changed, nil
}

func makeCursorHookEntry(command string, existing map[string]json.RawMessage) (cursorHookEntry, bool, error) {
	object := make(map[string]json.RawMessage, len(existing))
	for key, value := range existing {
		object[key] = value
	}
	commandRaw, err := json.Marshal(command)
	if err != nil {
		return cursorHookEntry{}, false, err
	}
	changed := !bytes.Equal(object["command"], commandRaw) || string(object["timeout"]) != "10"
	object["command"] = commandRaw
	object["timeout"] = json.RawMessage("10")
	raw, err := json.Marshal(object)
	if err != nil {
		return cursorHookEntry{}, false, err
	}
	timeout := float64(cursorHookTimeout)
	return cursorHookEntry{raw: raw, object: object, command: command, timeout: &timeout}, changed, nil
}

func writeCursorHooksDocument(path string, document *cursorHooksDocument) error {
	hooksRaw, err := json.Marshal(document.hooks)
	if err != nil {
		return fmt.Errorf("cursor hooks: encode hooks: %w", err)
	}
	document.root["hooks"] = hooksRaw
	data, err := json.MarshalIndent(document.root, "", "  ")
	if err != nil {
		return fmt.Errorf("cursor hooks: encode hooks.json: %w", err)
	}
	data = append(data, '\n')
	if err := atomicWriteCursorHooks(path, data); err != nil {
		return fmt.Errorf("cursor hooks: write hooks.json: %w", err)
	}
	return nil
}

func openCursorConfigRoot(path string) (*os.Root, error) {
	repoRoot := filepath.Dir(filepath.Dir(path))
	root, err := os.OpenRoot(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("cursor hooks: open repo_root: %w", err)
	}
	info, err := root.Lstat(".cursor")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("cursor hooks: inspect .cursor directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		_ = root.Close()
		return nil, errors.New("cursor hooks: .cursor directory must not be a symlink")
	}
	if !info.IsDir() {
		_ = root.Close()
		return nil, errors.New("cursor hooks: .cursor path is not a directory")
	}
	cursorRoot, err := root.OpenRoot(".cursor")
	_ = root.Close()
	if err != nil {
		return nil, fmt.Errorf("cursor hooks: open .cursor directory: %w", err)
	}
	return cursorRoot, nil
}

func atomicWriteCursorHooks(path string, data []byte) error {
	cursorRoot, err := openCursorConfigRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = cursorRoot.Close() }()

	permission := os.FileMode(0o644)
	if info, err := cursorRoot.Lstat("hooks.json"); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("hooks.json must not be a symlink")
		}
		if !info.Mode().IsRegular() {
			return errors.New("hooks.json is not a regular file")
		}
		permission = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect hooks.json: %w", err)
	}

	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("create temporary name: %w", err)
	}
	temporary := fmt.Sprintf(".hooks.json.tmp-%x", random[:])
	file, err := cursorRoot.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = cursorRoot.Remove(temporary)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := file.Chmod(permission); err != nil {
		_ = file.Close()
		return fmt.Errorf("set temporary file mode: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := cursorRoot.Rename(temporary, "hooks.json"); err != nil {
		return fmt.Errorf("replace hooks.json: %w", err)
	}
	keep = true
	if directory, err := cursorRoot.Open("."); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func cursorHookCommand(executable, event string) string {
	return shellQuoteCursorHookArg(filepath.Clean(executable)) + " hook " + event
}

func shellQuoteCursorHookArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func isOwnedCursorHook(command, event, currentCommand string) bool {
	if command == currentCommand {
		return true
	}
	executable, suffix, ok := parseCursorHookExecutable(command)
	if !ok || suffix != " hook "+event || !filepath.IsAbs(executable) {
		return false
	}
	base := filepath.Base(executable)
	return base == "ox-adapter-cursor" || base == "ox-adapter-cursor.exe"
}

func parseCursorHookExecutable(command string) (string, string, bool) {
	if len(command) < 2 || command[0] != '\'' {
		return "", "", false
	}
	var value strings.Builder
	for index := 1; index < len(command); {
		next := strings.IndexByte(command[index:], '\'')
		if next < 0 {
			return "", "", false
		}
		next += index
		value.WriteString(command[index:next])
		if strings.HasPrefix(command[next:], `'"'"'`) {
			value.WriteByte('\'')
			index = next + len(`'"'"'`)
			continue
		}
		return value.String(), command[next+1:], true
	}
	return "", "", false
}
