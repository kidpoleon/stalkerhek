package webui

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// User represents a WebUI user
type User struct {
	Username              string    `json:"username"`
	PasswordHash          string    `json:"password_hash"`
	SecurityQuestion      string    `json:"security_question,omitempty"`
	SecurityAnswerHash    string    `json:"security_answer_hash,omitempty"`
	AllowCustomQuestion   bool      `json:"allow_custom_question,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
	LastLogin             time.Time `json:"last_login,omitempty"`
}

// Session represents an active user session
type Session struct {
	Token     string    `json:"token"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// passwordResetToken represents a token with expiration
type passwordResetToken struct {
	Username  string
	ExpiresAt time.Time
}

var (
	// users holds registered users (username -> User)
	users   = make(map[string]User)
	usersMu sync.RWMutex

	// sessions holds active sessions (token -> Session)
	sessions   = make(map[string]Session)
	sessionsMu sync.RWMutex

	// passwordResetTokens holds temporary tokens for password reset (token -> passwordResetToken)
	passwordResetTokens   = make(map[string]passwordResetToken)
	passwordResetTokensMu sync.RWMutex

	// sessionCookieName is the name of the session cookie
	sessionCookieName = "stalkerhek_session"

	// authEnabled determines if authentication is required
	authEnabled = true

	// trustedSubnets are networks that can bypass authentication
	trustedSubnets []*net.IPNet

	// authFilePath is where users are persisted
	authFilePath string

	// allowRegistration determines if new users can register
	allowRegistration = false
)

// Security questions preset - following OWASP guidelines
var securityQuestions = []string{
	"What was the name of your first stuffed toy?",
	"What was the name of the first school you remember attending?",
	"What is the name of a college you applied to but did not attend?",
	"What was your driving instructor's first name?",
	"What is the name of the hospital where you were born?",
	"What was the name of your first manager at your first job?",
	"What was the name of your first best friend?",
	"What is your custom security question",
}

func init() {
	// Check if auth is disabled via environment
	if os.Getenv("STALKERHEK_DISABLE_AUTH") == "1" {
		authEnabled = false
	}

	// Check if registration is allowed
	if os.Getenv("STALKERHEK_ALLOW_REGISTER") == "1" {
		allowRegistration = true
	}

	// Setup auth file path
	if p := os.Getenv("STALKERHEK_AUTH_FILE"); p != "" {
		authFilePath = p
	} else if p := os.Getenv("STALKERHEK_PROFILES_FILE"); p != "" {
		authFilePath = filepath.Join(filepath.Dir(p), "auth.json")
	} else {
		authFilePath = "auth.json"
	}

	// Load trusted subnets
	loadTrustedSubnets()

	// Load existing users
	loadUsers()
}

// loadTrustedSubnets loads trusted subnets from environment
func loadTrustedSubnets() {
	subnets := os.Getenv("STALKERHEK_TRUSTED_SUBNETS")
	if subnets == "" {
		// Default: trust local networks
		subnets = "127.0.0.0/8,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,::1/128,fc00::/7"
	}
	
	trustedSubnets = nil
	for _, s := range strings.Split(subnets, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(s)
		if err == nil {
			trustedSubnets = append(trustedSubnets, ipnet)
		}
	}
}

// saveTrustedSubnets saves trusted subnets to environment (for persistence)
func saveTrustedSubnets() {
	var subnets []string
	for _, subnet := range trustedSubnets {
		subnets = append(subnets, subnet.String())
	}
	os.Setenv("STALKERHEK_TRUSTED_SUBNETS", strings.Join(subnets, ","))
}

// AuthMiddleware wraps handlers with authentication check
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth if disabled
		if !authEnabled {
			next.ServeHTTP(w, r)
			return
		}

		// Skip auth for public endpoints
		publicPaths := []string{"/login", "/register", "/account", "/forgot-password", "/reset-password",
			"/api/login", "/api/register", "/api/forgot-password", "/api/reset-password",
			"/api/auth/status", "/health", "/healthz", "/api/trusted-subnets", "/assets/banner.png"}
		if strings.HasPrefix(r.URL.Path, "/epg/") || strings.HasPrefix(r.URL.Path, "/vod/") {
			next.ServeHTTP(w, r)
			return
		}
		for _, path := range publicPaths {
			if r.URL.Path == path {
				next.ServeHTTP(w, r)
				return
			}
		}

		// Check if request is from trusted subnet - only bypass for non-dashboard API endpoints
		if isTrustedIP(r) {
			// Trusted IPs can access API endpoints without auth, but NOT the dashboard
			if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}
		}

		// Check if any users exist - if not, allow access for initial setup
		if !hasUsers() {
			if r.URL.Path == "/" || r.URL.Path == "/dashboard" ||
			   strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/static/") {
				next.ServeHTTP(w, r)
				return
			}
			http.Redirect(w, r, "/register", http.StatusSeeOther)
			return
		}

		// Validate session
		if !isAuthenticated(r) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isTrustedIP checks if the request IP is in a trusted subnet
func isTrustedIP(r *http.Request) bool {
	if len(trustedSubnets) == 0 {
		return false
	}

	ipStr := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ipStr = strings.Split(fwd, ",")[0]
	}
	ipStr = strings.TrimSpace(ipStr)
	if host, _, err := net.SplitHostPort(ipStr); err == nil {
		ipStr = host
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	for _, subnet := range trustedSubnets {
		if subnet.Contains(ip) {
			return true
		}
	}
	return false
}

// isAuthenticated checks if the request has a valid session
func isAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}

	sessionsMu.RLock()
	session, exists := sessions[cookie.Value]
	sessionsMu.RUnlock()

	if !exists {
		return false
	}

	if time.Now().After(session.ExpiresAt) {
		sessionsMu.Lock()
		delete(sessions, cookie.Value)
		sessionsMu.Unlock()
		return false
	}

	return true
}

// hasUsers checks if any users are registered
func hasUsers() bool {
	usersMu.RLock()
	defer usersMu.RUnlock()
	return len(users) > 0
}

// getSessionUsername returns the username from the session
func getSessionUsername(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}

	sessionsMu.RLock()
	defer sessionsMu.RUnlock()

	if session, exists := sessions[cookie.Value]; exists {
		return session.Username
	}
	return ""
}

// RegisterAuthHandlers registers authentication endpoints
func RegisterAuthHandlers(mux *http.ServeMux) {
	// Login page
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !authEnabled || !hasUsers() {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}
		renderLoginPage(w, "")
	})

	// Register page
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !allowRegistration && hasUsers() {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		renderRegisterPage(w, "")
	})

	// Forgot password page
	mux.HandleFunc("/forgot-password", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !authEnabled || !hasUsers() {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}
		renderForgotPasswordPage(w, "", nil)
	})

	// Reset password page
	mux.HandleFunc("/reset-password", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token := r.URL.Query().Get("token")
		if token == "" || !isValidResetToken(token) {
			renderForgotPasswordPage(w, "Invalid or expired reset link", nil)
			return
		}
		renderResetPasswordPage(w, "", token)
	})

	// Account page
	mux.HandleFunc("/account", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		
		username := getSessionUsername(r)
		if !authEnabled {
			renderAccountPage(w, "", "", true)
			return
		}
		
		var user User
		if username != "" {
			usersMu.RLock()
			user = users[username]
			usersMu.RUnlock()
		}
		
		renderAccountPage(w, username, user.SecurityQuestion, username != "")
	})

	// API endpoints
	mux.HandleFunc("/api/login", handleLogin)
	mux.HandleFunc("/api/register", handleRegister)
	mux.HandleFunc("/api/logout", handleLogout)
	mux.HandleFunc("/api/forgot-password", handleForgotPassword)
	mux.HandleFunc("/api/reset-password", handleResetPassword)
	mux.HandleFunc("/api/change-password", handleChangePassword)
	mux.HandleFunc("/api/auth/status", handleAuthStatus)
	mux.HandleFunc("/api/trusted-subnets", handleTrustedSubnets)
	
	// User management API endpoints
	mux.HandleFunc("/api/users", handleUsersList)
	mux.HandleFunc("/api/users/", handleUserDetail) // /api/users/:username
}

func handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !authEnabled {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled":       false,
			"authenticated": true,
		})
		return
	}

	username := getSessionUsername(r)
	authenticated := username != ""

	json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled":            authEnabled,
		"authenticated":      authenticated,
		"username":           username,
		"has_users":          hasUsers(),
		"allow_registration": allowRegistration || !hasUsers(),
		"trusted_subnets":    getTrustedSubnetsForDisplay(),
	})
}

func handleTrustedSubnets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodGet {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled":      len(trustedSubnets) > 0,
			"subnets":      getTrustedSubnetsForDisplay(),
			"local_bypass": isTrustedIP(r),
		})
		return
	}

	// POST - update trusted subnets (requires auth)
	if authEnabled && !isAuthenticated(r) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
		return
	}

	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "bad request"})
		return
	}

	action := r.FormValue("action")
	if action == "disable" {
		trustedSubnets = nil
		saveTrustedSubnets()
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	// Re-enable with defaults
	loadTrustedSubnets()
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func getTrustedSubnetsForDisplay() []string {
	var result []string
	for _, subnet := range trustedSubnets {
		result = append(result, subnet.String())
	}
	return result
}

// handleUsersList handles GET /api/users - returns list of all users
func handleUsersList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check authentication
	if authEnabled && !isAuthenticated(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
		return
	}

	currentUser := getSessionUsername(r)

	usersMu.RLock()
	defer usersMu.RUnlock()

	var userList []map[string]interface{}
	for username, user := range users {
		// Don't expose sensitive fields like password hashes
		userInfo := map[string]interface{}{
			"username":          username,
			"created_at":        user.CreatedAt.Format(time.RFC3339),
			"last_login":        user.LastLogin.Format(time.RFC3339),
			"has_security_question": user.SecurityQuestion != "",
			"is_current_user":   username == currentUser,
		}
		userList = append(userList, userInfo)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"users": userList,
		"count": len(userList),
	})
}

// handleUserDetail handles GET, PUT, DELETE /api/users/:username
func handleUserDetail(w http.ResponseWriter, r *http.Request) {
	// Check authentication
	if authEnabled && !isAuthenticated(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
		return
	}

	// Extract username from URL path
	path := strings.TrimPrefix(r.URL.Path, "/api/users/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || parts[0] == "" {
		http.Error(w, "username required", http.StatusBadRequest)
		return
	}
	targetUsername := parts[0]
	currentUser := getSessionUsername(r)

	switch r.Method {
	case http.MethodGet:
		// Get user details
		usersMu.RLock()
		user, exists := users[targetUsername]
		usersMu.RUnlock()

		if !exists {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "user not found"})
			return
		}

		// Only return limited info
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"username":          targetUsername,
			"created_at":        user.CreatedAt.Format(time.RFC3339),
			"last_login":        user.LastLogin.Format(time.RFC3339),
			"security_question": user.SecurityQuestion,
			"is_current_user":   targetUsername == currentUser,
		})

	case http.MethodPut:
		// Update user - can only update own account unless admin logic added
		if targetUsername != currentUser {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "can only edit your own account"})
			return
		}

		if err := r.ParseForm(); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "bad request"})
			return
		}

		usersMu.Lock()
		user := users[targetUsername]

		// Update security question if provided
		securityQ := r.FormValue("security_question")
		securityA := strings.TrimSpace(r.FormValue("security_answer"))
		customQ := r.FormValue("custom_question")

		if securityQ != "" {
			if securityQ == "What is your custom security question" && customQ != "" {
				user.SecurityQuestion = customQ
				user.AllowCustomQuestion = true
			} else {
				user.SecurityQuestion = securityQ
				user.AllowCustomQuestion = false
			}

			// Hash the answer if provided
			if securityA != "" {
				hash, err := hashSecurityAnswer(securityA)
				if err != nil {
					usersMu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": "error hashing answer"})
					return
				}
				user.SecurityAnswerHash = hash
			}
		}

		users[targetUsername] = user
		usersMu.Unlock()
		saveUsers()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	case http.MethodDelete:
		// Delete user - prevent self-deletion
		if targetUsername == currentUser {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "cannot delete your own account while logged in"})
			return
		}

		usersMu.Lock()
		delete(users, targetUsername)
		usersMu.Unlock()
		saveUsers()

		// Invalidate any sessions for this user
		sessionsMu.Lock()
		for token, session := range sessions {
			if session.Username == targetUsername {
				delete(sessions, token)
			}
		}
		sessionsMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// hashSecurityAnswer hashes a security question answer using bcrypt with lower cost
func hashSecurityAnswer(answer string) (string, error) {
	// Use a lower cost for security answers since they're not as critical as passwords
	// but still need protection
	normalized := strings.ToLower(strings.TrimSpace(answer))
	hash, err := bcrypt.GenerateFromPassword([]byte(normalized), bcrypt.MinCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// checkSecurityAnswer verifies a security question answer against a hash
func checkSecurityAnswer(answer, hash string) bool {
	if hash == "" {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(answer))
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(normalized))
	return err == nil
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	usersMu.RLock()
	user, exists := users[username]
	usersMu.RUnlock()

	if !exists || !checkPassword(password, user.PasswordHash) {
		if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid credentials"})
			return
		}
		renderLoginPage(w, "Invalid username or password")
		return
	}

	session := createSession(username)

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
		Expires:  session.ExpiresAt,
	})

	usersMu.Lock()
	user.LastLogin = time.Now()
	users[username] = user
	usersMu.Unlock()
	saveUsers()

	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Allow registration if:
	// 1. Public registration is enabled, OR
	// 2. No users exist yet (first setup), OR
	// 3. Request is from an authenticated user (creating additional accounts)
	isAuthenticated := isAuthenticated(r)
	if !allowRegistration && hasUsers() && !isAuthenticated {
		http.Error(w, "registration disabled", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	passwordConfirm := r.FormValue("password_confirm")
	securityQ := r.FormValue("security_question")
	securityA := strings.TrimSpace(r.FormValue("security_answer"))
	customQ := r.FormValue("custom_question")

	if username == "" || password == "" {
		renderRegisterPage(w, "Username and password are required")
		return
	}

	if len(password) < 4 {
		renderRegisterPage(w, "Password must be at least 4 characters")
		return
	}

	if password != passwordConfirm {
		renderRegisterPage(w, "Passwords do not match")
		return
	}

	usersMu.RLock()
	_, exists := users[username]
	usersMu.RUnlock()

	if exists {
		renderRegisterPage(w, "Username already exists")
		return
	}

	hash, err := hashPassword(password)
	if err != nil {
		renderRegisterPage(w, "Error creating account")
		return
	}

	// Build user object
	user := User{
		Username:         username,
		PasswordHash:     hash,
		CreatedAt:        time.Now(),
	}

	// Handle security question
	if securityQ != "" {
		if securityQ == "What is your custom security question" && customQ != "" {
			user.SecurityQuestion = customQ
			user.AllowCustomQuestion = true
		} else {
			user.SecurityQuestion = securityQ
			user.AllowCustomQuestion = false
		}

		// Hash the security answer if provided
		if securityA != "" {
			answerHash, err := hashSecurityAnswer(securityA)
			if err != nil {
				renderRegisterPage(w, "Error processing security answer")
				return
			}
			user.SecurityAnswerHash = answerHash
		}
	}

	usersMu.Lock()
	users[username] = user
	usersMu.Unlock()

	if err := saveUsers(); err != nil {
		renderRegisterPage(w, "Error saving account")
		return
	}

	// Only auto-login the new user if this is a self-registration (not admin creating user)
	if !isAuthenticated {
		session := createSession(username)
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    session.Token,
			Path:     "/",
			HttpOnly: true,
			Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
			SameSite: http.SameSiteStrictMode,
			Expires:  session.ExpiresAt,
		})
	}

	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	// If admin created the user, redirect back to account page; if self-registration, go to dashboard
	if isAuthenticated {
		http.Redirect(w, r, "/account?created=success", http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	}
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		sessionsMu.Lock()
		delete(sessions, cookie.Value)
		sessionsMu.Unlock()
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	answer := strings.TrimSpace(r.FormValue("answer"))

	usersMu.RLock()
	user, exists := users[username]
	usersMu.RUnlock()

	if !exists || user.SecurityQuestion == "" {
		// Don't reveal if user exists
		renderForgotPasswordPage(w, "If this account exists and has a security question, you can reset your password.", nil)
		return
	}

	// If answer is provided, verify it
	if answer != "" {
		if !checkSecurityAnswer(answer, user.SecurityAnswerHash) {
			// Wrong answer - don't reveal this
			renderForgotPasswordPage(w, "If this account exists and has a security question, you can reset your password.", nil)
			return
		}

		// Answer is correct - generate reset token
		token := generateResetToken(username)

		if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"status": "verified",
				"token":  token,
			})
			return
		}

		// Redirect to reset password page with token
		http.Redirect(w, r, "/reset-password?token="+token, http.StatusSeeOther)
		return
	}

	// No answer provided - return security question for verification
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"has_question": "true",
			"question":     user.SecurityQuestion,
		})
		return
	}

	renderForgotPasswordPage(w, "", &user)
}

func handleResetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	token := r.FormValue("token")
	newPassword := r.FormValue("new_password")
	confirmPassword := r.FormValue("confirm_password")

	if !isValidResetToken(token) {
		renderResetPasswordPage(w, "Invalid or expired reset link", token)
		return
	}

	if len(newPassword) < 4 {
		renderResetPasswordPage(w, "Password must be at least 4 characters", token)
		return
	}

	if newPassword != confirmPassword {
		renderResetPasswordPage(w, "Passwords do not match", token)
		return
	}

	username := getUsernameFromResetToken(token)
	if username == "" {
		renderResetPasswordPage(w, "Invalid reset token", token)
		return
	}

	hash, err := hashPassword(newPassword)
	if err != nil {
		renderResetPasswordPage(w, "Error resetting password", token)
		return
	}

	usersMu.Lock()
	user := users[username]
	user.PasswordHash = hash
	users[username] = user
	usersMu.Unlock()

	saveUsers()
	invalidateResetToken(token)

	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	http.Redirect(w, r, "/login?reset=success", http.StatusSeeOther)
}

func handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if authEnabled && !isAuthenticated(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	username := getSessionUsername(r)
	currentPassword := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	confirmPassword := r.FormValue("confirm_password")

	usersMu.RLock()
	user, exists := users[username]
	usersMu.RUnlock()

	if !exists || !checkPassword(currentPassword, user.PasswordHash) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "current password incorrect"})
		return
	}

	if len(newPassword) < 4 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "password must be at least 4 characters"})
		return
	}

	if newPassword != confirmPassword {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "passwords do not match"})
		return
	}

	hash, err := hashPassword(newPassword)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "error changing password"})
		return
	}

	usersMu.Lock()
	user.PasswordHash = hash
	users[username] = user
	usersMu.Unlock()

	saveUsers()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// createSession creates a new session for a user
func createSession(username string) Session {
	token := generateToken()
	session := Session{
		Token:     token,
		Username:  username,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}

	sessionsMu.Lock()
	sessions[token] = session
	sessionsMu.Unlock()

	return session
}

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func checkPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

func loadUsers() {
	data, err := os.ReadFile(authFilePath)
	if err != nil {
		return
	}

	var loaded map[string]User
	if err := json.Unmarshal(data, &loaded); err != nil {
		return
	}

	usersMu.Lock()
	users = loaded
	usersMu.Unlock()
}

func saveUsers() error {
	usersMu.RLock()
	data, err := json.MarshalIndent(users, "", "  ")
	usersMu.RUnlock()

	if err != nil {
		return err
	}

	return os.WriteFile(authFilePath, data, 0600)
}

// Password reset token functions
func isValidResetToken(token string) bool {
	passwordResetTokensMu.Lock()
	defer passwordResetTokensMu.Unlock()
	
	t, exists := passwordResetTokens[token]
	if !exists {
		return false
	}
	
	// Check if token has expired
	if time.Now().After(t.ExpiresAt) {
		delete(passwordResetTokens, token)
		return false
	}
	
	return true
}

func getUsernameFromResetToken(token string) string {
	passwordResetTokensMu.Lock()
	defer passwordResetTokensMu.Unlock()
	
	t, exists := passwordResetTokens[token]
	if !exists {
		return ""
	}
	
	// Check if token has expired
	if time.Now().After(t.ExpiresAt) {
		delete(passwordResetTokens, token)
		return ""
	}
	
	return t.Username
}

func invalidateResetToken(token string) {
	passwordResetTokensMu.Lock()
	delete(passwordResetTokens, token)
	passwordResetTokensMu.Unlock()
}

