package core

import "testing"

func TestCleanupReportHasErrors(t *testing.T) {
	if (CleanupReport{Results: []CleanupResult{{Component: "client"}}}).HasErrors() {
		t.Fatal("cleanup report without errors reports failure")
	}
	if !(CleanupReport{Results: []CleanupResult{{Component: "client", Error: "timeout"}}}).HasErrors() {
		t.Fatal("cleanup report with error reports success")
	}
}
