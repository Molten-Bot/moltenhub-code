package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/library"
)

//go:embed static/*
var staticFiles embed.FS

const maxLocalPromptBodyBytes = 16 << 20
const maxAgentAuthConfigureBodyBytes = 1 << 20
const maxHubSetupConfigureBodyBytes = 1 << 20
const streamSnapshotInterval = 120 * time.Millisecond
const maxStreamTaskLogs = 500
const bottomDockPlaceholder = "<!-- hub-bottom-dock -->"

var sitePageTemplate = template.Must(template.New("site-page").Parse(`<!doctype html>
<html lang="en" class="light">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}}</title>
  <script src="/static/lucide.min.js"></script>
  <script src="/static/site-header.js"></script>
  <link rel="stylesheet" href="/static/style.css">
</head>
<body class="site-page-body {{.BodyClass}}">
  <div class="site-page {{.PageClass}}">
    <moltenhub-code-header agent-harness="codex" agent-label="Codex"></moltenhub-code-header>
    <main class="site-page-main {{.MainClass}}" aria-label="{{.Heading}}">
      {{.Content}}
    </main>
    {{.BottomDock}}
  </div>
  <script>
    if (window.MoltenHubHeader && typeof window.MoltenHubHeader.startConnectionStatus === "function") {
      window.MoltenHubHeader.startConnectionStatus();
    }
    if (window.lucide) {
      window.lucide.createIcons();
    }
  </script>
</body>
</html>`))

type sitePageData struct {
	Title      string
	BodyClass  string
	PageClass  string
	MainClass  string
	Heading    string
	Content    template.HTML
	BottomDock template.HTML
}

// Server provides an HTTP UI for live hub/task monitoring.
type Server struct {
	Addr                    string
	Broker                  *Broker
	AutomaticMode           bool
	ConfiguredHarness       string
	Logf                    func(string, ...any)
	SubmitLocalPrompt       func(context.Context, []byte) (string, error)
	SubmitTaskRerun         func(context.Context, string, []byte, bool) (string, error)
	CloseTask               func(context.Context, string) error
	PauseTask               func(context.Context, string) error
	RunTask                 func(context.Context, string) error
	ForceRunTask            func(context.Context, string) error
	StopTask                func(context.Context, string) error
	ResolveTaskControls     func(string) TaskControls
	LoadLibraryTasks        func() ([]library.TaskSummary, error)
	AgentAuthStatus         func(context.Context) (AgentAuthState, error)
	StartAgentAuth          func(context.Context) (AgentAuthState, error)
	VerifyAgentAuth         func(context.Context) (AgentAuthState, error)
	ConfigureAgentAuth      func(context.Context, string) (AgentAuthState, error)
	HubSetupStatus          func(context.Context) (HubSetupState, error)
	ConfigureHubSetup       func(context.Context, HubSetupRequest) (HubSetupState, error)
	ConnectHubSetup         func(context.Context) (HubSetupState, error)
	DisconnectHubSetup      func(context.Context) (HubSetupState, error)
	RenderHubSetupStatus    func(context.Context) (HubSetupState, error)
	ResolveGitHubProfileURL func(context.Context) (string, error)
	ResolveGitHubRepos      func(context.Context) ([]GitHubRepo, error)
	gitHubRepos             *gitHubRepoCache
	Ready                   chan<- error
}

// AgentAuthState describes current runtime agent-auth readiness and device flow hints.
type AgentAuthState struct {
	Harness              string            `json:"harness,omitempty"`
	Required             bool              `json:"required"`
	Ready                bool              `json:"ready"`
	State                string            `json:"state,omitempty"`
	Message              string            `json:"message,omitempty"`
	AuthURL              string            `json:"auth_url,omitempty"`
	DeviceCode           string            `json:"device_code,omitempty"`
	AcceptsBrowserCode   bool              `json:"accepts_browser_code,omitempty"`
	ConfigureCommand     string            `json:"configure_command,omitempty"`
	ConfigurePlaceholder string            `json:"configure_placeholder,omitempty"`
	ConfigureOptions     []AgentAuthOption `json:"configure_options,omitempty"`
	UpdatedAt            string            `json:"updated_at,omitempty"`
}

