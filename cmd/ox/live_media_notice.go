package main

import "github.com/sageox/ox/internal/livesession"

// addLiveMediaNotice adds the one-time screenshot notice to a stop's output:
// image bytes upload without the secret scan text gets, and the person is told
// once per machine, after the first screenshot streamed.
//
// It does not mark the notice given: the returned function does, and the caller
// runs it only after the output was written, so a failed write leaves the
// notice owed for the next stop.
func addLiveMediaNotice(output *sessionStopOutput) (confirm func()) {
	if !livesession.PeekMediaNotice("") {
		return func() {}
	}
	output.DataWarnings = append(output.DataWarnings, livesession.MediaNotice)
	output.Guidance += " Tell the user once, in plain words, using the data_warnings entry about screenshots."
	return func() { livesession.ConfirmMediaNotice("") }
}
