package sessionstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Each convenience method must hit the route the contract names with the verb
// it names; a swapped path would pass every Streamer test that never calls it.
func TestClientMethodsUseTheContractRoutes(t *testing.T) {
	type seen struct{ method, path string }
	var got []seen
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.Path})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == PathKeys:
			_ = json.NewEncoder(w).Encode(RegisterKeyResponse{KeyID: "k"})
		case r.Method == http.MethodPut:
			_ = json.NewEncoder(w).Encode(OpenSessionResponse{Status: StatusLive})
		case r.Method == http.MethodPost && r.URL.Path == TeammateCommentAcksPath("cnv_x"):
			var req AckTeammateCommentsRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("ack body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(AckTeammateCommentsResponse{Recorded: len(req.Acks)})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(SessionState{ConversationID: "cnv_x"})
		default:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(ts.Close)
	_, priv := testKey(t)
	c := &Client{BaseURL: ts.URL, Token: "t", Key: priv, HTTP: ts.Client()}
	ctx := context.Background()

	if _, err := c.RegisterKey(ctx, "label"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Open(ctx, "cnv_x", OpenSessionRequest{}); err != nil {
		t.Fatal(err)
	}
	ack, err := c.AckTeammateComments(ctx, "cnv_x", AckTeammateCommentsRequest{LayerID: "l", Acks: []TeammateCommentAck{{ID: "a", DeliveredAtSeq: 1}}})
	if err != nil || ack.Recorded != 1 {
		t.Fatalf("ack: %+v %v", ack, err)
	}
	if err := c.Close(ctx, "cnv_x", CloseRequest{LayerID: "l"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "cnv_x"); err != nil {
		t.Fatal(err)
	}
	if st, err := c.State(ctx, "cnv_x"); err != nil || st.ConversationID != "cnv_x" {
		t.Fatalf("state: %+v %v", st, err)
	}
	want := []seen{
		{"POST", PathKeys}, {"PUT", SessionPath("cnv_x")}, {"POST", TeammateCommentAcksPath("cnv_x")},
		{"POST", ClosePath("cnv_x")}, {"DELETE", SessionPath("cnv_x")}, {"GET", SessionPath("cnv_x")},
	}
	if len(got) != len(want) {
		t.Fatalf("requests %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("request %d = %v, want %v", i, got[i], want[i])
		}
	}
}
