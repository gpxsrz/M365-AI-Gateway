package wp2evidence

import (
	"embed"
	"io/fs"
)

// acceptedArtifacts contains only the immutable, privacy-safe evidence package
// indexes and compressed payloads accepted for WP2 Issues #4 through #7.
//
//go:embed issue-4/evidence-index.json issue-4/evidence-set.json.gz.b64 issue-5/evidence-index.json issue-5/evidence-set.json.gz.b64 issue-6/evidence-index.json issue-6/evidence-set.json.gz.b64 issue-7/evidence-index.json issue-7/evidence-set.json.gz.b64
var acceptedArtifacts embed.FS

func AcceptedArtifacts() fs.FS {
	return acceptedArtifacts
}
