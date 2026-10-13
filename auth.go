package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const cookieName = "vibeguard_session"

// Auth implements single admin password authentication with signed session cookies.
type Auth struct {
	password string
	secret   []byte
	ttl      time.Duration
}

func NewAuth(password string) *Auth {
	secret := make([]byte, 32)
	rand.Read(secret)
	return &Auth{password: password, secret: secret, ttl: 24 * time.Hour}
}

func (a *Auth) sign(msg string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func (a *Auth) CheckPassword(p string) bool {
	x, y := sha256.Sum256([]byte(p)), sha256.Sum256([]byte(a.password))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

func (a *Auth) Token() string {
	exp := strconv.FormatInt(time.Now().Add(a.ttl).Unix(), 10)
	return exp + "." + a.sign(exp)
}

func (a *Auth) Valid(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	exp, sig, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(a.sign(exp))) {
		return false
	}
	t, err := strconv.ParseInt(exp, 10, 64)
	return err == nil && time.Now().Unix() < t
}

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Valid(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
