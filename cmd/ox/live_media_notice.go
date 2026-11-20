package main

import "github.com/sageox/ox/internal/livesession"

// addLiveMediaNotice adds the one-time screenshot notice to a stop's output:
// image bytes upload without the secret scan text gets, and the person is told
// once per machine, after the first screenshot streamed.
func addLiveMediaNotice(output *sessionStopOutput) {
	if livesession.TakeMediaNotice("") {
		output.DataWarnings = append(output.DataWarnings, livesession.MediaNotice)
		output.Guidance += " Tell the user once, in plain words, using the data_warnings entry about screenshots."
	}
}