type AgentAuthOption struct {
	Value       string `json:"value,omitempty"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

// HubSetupState describes whether Molten Hub is configured locally and what
// profile details should be reflected in config.json.
type HubSetupState struct {
	Configured bool   `json:"configured"`
	AgentMode  string `json:"agent_mode,omitempty"`
	TokenType  string `json:"token_type,omitempty"`
	Region     string `json:"region,omitempty"`
	Handle     string `json:"handle,omitempty"`
	Profile    struct {
		ProfileText string `json:"profile"`
		DisplayName string `json:"display_name"`
		Emoji       string `json:"emoji"`
	} `json:"profile"`
	ConnectURL       string         `json:"connect_url,omitempty"`
	DashboardURL     string         `json:"dashboard_url,omitempty"`
	Message          string         `json:"message,omitempty"`
	NeedsRestart     bool           `json:"needs_restart,omitempty"`
	Onboarding       []HubSetupStep `json:"onboarding,omitempty"`
	OnboardingActive bool           `json:"onboarding_active,omitempty"`
	OnboardingStage  string         `json:"onboarding_stage,omitempty"`
	ActivationReady  bool           `json:"activation_ready,omitempty"`
}

type HubSetupStep struct {
	ID     string `json:"id,omitempty"`
	Label  string `json:"label,omitempty"`
	Status string `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// GitHubRepo captures the repository fields shown in the chat view.
type GitHubRepo struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Description   string `json:"description,omitempty"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch,omitempty"`
	Private       bool   `json:"private"`
	Language      string `json:"language,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	PushedAt      string `json:"pushed_at,omitempty"`
}

type gitHubRepoCache struct {
	mu      sync.Mutex
	loaded  bool
	loading bool
	wait    chan struct{}
	repos   []GitHubRepo
}

// HubSetupRequest captures the late-stage Hub connect modal payload.
type HubSetupRequest struct {
	AgentMode string `json:"agent_mode"`
	TokenType string `json:"token_type"`
	Region    string `json:"region"`
	Token     string `json:"token"`
	Handle    string `json:"handle"`
	Profile   struct {
		ProfileText string `json:"profile"`
		DisplayName string `json:"display_name"`
		Emoji       string `json:"emoji"`
	} `json:"profile"`
}

// NewServer returns a monitor HTTP server.
func NewServer(addr string, broker *Broker) Server {
	return Server{
		Addr:        strings.TrimSpace(addr),
		Broker:      broker,
		Logf:        func(string, ...any) {},
		gitHubRepos: &gitHubRepoCache{},
		LoadLibraryTasks: func() ([]library.TaskSummary, error) {
			catalog, err := library.LoadCatalog(library.DefaultDir)
			if err != nil {
				return nil, err
			}
			return catalog.Summaries(), nil
		},
	}
}

// Run serves the monitor UI until ctx is canceled.
func (s Server) Run(ctx context.Context) error {
	if strings.TrimSpace(s.Addr) == "" {
		return nil
	}
	if s.Broker == nil {
		return fmt.Errorf("broker is required")
	}
	if s.Logf == nil {
		s.Logf = func(string, ...any) {}
	}

	httpServer := &http.Server{
		Addr:              s.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	listener, err := net.Listen("tcp", s.Addr)
	if err != nil {
		notifyServerReady(s.Ready, err)
		return err
	}
	notifyServerReady(s.Ready, nil)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	s.logf("hub.ui status=starting listen=%s", s.Addr)
	err = httpServer.Serve(listener)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		s.logf("hub.ui status=stopped")
		return nil
	}
	return err
}

func notifyServerReady(ready chan<- error, err error) {
	if ready == nil {
		return
	}
	select {
	case ready <- err:
	default:
	}
}

// Handler returns the HTTP handler for the monitor UI/API.
func (s Server) Handler() http.Handler {
	mux := http.NewServeMux()
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		s.logf("hub.ui status=warn event=load_static_files err=%q", err)
	} else {
		staticHandler := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))
		mux.Handle("/static/", withCacheControl(staticHandler, "public, max-age=3600"))
	}
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/chat", s.handleChat)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/status", s.handleState)
	mux.HandleFunc("/api/library", s.handleLibrary)
	mux.HandleFunc("/api/library/run", s.handleLibraryRun)
	mux.HandleFunc("/api/stream", s.handleStream)
	mux.HandleFunc("/api/local-prompt", s.handleLocalPrompt)
	mux.HandleFunc("/api/github/profile", s.handleGitHubProfile)
	mux.HandleFunc("/api/github/repos", s.handleGitHubRepos)
	mux.HandleFunc("/api/hub-setup", s.handleHubSetup)
	mux.HandleFunc("/api/hub-setup/connect", s.handleHubSetupConnect)
	mux.HandleFunc("/api/hub-setup/disconnect", s.handleHubSetupDisconnect)
	mux.HandleFunc("/api/agent-auth", s.handleAgentAuthStatus)
	mux.HandleFunc("/api/agent-auth/start-device", s.handleAgentAuthStart)
	mux.HandleFunc("/api/agent-auth/verify", s.handleAgentAuthVerify)
	mux.HandleFunc("/api/agent-auth/configure", s.handleAgentAuthConfigure)
	mux.HandleFunc("/api/tasks/", s.handleTaskAction)
	mux.HandleFunc("/healthz", s.handleHealth)
	return withGzip(mux)
}

func defaultAgentAuthState() AgentAuthState {
	return AgentAuthState{
		Required: false,
		Ready:    true,
		State:    "ready",
		Message:  "Agent auth is ready.",
	}
}

func defaultHubSetupState() HubSetupState {
	state := HubSetupState{
		Configured:      false,
		AgentMode:       "existing",
		TokenType:       "agent",
		Region:          "na",
		ConnectURL:      "https://molten.bot/login?target=hub",
		DashboardURL:    "https://app.molten.bot/hub",
		Onboarding:      DefaultHubSetupOnboarding("existing"),
		OnboardingStage: "bind",
	}
	state.Profile.ProfileText = ""
	state.Profile.DisplayName = ""
	state.Profile.Emoji = ""
	return state
}

func DefaultHubSetupOnboarding(agentMode string) []HubSetupStep {
	steps := []HubSetupStep{
		{ID: "bind", Label: "Bind", Status: "pending"},
		{ID: "work_bind", Label: "Work", Status: "pending", Detail: "Resolve and verify Molten Hub credentials."},
		{ID: "profile_set", Label: "Profile Set", Status: "pending", Detail: "Persist the agent profile in Molten Hub."},
		{ID: "work_activate", Label: "Work", Status: "pending", Detail: "Apply the runtime transport and confirm activation."},
	}
	if strings.EqualFold(strings.TrimSpace(agentMode), "existing") {
		steps[0].Detail = "Verify the existing Molten Hub agent credential."
	} else {
		steps[0].Detail = "Exchange the bind token for an agent credential."
	}
	return steps
}

func (s Server) handleGitHubProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	profileURL, err := s.resolveGitHubProfileURL(r.Context())
	if err != nil {
		s.logf("hub.ui status=warn endpoint=github_profile err=%q", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"profileUrl": profileURL,
	})
}

func (s Server) handleGitHubRepos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	repos, err := s.resolveGitHubRepos(r.Context())
	if err != nil {
		s.logf("hub.ui status=warn endpoint=github_repos err=%q", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"repos": []GitHubRepo{},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"repos": repos,
	})
}

func (s Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data, err := fs.ReadFile(staticFiles, "static/index.html")
	if err != nil {
		http.Error(w, "monitor ui is unavailable", http.StatusInternalServerError)
		return
	}
	data = s.injectIndexConfig(data)
	data = s.injectBottomDockComponent(r.Context(), data)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s Server) injectBottomDockComponent(ctx context.Context, data []byte) []byte {
	if !bytes.Contains(data, []byte(bottomDockPlaceholder)) {
		return data
	}
	dock, err := fs.ReadFile(staticFiles, "static/bottom-dock.html")
	if err != nil {
		s.logf("hub.ui status=warn event=load_bottom_dock_component err=%q", err)
		return data
	}
	dock = s.applyBottomDockHubState(ctx, dock)
	return bytes.Replace(data, []byte(bottomDockPlaceholder), bytes.TrimSpace(dock), 1)
}

func (s Server) applyBottomDockHubState(ctx context.Context, dock []byte) []byte {
	state, err := s.renderHubSetupState(ctx)
	if err != nil {
		s.logf("hub.ui status=warn event=load_bottom_dock_hub_state err=%q", err)
	}

	configured := state.Configured
	connectURL := strings.TrimSpace(state.ConnectURL)
	if connectURL == "" {
		connectURL = defaultHubSetupState().ConnectURL
	}
	dashboardURL := strings.TrimSpace(state.DashboardURL)
	if dashboardURL == "" {
		dashboardURL = defaultHubSetupState().DashboardURL
	}
	hubTitle := "Configure Molten Hub"
	hubURL := connectURL
	hubLinkClass := "prompt-mode-link prompt-mode-link-logo"
	profileButtonAttr := " hidden"
	plusClass := "hub-dock-plus"
	if configured {
		hubTitle = "Open Molten Hub"
		hubURL = dashboardURL
		profileButtonAttr = ""
		plusClass = "hub-dock-plus hidden"
	}

	dock = bytes.Replace(dock,
		[]byte(`<div id="moltenbot-hub-dock-group" class="hub-dock-group" data-configured="false">`),
		[]byte(fmt.Sprintf(`<div id="moltenbot-hub-dock-group" class="hub-dock-group" data-configured="%t">`, configured)),
		1,
	)
	dock = bytes.Replace(dock,
		[]byte(`href="https://molten.bot/login?target=hub"`),
		[]byte(`href="`+template.HTMLEscapeString(hubURL)+`"`),
		1,
	)
	dock = bytes.Replace(dock,
		[]byte("id=\"moltenbot-hub-link\"\n        class=\"prompt-mode-link prompt-mode-link-logo\""),
		[]byte("id=\"moltenbot-hub-link\"\n        class=\""+template.HTMLEscapeString(hubLinkClass)+"\""),
		1,
	)
	dock = bytes.Replace(dock,
		[]byte(`aria-label="Configure Molten Hub"`),
		[]byte(`aria-label="`+template.HTMLEscapeString(hubTitle)+`"`),
		1,
	)
	dock = bytes.Replace(dock,
		[]byte(`title="Configure Molten Hub"`),
		[]byte(`title="`+template.HTMLEscapeString(hubTitle)+`"`),
		1,
	)
	dock = bytes.Replace(dock,
		[]byte(`<span id="moltenbot-hub-plus" class="hub-dock-plus hidden" aria-hidden="true">+</span>`),
		[]byte(`<span id="moltenbot-hub-plus" class="`+plusClass+`" aria-hidden="true">+</span>`),
		1,
	)
	dock = bytes.Replace(dock,
		[]byte("        hidden>\n"),
		[]byte(profileButtonAttr+">\n"),
		1,
	)
	return dock
}

func (s Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/chat" {
		http.NotFound(w, r)
		return
	}

	s.renderSitePage(r.Context(), w, sitePageData{
		Title:     "Molten Hub Code Chat",
		BodyClass: "chat-body",
		PageClass: "chat-page",
		MainClass: "chat-main",
		Heading:   "Chat",
		Content: template.HTML(`<section class="chat-shell" aria-labelledby="chat-title">
        <div class="chat-head">
          <div>
            <p class="eyebrow">GitHub</p>
            <h1 id="chat-title">Chat</h1>
          </div>
          <p id="chat-status" class="chat-status" aria-live="polite"></p>
        </div>
        <div id="chat-repo-grid" class="chat-repo-grid" aria-label="GitHub repositories"></div>
        <nav id="chat-repo-pagination" class="chat-repo-pagination hidden" aria-label="GitHub repository pages"></nav>
      </section>
      <script>
        (function initChatRepos() {
          const grid = document.getElementById("chat-repo-grid");
          const status = document.getElementById("chat-status");
          const pagination = document.getElementById("chat-repo-pagination");
          const CHAT_REPOS_PER_PAGE = 24;
          let repoPage = 1;
          if (!grid || !status) return;

          function repoRunValue(repo) {
            const htmlURL = String(repo.html_url || "").trim();
            if (htmlURL) return htmlURL;
            const fullName = String(repo.full_name || "").trim();
            if (fullName) return "https://github.com/" + fullName.replace(/^\/+/, "");
            return String(repo.name || "").trim();
          }

          async function submitRepoPrompt(repo, input, statusNode) {
            const prompt = String(input.value || "").trim();
            if (!prompt) {
              statusNode.textContent = "Prompt is required.";
              statusNode.dataset.tone = "error";
              input.focus();
              return;
            }
            const payload = {
              repo: repoRunValue(repo),
              prompt: prompt
            };
            const branch = String(repo.default_branch || "").trim();
            if (branch) {
              payload.baseBranch = branch;
            }
            statusNode.textContent = "Submitting...";
            statusNode.dataset.tone = "warn";
            try {
              const response = await fetch("/api/local-prompt", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify(payload)
              });
              let body = null;
              try {
                body = await response.json();
              } catch (_err) {
                body = null;
              }
              if (!response.ok) {
                throw new Error(body && body.error ? body.error : "submit http " + response.status);
              }
              const requestID = body && body.request_id ? body.request_id : "(unknown)";
              input.value = "";
              statusNode.textContent = "Queued request " + requestID;
              statusNode.dataset.tone = "ok";
            } catch (err) {
              statusNode.textContent = "Submit failed: " + (err && err.message ? err.message : "unknown error");
              statusNode.dataset.tone = "error";
            }
          }

          function repoCard(repo) {
            const card = document.createElement("div");
            card.className = "chat-repo-card";
            card.tabIndex = 0;
            card.setAttribute("role", "button");
            card.setAttribute("aria-expanded", "false");
            card.setAttribute("aria-label", "Open " + String(repo.full_name || repo.name || "repository") + " panel");

            const title = document.createElement("span");
            title.className = "chat-repo-card-title";
            title.textContent = String(repo.full_name || repo.name || "Unnamed repository");
            card.appendChild(title);

            const description = document.createElement("span");
            description.className = "chat-repo-card-description";
            description.textContent = String(repo.description || "No description.");
            card.appendChild(description);

            const meta = document.createElement("span");
            meta.className = "chat-repo-card-meta";
            const visibility = repo.private ? "Private" : "Public";
            const language = String(repo.language || "").trim();
            meta.textContent = language ? visibility + " | " + language : visibility;
            card.appendChild(meta);

            const panel = document.createElement("span");
            panel.className = "chat-repo-panel";

            const input = document.createElement("textarea");
            input.className = "chat-repo-prompt";
            input.rows = 3;
            input.placeholder = "Prompt for default branch";
            input.setAttribute("aria-label", "Prompt for " + String(repo.full_name || repo.name || "repository"));

            const panelStatus = document.createElement("span");
            panelStatus.className = "chat-repo-submit-status";
            panelStatus.setAttribute("aria-live", "polite");
            panelStatus.textContent = "Press Enter to run.";

            input.addEventListener("keydown", (event) => {
              if (event.key !== "Enter" || event.shiftKey) return;
              event.preventDefault();
              event.stopPropagation();
              void submitRepoPrompt(repo, input, panelStatus);
            });
            input.addEventListener("click", (event) => {
              event.stopPropagation();
            });
            input.addEventListener("pointerdown", (event) => {
              event.stopPropagation();
            });
            panel.append(input, panelStatus);
            card.appendChild(panel);

            card.addEventListener("click", () => {
              const nextOpen = card.getAttribute("aria-expanded") !== "true";
              grid.querySelectorAll(".chat-repo-card[aria-expanded='true']").forEach((node) => {
                if (node !== card) node.setAttribute("aria-expanded", "false");
              });
              card.setAttribute("aria-expanded", String(nextOpen));
              if (nextOpen) input.focus();
            });
            card.addEventListener("keydown", (event) => {
              if (event.target === input || (event.key !== "Enter" && event.key !== " ")) return;
              event.preventDefault();
              card.click();
            });

            return card;
          }

          function renderPagination(totalRepos, totalPages, renderPage) {
            if (!pagination) return;
            pagination.replaceChildren();
            pagination.classList.toggle("hidden", totalPages <= 1);
            if (totalPages <= 1) return;

            const previous = document.createElement("button");
            previous.className = "chat-repo-page-button";
            previous.type = "button";
            previous.textContent = "Previous";
            previous.disabled = repoPage <= 1;
            previous.addEventListener("click", () => {
              if (repoPage <= 1) return;
              repoPage -= 1;
              renderPage();
            });

            const label = document.createElement("span");
            label.className = "chat-repo-page-label";
            label.textContent = "Page " + repoPage + " of " + totalPages;

            const next = document.createElement("button");
            next.className = "chat-repo-page-button";
            next.type = "button";
            next.textContent = "Next";
            next.disabled = repoPage >= totalPages || totalRepos === 0;
            next.addEventListener("click", () => {
              if (repoPage >= totalPages) return;
              repoPage += 1;
              renderPage();
            });

            pagination.append(previous, label, next);
          }

          function renderRepos(repos) {
            const totalPages = Math.max(1, Math.ceil(repos.length / CHAT_REPOS_PER_PAGE));
            repoPage = Math.min(Math.max(1, repoPage), totalPages);
            const start = (repoPage - 1) * CHAT_REPOS_PER_PAGE;
            const pageRepos = repos.slice(start, start + CHAT_REPOS_PER_PAGE);
            grid.replaceChildren(...pageRepos.map(repoCard));
            if (repos.length === 0) {
              status.textContent = "0 repositories";
              const empty = document.createElement("p");
              empty.className = "chat-empty";
              empty.textContent = "No repositories found.";
              grid.appendChild(empty);
            } else if (repos.length <= CHAT_REPOS_PER_PAGE) {
              status.textContent = repos.length === 1 ? "1 repository" : repos.length + " repositories";
            } else {
              const end = start + pageRepos.length;
              status.textContent = (start + 1) + "-" + end + " of " + repos.length + " repositories";
            }
            renderPagination(repos.length, totalPages, () => renderRepos(repos));
          }

          async function loadRepos() {
            try {
              const response = await fetch("/api/github/repos", { cache: "no-store" });
              const body = await response.json();
              if (!response.ok || !body || body.ok === false) {
                throw new Error(body && body.error ? body.error : "github repositories request failed");
              }
              const repos = Array.isArray(body.repos) ? body.repos : [];
              renderRepos(repos);
            } catch (err) {
              status.textContent = err && err.message ? err.message : "Unable to load repositories.";
            }
          }

          void loadRepos();
        })();
      </script>`),
	})
}

func (s Server) renderSitePage(ctx context.Context, w http.ResponseWriter, data sitePageData) {
	data.BottomDock = template.HTML(bottomDockPlaceholder)
	var page bytes.Buffer
	if err := sitePageTemplate.Execute(&page, data); err != nil {
		s.logf("hub.ui status=warn event=render_site_page err=%q", err)
		http.Error(w, "page is unavailable", http.StatusInternalServerError)
		return
	}
	rendered := s.injectBottomDockComponent(ctx, page.Bytes())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(rendered)
}

func (s Server) injectIndexConfig(data []byte) []byte {
	type indexConfig struct {
		AutomaticMode        bool         `json:"automaticMode"`
		ConfiguredHarness    string       `json:"configuredHarness"`
		ConfiguredAgentLabel string       `json:"configuredAgentLabel"`
		DefaultRepository    string       `json:"defaultRepository"`
		PromptImageHarnesses []string     `json:"promptImageHarnesses"`
		GitHubReposReady     bool         `json:"githubReposReady"`
		GitHubRepos          []GitHubRepo `json:"githubRepos,omitempty"`
	}
	configuredHarness := strings.TrimSpace(s.ConfiguredHarness)
	configuredAgentLabel := ""
	if configuredHarness != "" {
		configuredAgentLabel = agentruntime.DisplayName(configuredHarness)
	}
	s.preloadGitHubRepos()
	repos, reposReady := s.cachedGitHubRepos()
	cfg, err := json.Marshal(indexConfig{
		AutomaticMode:        s.AutomaticMode,
		ConfiguredHarness:    configuredHarness,
		ConfiguredAgentLabel: configuredAgentLabel,
		DefaultRepository:    config.DefaultRepositoryURL,
		PromptImageHarnesses: agentruntime.SupportedPromptImageHarnesses(),
		GitHubReposReady:     reposReady,
		GitHubRepos:          repos,
	})
	if err != nil {
		s.logf("hub.ui status=warn event=marshal_index_config err=%q", err)
		return data
	}

	return bytes.Replace(
		data,
		[]byte(`window.__HUB_UI_CONFIG__ = {"automaticMode":false,"configuredHarness":"","configuredAgentLabel":"","defaultRepository":"git@github.com:Molten-Bot/moltenhub-code.git","promptImageHarnesses":["codex","pi"],"githubReposReady":false};`),
		[]byte("window.__HUB_UI_CONFIG__ = "+string(cfg)+";"),
		1,
	)
}

func (s Server) handleState(w http.ResponseWriter, _ *http.Request) {
	if s.Broker == nil {
		http.Error(w, "monitor broker is unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, s.snapshot())
}

func (s Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.LoadLibraryTasks == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    true,
			"tasks": []library.TaskSummary{},
		})
		return
	}

	tasks, err := s.LoadLibraryTasks()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("load library tasks: %v", err),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"tasks": tasks,
	})
}

func (s Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if s.Broker == nil {
		http.Error(w, "monitor broker is unavailable", http.StatusServiceUnavailable)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	updates, cancel := s.Broker.Subscribe()
	defer cancel()
	lastSnapshotAt := time.Now()
	var snapshotTimer *time.Timer
	var snapshotTimerCh <-chan time.Time

	writeSSESnapshot := func() bool {
		payload, err := json.Marshal(compactStreamSnapshot(s.snapshot()))
		if err != nil {
			s.logf("hub.ui status=warn event=marshal_snapshot err=%q", err)
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		lastSnapshotAt = time.Now()
		return true
	}
	stopSnapshotTimer := func() {
		if snapshotTimer == nil {
			return
		}
		if !snapshotTimer.Stop() {
			select {
			case <-snapshotTimer.C:
			default:
			}
		}
		snapshotTimer = nil
		snapshotTimerCh = nil
	}
	scheduleSnapshot := func() {
		if snapshotTimer != nil {
			return
		}
		wait := streamSnapshotInterval - time.Since(lastSnapshotAt)
		if wait < 0 {
			wait = 0
		}
		snapshotTimer = time.NewTimer(wait)
		snapshotTimerCh = snapshotTimer.C
	}
	defer stopSnapshotTimer()

	if !writeSSESnapshot() {
		return
	}

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates:
			if time.Since(lastSnapshotAt) >= streamSnapshotInterval {
				stopSnapshotTimer()
				if !writeSSESnapshot() {
					return
				}
				continue
			}
			scheduleSnapshot()
		case <-snapshotTimerCh:
			stopSnapshotTimer()
			if !writeSSESnapshot() {
				return
			}
		case <-keepalive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s Server) snapshot() Snapshot {
	if s.Broker == nil {
		return Snapshot{}
	}

	snapshot := s.Broker.Snapshot()
	if s.ResolveTaskControls == nil {
		return snapshot
	}

	for i := range snapshot.Tasks {
		snapshot.Tasks[i].Controls = s.ResolveTaskControls(snapshot.Tasks[i].RequestID)
	}
	return snapshot
}

func (s Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s Server) currentHubSetupState(ctx context.Context) (HubSetupState, error) {
	state := defaultHubSetupState()
	if s.HubSetupStatus == nil {
		return state, nil
	}
	next, err := s.HubSetupStatus(ctx)
	if strings.TrimSpace(next.ConnectURL) == "" {
		next.ConnectURL = state.ConnectURL
	}
	if strings.TrimSpace(next.DashboardURL) == "" {
		next.DashboardURL = state.DashboardURL
	}
	if strings.TrimSpace(next.AgentMode) == "" {
		next.AgentMode = state.AgentMode
	}
	if strings.TrimSpace(next.TokenType) == "" {
		next.TokenType = state.TokenType
	}
	return next, err
}

func (s Server) renderHubSetupState(ctx context.Context) (HubSetupState, error) {
	if s.RenderHubSetupStatus == nil {
		return s.currentHubSetupState(ctx)
	}
	state := defaultHubSetupState()
	next, err := s.RenderHubSetupStatus(ctx)
	if strings.TrimSpace(next.ConnectURL) == "" {
		next.ConnectURL = state.ConnectURL
	}
	if strings.TrimSpace(next.DashboardURL) == "" {
		next.DashboardURL = state.DashboardURL
	}
	if strings.TrimSpace(next.AgentMode) == "" {
		next.AgentMode = state.AgentMode
	}
	if strings.TrimSpace(next.TokenType) == "" {
		next.TokenType = state.TokenType
	}
	return next, err
}

func (s Server) handleHubSetup(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state, err := s.currentHubSetupState(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": fmt.Sprintf("load hub setup state: %v", err),
				"hub":   state,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":  true,
			"hub": state,
		})
	case http.MethodPost:
		if s.ConfigureHubSetup == nil {
			writeJSON(w, http.StatusNotImplemented, map[string]any{
				"ok":    false,
				"error": "hub setup is unavailable",
				"hub":   defaultHubSetupState(),
			})
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxHubSetupConfigureBodyBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": fmt.Sprintf("read request body: %v", err),
				"hub":   defaultHubSetupState(),
			})
			return
		}

		var req HubSetupRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": fmt.Sprintf("decode request body: %v", err),
				"hub":   defaultHubSetupState(),
			})
			return
		}

		state, err := s.ConfigureHubSetup(r.Context(), req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
				"hub":   state,
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":  true,
			"hub": state,
		})
	default:
		w.Header().Set("Allow", strings.Join([]string{http.MethodGet, http.MethodPost}, ", "))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s Server) handleHubSetupConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.ConnectHubSetup == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "hub connect is unavailable",
			"hub":   defaultHubSetupState(),
		})
		return
	}

	state, err := s.ConnectHubSetup(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"hub":   state,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":  true,
		"hub": state,
	})
}

func (s Server) handleHubSetupDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.DisconnectHubSetup == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "hub disconnect is unavailable",
			"hub":   defaultHubSetupState(),
		})
		return
	}

	state, err := s.DisconnectHubSetup(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"hub":   state,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":  true,
		"hub": state,
	})
}

func (s Server) handleAgentAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state, err := s.currentAgentAuthState(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("load agent auth state: %v", err),
			"auth":  state,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"auth": state,
	})
}

func (s Server) handleAgentAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.StartAgentAuth == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "agent device auth is unavailable",
			"auth":  defaultAgentAuthState(),
		})
		return
	}
	state, err := s.StartAgentAuth(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"auth":  state,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"auth": state,
	})
}

func (s Server) handleAgentAuthVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.logf("hub.ui status=start endpoint=agent_auth_verify")
	if s.VerifyAgentAuth == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "agent auth verification is unavailable",
			"auth":  defaultAgentAuthState(),
		})
		return
	}
	state, err := s.VerifyAgentAuth(r.Context())
	if err != nil {
		s.logf("hub.ui status=error endpoint=agent_auth_verify state=%s err=%q", strings.TrimSpace(state.State), err)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"auth":  state,
		})
		return
	}
	s.logf("hub.ui status=ok endpoint=agent_auth_verify state=%s ready=%t", strings.TrimSpace(state.State), state.Ready)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"auth": state,
	})
}

type agentAuthConfigureRequest struct {
	AugmentSessionAuth      string `json:"augment_session_auth"`
	AugmentSessionAuthAlias string `json:"augmentSessionAuth"`
	PiAuthJSON              string `json:"pi_auth_json"`
	PiAuthJSONAlias         string `json:"piAuthJSON"`
	PiProviderAuth          string `json:"pi_provider_auth"`
	PiProviderAuthAlias     string `json:"piProviderAuth"`
	SessionAuth             string `json:"session_auth"`
	SessionAuthAlias        string `json:"sessionAuth"`
	GitHubToken             string `json:"github_token"`
	GitHubTokenAlias        string `json:"githubToken"`
	ClaudeAuthCode          string `json:"claude_auth_code"`
	ClaudeAuthCodeAlias     string `json:"claudeAuthCode"`
	Value                   string `json:"value"`
}

func (s Server) handleAgentAuthConfigure(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.logf("hub.ui status=start endpoint=agent_auth_configure")
	if s.ConfigureAgentAuth == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "agent auth configure is unavailable",
			"auth":  defaultAgentAuthState(),
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxAgentAuthConfigureBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("read request body: %v", err),
			"auth":  defaultAgentAuthState(),
		})
		return
	}

	var req agentAuthConfigureRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("decode request body: %v", err),
			"auth":  defaultAgentAuthState(),
		})
		return
	}

	sessionAuth := firstNonEmptyString(
		req.AugmentSessionAuth,
		req.AugmentSessionAuthAlias,
		req.PiAuthJSON,
		req.PiAuthJSONAlias,
		req.PiProviderAuth,
		req.PiProviderAuthAlias,
		req.SessionAuth,
		req.SessionAuthAlias,
		req.GitHubToken,
		req.GitHubTokenAlias,
		req.ClaudeAuthCode,
		req.ClaudeAuthCodeAlias,
		req.Value,
	)
	if sessionAuth == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "configure value is required",
			"auth":  defaultAgentAuthState(),
		})
		return
	}

	state, err := s.ConfigureAgentAuth(r.Context(), sessionAuth)
	if err != nil {
		s.logf("hub.ui status=error endpoint=agent_auth_configure state=%s err=%q", strings.TrimSpace(state.State), err)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"auth":  state,
		})
		return
	}
	s.logf("hub.ui status=ok endpoint=agent_auth_configure state=%s ready=%t", strings.TrimSpace(state.State), state.Ready)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"auth": state,
	})
}

func (s Server) currentAgentAuthState(ctx context.Context) (AgentAuthState, error) {
	if s.AgentAuthStatus == nil {
		return defaultAgentAuthState(), nil
	}
	state, err := s.AgentAuthStatus(ctx)
	if strings.TrimSpace(state.State) == "" {
		if state.Ready {
			state.State = "ready"
		} else {
			state.State = "needs_device_auth"
		}
	}
	return state, err
}

func (s Server) resolveGitHubProfileURL(ctx context.Context) (string, error) {
	if s.ResolveGitHubProfileURL != nil {
		return s.ResolveGitHubProfileURL(ctx)
	}
	return resolveAuthenticatedGitHubProfileURL(ctx, http.DefaultClient)
}

func githubTokenFromEnv() (string, error) {
	token := strings.TrimSpace(os.Getenv("GH_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if token == "" {
		return "", fmt.Errorf("github token is not configured")
	}
	return token, nil
}

func newGitHubAPIRequest(ctx context.Context, method, endpoint string) (*http.Request, error) {
	token, err := githubTokenFromEnv()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "moltenhub-code")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return req, nil
}

func (s Server) resolveGitHubRepos(ctx context.Context) ([]GitHubRepo, error) {
	if s.gitHubRepos != nil {
		return s.gitHubRepos.resolve(ctx, func(loadCtx context.Context) ([]GitHubRepo, error) {
			return s.loadGitHubRepos(loadCtx)
		})
	}
	return s.loadGitHubRepos(ctx)
}

func (s Server) loadGitHubRepos(ctx context.Context) ([]GitHubRepo, error) {
	if s.ResolveGitHubRepos != nil {
		return s.ResolveGitHubRepos(ctx)
	}
	return resolveAuthenticatedGitHubRepos(ctx, http.DefaultClient)
}

func (s Server) cachedGitHubRepos() ([]GitHubRepo, bool) {
	if s.gitHubRepos == nil {
		return nil, false
	}
	return s.gitHubRepos.snapshot()
}

func (s Server) preloadGitHubRepos() {
	if s.gitHubRepos == nil || s.gitHubRepos.active() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := s.resolveGitHubRepos(ctx); err != nil {
			s.logf("hub.ui status=warn event=preload_github_repos err=%q", err)
		}
	}()
}

func (c *gitHubRepoCache) snapshot() ([]GitHubRepo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		return nil, false
	}
	return append([]GitHubRepo(nil), c.repos...), true
}

func (c *gitHubRepoCache) active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loaded || c.loading
}

func (c *gitHubRepoCache) resolve(ctx context.Context, load func(context.Context) ([]GitHubRepo, error)) ([]GitHubRepo, error) {
	c.mu.Lock()
	if c.loaded {
		defer c.mu.Unlock()
		return append([]GitHubRepo(nil), c.repos...), nil
	}
	if c.loading {
		wait := c.wait
		c.mu.Unlock()
		select {
		case <-wait:
			return c.resolve(ctx, load)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c.loading = true
	c.wait = make(chan struct{})
	wait := c.wait
	c.mu.Unlock()

	repos, err := load(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loading = false
	close(wait)
	if err != nil {
		return nil, err
	}
	c.repos = append([]GitHubRepo(nil), repos...)
	c.loaded = true
	return append([]GitHubRepo(nil), c.repos...), nil
}

func resolveAuthenticatedGitHubProfileURL(ctx context.Context, client *http.Client) (string, error) {
	if _, err := githubTokenFromEnv(); err != nil {
		return "", err
	}
	if client == nil {
		client = http.DefaultClient
	}

	req, err := newGitHubAPIRequest(ctx, http.MethodGet, "https://api.github.com/user")
	if err != nil {
		return "", fmt.Errorf("build github profile request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("load github profile: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		Login   string `json:"login"`
		HTMLURL string `json:"html_url"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("decode github profile: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		message := strings.TrimSpace(body.Message)
		if message == "" {
			message = fmt.Sprintf("github api status=%d", resp.StatusCode)
		}
		return "", fmt.Errorf("github profile lookup failed: %s", message)
	}
	if profileURL := strings.TrimSpace(body.HTMLURL); profileURL != "" {
		return profileURL, nil
	}
	if login := strings.TrimSpace(body.Login); login != "" {
		return "https://github.com/" + login, nil
	}
	return "", fmt.Errorf("github profile lookup failed: missing profile url")
}

