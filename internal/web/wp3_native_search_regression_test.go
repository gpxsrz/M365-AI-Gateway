package web

import (
	"bytes"
	"strings"
	"testing"
)

func TestBuildWP3NativeSearchRegressionPackageCoversHTTPAndSSEWithoutContent(t *testing.T) {
	options := WP3NativeSearchRegressionHarnessOptions{
		NormativeADRSHA256: strings.Repeat("1", 64),
		NormativeADRBytes:  135432,
		SourceHead:         strings.Repeat("2", 40),
		SourceTree:         strings.Repeat("5", 40),
		HarnessSHA256:      strings.Repeat("3", 64),
		HarnessBytes:       123456,
	}
	first, err := BuildWP3NativeSearchRegressionPackage(options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildWP3NativeSearchRegressionPackage(options)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.CanonicalJSON, second.CanonicalJSON) || first.ChecksumSHA256 != second.ChecksumSHA256 {
		t.Fatal("harness package is not deterministic")
	}
	if len(first.Package.Observations) != 10 {
		t.Fatalf("observations=%d", len(first.Package.Observations))
	}
	for _, observation := range first.Package.Observations {
		if observation.HTTPStatus != 200 || observation.UpstreamAttempts != 1 || !observation.SourceAttributionObserved || observation.RawEventsRetained || observation.ContentRetained {
			t.Fatalf("observation=%#v", observation)
		}
	}
	encoded := string(first.CanonicalJSON)
	for _, forbidden := range []string{"WP3_SYNTHETIC_SEARCH_QUERY", "WP3_SYNTHETIC_SEARCH_ANSWER", "synthetic.example", "sourceAttributions", "searchQueries", `"target":"update"`, "legacy_global_restriction_seen", "scoped_search_allowance_observed"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("package leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestWP3NativeSearchRegressionRequiresDefaultEventPrivacy(t *testing.T) {
	t.Setenv("M365_INCLUDE_UPSTREAM_EVENTS", "true")
	_, err := BuildWP3NativeSearchRegressionPackage(WP3NativeSearchRegressionHarnessOptions{
		NormativeADRSHA256: strings.Repeat("1", 64),
		NormativeADRBytes:  135432,
		SourceHead:         strings.Repeat("2", 40),
		SourceTree:         strings.Repeat("5", 40),
		HarnessSHA256:      strings.Repeat("3", 64),
		HarnessBytes:       123456,
	})
	if err == nil {
		t.Fatal("harness accepted raw upstream event exposure")
	}
}
