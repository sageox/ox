package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/proc"
	"github.com/sageox/ox/internal/selfexec"
	"github.com/sageox/ox/internal/version"
)

// postHogKey and postHogHost are set by ldflags in .config/goreleaser.yml, so
// release builds send to PostHog. make build, go install, and go test leave
// them empty, and those builds send nothing.
var (
	postHogKey  string
	postHogHost string
)

// PostHogSenderArg, as os.Args[1], makes ox post the PostHog event on its
// stdin and exit instead of running a command. main checks for it before any
// other setup.
const PostHogSenderArg = "__posthog-send"

// postHogMaxPayload bounds an event. CapturePostHog writes the event into a
// pipe before the sender starts, and a write that fits in the pipe's buffer
// (at least 4 KiB on the platforms ox builds for) cannot block the command.
// An event is under 1 KiB.
const postHogMaxPayload = 4096

// postHogSendTimeout bounds the detached sender. Nothing waits on it.
const postHogSendTimeout = 10 * time.Second

// postHogSenderSlots caps how many senders run at once. On a network that
// drops packets to PostHog each sender waits out postHogSendTimeout, and an AI
// coworker or a script can run commands faster than that.
const postHogSenderSlots = 4

// postHogExecutable is the binary started as the sender. Tests replace it,
// because selfexec.Path refuses to run under go test.
var postHogExecutable = selfexec.Path

// PostHogConfigured reports whether this build sends to PostHog, so callers can
// skip building an event that would go nowhere.
func PostHogConfigured() bool {
	return postHogKey != "" && postHogHost != ""
}

type postHogEvent struct {
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Timestamp  time.Time      `json:"timestamp"`
	Properties map[string]any `json:"properties"`
}

// CapturePostHog sends one event to PostHog from a detached ox process, so
// the command that produced it never waits on the network. A client inside
// the command cannot do that: the command exits before the client's
// background flush, so it would have to either block at exit or lose the
// event.
//
// Its distinct_id is the install ID, and it creates no PostHog person
// profile. It adds the version, OS, and arch to props.
// Nothing is sent when the build has no PostHog key, or when the install ID
// cannot be saved, since an ID that changes every run would count each run
// as a new install. Callers check the user's telemetry opt-out.
func CapturePostHog(event string, props map[string]any) {
	if !PostHogConfigured() {
		return
	}
	installID, err := config.InstallID()
	if err != nil {
		return
	}
	props["$process_person_profile"] = false
	props["$geoip_disable"] = true
	props["$lib"] = "ox-cli"
	props["$lib_version"] = version.Version
	props["os"] = runtime.GOOS
	props["arch"] = runtime.GOARCH

	payload, err := json.Marshal(postHogEvent{
		Event:      event,
		DistinctID: installID,
		Timestamp:  time.Now().UTC(),
		Properties: props,
	})
	if err != nil || len(payload) > postHogMaxPayload {
		return
	}
	exe, err := postHogExecutable()
	if err != nil {
		return
	}
	// The event reaches the sender on stdin, not in its arguments, which any
	// local user can read from the process list. It is written and the pipe
	// closed before the sender starts, so the kernel holds the whole event
	// even after this command exits.
	stdin, w, err := os.Pipe()
	if err != nil {
		return
	}
	defer stdin.Close()
	_, err = w.Write(payload)
	if closeErr := w.Close(); err != nil || closeErr != nil {
		return
	}
	// Stdout and Stderr stay nil (/dev/null). A sender holding this command's
	// output pipes would keep anything reading them to EOF waiting until the
	// upload finished. proc.Detach does nothing on Windows, which has no ox
	// release (.config/goreleaser.yml); a Windows build would need its own
	// creation flags so the sender opens no console window.
	cmd := exec.Command(exe, PostHogSenderArg)
	cmd.Stdin = stdin
	proc.Detach(cmd)
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Release()
	}
}

// RunPostHogSender posts the event CapturePostHog wrote to stdin. It runs in
// the detached process and reports nothing, because nothing is waiting for it.
// When every sender slot is taken it drops the event instead of adding
// another waiting process.
func RunPostHogSender(stdin io.Reader) {
	if !PostHogConfigured() {
		return
	}
	event, err := io.ReadAll(io.LimitReader(stdin, postHogMaxPayload))
	if err != nil {
		return
	}
	body, err := json.Marshal(struct {
		APIKey string            `json:"api_key"`
		Batch  []json.RawMessage `json:"batch"`
	}{postHogKey, []json.RawMessage{event}})
	if err != nil {
		return
	}
	for slot := range postHogSenderSlots {
		slotPath := filepath.Join(os.TempDir(), fmt.Sprintf("ox-posthog-sender-%d", slot))
		// A moment, not zero: a zero timeout can lose to its own timer and
		// skip a free slot.
		err := fileutil.WithFileLockTimeout(context.Background(), slotPath, 10*time.Millisecond, func() error {
			postToPostHog(body)
			return nil
		})
		if err == nil {
			return
		}
	}
}

func postToPostHog(body []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), postHogSendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, postHogHost+"/batch/", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}
