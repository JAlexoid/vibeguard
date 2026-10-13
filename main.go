package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

//go:embed web
var webFS embed.FS

type Server struct {
	store  *Store
	runner Runner
	auth   *Auth
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServerFS(sub))

	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Password string }
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil || !s.auth.CheckPassword(in.Password) {
			time.Sleep(500 * time.Millisecond)
			fail(w, 401, errString("invalid password"))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.auth.Token(), Path: "/", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode, MaxAge: int(s.auth.ttl.Seconds())})
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", HttpOnly: true, Secure: secure(r), MaxAge: -1})
		writeJSON(w, 200, map[string]bool{"ok": true})
	})

	api := http.NewServeMux()
	api.HandleFunc("GET /api/services", func(w http.ResponseWriter, r *http.Request) {
		type view struct {
			Service
			PrivateKey string `json:"privateKey,omitempty"`
			Active     bool   `json:"active"`
			Users      int    `json:"users"`
		}
		out := []view{}
		for _, v := range s.store.Services() {
			out = append(out, view{Service: v, Active: s.runner.Active(v.Name), Users: len(s.store.Users(v.Name))})
		}
		writeJSON(w, 200, out)
	})
	api.HandleFunc("POST /api/services", func(w http.ResponseWriter, r *http.Request) {
		var in Service
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&in) != nil {
			fail(w, 400, errString("bad request"))
			return
		}
		v, err := s.store.AddService(in)
		if err != nil {
			fail(w, 400, err)
			return
		}
		if err := s.runner.Write(v, nil); err != nil {
			fail(w, 500, err)
			return
		}
		v.PrivateKey = ""
		writeJSON(w, 201, v)
	})
	api.HandleFunc("POST /api/services/{name}/{action}", func(w http.ResponseWriter, r *http.Request) {
		name, action := r.PathValue("name"), r.PathValue("action")
		v, ok := s.store.Service(name)
		if !ok {
			fail(w, 404, errString("service not found"))
			return
		}
		var err error
		switch action {
		case "start":
			if err = s.runner.Write(v, s.store.Users(name)); err == nil && !s.runner.Active(name) {
				err = s.runner.Start(name)
			}
		case "stop":
			if s.runner.Active(name) {
				err = s.runner.Stop(name)
			}
		default:
			fail(w, 404, errString("unknown action"))
			return
		}
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	api.HandleFunc("DELETE /api/services/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if _, ok := s.store.Service(name); !ok {
			fail(w, 404, errString("service not found"))
			return
		}
		if s.runner.Active(name) {
			if err := s.runner.Stop(name); err != nil {
				fail(w, 500, err)
				return
			}
		}
		os.Remove(s.runner.confPath(name))
		if err := s.store.DeleteService(name); err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	api.HandleFunc("GET /api/users", func(w http.ResponseWriter, r *http.Request) {
		users := s.store.Users(r.URL.Query().Get("service"))
		for i := range users {
			users[i].PrivateKey, users[i].PresharedKey = "", ""
		}
		writeJSON(w, 200, users)
	})
	api.HandleFunc("POST /api/users", func(w http.ResponseWriter, r *http.Request) {
		var in User
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&in) != nil {
			fail(w, 400, errString("bad request"))
			return
		}
		u, err := s.store.AddUser(in.Service, in.Name, in.AllowedIPs)
		if err != nil {
			fail(w, 400, err)
			return
		}
		if err := s.reapply(u.Service); err != nil {
			fail(w, 500, err)
			return
		}
		u.PrivateKey, u.PresharedKey = "", ""
		writeJSON(w, 201, u)
	})
	api.HandleFunc("DELETE /api/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		u, err := s.store.DeleteUser(r.PathValue("id"))
		if err != nil {
			fail(w, 404, err)
			return
		}
		if err := s.reapply(u.Service); err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	clientCfg := func(w http.ResponseWriter, r *http.Request) (string, User, bool) {
		u, ok := s.store.User(r.PathValue("id"))
		if !ok {
			fail(w, 404, errString("user not found"))
			return "", u, false
		}
		svc, _ := s.store.Service(u.Service)
		if svc.Endpoint == "" {
			fail(w, 400, errString("service has no endpoint configured"))
			return "", u, false
		}
		return ClientConfig(svc, u), u, true
	}
	api.HandleFunc("GET /api/users/{id}/config", func(w http.ResponseWriter, r *http.Request) {
		if cfg, u, ok := clientCfg(w, r); ok {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(u.Service+"-"+u.ID+".conf"))
			w.Write([]byte(cfg))
		}
	})
	api.HandleFunc("GET /api/users/{id}/qr.png", func(w http.ResponseWriter, r *http.Request) {
		if cfg, _, ok := clientCfg(w, r); ok {
			png, err := QRPNG(cfg)
			if err != nil {
				fail(w, 500, err)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(png)
		}
	})
	mux.Handle("/api/", s.auth.Middleware(api))
	return mux
}

// reapply rewrites the service config and syncs the running interface.
func (s *Server) reapply(name string) error {
	svc, ok := s.store.Service(name)
	if !ok {
		return nil
	}
	if err := s.runner.Write(svc, s.store.Users(name)); err != nil {
		return err
	}
	return s.runner.Sync(name)
}

func secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

type errString string

func (e errString) Error() string { return string(e) }

func main() {
	pw := os.Getenv("VIBEGUARD_ADMIN_PASSWORD")
	if pw == "" {
		log.Fatal("VIBEGUARD_ADMIN_PASSWORD must be set")
	}
	st, err := NewStore(env("VIBEGUARD_DATA", "/var/lib/vibeguard/data.json"))
	if err != nil {
		log.Fatal(err)
	}
	srv := &Server{store: st, runner: Runner{Dir: env("VIBEGUARD_WG_DIR", "/etc/wireguard")}, auth: NewAuth(pw)}
	for _, v := range st.Services() {
		if err := srv.runner.Write(v, st.Users(v.Name)); err != nil {
			log.Printf("write %s: %v", v.Name, err)
		}
	}
	if up := os.Getenv("VIBEGUARD_MDNS_UPSTREAM"); up != "" {
		go func() { log.Printf("mdns reflector stopped: %v", NewReflector(up, st).Run()) }()
	}
	addr := env("VIBEGUARD_LISTEN", ":8080")
	log.Printf("vibeguard listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}
