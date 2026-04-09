package browser

// HARLog represents a simplified HAR log containing captured HTTP entries.
type HARLog struct {
	Entries []HAREntry `json:"entries"`
}

// HAREntry represents a single HTTP request/response pair.
type HAREntry struct {
	Request  HARRequest  `json:"request"`
	Response HARResponse `json:"response"`
}

// HARRequest represents an HTTP request.
type HARRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body,omitempty"`
}

// HARResponse represents an HTTP response.
type HARResponse struct {
	Status      int               `json:"status"`
	StatusText  string            `json:"status_text"`
	ContentType string            `json:"content_type"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body,omitempty"`
}
