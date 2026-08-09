package chathub

type Attachment struct {
	Type     string `json:"type"`
	URL      string `json:"url,omitempty"`
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Detail   string `json:"detail,omitempty"`

	// Request-scoped Microsoft transport state. These fields are deliberately
	// excluded from JSON and never form a second persisted attachment ledger.
	OriginalName           string `json:"-"`
	TransportName          string `json:"-"`
	DocID                  string `json:"-"`
	FileType               string `json:"-"`
	ReferenceURL           string `json:"-"`
	UploadedConversationID string `json:"-"`
	Size                   int64  `json:"-"`
}