// generateResetToken creates a new password reset token for a user (expires in 1 hour)
func generateResetToken(username string) string {
	token := generateToken()
	passwordResetTokensMu.Lock()
	passwordResetTokens[token] = passwordResetToken{
		Username:  username,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
	passwordResetTokensMu.Unlock()
	return token
}

// HTML rendering functions with dark green theme matching the main WebUI

const authCSS = `:root{--bg:#0a0f0a;--card:#0d1410;--card2:#111815;--border:#1f2e23;--text:#e0e6e0;--muted:#9aaa9a;--brand:#2d7a4e;--brand-hover:#3a8f5e;--brand-light:#5fb970;--ok:#3fb970;--warn:#d4a94a;--bad:#e85d4d;--danger-bg:rgba(232,93,77,.12);--focus-ring:rgba(45,122,78,.4)}
*{box-sizing:border-box}html,body{margin:0;padding:0;font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu,Helvetica,Arial,sans-serif;background:linear-gradient(180deg,#0d1410 0%,#0a0f0a 100%);color:var(--text);min-height:100dvh}
.wrap{max-width:480px;margin:0 auto;padding:clamp(20px,5vw,40px) clamp(16px,4vw,24px);min-height:100dvh;display:flex;align-items:center;justify-content:center}
.card{width:100%;background:linear-gradient(180deg,rgba(17,24,21,.98),rgba(13,20,16,.96));border:1px solid var(--border);border-radius:18px;padding:clamp(24px,5vw,32px);box-shadow:0 16px 48px rgba(0,0,0,.45)}
.logo{text-align:center;margin-bottom:24px}
.logo i{font-size:48px;color:var(--brand);filter:drop-shadow(0 0 12px rgba(45,122,78,.4))}
h1{margin:0 0 8px 0;font-size:clamp(22px,5vw,26px);font-weight:700;letter-spacing:-.3px;color:var(--text)}
.subtitle{color:var(--muted);font-size:14px;margin-bottom:24px;text-align:center}
.row{margin-bottom:18px}
.row:last-of-type{margin-bottom:0}
label{display:block;font-size:12px;font-weight:600;color:var(--muted);text-transform:uppercase;letter-spacing:.5px;margin-bottom:8px}
input,select{width:100%;padding:14px 16px;border-radius:12px;border:1px solid var(--border);background:#0f1612;color:var(--text);font-size:15px;outline:none;transition:border-color .2s,box-shadow .2s}
input:focus,select:focus{border-color:var(--brand);box-shadow:0 0 0 3px var(--focus-ring)}
input:focus-visible,select:focus-visible{outline:2px solid var(--brand);outline-offset:2px}
input::placeholder{color:var(--muted);opacity:.6}
input:disabled,select:disabled{opacity:.5;cursor:not-allowed;background:rgba(31,46,35,.5)}
.hint{font-size:12px;color:var(--muted);margin-top:6px;line-height:1.4}
.error{color:var(--bad);font-size:13px;margin:12px 0;padding:12px 14px;background:var(--danger-bg);border-radius:10px;border:1px solid rgba(232,93,77,.25);display:flex;align-items:center;gap:10px}
.success{color:var(--ok);font-size:13px;margin:12px 0;padding:12px 14px;background:rgba(63,185,112,.12);border-radius:10px;border:1px solid rgba(63,185,112,.25);display:flex;align-items:center;gap:10px}
.actions{display:flex;gap:12px;margin-top:28px;align-items:center}
.actions.center{justify-content:center}
.actions.between{justify-content:space-between}
button{cursor:pointer;border:none;border-radius:12px;padding:14px 24px;font-size:15px;font-weight:650;transition:all .15s;display:inline-flex;align-items:center;gap:10px;flex:1;justify-content:center;position:relative;overflow:hidden}
button:focus-visible{outline:2px solid var(--brand);outline-offset:2px}
button:active{transform:translateY(0) scale(.98)}
button:disabled{opacity:.5;cursor:not-allowed;transform:none}
button:disabled:hover{transform:none;box-shadow:none}
button.primary{background:var(--brand);color:#fff}
button.primary:hover:not(:disabled){background:var(--brand-hover);transform:translateY(-1px);box-shadow:0 8px 20px rgba(45,122,78,.3)}
button.primary:active:not(:disabled){background:#2a6b42;box-shadow:0 4px 12px rgba(45,122,78,.2)}
button.secondary{background:rgba(31,46,35,.6);color:var(--text);border:1px solid var(--border)}
button.secondary:hover:not(:disabled){background:rgba(45,122,78,.15);border-color:var(--brand)}
button.secondary:active:not(:disabled){background:rgba(45,122,78,.25)}
button.danger{background:var(--bad);color:#fff}
button.danger:hover:not(:disabled){background:#d64e4e;transform:translateY(-1px)}
button.danger:active:not(:disabled){background:#c44545}
button.plain{background:transparent;color:var(--muted);padding:8px 12px;flex:0}
button.plain:hover:not(:disabled){color:var(--text);background:rgba(255,255,255,.05)}
button.plain:active:not(:disabled){background:rgba(255,255,255,.1)}
.link{color:var(--brand);text-decoration:none;font-size:14px;font-weight:500;transition:color .15s;display:inline-flex;align-items:center;gap:8px;padding:4px 0;border-radius:4px}
.link:focus-visible{outline:2px solid var(--brand);outline-offset:2px}
.link:hover{color:var(--brand-hover);text-decoration:underline}
.userinfo{background:rgba(45,122,78,.1);border:1px solid rgba(45,122,78,.25);border-radius:12px;padding:16px;margin:20px 0}
.userinfo .label{font-size:11px;color:var(--muted);text-transform:uppercase;letter-spacing:.8px;margin-bottom:4px}
.userinfo .value{font-size:16px;font-weight:600;color:var(--text);display:flex;align-items:center;gap:10px}
.info-box{background:rgba(45,122,78,.08);border:1px solid rgba(45,122,78,.15);border-radius:10px;padding:14px;margin:16px 0;font-size:13px;color:var(--muted)}
.info-box i{color:var(--brand);margin-right:8px}
.section-title{font-size:13px;font-weight:700;color:var(--muted);text-transform:uppercase;letter-spacing:1px;margin:28px 0 16px 0;border-bottom:1px solid var(--border);padding-bottom:8px}
.toggle-row{display:flex;align-items:center;justify-content:space-between;padding:12px 0;border-bottom:1px solid rgba(31,46,35,.5)}
.toggle-row:last-child{border-bottom:none}
.toggle-label{font-size:14px;color:var(--text)}
.toggle-sublabel{font-size:12px;color:var(--muted);margin-top:2px}
.toggle{position:relative;display:inline-block;width:48px;height:26px}
.toggle input{opacity:0;width:0;height:0}
.slider{position:absolute;cursor:pointer;top:0;left:0;right:0;bottom:0;background:rgba(31,46,35,.8);border-radius:26px;transition:.3s;border:1px solid var(--border)}
.slider:before{position:absolute;content:"";height:18px;width:18px;left:3px;bottom:3px;background:var(--muted);border-radius:50%;transition:.3s}
input:checked+.slider{background:var(--brand);border-color:var(--brand)}
input:checked+.slider:before{background:#fff;transform:translateX(22px)}
.toggle input:focus-visible+.slider{outline:2px solid var(--brand);outline-offset:2px}
.tabs{display:flex;gap:8px;margin-bottom:24px;border-bottom:1px solid var(--border);padding-bottom:12px}
.tab{flex:1;text-align:center;padding:12px;border-radius:10px;font-size:14px;font-weight:600;color:var(--muted);cursor:pointer;transition:all .15s;border:none;background:transparent;position:relative}
.tab:focus-visible{outline:2px solid var(--brand);outline-offset:2px}
.tab.active{background:rgba(45,122,78,.15);color:var(--brand-light)}
.tab:hover:not(.active){color:var(--text);background:rgba(31,46,35,.4)}
.tab:active:not(.active){transform:translateY(1px)}
@media(max-width:400px){.actions{flex-direction:column}button{width:100%}.card{padding:20px}}
@media(min-width:768px){.wrap{max-width:520px}}
@keyframes fadeIn{from{opacity:0;transform:translateY(10px)}to{opacity:1;transform:translateY(0)}}
.card{animation:fadeIn .4s ease}
.skip-link{position:absolute;top:-40px;left:0;background:var(--brand);color:#fff;padding:8px 16px;text-decoration:none;border-radius:0 0 8px 0;z-index:1001;transition:top .3s}
.skip-link:focus{top:0}
.visually-hidden{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}`

func renderLoginPage(w http.ResponseWriter, errorMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	tpl := `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Login - Stalkerhek</title>
<style>` + authCSS + `</style>
<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.5.1/css/all.min.css">
</head>
<body>
<div class="wrap">
<div class="card">
<div class="logo"><i class="fa-solid fa-shield-halved"></i></div>
<h1 style="text-align:center">Welcome Back</h1>
<p class="subtitle">Sign in to access your Stalkerhek dashboard</p>
{{if .Error}}
<div class="error"><i class="fa-solid fa-circle-exclamation"></i> {{.Error}}</div>
{{end}}
<form method="post" action="/api/login">
<div class="row">
<label for="username">Username</label>
<input id="username" name="username" type="text" required autocomplete="username" placeholder="Enter your username">
</div>
<div class="row">
<label for="password">Password</label>
<input id="password" name="password" type="password" required autocomplete="current-password" placeholder="Enter your password">
</div>
<div class="actions between">
<a href="/dashboard" class="link"><i class="fa-solid fa-arrow-left"></i> Back</a>
<button type="submit" class="primary"><i class="fa-solid fa-right-to-bracket"></i> Sign In</button>
</div>
</form>
<div style="text-align:center;margin-top:20px">
<a href="/forgot-password" class="link" style="font-size:13px"><i class="fa-solid fa-key"></i> Forgot password?</a>
</div>
</div>
</div>
</body>
</html>`

	t := template.Must(template.New("login").Parse(tpl))
	t.Execute(w, struct{ Error string }{Error: errorMsg})
}

func renderRegisterPage(w http.ResponseWriter, errorMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	tpl := `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Create Account - Stalkerhek</title>
<style>` + authCSS + `
.custom-question-input{margin-top:10px;display:none}
.custom-question-input.visible{display:block}
</style>
<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.5.1/css/all.min.css">
</head>
<body>
<div class="wrap">
<div class="card">
<div class="logo"><i class="fa-solid fa-user-plus"></i></div>
<h1 style="text-align:center">Create Account</h1>
<p class="subtitle">Set up your admin account for Stalkerhek</p>
{{if .Error}}
<div class="error"><i class="fa-solid fa-circle-exclamation"></i> {{.Error}}</div>
{{end}}
<form method="post" action="/api/register">
<div class="row">
<label for="username">Username</label>
<input id="username" name="username" type="text" required autocomplete="username" placeholder="Choose a username">
<div class="hint">This will be your admin username</div>
</div>
<div class="row">
<label for="password">Password</label>
<input id="password" name="password" type="password" required autocomplete="new-password" placeholder="Create a password" minlength="4">
<div class="hint">Must be at least 4 characters</div>
</div>
<div class="row">
<label for="password_confirm">Confirm Password</label>
<input id="password_confirm" name="password_confirm" type="password" required autocomplete="new-password" placeholder="Confirm your password">
</div>
<div class="row">
<label for="security_question">Security Question (optional)</label>
<select id="security_question" name="security_question" onchange="toggleCustomQuestion(this)">
<option value="">-- Select a security question --</option>
{{range .Questions}}
<option value="{{.}}">{{.}}</option>
{{end}}
</select>
<div class="hint">Used for password recovery if you forget</div>
</div>
<div class="row custom-question-input" id="custom-question-container">
<label for="custom_question">Your Custom Question</label>
<input id="custom_question" name="custom_question" type="text" placeholder="Enter your custom security question">
</div>
<div class="row">
<label for="security_answer">Answer</label>
<input id="security_answer" name="security_answer" type="text" placeholder="Your answer (case insensitive)">
</div>
<div class="actions between">
<a href="/login" class="link"><i class="fa-solid fa-arrow-left"></i> Back to Login</a>
<button type="submit" class="primary"><i class="fa-solid fa-user-plus"></i> Create Account</button>
</div>
</form>
</div>
</div>
<script>
function toggleCustomQuestion(select) {
	const container = document.getElementById('custom-question-container');
	if (select.value === 'What is your custom security question') {
		container.classList.add('visible');
		document.getElementById('custom_question').required = true;
	} else {
		container.classList.remove('visible');
		document.getElementById('custom_question').required = false;
	}
}
</script>
</body>
</html>`

	t := template.Must(template.New("register").Parse(tpl))
	t.Execute(w, struct {
	Error     string
	Questions []string
	}{Error: errorMsg, Questions: securityQuestions})
}

func renderForgotPasswordPage(w http.ResponseWriter, message string, user *User) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	tpl := `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Reset Password - Stalkerhek</title>
<style>` + authCSS + `</style>
<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.5.1/css/all.min.css">
</head>
<body>
<div class="wrap">
<div class="card">
<div class="logo"><i class="fa-solid fa-key"></i></div>
<h1 style="text-align:center">Reset Password</h1>
<p class="subtitle">Recover access to your account</p>
{{if .Message}}
<div class="{{if .HasUser}}success{{else}}info-box{{end}}"><i class="fa-solid {{if .HasUser}}fa-check-circle{{else}}fa-circle-info{{end}}"></i> {{.Message}}</div>
{{end}}
{{if .User}}
<form method="post" action="/api/forgot-password" id="verifyForm">
<input type="hidden" name="username" value="{{.User.Username}}">
<div class="row">
<label>Security Question</label>
<div class="info-box"><i class="fa-solid fa-circle-question"></i> {{.User.SecurityQuestion}}</div>
</div>
<div class="row">
<label for="answer">Your Answer</label>
<input id="answer" name="answer" type="text" required placeholder="Enter your answer (case insensitive)">
</div>
<div class="actions">
<button type="submit" class="primary"><i class="fa-solid fa-check"></i> Verify & Continue</button>
</div>
</form>
{{else}}
<form method="post" action="/api/forgot-password">
<div class="row">
<label for="username">Username</label>
<input id="username" name="username" type="text" required placeholder="Enter your username">
</div>
<div class="actions between">
<a href="/login" class="link"><i class="fa-solid fa-arrow-left"></i> Back to Login</a>
<button type="submit" class="primary"><i class="fa-solid fa-magnifying-glass"></i> Find Account</button>
</div>
</form>
{{end}}
</div>
</div>
</body>
</html>`

	t := template.Must(template.New("forgot").Parse(tpl))
	t.Execute(w, struct {
	Message string
	User    *User
	HasUser bool
	}{Message: message, User: user, HasUser: user != nil})
}

