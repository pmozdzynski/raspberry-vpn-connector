package handlers

import (
	"crypto/rand"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gorilla/sessions"
)

var (
	store           *sessions.CookieStore
	defaultUsername = "admin"
	defaultPassword = "admin"
	credentialsMu   sync.RWMutex
)

const sessionName = "vpn-connector-session"

func init() {
	secretKey := loadOrCreateSessionSecret()

	store = sessions.NewCookieStore(secretKey)
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7,
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	}

	if u := os.Getenv("AUTH_USERNAME"); u != "" {
		defaultUsername = u
	}
	if p := os.Getenv("AUTH_PASSWORD"); p != "" {
		defaultPassword = p
	}
}

func loadOrCreateSessionSecret() []byte {
	if envSecret := os.Getenv("SESSION_SECRET"); envSecret != "" {
		return []byte(envSecret)
	}

	const secretFile = configDir + "/session.key"
	if data, err := os.ReadFile(secretFile); err == nil && len(data) >= 32 {
		return data
	}

	secretKey := make([]byte, 32)
	if _, err := rand.Read(secretKey); err != nil {
		log.Fatal("Failed to generate session secret:", err)
	}
	if err := os.MkdirAll(configDir, 0750); err == nil {
		if err := os.WriteFile(secretFile, secretKey, 0600); err != nil {
			log.Printf("Warning: could not persist session secret: %v", err)
		}
	}
	return secretKey
}

func loadCredentialsFromConfig() {
	cfg := GetRouterConfig()
	credentialsMu.Lock()
	defer credentialsMu.Unlock()
	if cfg.AdminUsername != "" {
		defaultUsername = strings.TrimSpace(cfg.AdminUsername)
	}
	if cfg.AdminPassword != "" {
		defaultPassword = cfg.AdminPassword
	}
}

func ReloadAuthCredentials() {
	loadCredentialsFromConfig()
}

func SetRuntimeCredentials(username, password string) {
	credentialsMu.Lock()
	defer credentialsMu.Unlock()
	if username != "" {
		defaultUsername = username
	}
	if password != "" {
		defaultPassword = password
	}
}

func getCredentials() (string, string) {
	credentialsMu.RLock()
	defer credentialsMu.RUnlock()
	return defaultUsername, defaultPassword
}

func LoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		if !IsConfigured() {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		http.ServeFile(w, r, "./templates/login.html")
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Error parsing form", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	loadCredentialsFromConfig()

	expectedUser, expectedPass := getCredentials()
	if username != expectedUser || password != expectedPass {
		http.Redirect(w, r, "/login?error=unauthorized", http.StatusSeeOther)
		return
	}

	session, err := store.Get(r, sessionName)
	if err != nil {
		// Stale cookie from before a service restart; replace on successful login.
		log.Printf("Replacing invalid session cookie: %v", err)
		session, _ = store.New(r, sessionName)
	}
	session.Values["authenticated"] = true
	session.Values["username"] = username
	if err := session.Save(r, w); err != nil {
		http.Error(w, "Error saving session", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func LogoutHandler(w http.ResponseWriter, r *http.Request) {
	session, err := store.Get(r, sessionName)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	session.Values["authenticated"] = false
	session.Options.MaxAge = -1
	session.Save(r, w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !IsConfigured() {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		if r.URL.Path == "/login" || r.URL.Path == "/logout" {
			next(w, r)
			return
		}
		for _, p := range []string{"/styles.css", "/app.js"} {
			if r.URL.Path == p {
				next(w, r)
				return
			}
		}

		session, err := store.Get(r, sessionName)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		auth, ok := session.Values["authenticated"].(bool)
		if !ok || !auth {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}
