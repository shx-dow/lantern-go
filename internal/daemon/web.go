package daemon

import _ "embed"

// uiPage is the embedded minimal web interface. The page itself carries no
// secrets and is served without auth; every API call it makes presents the
// bearer token from the browser's local storage.
//
//go:embed web/index.html
var uiPage []byte

// UI returns the embedded page and its content type.
func UI() ([]byte, string) {
	return uiPage, "text/html; charset=utf-8"
}
