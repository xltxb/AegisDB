package service

import "testing"

// B2: exports must be bounded by cumulative bytes, not just row count. A query
// returning wide columns (large BLOB/TEXT) can blow up memory/disk with only a
// few thousand rows, which the row cap alone never catches.
func TestExportLimits_ByteCapRejects(t *testing.T) {
	defer SetExportMaxRows(exportMaxRows)   // restore originals
	defer SetExportMaxBytes(exportMaxBytes) //

	SetExportMaxRows(1_000_000)
	SetExportMaxBytes(1000)

	if err := exportLimitErr(0, 500); err != nil {
		t.Errorf("within caps should pass, got %v", err)
	}
	if err := exportLimitErr(0, 1001); err == nil {
		t.Error("expected the byte cap breach to error")
	}
	// row cap still enforced
	if err := exportLimitErr(1_000_000, 0); err == nil {
		t.Error("expected the row cap breach to error")
	}
}