func renderResetPasswordPage(w http.ResponseWriter, errorMsg, token string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	tpl := `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Set New Password - Stalkerhek</title>
<style>` + authCSS + `</style>
<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.5.1/css/all.min.css">
</head>
<body>
<div class="wrap">
<div class="card">
<div class="logo"><i class="fa-solid fa-lock"></i></div>
<h1 style="text-align:center">Set New Password</h1>
<p class="subtitle">Enter your new password below</p>
{{if .Error}}
<div class="error"><i class="fa-solid fa-circle-exclamation"></i> {{.Error}}</div>
{{end}}
<form method="post" action="/api/reset-password">
<input type="hidden" name="token" value="{{.Token}}">
<div class="row">
<label for="new_password">New Password</label>
<input id="new_password" name="new_password" type="password" required placeholder="Create new password" minlength="4">
<div class="hint">Must be at least 4 characters</div>
</div>
<div class="row">
<label for="confirm_password">Confirm Password</label>
<input id="confirm_password" name="confirm_password" type="password" required placeholder="Confirm new password">
</div>
<div class="actions between">
<a href="/login" class="link"><i class="fa-solid fa-arrow-left"></i> Back</a>
<button type="submit" class="primary"><i class="fa-solid fa-floppy-disk"></i> Save Password</button>
</div>
</form>
</div>
</div>
</body>
</html>`

	t := template.Must(template.New("reset").Parse(tpl))
	t.Execute(w, struct {
	Error string
	Token string
	}{Error: errorMsg, Token: token})
}

