package service

import "testing"

// B2: exports must be bounded by cumulative bytes, not just row count. A query
// returning wide columns (large BLOB/TEXT) can blow up memory/disk with only a
// few thousand rows, which the row cap alone never catches.
func TestExportLimits_ByteCapRejects(t *testing.T) {
	if err := exportLimitErr(0, 500, 1_000_000, 1000); err != nil {
		t.Errorf("within caps should pass, got %v", err)
	}
	if err := exportLimitErr(0, 1001, 1_000_000, 1000); err == nil {
		t.Error("expected the byte cap breach to error")
	}
	// row cap still enforced
	if err := exportLimitErr(1_000_000, 0, 1_000_000, 1000); err == nil {
		t.Error("expected the row cap breach to error")
	}
	// 0 disables a cap
	if err := exportLimitErr(1_000_000, 5000, 0, 0); err != nil {
		t.Errorf("0 caps should disable the limits, got %v", err)
	}
}