func resolveAuthenticatedGitHubRepos(ctx context.Context, client *http.Client) ([]GitHubRepo, error) {
	if client == nil {
		client = http.DefaultClient
	}

	var repos []GitHubRepo
	nextURL := "https://api.github.com/user/repos?per_page=100&affiliation=owner,collaborator,organization_member&sort=pushed"
	for nextURL != "" {
		req, err := newGitHubAPIRequest(ctx, http.MethodGet, nextURL)
		if err != nil {
			return nil, fmt.Errorf("build github repositories request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("load github repositories: %w", err)
		}

		bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read github repositories response: %w", readErr)
		}
		if resp.StatusCode/100 != 2 {
			var errBody struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(bodyBytes, &errBody)
			message := strings.TrimSpace(errBody.Message)
			if message == "" {
				message = fmt.Sprintf("github api status=%d", resp.StatusCode)
			}
			return nil, fmt.Errorf("github repositories lookup failed: %s", message)
		}

		var body []struct {
			Name          string `json:"name"`
			FullName      string `json:"full_name"`
			Description   string `json:"description"`
			HTMLURL       string `json:"html_url"`
			DefaultBranch string `json:"default_branch"`
			Private       bool   `json:"private"`
			Language      string `json:"language"`
			UpdatedAt     string `json:"updated_at"`
			PushedAt      string `json:"pushed_at"`
		}
		decodeErr := json.Unmarshal(bodyBytes, &body)
		if decodeErr != nil && !errors.Is(decodeErr, io.EOF) {
			return nil, fmt.Errorf("decode github repositories: %w", decodeErr)
		}
		for _, repo := range body {
			repos = append(repos, GitHubRepo{
				Name:          strings.TrimSpace(repo.Name),
				FullName:      strings.TrimSpace(repo.FullName),
				Description:   strings.TrimSpace(repo.Description),
				HTMLURL:       strings.TrimSpace(repo.HTMLURL),
				DefaultBranch: strings.TrimSpace(repo.DefaultBranch),
				Private:       repo.Private,
				Language:      strings.TrimSpace(repo.Language),
				UpdatedAt:     strings.TrimSpace(repo.UpdatedAt),
				PushedAt:      strings.TrimSpace(repo.PushedAt),
			})
		}
		nextURL = nextGitHubPageURL(resp.Header.Get("Link"))
	}
	sortGitHubReposByActivity(repos)
	return repos, nil
}

