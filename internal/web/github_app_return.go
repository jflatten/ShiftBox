package web

import (
	"net/http"
	"net/url"
	"regexp"
)

var (
	githubAppReturnCode  = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,256}$`)
	githubAppReturnState = regexp.MustCompile(`^[a-zA-Z0-9_-]{43}$`)
)

// GitHubAppReturn bridges GitHub's cross-site navigation to the same-origin
// settings page. Only its authenticated POST can redeem a registration code.
func GitHubAppReturn(origin string) http.Handler {
	configured, _ := url.Parse(origin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		query, err := url.ParseQuery(r.URL.RawQuery)
		if r.Method != http.MethodGet || configured == nil || configured.Scheme != "https" || r.TLS == nil ||
			r.Host != configured.Host || err != nil || len(query["code"]) != 1 || len(query["state"]) != 1 ||
			!githubAppReturnCode.MatchString(query.Get("code")) || !githubAppReturnState.MatchString(query.Get("state")) {
			WriteProblem(w, RequestID(r.Context()), Problem{Status: 400, Code: "github_app.invalid_return", Title: "Return to GitHub App settings to start setup"})
			return
		}
		fragment := url.Values{"github-code": {query.Get("code")}, "github-state": {query.Get("state")}}
		w.Header().Set("Location", "/settings/?section=github-app#"+fragment.Encode())
		w.WriteHeader(http.StatusSeeOther)
	})
}
