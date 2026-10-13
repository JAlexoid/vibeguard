package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func testServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	st, err := NewStore(filepath.Join(t.TempDir(), "d.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, runner: Runner{Dir: t.TempDir()}, auth: NewAuth("secret")}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	jar := &cookieJar{}
	return ts, &http.Client{Jar: jar}
}

type cookieJar struct{ c []*http.Cookie }

func (j *cookieJar) SetCookies(_ *url.URL, cs []*http.Cookie) { j.c = cs }
func (j *cookieJar) Cookies(*url.URL) []*http.Cookie          { return j.c }

func do(t *testing.T, c *http.Client, method, url, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, bytes.NewBufferString(body))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAuthRequired(t *testing.T) {
	ts, c := testServer(t)
	if r := do(t, c, "GET", ts.URL+"/api/services", ""); r.StatusCode != 401 {
		t.Fatalf("want 401, got %d", r.StatusCode)
	}
	if r := do(t, c, "POST", ts.URL+"/api/login", `{"password":"wrong"}`); r.StatusCode != 401 {
		t.Fatalf("want 401, got %d", r.StatusCode)
	}
	if r := do(t, c, "POST", ts.URL+"/api/login", `{"password":"secret"}`); r.StatusCode != 200 {
		t.Fatalf("login failed: %d", r.StatusCode)
	}
	if r := do(t, c, "GET", ts.URL+"/api/services", ""); r.StatusCode != 200 {
		t.Fatalf("want 200, got %d", r.StatusCode)
	}
}

func TestServiceUserQR(t *testing.T) {
	ts, c := testServer(t)
	do(t, c, "POST", ts.URL+"/api/login", `{"password":"secret"}`)
	r := do(t, c, "POST", ts.URL+"/api/services", `{"name":"wg0","address":"10.8.0.1/24","listenPort":51820,"endpoint":"vpn.example.com:51820"}`)
	if r.StatusCode != 201 {
		t.Fatalf("add service: %d", r.StatusCode)
	}
	r = do(t, c, "POST", ts.URL+"/api/users", `{"service":"wg0","name":"alice"}`)
	if r.StatusCode != 201 {
		t.Fatalf("add user: %d", r.StatusCode)
	}
	r = do(t, c, "GET", ts.URL+"/api/users", "")
	var buf bytes.Buffer
	buf.ReadFrom(r.Body)
	id := between(buf.String(), `"id":"`, `"`)
	if !strings.Contains(buf.String(), `"privateKey":""`) {
		t.Fatal("user list leaks private key")
	}
	r = do(t, c, "GET", ts.URL+"/api/users/"+id+"/qr.png", "")
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("qr: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	buf.Reset()
	buf.ReadFrom(do(t, c, "GET", ts.URL+"/api/users/"+id+"/config", "").Body)
	if !strings.Contains(buf.String(), "Endpoint = vpn.example.com:51820") || !strings.Contains(buf.String(), "Address = 10.8.0.2/32") {
		t.Fatalf("bad client config:\n%s", buf.String())
	}
}

func between(s, a, b string) string {
	i := strings.Index(s, a) + len(a)
	return s[i : i+strings.Index(s[i:], b)]
}

func TestAddressAllocation(t *testing.T) {
	st, _ := NewStore(filepath.Join(t.TempDir(), "d.json"))
	if _, err := st.AddService(Service{Name: "wg0", Address: "10.8.0.1/30", ListenPort: 1}); err != nil {
		t.Fatal(err)
	}
	u, err := st.AddUser("wg0", "a", "")
	if err != nil || u.Address != "10.8.0.2/32" {
		t.Fatalf("got %v %v", u.Address, err)
	}
	if _, err := st.AddUser("wg0", "b", ""); err == nil {
		t.Fatal("expected subnet exhaustion")
	}
}