func sortGitHubReposByActivity(repos []GitHubRepo) {
	sort.SliceStable(repos, func(i, j int) bool {
		left := githubRepoActivityTime(repos[i])
		right := githubRepoActivityTime(repos[j])
		if !left.Equal(right) {
			return left.After(right)
		}
		return strings.ToLower(repos[i].FullName) < strings.ToLower(repos[j].FullName)
	})
}

func githubRepoActivityTime(repo GitHubRepo) time.Time {
	for _, value := range []string{repo.PushedAt, repo.UpdatedAt} {
		if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(value)); err == nil {
			return ts
		}
	}
	return time.Time{}
}

func nextGitHubPageURL(linkHeader string) string {
	for _, part := range strings.Split(linkHeader, ",") {
		section := strings.TrimSpace(part)
		if !strings.Contains(section, `rel="next"`) {
			continue
		}
		start := strings.Index(section, "<")
		end := strings.Index(section, ">")
		if start >= 0 && end > start+1 {
			return strings.TrimSpace(section[start+1 : end])
		}
	}
	return ""
}

func (s Server) handleLocalPrompt(w http.ResponseWriter, r *http.Request) {
	s.handlePromptSubmit(w, r, s.SubmitLocalPrompt, "studio submit is unavailable")
}

func (s Server) handleLibraryRun(w http.ResponseWriter, r *http.Request) {
	s.handlePromptSubmit(w, r, s.SubmitLocalPrompt, "library task submit is unavailable")
}

