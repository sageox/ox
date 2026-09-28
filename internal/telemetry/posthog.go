package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// PostHogSenderArg, as os.Args[1], makes ox post one PostHog event and exit
// instead of running a command. main checks for it before any other setup.
const PostHogSenderArg = "__posthog-send"

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
	if err != nil {
		return
	}
	exe, err := postHogExecutable()
	if err != nil {
		return
	}
	// Stdout and Stderr stay nil (/dev/null). A sender holding this command's
	// output pipes would keep anything reading them to EOF waiting until the
	// upload finished. proc.Detach does nothing on Windows, which has no ox
	// release (.config/goreleaser.yml); a Windows build would need its own
	// creation flags so the sender opens no console window.
	cmd := exec.Command(exe, PostHogSenderArg, string(payload))
	proc.Detach(cmd)
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Release()
	}
}

// RunPostHogSender posts one event built by CapturePostHog. It runs in the
// detached process and reports nothing, because nothing is waiting for it.
// When every sender slot is taken it drops the event instead of adding
// another waiting process.
func RunPostHogSender(event string) {
	if !PostHogConfigured() {
		return
	}
	body, err := json.Marshal(struct {
		APIKey string            `json:"api_key"`
		Batch  []json.RawMessage `json:"batch"`
	}{postHogKey, []json.RawMessage{json.RawMessage(event)}})
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
