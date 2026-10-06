package daemon

import (
	"net/http"
)

// CORS lets the desktop webview (Wails asset origin) and any browser page
// call the localhost daemon API directly. The bearer token stays mandatory,
// so reflecting the Origin is safe: the server only binds loopback, and a
// caller without the token still gets 401. Preflights (OPTIONS) are answered
// here, before RequireAuth, because they never carry credentials.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")
			// PATCH is here because changing what a paired device may do is a
			// PATCH; without it the browser UI cannot change a tier at all.
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
