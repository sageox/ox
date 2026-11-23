package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A UTF-8 character split across two Writes (io.Copy hands the stripper
// 32 KiB chunks, so any long output can split one) must come out whole, not
// as U+FFFD halves. Every split point of every multi-byte width is tried.
func TestANSIStripperKeepsCharactersSplitAcrossWrites(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"dirs (`docs/adr`, …) done", // 3-byte, the release-notes case
		"café au lait",              // 2-byte
		"ship it \U0001F680 now",    // 4-byte
		"\x1b[1mbold …\x1b[0m tail", // escape codes around a split character
	} {
		want := "dirs (`docs/adr`, …) done"
		switch text {
		case "café au lait", "ship it \U0001F680 now":
			want = text
		case "\x1b[1mbold …\x1b[0m tail":
			want = "bold … tail"
		}
		for cut := 1; cut < len(text); cut++ {
			var out bytes.Buffer
			s := &ansiStripper{w: &out}
			n, err := s.Write([]byte(text[:cut]))
			require.NoError(t, err)
			assert.Equal(t, cut, n, "Write reports the caller's length")
			_, err = s.Write([]byte(text[cut:]))
			require.NoError(t, err)
			require.NoError(t, s.Flush())
			assert.Equal(t, want, out.String(), "text %q cut at byte %d", text, cut)
		}
	}
}

// Feeding one byte per Write is the worst case for holding back.
func TestANSIStripperByteAtATime(t *testing.T) {
	t.Parallel()
	text := "a…b\U0001F680cé"
	var out bytes.Buffer
	s := &ansiStripper{w: &out}
	for i := 0; i < len(text); i++ {
		_, err := s.Write([]byte{text[i]})
		require.NoError(t, err)
	}
	require.NoError(t, s.Flush())
	assert.Equal(t, text, out.String())
}

// A sequence still unfinished at EOF is not swallowed: Flush emits it.
func TestANSIStripperFlushEmitsUnfinishedTail(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	s := &ansiStripper{w: &out}
	_, err := s.Write([]byte("end \xe2\x80"))
	require.NoError(t, err)
	assert.Equal(t, "end ", out.String(), "the unfinished tail waits for the next Write")
	require.NoError(t, s.Flush())
	assert.True(t, bytes.HasPrefix(out.Bytes(), []byte("end ")))
	assert.Greater(t, out.Len(), len("end "), "Flush wrote the held bytes")
}
