package web

import (
	"strings"
	"testing"

	"trafficmonitor/internal/snapshot"
)

const testGUIToken = "0123456789abcdef0123"

func guiServer() *Server {
	return New(nil, snapshot.New(nil, testState()), Options{UI: true, User: "admin", Pass: "pw", GUIToken: testGUIToken})
}

func TestGUIProxyAuth(t *testing.T) {
	h := guiServer().Handler()
	if c := get(t, h, "GET", "/api/status").Code; c != 401 {
		t.Errorf("no credentials: got %d want 401", c)
	}
	if c := get(t, h, "GET", "/api/status", guiTokenHeader, "wrong-token-wrong-token").Code; c != 401 {
		t.Errorf("wrong gui token: got %d want 401", c)
	}
	rec := get(t, h, "GET", "/api/status", guiTokenHeader, testGUIToken)
	if rec.Code != 200 {
		t.Fatalf("gui token: got %d want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("gui X-Frame-Options = %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'self'") {
		t.Errorf("gui CSP = %q", got)
	}
	// changes still need the dashboard's CSRF header
	if c := get(t, h, "POST", "/api/hosts/rename", guiTokenHeader, testGUIToken).Code; c != 403 {
		t.Errorf("gui POST without CSRF header: got %d want 403", c)
	}

	// without a configured token the header means nothing
	plain := New(nil, snapshot.New(nil, testState()), Options{UI: true, User: "admin", Pass: "pw"}).Handler()
	rec = get(t, plain, "GET", "/api/status", guiTokenHeader, "")
	if rec.Code != 401 || rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("no gui token configured: got %d, X-Frame-Options %q", rec.Code, rec.Header().Get("X-Frame-Options"))
	}
}

func TestGUIRewrite(t *testing.T) {
	h := guiServer().Handler()
	const base = "/traffic_monitor.php?p="
	hdr := []string{guiTokenHeader, testGUIToken, guiBaseHeader, base, guiCSRFHeader, "abc123"}

	page := get(t, h, "GET", "/", hdr...).Body.String()
	for _, want := range []string{
		`href="` + base + `style.css"`,
		`src="` + base + `app.js"`,
		`<meta name="tm-base" content="` + base + `">`,
		`<meta name="tm-csrf" content="abc123">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("index.html missing %s", want)
		}
	}
	js := get(t, h, "GET", "/app.js", hdr...).Body.String()
	if !strings.Contains(js, `from "`+base+`embed.js"`) || strings.Contains(js, `from "./`) {
		t.Errorf("app.js imports not rewritten:\n%s", js[:200])
	}
	if css := get(t, h, "GET", "/style.css", hdr...); css.Code != 200 {
		t.Errorf("style.css: %d", css.Code)
	}

	// a direct visit (basic auth) is served unchanged
	direct := get(t, h, "GET", "/", "Authorization", "Basic YWRtaW46cHc=").Body.String()
	if !strings.Contains(direct, `href="style.css"`) || !strings.Contains(direct, `<meta name="tm-base" content="">`) {
		t.Error("direct visit should not be rewritten")
	}
	// unsafe values are ignored rather than written into the page
	bad := get(t, h, "GET", "/", guiTokenHeader, testGUIToken, guiBaseHeader, `/x"><script>`).Body.String()
	if strings.Contains(bad, "<script>\"") || !strings.Contains(bad, `href="style.css"`) {
		t.Error("unsafe base should be ignored")
	}
}
