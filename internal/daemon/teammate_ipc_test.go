package daemon

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon/ipcfault"
)

// The hook-side client must turn every way the daemon can answer wrongly into an
// error, so a hook that cannot get its notes shows none instead of guessing.
func TestTeammateClientTurnsABadAnswerIntoAnError(t *testing.T) {
	reply := func(r Response) func([]byte) []byte {
		return func([]byte) []byte { b, _ := json.Marshal(r); return b }
	}
	for name, tc := range map[string]struct {
		answer  Response
		wantErr bool
	}{
		"refused":     {Response{Success: false, Error: "no"}, true},
		"garbage":     {Response{Success: true, Data: json.RawMessage(`"not an object"`)}, true},
		"fine":        {Response{Success: true, Data: json.RawMessage(`{"lease_id":"L","entries":[]}`)}, false},
		"fine, empty": {Response{Success: true, Data: json.RawMessage(`{"entries":[]}`)}, false},
	} {
		t.Run(name, func(t *testing.T) {
			srv := ipcfault.New(t, reply(tc.answer))
			c := NewClientWithSocketAndTimeout(srv.Path(), 5*time.Second)
			_, takeErr := c.TeammateCommentsTake("a", "cnv", 3)
			if (takeErr != nil) != tc.wantErr {
				t.Errorf("take err = %v", takeErr)
			}
			confirmErr := c.TeammateCommentsConfirm("a", "L", []string{"x"})
			if wantConfirmErr := !tc.answer.Success; (confirmErr != nil) != wantConfirmErr {
				t.Errorf("confirm err = %v", confirmErr)
			}
		})
	}
}

// A client that connects and leaves without a word must not wedge the server.
func TestFaultServerSurvivesAClientThatSaysNothing(t *testing.T) {
	srv := ipcfault.New(t, func([]byte) []byte { return []byte(`{}`) })
	conn, err := net.Dial("unix", srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	// a following request is still served
	srv.WaitServed(1)
	c := NewClientWithSocketAndTimeout(srv.Path(), 5*time.Second)
	if err := c.TeammateCommentsConfirm("a", "L", nil); err == nil {
		t.Log("a bare {} is not a success answer, as expected")
	}
}

// A daemon whose service fails answers the hook with an error, never a success.
func TestHandlersAnswerAnErrorWhenTheServiceFails(t *testing.T) {
	s := NewServer(slog.Default())
	s.SetTeammateCommentsTakeHandler(func(TeammateCommentsTakePayload) (TeammateCommentsTakeResponse, error) {
		return TeammateCommentsTakeResponse{}, errors.New("db locked")
	})
	s.SetTeammateCommentsConfirmHandler(func(TeammateCommentsConfirmPayload) error { return errors.New("db locked") })
	if res := handleTeammateCommentsTake(s, Message{Payload: []byte(`{"agent_id":"a"}`)}, nil); res.Response.Success {
		t.Error("take error was reported as success")
	}
	if res := handleTeammateCommentsConfirm(s, Message{Payload: []byte(`{"agent_id":"a"}`)}, nil); res.Response.Success {
		t.Error("confirm error was reported as success")
	}
}

// The unwired callback service does nothing and fails nothing.
func TestUnwiredTeammateCallbacksAreHarmless(t *testing.T) {
	cb := &CallbackService{}
	if out, err := cb.TeammateCommentsTake(TeammateCommentsTakePayload{}); err != nil || out.LeaseID != "" {
		t.Fatalf("%v %v", out, err)
	}
	if err := cb.TeammateCommentsConfirm(TeammateCommentsConfirmPayload{}); err != nil {
		t.Fatal(err)
	}
}