func (s Server) handlePromptSubmit(
	w http.ResponseWriter,
	r *http.Request,
	submit func(context.Context, []byte) (string, error),
	unavailableMessage string,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if submit == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": unavailableMessage,
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxLocalPromptBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("read request body: %v", err),
		})
		return
	}

	requestID, err := submit(r.Context(), body)
	if err != nil {
		if duplicateRequestID, duplicateState, ok := duplicateSubmissionDetails(err); ok {
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":         false,
				"error":      err.Error(),
				"duplicate":  true,
				"request_id": duplicateRequestID,
				"state":      duplicateState,
			})
			return
		}
		if s.Broker != nil {
			s.Broker.RecordRejectedPromptSubmission(body, "invalid", err)
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":         true,
		"request_id": requestID,
	})
}

func (s Server) handleTaskAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	if path == r.URL.Path || path == "" {
		http.NotFound(w, r)
		return
	}
	action := ""
	switch {
	case strings.HasSuffix(path, "/rerun"):
		action = "rerun"
	case strings.HasSuffix(path, "/close"):
		action = "close"
	case strings.HasSuffix(path, "/pause"):
		action = "pause"
	case strings.HasSuffix(path, "/run"):
		action = "run"
	case strings.HasSuffix(path, "/stop"):
		action = "stop"
	default:
		http.NotFound(w, r)
		return
	}

	requestID := strings.TrimSuffix(path, "/"+action)
	requestID = strings.TrimSuffix(requestID, "/")
	decoded, err := url.PathUnescape(requestID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "invalid request id",
		})
		return
	}
	decoded = strings.TrimSpace(decoded)
	if decoded == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "request id is required",
		})
		return
	}

	switch action {
	case "rerun":
		s.handleTaskRerun(w, r, decoded)
	case "close":
		s.handleTaskClose(w, r, decoded)
	case "pause":
		s.handleTaskControl(w, r, decoded, "pause", "paused", s.PauseTask)
	case "run":
		runHandler := s.RunTask
		force := parseTruthyQueryParam(r.URL.Query().Get("force"))
		if force && s.ForceRunTask != nil {
			runHandler = s.ForceRunTask
		}
		s.handleTaskControlWithMeta(
			w,
			r,
			decoded,
			"run",
			"running",
			runHandler,
			map[string]any{
				"forced": force,
			},
		)
	case "stop":
		s.handleTaskControl(w, r, decoded, "stop", "stopped", s.StopTask)
	default:
		http.NotFound(w, r)
	}
}

