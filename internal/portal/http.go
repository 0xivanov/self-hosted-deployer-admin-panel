package portal

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

//go:embed static/*
var webAssets embed.FS

type HTTPOptions struct {
	TestWebhookSecret string
	TestBilling       bool
	Origin            string
	Development       bool
	Mail              *AccountMail
	Signup            bool
	PublicationSites  map[string]string
}
type attemptWindow struct {
	start time.Time
	count int
}
type HTTP struct {
	billingWebhook       http.Handler
	testBilling          bool
	mail                 *AccountMail
	publicationSites     map[string]string
	signup               bool
	store                *Store
	origin, host, cookie string
	development          bool
	slots                chan struct{}
	mu                   sync.Mutex
	attempts             map[string]attemptWindow
}

func NewHTTP(store *Store, opts HTTPOptions) (*HTTP, error) {
	u, err := url.Parse(opts.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("origin must be a complete origin without path or credentials")
	}
	if opts.Development {
		if u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
			return nil, errors.New("development origin must use HTTP on 127.0.0.1")
		}
	} else if u.Scheme != "https" {
		return nil, errors.New("HTTPS required")
	}
	if opts.Signup && opts.Mail == nil {
		return nil, errors.New("signup requires account mail")
	}
	if opts.Mail != nil && (opts.Mail.store != store || opts.Mail.origin != opts.Origin) {
		return nil, errors.New("mail and portal must use the same store and origin")
	}
	sites := map[string]string{}
	for project, origin := range opts.PublicationSites {
		id, err := hex.DecodeString(project)
		site, e := url.Parse(origin)
		if err != nil || len(id) != 32 || e != nil || site.Scheme != "https" || site.Host == "" || site.User != nil || site.Path != "" || site.RawQuery != "" || site.ForceQuery || site.Fragment != "" || strings.EqualFold(site.Hostname(), u.Hostname()) {
			return nil, errors.New("invalid assigned content origin")
		}
		sites[project] = origin
	}
	var webhook http.Handler
	if opts.TestWebhookSecret != "" {
		if !opts.TestBilling || opts.Development {
			return nil, errors.New("billing webhook requires test billing and HTTPS mode")
		}
		webhook, err = BillingWebhookHandler(store, u.Host, opts.TestWebhookSecret)
		if err != nil {
			return nil, err
		}
	}
	cookie := "__Host-portal-session"
	if opts.Development {
		cookie = "portal-dev-session"
	}
	return &HTTP{billingWebhook: webhook, testBilling: opts.TestBilling, publicationSites: sites, mail: opts.Mail, signup: opts.Signup, store: store, origin: opts.Origin, host: u.Host, cookie: cookie, development: opts.Development, slots: make(chan struct{}, 8), attempts: map[string]attemptWindow{}}, nil
}
func csrfFor(token string) string {
	sum := sha256.Sum256([]byte("portal-csrf:" + token))
	return hex.EncodeToString(sum[:])
}
func (h *HTTP) allowLogin(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, value := range h.attempts {
		if now.Sub(value.start) >= time.Minute {
			delete(h.attempts, key)
		}
	}
	w, exists := h.attempts[host]
	if !exists {
		if len(h.attempts) >= 4096 {
			return false
		}
		w.start = now
	}
	if w.count >= 10 {
		return false
	}
	w.count++
	h.attempts[host] = w
	return true
}
func (h *HTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/webhooks/stripe-test" {
		if h.billingWebhook == nil || r.URL.EscapedPath() != "/webhooks/stripe-test" || r.URL.RawQuery != "" || r.URL.ForceQuery {
			httpError(w, 404, "Not found")
			return
		}
		h.billingWebhook.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if r.Host != h.host || (!h.development && r.TLS == nil) || (strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("Sec-Fetch-Site") == "cross-site") {
		httpError(w, 403, "Invalid request origin")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != h.origin {
		httpError(w, 403, "Invalid request origin")
		return
	}
	if r.Method != "GET" && r.Method != "POST" {
		httpError(w, 405, "Method not allowed")
		return
	}
	if r.Method == "POST" && r.Header.Get("Origin") != h.origin {
		httpError(w, 403, "Origin required")
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Method != "GET" {
			httpError(w, 405, "Method not allowed")
			return
		}
		name, kind := "", ""
		switch r.URL.Path {
		case "/", "/billing/success", "/billing/cancel":
			name = "index.html"
			kind = "text/html; charset=utf-8"
		case "/portal.js":
			name = "portal.js"
			kind = "text/javascript; charset=utf-8"
		case "/portal.css":
			name = "portal.css"
			kind = "text/css; charset=utf-8"
		default:
			http.NotFound(w, r)
			return
		}
		data, err := webAssets.ReadFile("static/" + name)
		if err != nil {
			httpError(w, 500, "Page unavailable")
			return
		}
		w.Header().Set("Content-Type", kind)
		w.Write(data)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.Header().Set("Retry-After", "5")
		httpError(w, 429, "Please retry shortly")
		return
	}
	if r.URL.Path == "/api/config" && r.Method == "GET" {
		httpJSON(w, map[string]bool{"signup": h.signup, "account_mail": h.mail != nil, "test_billing": h.testBilling})
		return
	}
	if h.mail != nil && r.Method == "POST" && (r.URL.Path == "/api/register" || r.URL.Path == "/api/verify" || r.URL.Path == "/api/password/forgot" || r.URL.Path == "/api/password/reset") {
		h.accountAction(w, r)
		return
	}
	if r.URL.Path == "/api/login" && r.Method == "POST" {
		if !h.allowLogin(r.RemoteAddr) {
			w.Header().Set("Retry-After", "60")
			httpError(w, 429, "Too many login attempts; retry in a minute")
			return
		}
		var input struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		session, err := h.store.Login(r.Context(), input.Email, input.Password)
		if err != nil {
			if errors.Is(err, ErrCredentials) {
				httpError(w, 401, "Unable to sign in with these credentials")
			} else {
				httpError(w, 500, "Sign-in unavailable")
			}
			return
		}
		if old, err := r.Cookie(h.cookie); err == nil {
			if err = h.store.Logout(r.Context(), old.Value); err != nil {
				h.store.Logout(r.Context(), session.Token)
				httpError(w, 500, "Sign-in unavailable")
				return
			}
		}
		http.SetCookie(w, &http.Cookie{Name: h.cookie, Value: session.Token, Path: "/", Secure: !h.development, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: session.ExpiresAt, MaxAge: 86400})
		httpJSON(w, map[string]any{"account": session.Account, "csrf": csrfFor(session.Token)})
		return
	}
	cookie, err := r.Cookie(h.cookie)
	if err != nil {
		httpError(w, 401, "Sign in to continue")
		return
	}
	account, err := h.store.Authenticate(r.Context(), cookie.Value)
	if err != nil {
		if errors.Is(err, ErrDenied) {
			httpError(w, 401, "Sign in to continue")
		} else {
			httpError(w, 500, "Session unavailable")
		}
		return
	}
	if r.Method == "POST" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(csrfFor(cookie.Value))) != 1 {
		httpError(w, 403, "Reload the page and retry")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/billing/") {
		h.billingHTTP(w, r, cookie.Value)
		return
	}
	switch {
	case r.URL.Path == "/api/publications/resume" && r.Method == "POST":
		var input struct {
			Project string `json:"project"`
			Job     string `json:"job"`
			Upload  string `json:"upload"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		if h.publicationSites[input.Project] == "" {
			httpError(w, 403, "Publishing is not enabled for this project")
			return
		}
		job, err := h.store.ResumePublication(r.Context(), cookie.Value, input.Project, input.Job, input.Upload)
		if err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, job)
	case r.URL.Path == "/api/publications" && r.Method == "GET":
		project := r.URL.Query().Get("project")
		jobs, active, err := h.store.PublicationJobs(r.Context(), cookie.Value, project)
		if err != nil {
			h.storeError(w, err)
			return
		}
		p, err := h.store.GetProject(r.Context(), cookie.Value, project)
		if err != nil {
			h.storeError(w, err)
			return
		}
		site := h.publicationSites[project]
		httpJSON(w, map[string]any{"jobs": jobs, "active": active, "site": site, "available": site != "" && p.Kind == "static"})
	case r.URL.Path == "/api/publications" && r.Method == "POST":
		var input struct {
			Project string `json:"project"`
			Upload  string `json:"upload"`
			Key     string `json:"key"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		if h.publicationSites[input.Project] == "" {
			httpError(w, 403, "Publishing is not enabled for this project")
			return
		}
		job, err := h.store.RequestPublication(r.Context(), cookie.Value, input.Project, input.Upload, input.Key)
		if err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, job)
	case r.URL.Path == "/api/uploads" && r.Method == "GET":
		uploads, err := h.store.Uploads(r.Context(), cookie.Value, r.URL.Query().Get("project"))
		if err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, map[string]any{"uploads": uploads})
	case r.URL.Path == "/api/uploads" && r.Method == "POST":
		project := r.URL.Query().Get("project")
		if _, err := h.store.UploadAccess(r.Context(), cookie.Value, project); err != nil {
			h.storeError(w, err)
			return
		}
		typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || typ != "application/zip" {
			httpError(w, 415, "Upload a ZIP archive")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, projectarchive.MaxCompressed)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			httpError(w, 413, "ZIP must be no larger than 10 MiB")
			return
		}
		upload, err := h.store.SaveUpload(r.Context(), cookie.Value, project, data)
		if err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, upload)
	case r.URL.Path == "/api/uploads/delete" && r.Method == "POST":
		var input struct {
			Project string `json:"project"`
			ID      string `json:"id"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		if err := h.store.DeleteUpload(r.Context(), cookie.Value, input.Project, input.ID); err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, map[string]bool{"ok": true})
	case r.URL.Path == "/api/session" && r.Method == "GET":
		workspaces, err := h.store.Workspaces(r.Context(), cookie.Value)
		if err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, map[string]any{"account": account, "workspaces": workspaces, "csrf": csrfFor(cookie.Value)})
	case r.URL.Path == "/api/logout" && r.Method == "POST":
		if err := h.store.Logout(r.Context(), cookie.Value); err != nil {
			h.storeError(w, err)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: h.cookie, Value: "", Path: "/", Secure: !h.development, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		httpJSON(w, map[string]bool{"ok": true})
	case r.URL.Path == "/api/invitations" && r.Method == "GET":
		invites, err := h.store.Invitations(r.Context(), cookie.Value, r.URL.Query().Get("workspace"))
		if err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, map[string]any{"invitations": invites})
	case r.URL.Path == "/api/invitations" && r.Method == "POST":
		if h.mail == nil {
			httpError(w, 503, "Invitation email is unavailable")
			return
		}
		if !h.allowLogin(r.RemoteAddr) {
			httpError(w, 429, "Too many invitations; retry later")
			return
		}
		var input struct {
			Workspace string `json:"workspace"`
			Email     string `json:"email"`
			Role      string `json:"role"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		invite, err := h.mail.Invite(r.Context(), cookie.Value, input.Workspace, input.Email, input.Role)
		if err != nil {
			if errors.Is(err, ErrExists) {
				httpError(w, 409, "This person is already a member")
			} else {
				h.storeError(w, err)
			}
			return
		}
		httpJSON(w, invite)
	case r.URL.Path == "/api/invitations/revoke" && r.Method == "POST":
		var input struct {
			Workspace string `json:"workspace"`
			ID        string `json:"id"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		if err := h.store.RevokeInvitation(r.Context(), cookie.Value, input.Workspace, input.ID); err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, map[string]bool{"ok": true})
	case r.URL.Path == "/api/invitations/accept" && r.Method == "POST":
		var input struct {
			Token string `json:"token"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		workspace, err := h.store.AcceptInvitation(r.Context(), cookie.Value, input.Token)
		if err != nil {
			httpError(w, 403, "Invitation is expired, revoked, already used, or belongs to another email")
			return
		}
		httpJSON(w, map[string]string{"workspace": workspace, "message": "You have joined the workspace."})
	case r.URL.Path == "/api/members" && r.Method == "GET":
		members, err := h.store.Members(r.Context(), cookie.Value, r.URL.Query().Get("workspace"))
		if err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, map[string]any{"members": members})
	case r.URL.Path == "/api/members" && r.Method == "POST":
		var input struct {
			Workspace string  `json:"workspace"`
			User      string  `json:"user"`
			Role      *string `json:"role"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		if input.Role == nil {
			httpError(w, 400, "An explicit role or removal is required")
			return
		}
		if err := h.store.ChangeMember(r.Context(), cookie.Value, input.Workspace, input.User, *input.Role); err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, map[string]bool{"ok": true})
	case r.URL.Path == "/api/projects" && r.Method == "GET":
		projects, err := h.store.Projects(r.Context(), cookie.Value, r.URL.Query().Get("workspace"))
		if err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, map[string]any{"projects": projects})
	case r.URL.Path == "/api/projects" && r.Method == "POST":
		var input struct {
			Workspace string `json:"workspace"`
			Name      string `json:"name"`
			Kind      string `json:"kind"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		project, err := h.store.CreateProject(r.Context(), cookie.Value, input.Workspace, input.Name, input.Kind)
		if err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, project)
	default:
		httpError(w, 404, "Not found")
	}
}
func (h *HTTP) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrPublishing):
		httpError(w, 409, "A publication is already pending. Refresh its status before retrying.")
	case errors.Is(err, ErrConflict):
		httpError(w, 409, "Publication request conflicts with an existing operation")
	case errors.Is(err, ErrRetained):
		httpError(w, 409, "This upload is retained by publication history")
	case errors.Is(err, ErrQuota):
		httpError(w, 409, "Workspace upload limit reached (20 archives or 100 MiB). Delete unused uploads first.")
	case errors.Is(err, ErrArchive):
		httpError(w, 400, err.Error())
	case errors.Is(err, ErrDenied):
		httpError(w, 403, "Workspace access denied")
	case errors.Is(err, ErrLastOwner):
		httpError(w, 409, "Keep at least one active workspace owner")
	case errors.Is(err, ErrExists):
		httpError(w, 409, "A project with this name already exists")
	case errors.Is(err, ErrInvalid):
		httpError(w, 400, "Check the project name and type")
	default:
		httpError(w, 500, "Operation unavailable")
	}
}
func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func httpError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
func httpDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		httpError(w, 415, "JSON required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		httpError(w, 400, "Invalid request")
		return false
	}
	if err = d.Decode(&struct{}{}); err != io.EOF {
		httpError(w, 400, "Invalid request")
		return false
	}
	return true
}

