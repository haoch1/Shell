package main

import (
	"slices"
	"testing"
)

func TestBatchDownloadKeepsExactNamesAndChecksDestinations(t *testing.T) {
	got, err := batchDownloadPaths([]string{"/dir/name ", "", "/dir/sub", "/dir/name "})
	if err != nil || !slices.Equal(got, []string{"/dir/name ", "/dir/sub"}) {
		t.Fatalf("download paths: %q, %v", got, err)
	}
	for _, paths := range [][]string{
		{"/dir/File", "/dir/file"},
		{"/dir/key:1", "/dir/key?1"},
		{"/dir/name", "/dir/name "},
	} {
		if _, err := batchDownloadPaths(paths); err == nil {
			t.Errorf("allowed colliding download destinations for %q", paths)
		}
	}
}