func (s Server) handleTaskControl(
	w http.ResponseWriter,
	r *http.Request,
	requestID string,
	action string,
	status string,
	handler func(context.Context, string) error,
) {
	s.handleTaskControlWithMeta(w, r, requestID, action, status, handler, nil)
}

func (s Server) handleTaskControlWithMeta(
	w http.ResponseWriter,
	r *http.Request,
	requestID string,
	action string,
	status string,
	handler func(context.Context, string) error,
	meta map[string]any,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if handler == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("task %s is unavailable", action),
		})
		return
	}
	if err := handler(r.Context(), requestID); err != nil {
		switch {
		case errors.Is(err, ErrTaskNotFound):
			statusCode, errText := s.taskControlUnavailableResponse(requestID, action)
			if statusCode != 0 {
				writeJSON(w, statusCode, map[string]any{
					"ok":    false,
					"error": errText,
				})
				return
			}
			writeJSON(w, http.StatusNotFound, map[string]any{
				"ok":    false,
				"error": "task not found",
			})
		default:
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
		}
		return
	}
	response := map[string]any{
		"ok":         true,
		"request_id": requestID,
		"action":     action,
		"status":     status,
	}
	for key, value := range meta {
		response[key] = value
	}
	writeJSON(w, http.StatusOK, response)
}

