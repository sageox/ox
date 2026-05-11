package main

import (
	"os"
	"testing"

	"github.com/sageox/ox/internal/testutil/slogquiet"
)

func TestMain(m *testing.M) {
	slogquiet.Silence()
	os.Exit(m.Run())
}
