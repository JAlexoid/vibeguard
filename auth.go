package main

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const cookieName = "vibeguard_session"
const passwordIterations = 210000

var errCurrentPassword = errors.New("current password is incorrect")
var errPasswordLength = errors.New("new password must contain at least 12 characters and at most 1024 bytes")

type passwordRecord struct {
	Salt       []byte `json:"salt"`
	Hash       []byte `json:"hash"`
	Iterations int    `json:"iterations"`
}

// Auth holds one password. A changed password overrides the bootstrap environment
// password on subsequent starts, and rotating the signing key expires all sessions.
type Auth struct {
	mu     sync.RWMutex
	record passwordRecord
	path   string
	secret []byte
	ttl    time.Duration
}

func hashPassword(password string, salt []byte, iterations int) []byte {
	key, _ := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	return key
}

func newPasswordRecord(password string) passwordRecord {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	return passwordRecord{Salt: salt, Hash: hashPassword(password, salt, passwordIterations), Iterations: passwordIterations}
}

func NewAuth(password string) *Auth {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic(err)
	}
	return &Auth{record: newPasswordRecord(password), secret: secret, ttl: 24 * time.Hour}
}

func NewPersistentAuth(password, path string) (*Auth, error) {
	a := NewAuth(password)
	a.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if password == "" {
			return nil, errors.New("VIBEGUARD_ADMIN_PASSWORD must be set for the first start")
		}
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &a.record); err != nil {
		return nil, err
	}
	if len(a.record.Salt) != 32 || len(a.record.Hash) != 32 || a.record.Iterations < 100000 || a.record.Iterations > 1000000 {
		return nil, errors.New("invalid saved password record")
	}
	return a, nil
}

func (a *Auth) checkPassword(p string) bool {
	return subtle.ConstantTimeCompare(hashPassword(p, a.record.Salt, a.record.Iterations), a.record.Hash) == 1
}

func (a *Auth) CheckPassword(p string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.checkPassword(p)
}

// Authenticate checks the password and signs under one lock so a password
// change cannot grant a fresh session to an already-checked old password.
func (a *Auth) Authenticate(password string) (string, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.checkPassword(password) {
		return "", false
	}
	exp := strconv.FormatInt(time.Now().Add(a.ttl).Unix(), 10)
	return exp + "." + a.sign(exp), true
}

func (a *Auth) ChangePassword(current, password string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.checkPassword(current) {
		return errCurrentPassword
	}
	if utf8.RuneCountInString(password) < 12 || len(password) > 1024 {
		return errPasswordLength
	}
	record := newPasswordRecord(password)
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	if a.path != "" {
		b, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := atomicWrite(a.path, b); err != nil {
			return err
		}
	}
	a.record, a.secret = record, secret
	return nil
}

// sign must be called with mu held.
func (a *Auth) sign(msg string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func (a *Auth) Token() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	exp := strconv.FormatInt(time.Now().Add(a.ttl).Unix(), 10)
	return exp + "." + a.sign(exp)
}

func (a *Auth) Valid(r *http.Request) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
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
		w.Header().Set("Cache-Control", "no-store")
		if !a.Valid(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
