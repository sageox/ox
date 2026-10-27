package gitutil

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/sageox/ox/internal/secretguard"
)

// A scan bound is a refusal threshold, never a reason to silently skip data.
const sessionPublicationScanLimit = 8 << 20

var freshSessionPublicationRules = secretguard.LatestRules

// sessionBlobPaths maps each blob the unpushed session history adds or changes
// to every path it lands at. rev-list --objects names one path per blob, so a
// blob shared by meta.json and another file would otherwise get meta.json's
// exemption at both.
func sessionBlobPaths(ctx context.Context, ledgerPath string) (map[string][]string, error) {
	args := []string{"-C", ledgerPath, "log", "--full-history", "-m", "--format=", "--raw", "--no-abbrev", "-z", "--no-renames", "--diff-filter=ACMT", "HEAD"}
	if exec.CommandContext(ctx, "git", "-C", ledgerPath, "rev-parse", "--verify", "@{upstream}").Run() == nil {
		args = append(args, "^@{upstream}")
	}
	args = append(args, "--", "sessions/")
	out, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		return nil, err
	}
	paths := map[string][]string{}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(fields); i++ {
		// -z raw records are ":<old mode> <new mode> <old oid> <new oid> <status>" then the path.
		record := strings.Fields(strings.TrimLeft(fields[i], "\n"))
		if len(record) != 5 || !strings.HasPrefix(record[0], ":") {
			continue
		}
		paths[record[3]] = append(paths[record[3]], fields[i+1])
		i++
	}
	return paths, nil
}

// metaOnlyPath returns a session meta path only when every path of a blob is
// one; otherwise "" so the blob is scanned unmodified.
func metaOnlyPath(paths []string) string {
	if len(paths) == 0 || slices.ContainsFunc(paths, func(p string) bool { return !isSessionMetaPath(p) }) {
		return ""
	}
	return paths[0]
}

// ValidateSessionPublication checks every newly reachable session blob, not
// the working tree or only the final diff. A later redaction/deletion commit
// does not remove an earlier credential-bearing blob from the pack being sent.
func ValidateSessionPublication(ctx context.Context, ledgerPath string) error {
	args := []string{"-C", ledgerPath, "rev-list", "--full-history", "--objects", "HEAD"}
	if exec.CommandContext(ctx, "git", "-C", ledgerPath, "rev-parse", "--verify", "@{upstream}").Run() == nil {
		args = append(args, "^@{upstream}")
	}
	args = append(args, "--", "sessions/")
	out, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		return errors.New("cannot enumerate committed session objects; publication pending")
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil
	}
	ids := make([]string, 0)
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		id, _, _ := strings.Cut(line, " ")
		ids = append(ids, id)
	}
	blobPaths, err := sessionBlobPaths(ctx, ledgerPath)
	if err != nil {
		return errors.New("cannot inspect committed session paths; publication pending")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", ledgerPath, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(strings.Join(ids, "\n") + "\n")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	reader := bufio.NewReader(stdout)
	var rules *secretguard.RuleSet
	for _, id := range ids {
		header, err := reader.ReadString('\n')
		if err != nil {
			return errors.New("cannot read committed session objects; publication pending")
		}
		fields := strings.Fields(header)
		if len(fields) != 3 {
			return errors.New("invalid committed session object; publication pending")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return errors.New("invalid committed session object size; publication pending")
		}
		if fields[1] != "blob" {
			if _, err := io.CopyN(io.Discard, reader, size+1); err != nil {
				return errors.New("incomplete committed session object; publication pending")
			}
			continue
		}
		// Bound per-object allocation. Exceeding a scan limit is an explicit hold,
		// never evidence that the object is safe to publish.
		if size > sessionPublicationScanLimit {
			return errors.New("committed session object exceeds scan limit; publication pending")
		}
		data := make([]byte, int(size)+1)
		if _, err := io.ReadFull(reader, data); err != nil || data[len(data)-1] != '\n' {
			return errors.New("incomplete committed session blob; publication pending")
		}
		data = secretScanView(metaOnlyPath(blobPaths[id]), data[:len(data)-1])
		if err := secretguard.ValidateContent("sessions/content", data); err != nil {
			return fmt.Errorf("committed session content refused; clean unpushed history before retry: %w", err)
		}
		if rules == nil {
			rules, err = freshSessionPublicationRules(ctx)
			if err != nil {
				return fmt.Errorf("current session rules required before push: %w", err)
			}
			// Every use follows the bundled check of the same bytes, so only
			// the rules the bundled catalog lacks can still refuse them.
			rules = rules.Delta(secretguard.Bundled())
		}
		if err := rules.ValidateContent("sessions/content", data); err != nil {
			return fmt.Errorf("committed session content refused by current rules: %w", err)
		}
	}
	if err := cmd.Wait(); err != nil {
		return errors.New("committed session scan failed; publication pending")
	}
	{
		// rev-list deduplicates identical blob OIDs and reports only one path, and
		// it omits blobs upstream already has. Inspect every added/changed filename
		// whether or not a new blob was scanned: a rename to a sensitive file
		// extension must not bypass path-only rules.
		pathArgs := []string{"-C", ledgerPath, "log", "--full-history", "--format=", "--name-only", "-z", "--no-renames", "--diff-filter=ACMT", "HEAD"}
		if exec.CommandContext(ctx, "git", "-C", ledgerPath, "rev-parse", "--verify", "@{upstream}").Run() == nil {
			pathArgs = append(pathArgs, "^@{upstream}")
		}
		pathArgs = append(pathArgs, "--", "sessions/")
		paths, err := exec.CommandContext(ctx, "git", pathArgs...).Output()
		if err != nil {
			return errors.New("cannot inspect session filenames; publication pending")
		}
		for path := range strings.SplitSeq(string(paths), "\x00") {
			if path == "" {
				continue
			}
			if err := secretguard.ValidateContent(path, nil); err != nil {
				return fmt.Errorf("session filename refused: %w", err)
			}
			if rules == nil {
				if rules, err = freshSessionPublicationRules(ctx); err != nil {
					return fmt.Errorf("current session rules required before push: %w", err)
				}
				rules = rules.Delta(secretguard.Bundled())
			}
			if err := rules.ValidateContent(path, nil); err != nil {
				return fmt.Errorf("session filename refused by current rules: %w", err)
			}
		}
	}
	return nil
}
