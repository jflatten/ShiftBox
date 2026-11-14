package proxy

import (
	"net/http"
	"strings"
)

// Sanitize removes controller credentials and connection-local headers before
// forwarding to an untrusted guest. Application cookies remain application-owned.
// WebSocket transport code reconstructs its validated upgrade separately.
func Sanitize(request *http.Request) {
	remove := map[string]bool{
		"connection": true, "proxy-connection": true, "keep-alive": true,
		"proxy-authenticate": true, "proxy-authorization": true, "te": true,
		"trailer": true, "transfer-encoding": true, "upgrade": true, "x-csrf-token": true,
	}
	for key, values := range request.Header {
		if !strings.EqualFold(key, "Connection") {
			continue
		}
		for _, value := range values {
			for _, token := range strings.Split(value, ",") {
				remove[strings.ToLower(strings.TrimSpace(token))] = true
			}
		}
	}
	for key := range request.Header {
		lower := strings.ToLower(key)
		if remove[lower] || strings.HasPrefix(lower, "x-shiftbox-") {
			delete(request.Header, key)
		}
	}
	// Normalize even directly constructed headers before using Go's cookie parser.
	cookieRequest := &http.Request{Header: make(http.Header)}
	for key, values := range request.Header {
		if strings.EqualFold(key, "Cookie") {
			for _, value := range values {
				cookieRequest.Header.Add("Cookie", value)
			}
			delete(request.Header, key)
		}
	}
	for _, cookie := range cookieRequest.Cookies() {
		name := strings.ToLower(cookie.Name)
		if strings.HasPrefix(name, "__host-shiftbox_") || strings.HasPrefix(name, "__secure-shiftbox_") || strings.HasPrefix(name, "shiftbox_") {
			continue
		}
		request.AddCookie(cookie)
	}
}