func (s Server) taskControlUnavailableResponse(requestID string, action string) (int, string) {
	if s.Broker == nil {
		return 0, ""
	}

	task, ok := s.Broker.Task(requestID)
	if !ok {
		return 0, ""
	}

	switch normalizeTaskTerminalStatus(task.Status) {
	case "completed", "no_changes", "duplicate":
		return http.StatusConflict, "task is already completed"
	case "stopped":
		return http.StatusConflict, "task is already stopped"
	case "error", "invalid":
		return http.StatusConflict, "task is already finished"
	}

	action = strings.TrimSpace(action)
	if action == "" {
		action = "control"
	}
	return http.StatusConflict, fmt.Sprintf("task %s is unavailable for this task", action)
}

func (s Server) handleTaskRerun(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Broker == nil {
		http.Error(w, "monitor broker is unavailable", http.StatusServiceUnavailable)
		return
	}
	if s.SubmitLocalPrompt == nil && s.SubmitTaskRerun == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "task rerun is unavailable",
		})
		return
	}

	runConfigJSON, ok := s.Broker.TaskRunConfig(requestID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"ok":    false,
			"error": "run config for task is unavailable",
		})
		return
	}

	force := parseTruthyQueryParam(r.URL.Query().Get("force"))

	submit := func(ctx context.Context, body []byte) (string, error) {
		return s.SubmitLocalPrompt(ctx, body)
	}
	if s.SubmitTaskRerun != nil {
		submit = func(ctx context.Context, body []byte) (string, error) {
			return s.SubmitTaskRerun(ctx, requestID, body, force)
		}
	}

	sourceTask, sourceTaskFound := s.Broker.Task(requestID)
	newRequestID, err := submit(r.Context(), runConfigJSON)
	if err != nil {
		if duplicateRequestID, duplicateState, ok := duplicateSubmissionDetails(err); ok {
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":           false,
				"error":        err.Error(),
				"duplicate":    true,
				"request_id":   duplicateRequestID,
				"state":        duplicateState,
				"duplicate_of": requestID,
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	s.Broker.RecordTaskRerunAttempt(requestID, newRequestID)
	if sourceTaskFound && isCompletedTaskStatus(sourceTask.Status) {
		if err := s.Broker.CloseTask(requestID); err != nil {
			s.logf("hub.ui status=warn event=task_rerun_close_source request_id=%s rerun_request_id=%s err=%q", requestID, newRequestID, err)
		}
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":         true,
		"forced":     force,
		"request_id": newRequestID,
		"rerun_of":   requestID,
	})
}

