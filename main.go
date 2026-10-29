package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

type Server struct {
	store        *Store
	runner       Runner
	auth         *Auth
	mdnsDefaults MDNSSettings
	mutations    sync.Mutex
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
		decodeErr := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in)
		token, ok := s.auth.Authenticate(in.Password)
		if decodeErr != nil || !ok {
			time.Sleep(500 * time.Millisecond)
			fail(w, 401, errString("invalid password"))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode, MaxAge: int(s.auth.ttl.Seconds())})
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", HttpOnly: true, Secure: secure(r), MaxAge: -1})
		writeJSON(w, 200, map[string]bool{"ok": true})
	})

	api := http.NewServeMux()
	api.HandleFunc("POST /api/password", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			CurrentPassword string `json:"currentPassword"`
			NewPassword     string `json:"newPassword"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
			fail(w, 400, errString("bad request"))
			return
		}
		if err := s.auth.ChangePassword(in.CurrentPassword, in.NewPassword); err != nil {
			code := 500
			if errors.Is(err, errPasswordLength) {
				code = 400
			}
			if errors.Is(err, errCurrentPassword) {
				code = 403
			}
			fail(w, code, err)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	api.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"mdns": s.store.MDNS(s.mdnsDefaults), "systemd": s.runner.Systemd})
	})
	api.HandleFunc("PUT /api/settings/mdns", func(w http.ResponseWriter, r *http.Request) {
		var in MDNSSettings
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
			fail(w, 400, errString("bad request"))
			return
		}
		if err := s.store.SetMDNS(in); err != nil {
			fail(w, 400, err)
			return
		}
		if err := s.runner.MDNSFirewall(s.store.MDNS(s.mdnsDefaults)); err != nil {
			fail(w, 500, fmt.Errorf("mDNS settings saved, but firewall update failed: %w", err))
			return
		}
		writeJSON(w, 200, s.store.MDNS(s.mdnsDefaults))
	})
	api.HandleFunc("GET /api/interfaces", func(w http.ResponseWriter, r *http.Request) {
		ifcs, err := localInterfaces()
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, ifcs)
	})
	api.HandleFunc("GET /api/services", func(w http.ResponseWriter, r *http.Request) {
		type view struct {
			Service
			PrivateKey string `json:"privateKey,omitempty"`
			Active     bool   `json:"active"`
			Enabled    bool   `json:"enabled"`
			Users      int    `json:"users"`
		}
		out := []view{}
		for _, v := range s.store.Services() {
			out = append(out, view{Service: v, Active: s.runner.Active(v.Name), Enabled: s.runner.Enabled(v.Name), Users: len(s.store.Users(v.Name))})
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
		if cfg := s.store.MDNS(s.mdnsDefaults); cfg.Enabled {
			if err := s.runner.MDNSFirewall(cfg); err != nil {
				fail(w, 500, fmt.Errorf("interface saved, but mDNS firewall update failed: %w", err))
				return
			}
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
		case "start", "restart":
			if err = s.runner.Write(v, s.store.Users(name)); err == nil {
				if action == "restart" {
					err = s.runner.Restart(name)
				} else {
					err = s.runner.Start(name)
				}
			}
		case "stop":
			err = s.runner.Stop(name)
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
		if err := s.runner.Stop(name); err != nil {
			fail(w, 500, err)
			return
		}
		if err := os.Remove(s.runner.confPath(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			fail(w, 500, err)
			return
		}
		oldMDNS := s.store.MDNS(s.mdnsDefaults)
		if err := s.store.DeleteService(name); err != nil {
			fail(w, 500, err)
			return
		}
		if oldMDNS.Enabled || s.store.MDNS(s.mdnsDefaults).Enabled {
			if err := s.runner.MDNSFirewall(s.store.MDNS(s.mdnsDefaults)); err != nil {
				fail(w, 500, fmt.Errorf("service deleted, but mDNS firewall update failed: %w", err))
				return
			}
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
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			s.mutations.Lock()
			defer s.mutations.Unlock()
		}
		s.auth.Middleware(api).ServeHTTP(w, r)
	}))
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
	if len(os.Args) > 1 && os.Args[1] == "mdns-firewall" {
		if err := runMDNSFirewallCommand(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	pw := os.Getenv("VIBEGUARD_ADMIN_PASSWORD")
	st, err := NewStore(env("VIBEGUARD_DATA", "/var/lib/vibeguard/data.json"))
	if err != nil {
		log.Fatal(err)
	}
	auth, err := NewPersistentAuth(pw, env("VIBEGUARD_PASSWORD_FILE", filepath.Join(filepath.Dir(st.path), "admin-password.json")))
	if err != nil {
		log.Fatal(err)
	}
	runner, err := newRunner(os.Getenv("VIBEGUARD_WG_DIR"), env("VIBEGUARD_SYSTEMD", "auto"))
	if err != nil {
		log.Fatal(err)
	}
	if runner.Dir == "" {
		runner.Dir = "/etc/wireguard"
		if runner.Systemd {
			runner.Dir = "/var/lib/vibeguard/wireguard"
		}
	}
	if runner.Systemd && filepath.Clean(runner.Dir) != "/var/lib/vibeguard/wireguard" {
		log.Fatal("systemd mode requires VIBEGUARD_WG_DIR=/var/lib/vibeguard/wireguard (the installed wg-quick helper path)")
	}
	reflector := NewReflector(os.Getenv("VIBEGUARD_MDNS_UPSTREAM"), st)
	srv := &Server{store: st, runner: runner, auth: auth, mdnsDefaults: reflector.defaults}
	for _, v := range st.Services() {
		if err := srv.runner.Write(v, st.Users(v.Name)); err != nil {
			log.Printf("write %s: %v", v.Name, err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if !runner.Systemd {
		if err := runner.MDNSFirewall(st.MDNS(reflector.defaults)); err != nil {
			log.Printf("mDNS firewall startup: %v", err)
		}
	}
	go func() {
		for ctx.Err() == nil {
			if err := reflector.RunContext(ctx); err != nil {
				log.Printf("mdns reflector: %v", err)
			}
			time.Sleep(5 * time.Second)
		}
	}()
	addr := env("VIBEGUARD_LISTEN", ":8080")
	log.Printf("vibeguard listening on %s", addr)
	httpServer := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	failed := make(chan error, 1)
	go func() { failed <- httpServer.ListenAndServe() }()
	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-failed:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdown); err != nil {
		log.Printf("HTTP shutdown: %v", err)
	}
	if !runner.Systemd {
		if err := runner.MDNSFirewall(MDNSSettings{}); err != nil {
			log.Printf("mDNS firewall shutdown: %v", err)
		}
	}
	if serveErr != nil {
		log.Fatal(serveErr)
	}
}
