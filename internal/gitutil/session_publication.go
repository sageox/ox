package gitutil

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/sageox/ox/internal/secretguard"
)

// A scan bound is a refusal threshold, never a reason to silently skip data.
const sessionPublicationScanLimit = 8 << 20

var freshSessionPublicationRules = secretguard.LatestRules

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
	paths := make([]string, 0)
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		id, path, _ := strings.Cut(line, " ")
		ids = append(ids, id)
		paths = append(paths, path)
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
	for i := range ids {
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
		data = secretScanView(paths[i], data[:len(data)-1])
		if err := secretguard.ValidateContent("sessions/content", data); err != nil {
			return fmt.Errorf("committed session content refused; clean unpushed history before retry: %w", err)
		}
		if rules == nil {
			rules, err = freshSessionPublicationRules(ctx)
			if err != nil {
				return fmt.Errorf("current session rules required before push: %w", err)
			}
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
			}
			if err := rules.ValidateContent(path, nil); err != nil {
				return fmt.Errorf("session filename refused by current rules: %w", err)
			}
		}
	}
	return nil
}