func renderAccountPage(w http.ResponseWriter, username, securityQuestion string, canRegister bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	tpl := `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Account - Stalkerhek</title>
<style>` + authCSS + `
/* Additional styles for user management */
.user-list{margin-top:20px}
.user-item{display:flex;align-items:center;justify-content:space-between;padding:14px 16px;background:rgba(31,46,35,.4);border:1px solid var(--border);border-radius:12px;margin-bottom:10px;transition:background .2s}
.user-item:hover{background:rgba(31,46,35,.6)}
.user-info{display:flex;align-items:center;gap:12px}
.user-icon{width:40px;height:40px;background:var(--brand);border-radius:50%;display:flex;align-items:center;justify-content:center;color:#fff;font-size:16px}
.user-details{flex:1}
.user-name{font-weight:600;color:var(--text);font-size:15px}
.user-meta{font-size:12px;color:var(--muted);margin-top:2px}
.user-actions{display:flex;gap:8px}
.user-actions button{padding:8px 12px;font-size:13px}
.user-actions button.plain{color:var(--muted)}
.user-actions button.plain:hover{color:var(--text)}
.user-actions button.danger{padding:8px 12px;font-size:13px;background:transparent;color:var(--bad);border:1px solid var(--bad)}
.user-actions button.danger:hover{background:var(--bad);color:#fff}
.badge{display:inline-block;padding:3px 8px;border-radius:6px;font-size:11px;font-weight:500;text-transform:uppercase;letter-spacing:.5px}
.badge-current{background:var(--brand);color:#fff}
.badge-admin{background:var(--warn);color:#000}
.empty-state{text-align:center;padding:40px 20px;color:var(--muted)}
.empty-state i{font-size:48px;color:var(--border);margin-bottom:16px;display:block}
/* Modal styles */
.modal-overlay{position:fixed;top:0;left:0;right:0;bottom:0;background:rgba(0,0,0,.7);display:none;align-items:center;justify-content:center;z-index:1000;padding:20px}
.modal-overlay.active{display:flex}
.modal-content{width:100%;max-width:420px;background:linear-gradient(180deg,rgba(17,24,21,.98),rgba(13,20,16,.96));border:1px solid var(--border);border-radius:18px;padding:24px;position:relative}
.modal-header{display:flex;align-items:center;margin-bottom:20px}
.modal-title{font-size:18px;font-weight:600;color:var(--text);flex:1}
.modal-close{position:absolute;top:16px;right:16px;background:none;border:none;color:var(--muted);font-size:20px;cursor:pointer;padding:8px;width:36px;height:36px;display:flex;align-items:center;justify-content:center;border-radius:8px;transition:all .2s}
.modal-close:hover{color:var(--text);background:rgba(255,255,255,.1)}
/* Custom question input */
.custom-question-input{margin-top:10px;display:none}
.custom-question-input.visible{display:block}
/* Confirmation dialog */
.confirm-dialog{text-align:center}
.confirm-dialog i{font-size:48px;color:var(--bad);margin-bottom:16px}
.confirm-dialog h3{margin:0 0 8px 0;font-size:18px}
.confirm-dialog p{color:var(--muted);margin:0 0 24px 0;font-size:14px}
.confirm-actions{display:flex;gap:12px;justify-content:center}
</style>
<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.5.1/css/all.min.css">
</head>
<body>
<div class="wrap">
<div class="card" style="max-width:600px">
<div class="logo"><i class="fa-solid fa-user-shield"></i></div>
<h1 style="text-align:center">Account</h1>
<p class="subtitle">Manage your account and security settings</p>

{{if .Username}}
<!-- Logged in user view -->
<div class="userinfo">
<div class="label">Logged in as</div>
<div class="value"><i class="fa-solid fa-user"></i> {{.Username}}</div>
</div>

<!-- Tab navigation -->
<div class="tabs">
<div class="tab active" onclick="showTab('password')" id="tab-password"><i class="fa-solid fa-lock"></i> Password</div>
<div class="tab" onclick="showTab('security')" id="tab-security"><i class="fa-solid fa-shield-halved"></i> Security</div>
{{if .CanRegister}}
<div class="tab" onclick="showTab('users')" id="tab-users"><i class="fa-solid fa-users"></i> Users</div>
{{end}}
</div>

<!-- Password change tab -->
<div id="panel-password" class="tab-panel">
<div class="section-title">Change Password</div>
<form method="post" action="/api/change-password" onsubmit="return handleChangePassword(event)">
<div class="row">
<label for="current_password">Current Password</label>
<input id="current_password" name="current_password" type="password" required placeholder="Enter current password">
</div>
<div class="row">
<label for="new_password">New Password</label>
<input id="new_password" name="new_password" type="password" required placeholder="Create new password" minlength="4">
<div class="hint">Minimum 4 characters</div>
</div>
<div class="row">
<label for="confirm_password">Confirm New Password</label>
<input id="confirm_password" name="confirm_password" type="password" required placeholder="Confirm new password">
</div>
<div id="password-message"></div>
<div class="actions center">
<button type="submit" class="primary"><i class="fa-solid fa-rotate"></i> Update Password</button>
</div>
</form>
</div>

<!-- Security tab -->
<div id="panel-security" class="tab-panel" style="display:none">
<div class="section-title">Security Settings</div>

<div class="toggle-row">
<div>
<div class="toggle-label"><i class="fa-solid fa-wifi" style="color:var(--brand);margin-right:8px"></i>Local Network Bypass</div>
<div class="toggle-sublabel">Skip authentication when on same network</div>
</div>
<label class="toggle">
<input type="checkbox" id="local-bypass" onchange="toggleLocalBypass(this)" checked>
<span class="slider"></span>
</label>
</div>

<div class="section-title" style="margin-top:24px">Security Question</div>
<div class="info-box">
<i class="fa-solid fa-circle-info"></i>
{{if .SecurityQuestion}}
<strong>Current:</strong> {{.SecurityQuestion}}
<br><br>
<i class="fa-solid fa-circle-check" style="color:var(--ok)"></i> Security question configured for password recovery.
{{else}}
<i class="fa-solid fa-triangle-exclamation" style="color:var(--warn)"></i> No security question set. Configure one below for password recovery.
{{end}}
</div>

<form method="post" action="/api/users/{{.Username}}" onsubmit="return handleUpdateSecurityQuestion(event)">
<div class="row">
<label for="edit_security_question">Update Security Question</label>
<select id="edit_security_question" name="security_question" onchange="toggleCustomQuestion(this)">
<option value="">-- Select a security question --</option>
{{range .Questions}}
<option value="{{.}}" {{if eq . $.SecurityQuestion}}selected{{end}}>{{.}}</option>
{{end}}
</select>
</div>
<div class="row custom-question-input" id="custom-question-container">
<label for="custom_question">Your Custom Question</label>
<input id="custom_question" name="custom_question" type="text" placeholder="Enter your custom security question">
</div>
<div class="row">
<label for="edit_security_answer">Answer</label>
<input id="edit_security_answer" name="security_answer" type="text" required placeholder="Your answer (case insensitive)">
<div class="hint">Minimum 4 characters recommended</div>
</div>
<div id="security-message"></div>
<div class="actions center">
<button type="submit" class="primary"><i class="fa-solid fa-floppy-disk"></i> Save Security Question</button>
</div>
</form>
</div>

<!-- Users tab -->
{{if .CanRegister}}
<div id="panel-users" class="tab-panel" style="display:none">

<!-- User List Section -->
<div class="section-title">User Accounts</div>
<div id="user-list-container" class="user-list">
<div class="empty-state">
<i class="fa-solid fa-spinner fa-spin"></i>
<p>Loading users...</p>
</div>
</div>

<div class="section-title" style="margin-top:32px">Add New User</div>
<div class="info-box"><i class="fa-solid fa-circle-info"></i> Creating a new user account allows multiple people to access this Stalkerhek instance.</div>
<form method="post" action="/api/register" onsubmit="return handleRegisterUser(event)">
<div class="row">
<label for="new_username">Username</label>
<input id="new_username" name="username" type="text" required placeholder="Choose a username">
</div>
<div class="row">
<label for="new_user_password">Password</label>
<input id="new_user_password" name="password" type="password" required placeholder="Create a password" minlength="4">
<div class="hint">Minimum 4 characters</div>
</div>
<div class="row">
<label for="new_user_password_confirm">Confirm Password</label>
<input id="new_user_password_confirm" name="password_confirm" type="password" required placeholder="Confirm password">
</div>
<div class="row">
<label for="new_security_question">Security Question (optional)</label>
<select id="new_security_question" name="security_question" onchange="toggleCustomQuestionNewUser(this)">
<option value="">-- Select a security question --</option>
{{range .Questions}}
<option value="{{.}}">{{.}}</option>
{{end}}
</select>
</div>
<div class="row custom-question-input" id="new-custom-question-container">
<label for="new_custom_question">Your Custom Question</label>
<input id="new_custom_question" name="custom_question" type="text" placeholder="Enter your custom security question">
</div>
<div class="row">
<label for="new_security_answer">Answer</label>
<input id="new_security_answer" name="security_answer" type="text" placeholder="Your answer">
</div>
<div id="register-message"></div>
<div class="actions center">
<button type="submit" class="primary"><i class="fa-solid fa-user-plus"></i> Create User</button>
</div>
</form>
</div>
{{end}}

<div class="actions between" style="margin-top:32px;border-top:1px solid var(--border);padding-top:24px">
<a href="/dashboard" class="link"><i class="fa-solid fa-arrow-left"></i> Back to Dashboard</a>
<form method="post" action="/api/logout" style="margin:0">
<button type="submit" class="danger"><i class="fa-solid fa-right-from-bracket"></i> Logout</button>
</form>
</div>

{{else}}
<!-- Not logged in / auth disabled view -->
<div class="info-box"><i class="fa-solid fa-circle-info"></i> {{if .Message}}{{.Message}}{{else}}Please sign in to manage your account.{{end}}</div>
<div class="actions center">
<a href="/login" class="link" style="padding:14px 24px;background:var(--brand);color:#fff;border-radius:12px;text-decoration:none;font-weight:600"><i class="fa-solid fa-right-to-bracket"></i> Sign In</a>
</div>
{{end}}
</div>
</div>

<!-- Edit User Modal -->
<div class="modal-overlay" id="edit-modal" role="dialog" aria-modal="true" aria-labelledby="edit-modal-title">
<div class="modal-content">
<button class="modal-close" onclick="closeEditModal()" aria-label="Close edit user modal"><i class="fa-solid fa-xmark"></i></button>
<div class="modal-header">
<div class="modal-title" id="edit-modal-title"><i class="fa-solid fa-user-pen"></i> Edit User</div>
</div>
<form id="edit-user-form" onsubmit="return handleEditUser(event)">
<input type="hidden" id="edit-target-username" name="target_username">
<div class="row">
<label>Username</label>
<div id="edit-username-display" style="padding:12px 14px;background:rgba(31,46,35,.6);border:1px solid var(--border);border-radius:10px;color:var(--text);font-weight:500"></div>
</div>
<div class="row">
<label for="edit_user_security_question">Security Question</label>
<select id="edit_user_security_question" name="security_question" onchange="toggleCustomQuestionEdit(this)">
<option value="">-- Select a security question --</option>
{{range .Questions}}
<option value="{{.}}">{{.}}</option>
{{end}}
</select>
</div>
<div class="row custom-question-input" id="edit-custom-question-container">
<label for="edit_user_custom_question">Your Custom Question</label>
<input id="edit_user_custom_question" name="custom_question" type="text" placeholder="Enter your custom security question">
</div>
<div class="row">
<label for="edit_user_security_answer">Answer</label>
<input id="edit_user_security_answer" name="security_answer" type="text" placeholder="Your answer (leave empty to keep current)">
<div class="hint">Leave empty to keep current answer</div>
</div>
<div id="edit-user-message"></div>
<div class="actions center">
<button type="submit" class="primary"><i class="fa-solid fa-floppy-disk"></i> Save Changes</button>
</div>
</form>
</div>
</div>

<!-- Delete Confirmation Modal -->
<div class="modal-overlay" id="delete-modal" role="dialog" aria-modal="true" aria-labelledby="delete-modal-title">
<div class="modal-content confirm-dialog">
<button class="modal-close" onclick="closeDeleteModal()" aria-label="Close delete confirmation modal"><i class="fa-solid fa-xmark"></i></button>
<i class="fa-solid fa-triangle-exclamation" aria-hidden="true"></i>
<h3 id="delete-modal-title">Delete User Account</h3>
<p>Are you sure you want to delete <strong id="delete-username"></strong>? This action cannot be undone.</p>
<div class="confirm-actions">
<button class="plain" onclick="closeDeleteModal()">Cancel</button>
<button class="danger" onclick="confirmDeleteUser()">Delete User</button>
</div>
</div>
</div>

<script>
let usersData = [];
let userToDelete = null;

function showTab(name) {
	document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
	document.getElementById('tab-' + name).classList.add('active');
	document.querySelectorAll('.tab-panel').forEach(p => p.style.display = 'none');
	document.getElementById('panel-' + name).style.display = 'block';
	
	// Load users when switching to users tab
	if (name === 'users') {
		loadUsers();
	}
}

async function handleChangePassword(e) {
	e.preventDefault();
	const msg = document.getElementById('password-message');
	const form = e.target;
	
	if (form.new_password.value !== form.confirm_password.value) {
		msg.innerHTML = '<div class="error"><i class="fa-solid fa-circle-exclamation"></i> Passwords do not match</div>';
		return false;
	}
	
	try {
		const res = await fetch('/api/change-password', {
			method: 'POST',
			headers: {'Content-Type': 'application/x-www-form-urlencoded'},
			body: new URLSearchParams(new FormData(form))
		});
		const data = await res.json();
		if (data.status === 'ok') {
			msg.innerHTML = '<div class="success"><i class="fa-solid fa-check-circle"></i> Password updated successfully!</div>';
			form.reset();
		} else {
			msg.innerHTML = '<div class="error"><i class="fa-solid fa-circle-exclamation"></i> ' + (data.error || 'Failed to update password') + '</div>';
		}
	} catch(err) {
		msg.innerHTML = '<div class="error"><i class="fa-solid fa-circle-exclamation"></i> Network error</div>';
	}
	return false;
}

async function handleUpdateSecurityQuestion(e) {
	e.preventDefault();
	const msg = document.getElementById('security-message');
	const form = e.target;
	
	const answer = form.security_answer.value.trim();
	if (answer.length < 4) {
		msg.innerHTML = '<div class="error"><i class="fa-solid fa-circle-exclamation"></i> Answer must be at least 4 characters</div>';
		return false;
	}
	
	try {
		const res = await fetch(form.action, {
			method: 'PUT',
			headers: {'Content-Type': 'application/x-www-form-urlencoded'},
			body: new URLSearchParams(new FormData(form))
		});
		const data = await res.json();
		if (data.status === 'ok') {
			msg.innerHTML = '<div class="success"><i class="fa-solid fa-check-circle"></i> Security question updated successfully!</div>';
			form.reset();
			setTimeout(() => location.reload(), 1000);
		} else {
			msg.innerHTML = '<div class="error"><i class="fa-solid fa-circle-exclamation"></i> ' + (data.error || 'Failed to update') + '</div>';
		}
	} catch(err) {
		msg.innerHTML = '<div class="error"><i class="fa-solid fa-circle-exclamation"></i> Network error</div>';
	}
	return false;
}

async function toggleLocalBypass(checkbox) {
	try {
		const action = checkbox.checked ? 'enable' : 'disable';
		await fetch('/api/trusted-subnets', {
			method: 'POST',
			headers: {'Content-Type': 'application/x-www-form-urlencoded'},
			body: 'action=' + action
		});
	} catch(err) {
		console.error('Failed to toggle:', err);
	}
}

function toggleCustomQuestion(select) {
	const container = document.getElementById('custom-question-container');
	if (select.value === 'What is your custom security question') {
		container.classList.add('visible');
		document.getElementById('custom_question').required = true;
	} else {
		container.classList.remove('visible');
		document.getElementById('custom_question').required = false;
	}
}

function toggleCustomQuestionNewUser(select) {
	const container = document.getElementById('new-custom-question-container');
	if (select.value === 'What is your custom security question') {
		container.classList.add('visible');
		document.getElementById('new_custom_question').required = true;
	} else {
		container.classList.remove('visible');
		document.getElementById('new_custom_question').required = false;
	}
}

function toggleCustomQuestionEdit(select) {
	const container = document.getElementById('edit-custom-question-container');
	if (select.value === 'What is your custom security question') {
		container.classList.add('visible');
		document.getElementById('edit_user_custom_question').required = true;
	} else {
		container.classList.remove('visible');
		document.getElementById('edit_user_custom_question').required = false;
	}
}

{{if .CanRegister}}
async function handleRegisterUser(e) {
	e.preventDefault();
	const msg = document.getElementById('register-message');
	const form = e.target;
	
	if (form.password.value !== form.password_confirm.value) {
		msg.innerHTML = '<div class="error"><i class="fa-solid fa-circle-exclamation"></i> Passwords do not match</div>';
		return false;
	}
	
	if (form.password.value.length < 4) {
		msg.innerHTML = '<div class="error"><i class="fa-solid fa-circle-exclamation"></i> Password must be at least 4 characters</div>';
		return false;
	}
	
	try {
		const res = await fetch('/api/register', {
			method: 'POST',
			headers: {'Content-Type': 'application/x-www-form-urlencoded'},
			body: new URLSearchParams(new FormData(form))
		});
		const data = await res.json();
		if (data.status === 'ok') {
			msg.innerHTML = '<div class="success"><i class="fa-solid fa-check-circle"></i> User created successfully!</div>';
			form.reset();
			loadUsers(); // Refresh user list
		} else {
			msg.innerHTML = '<div class="error"><i class="fa-solid fa-circle-exclamation"></i> ' + (data.error || 'Failed to create user') + '</div>';
		}
	} catch(err) {
		msg.innerHTML = '<div class="error"><i class="fa-solid fa-circle-exclamation"></i> Network error</div>';
	}
	return false;
}

async function loadUsers() {
	const container = document.getElementById('user-list-container');
	try {
		const res = await fetch('/api/users');
		const data = await res.json();
		
		if (data.users && data.users.length > 0) {
			usersData = data.users;
			container.innerHTML = data.users.map(user => 
				'<div class="user-item">' +
					'<div class="user-info">' +
						'<div class="user-icon"><i class="fa-solid fa-user"></i></div>' +
						'<div class="user-details">' +
							'<div class="user-name">' + escapeHtml(user.username) +
								(user.is_current_user ? ' <span class="badge badge-current">You</span>' : '') +
							'</div>' +
							'<div class="user-meta">' +
								'<i class="fa-solid fa-clock"></i> Created ' + new Date(user.created_at).toLocaleDateString() +
								(user.has_security_question ? ' <i class="fa-solid fa-lock" style="margin-left:12px"></i> Secured' : '') +
							'</div>' +
						'</div>' +
					'</div>' +
					'<div class="user-actions">' +
						(user.is_current_user ? '<button class="plain" onclick="openEditModal(\'' + escapeHtml(user.username) + '\')" title="Edit security question"><i class="fa-solid fa-pen"></i></button>' : '') +
						(!user.is_current_user ? '<button class="danger" onclick="openDeleteModal(\'' + escapeHtml(user.username) + '\')" title="Delete user"><i class="fa-solid fa-trash"></i></button>' : '') +
					'</div>' +
				'</div>'
			).join('');
		} else {
			container.innerHTML = 
				'<div class="empty-state">' +
					'<i class="fa-solid fa-users-slash"></i>' +
					'<p>No users found</p>' +
				'</div>';
		}
	} catch(err) {
		container.innerHTML = 
			'<div class="empty-state">' +
				'<i class="fa-solid fa-triangle-exclamation"></i>' +
				'<p>Failed to load users</p>' +
			'</div>';
	}
}

function escapeHtml(text) {
	const div = document.createElement('div');
	div.textContent = text;
	return div.innerHTML;
}

function openEditModal(username) {
	const user = usersData.find(u => u.username === username);
	if (!user) return;
	
	document.getElementById('edit-target-username').value = username;
	document.getElementById('edit-username-display').textContent = username;
	document.getElementById('edit-modal').classList.add('active');
}

function closeEditModal() {
	document.getElementById('edit-modal').classList.remove('active');
	document.getElementById('edit-user-form').reset();
	document.getElementById('edit-user-message').innerHTML = '';
	document.getElementById('edit-custom-question-container').classList.remove('visible');
}

async function handleEditUser(e) {
	e.preventDefault();
	const msg = document.getElementById('edit-user-message');
	const form = e.target;
	const username = document.getElementById('edit-target-username').value;
	
	const answer = document.getElementById('edit_user_security_answer').value.trim();
	if (answer && answer.length < 4) {
		msg.innerHTML = '<div class="error"><i class="fa-solid fa-circle-exclamation"></i> Answer must be at least 4 characters</div>';
		return false;
	}
	
	try {
		const res = await fetch('/api/users/' + encodeURIComponent(username), {
			method: 'PUT',
			headers: {'Content-Type': 'application/x-www-form-urlencoded'},
			body: new URLSearchParams(new FormData(form))
		});
		const data = await res.json();
		if (data.status === 'ok') {
			msg.innerHTML = '<div class="success"><i class="fa-solid fa-check-circle"></i> Changes saved!</div>';
			setTimeout(() => {
				closeEditModal();
				loadUsers();
				location.reload();
			}, 1000);
		} else {
			msg.innerHTML = '<div class="error"><i class="fa-solid fa-circle-exclamation"></i> ' + (data.error || 'Failed to update') + '</div>';
		}
	} catch(err) {
		msg.innerHTML = '<div class="error"><i class="fa-solid fa-circle-exclamation"></i> Network error</div>';
	}
	return false;
}

function openDeleteModal(username) {
	userToDelete = username;
	document.getElementById('delete-username').textContent = username;
	document.getElementById('delete-modal').classList.add('active');
}

function closeDeleteModal() {
	document.getElementById('delete-modal').classList.remove('active');
	userToDelete = null;
}

async function confirmDeleteUser() {
	if (!userToDelete) return;
	
	try {
		const res = await fetch('/api/users/' + encodeURIComponent(userToDelete), {
			method: 'DELETE'
		});
		const data = await res.json();
		if (data.status === 'ok') {
			closeDeleteModal();
			loadUsers();
		} else {
			alert('Failed to delete user: ' + (data.error || 'Unknown error'));
		}
	} catch(err) {
		alert('Network error while deleting user');
	}
}

// Close modals on overlay click
document.getElementById('edit-modal').addEventListener('click', function(e) {
	if (e.target === this) closeEditModal();
});
document.getElementById('delete-modal').addEventListener('click', function(e) {
	if (e.target === this) closeDeleteModal();
});
{{end}}
</script>
</body>
</html>`

	t := template.Must(template.New("account").Parse(tpl))
	
	// Check if on local network
	isLocal := false
	for _, subnet := range trustedSubnets {
		if subnet.String() == "127.0.0.0/8" || strings.HasPrefix(subnet.String(), "10.") || 
		   strings.HasPrefix(subnet.String(), "192.168.") || strings.HasPrefix(subnet.String(), "172.") {
			isLocal = true
			break
		}
	}
	
	t.Execute(w, struct {
	Username         string
	SecurityQuestion string
	CanRegister      bool
	Questions        []string
	IsLocal          bool
	Message          string
	}{
		Username:         username,
		SecurityQuestion: securityQuestion,
		CanRegister:      canRegister,
		Questions:        securityQuestions,
		IsLocal:          isLocal,
	})
}