func (s Server) handleTaskClose(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Broker == nil {
		http.Error(w, "monitor broker is unavailable", http.StatusServiceUnavailable)
		return
	}

	if err := s.Broker.CloseTask(requestID); err != nil {
		switch {
		case errors.Is(err, ErrTaskNotFound):
			writeJSON(w, http.StatusNotFound, map[string]any{
				"ok":    false,
				"error": "task not found",
			})
		case errors.Is(err, ErrTaskNotCompleted):
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":    false,
				"error": "task is not completed",
			})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
		}
		return
	}

	if s.CloseTask != nil {
		if err := s.CloseTask(r.Context(), requestID); err != nil {
			s.logf("hub.ui status=warn event=task_close_cleanup request_id=%s err=%q", requestID, err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"request_id": requestID,
		"closed":     true,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func compactStreamSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Events = nil
	for i := range snapshot.Tasks {
		logs := snapshot.Tasks[i].Logs
		if len(logs) <= maxStreamTaskLogs {
			continue
		}
		snapshot.Tasks[i].Logs = logs[len(logs)-maxStreamTaskLogs:]
	}
	return snapshot
}

func withCacheControl(next http.Handler, cacheControl string) http.Handler {
	if next == nil {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	}
	cacheControl = strings.TrimSpace(cacheControl)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cacheControl != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			w.Header().Set("Cache-Control", cacheControl)
		}
		next.ServeHTTP(w, r)
	})
}

func withGzip(next http.Handler) http.Handler {
	if next == nil {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requestWantsGzip(r) || !isCompressiblePath(r.URL.Path) || strings.HasPrefix(r.URL.Path, "/api/stream") {
			next.ServeHTTP(w, r)
			return
		}
		gz := gzip.NewWriter(w)
		defer func() {
			_ = gz.Close()
		}()
		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, writer: gz}, r)
	})
}

func requestWantsGzip(r *http.Request) bool {
	if r == nil {
		return false
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip")
}

func isCompressiblePath(path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	if path == "/" || strings.HasPrefix(path, "/api/") {
		return true
	}
	return strings.HasSuffix(path, ".css") ||
		strings.HasSuffix(path, ".js") ||
		strings.HasSuffix(path, ".html") ||
		strings.HasSuffix(path, ".svg") ||
		strings.HasSuffix(path, ".json")
}

type gzipResponseWriter struct {
	http.ResponseWriter
	writer      *gzip.Writer
	wroteHeader bool
}

func (w *gzipResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	headers := w.ResponseWriter.Header()
	if strings.TrimSpace(headers.Get("Content-Encoding")) == "" {
		headers.Set("Content-Encoding", "gzip")
	}
	addVaryHeader(headers, "Accept-Encoding")
	headers.Del("Content-Length")
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *gzipResponseWriter) Write(payload []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.writer.Write(payload)
}

func (w *gzipResponseWriter) Flush() {
	if w.writer != nil {
		_ = w.writer.Flush()
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func addVaryHeader(header http.Header, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	existing := header.Values("Vary")
	for _, current := range existing {
		for _, token := range strings.Split(current, ",") {
			if strings.EqualFold(strings.TrimSpace(token), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}

func (s Server) logf(format string, args ...any) {
	if s.Logf == nil {
		return
	}
	s.Logf(format, args...)
}

type duplicateSubmission interface {
	error
	DuplicateRequestID() string
	DuplicateState() string
}

func duplicateSubmissionDetails(err error) (requestID string, state string, ok bool) {
	if err == nil {
		return "", "", false
	}
	var duplicateErr duplicateSubmission
	if !errors.As(err, &duplicateErr) {
		return "", "", false
	}
	return strings.TrimSpace(duplicateErr.DuplicateRequestID()), strings.TrimSpace(duplicateErr.DuplicateState()), true
}

func parseTruthyQueryParam(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	default:
		return false
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