func (h *HTTP) accountAction(w http.ResponseWriter, r *http.Request) {
	if !h.allowLogin(r.RemoteAddr) {
		w.Header().Set("Retry-After", "60")
		httpError(w, 429, "Too many account requests; retry in a minute")
		return
	}
	switch r.URL.Path {
	case "/api/register":
		if !h.signup {
			httpError(w, 403, "Registration is closed")
			return
		}
		var input struct {
			Email     string `json:"email"`
			Password  string `json:"password"`
			Workspace string `json:"workspace"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		err := h.mail.Register(r.Context(), input.Email, input.Password, input.Workspace)
		if errors.Is(err, ErrInvalid) {
			httpError(w, 400, "Use a valid email, a workspace name and a password of 12 to 1024 characters")
			return
		}
		if err != nil && !errors.Is(err, ErrExists) {
			httpError(w, 503, "Registration unavailable")
			return
		}
		httpJSON(w, map[string]string{"message": "If this email can be registered, a verification link will arrive shortly. Existing users can sign in or reset their password."})
	case "/api/password/forgot":
		var input struct {
			Email string `json:"email"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		if err := h.mail.RequestReset(r.Context(), input.Email); err != nil {
			httpError(w, 503, "Account recovery unavailable")
			return
		}
		httpJSON(w, map[string]string{"message": "If an eligible account exists, a reset link will arrive shortly."})
	case "/api/verify":
		var input struct {
			Token string `json:"token"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		if err := h.store.Verify(r.Context(), input.Token); err != nil {
			httpError(w, 400, "Verification link is invalid or expired")
			return
		}
		httpJSON(w, map[string]string{"message": "Email verified. You can sign in now."})
	case "/api/password/reset":
		var input struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		if err := h.store.ResetPassword(r.Context(), input.Token, input.Password); err != nil {
			httpError(w, 400, "Reset link is invalid, expired, or the password does not meet the length requirement")
			return
		}
		httpJSON(w, map[string]string{"message": "Password changed. Sign in with your new password."})
	}
}
