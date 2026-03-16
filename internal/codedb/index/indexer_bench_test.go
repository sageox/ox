package index

import (
	"fmt"
	"strconv"
	"testing"
)

// BenchmarkBleveDocIDFmtSprintf measures the current fmt.Sprintf doc ID construction.
func BenchmarkBleveDocIDFmtSprintf(b *testing.B) {
	var id int64 = 123456
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = fmt.Sprintf("blob_%d", id)
	}
}

// BenchmarkBleveDocIDStrconv measures the optimized strconv doc ID construction.
func BenchmarkBleveDocIDStrconv(b *testing.B) {
	var id int64 = 123456
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = "blob_" + strconv.FormatInt(id, 10)
	}
}

// BenchmarkDiffDocIDFmtSprintf measures fmt.Sprintf for diff doc IDs.
func BenchmarkDiffDocIDFmtSprintf(b *testing.B) {
	var id int64 = 789012
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = fmt.Sprintf("diff_%d", id)
	}
}

// BenchmarkDiffDocIDStrconv measures strconv for diff doc IDs.
func BenchmarkDiffDocIDStrconv(b *testing.B) {
	var id int64 = 789012
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = "diff_" + strconv.FormatInt(id, 10)
	}
}
