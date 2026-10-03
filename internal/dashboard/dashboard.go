package dashboard

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/tiagoboas/antigravity-operator/internal/analytics"
	"github.com/tiagoboas/antigravity-operator/internal/doctor"
	"github.com/tiagoboas/antigravity-operator/internal/platform"
	"github.com/tiagoboas/antigravity-operator/internal/profile"
	"github.com/tiagoboas/antigravity-operator/internal/session"
	"github.com/tiagoboas/antigravity-operator/internal/watcher"
)

//go:embed dashboard.html
var dashboardHTML string

// Config configura o servidor do dashboard local.
type Config struct {
	Port         int
	TargetDir    string
	PlatformInfo *platform.Info
	OpenBrowser  bool
}

// Server encapsula o servidor HTTP do dashboard.
type Server struct {
	cfg      Config
	server   *http.Server
	listener net.Listener

	analyticsMu     sync.Mutex
	cachedAnalytics *analytics.SessionAnalytics
	cachedMtime     time.Time
	cachedSize      int64
	cachedPath      string
}

// ConsolidatedData agrupa todos os dados para consumo da UI em 1 requisição.
type ConsolidatedData struct {
	Timestamp time.Time                   `json:"timestamp"`
	HostDir   string                      `json:"host_dir"`
	Session   map[string]interface{}      `json:"session"`
	Doctor    map[string]interface{}      `json:"doctor"`
	Tabs      []profile.Tab               `json:"tabs"`
	Events    []string                    `json:"events"`
	Analytics *analytics.SessionAnalytics `json:"analytics,omitempty"`
}

// NewServer inicializa o servidor de dashboard com suas rotas.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Port < 0 {
		cfg.Port = 8080
	}
	if cfg.TargetDir == "" {
		cfg.TargetDir = "."
	}
	if cfg.PlatformInfo == nil {
		info, err := platform.Detect()
		if err != nil {
			info = &platform.Info{OS: runtime.GOOS, Arch: runtime.GOARCH}
		}
		cfg.PlatformInfo = info
	}

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("falha ao abrir porta %d para o dashboard: %w", cfg.Port, err)
	}

	mux := http.NewServeMux()
	s := &Server{
		cfg:      cfg,
		listener: listener,
	}

	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/all", s.handleAll)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/doctor", s.handleDoctor)
	mux.HandleFunc("/api/tabs", s.handleTabs)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/analytics", s.handleAnalytics)

	s.server = &http.Server{
		Handler:      s.securityMiddleware(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	return s, nil
}

func (s *Server) securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "localhost" && host != "127.0.0.1" {
			http.Error(w, "Forbidden: invalid host header", http.StatusForbidden)
			return
		}

		origin := r.Header.Get("Origin")
		if origin != "" {
			u, err := url.Parse(origin)
			if err != nil || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
				http.Error(w, "Forbidden: cross-origin access forbidden", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// Addr retorna o endereço TCP resolvido do listener.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

// Start inicia a escuta HTTP bloqueante.
func (s *Server) Start() error {
	url := fmt.Sprintf("http://%s", s.Addr())
	fmt.Printf("🚀 Antigravity Operator Dashboard ativo em: %s\n", url)
	fmt.Println("📊 Pressione Ctrl+C para encerrar o servidor.")

	if s.cfg.OpenBrowser {
		go OpenBrowser(url)
	}

	err := s.server.Serve(s.listener)
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown desliga o servidor graciosamente.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// OpenBrowser tenta abrir a URL no navegador padrão do sistema operacional.
func OpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(dashboardHTML))
}

func (s *Server) handleAll(w http.ResponseWriter, r *http.Request) {
	hostCwd, _ := os.Getwd()
	if s.cfg.TargetDir != "" && s.cfg.TargetDir != "." {
		if abs, err := filepath.Abs(s.cfg.TargetDir); err == nil {
			hostCwd = abs
		} else {
			hostCwd = s.cfg.TargetDir
		}
	}

	data := ConsolidatedData{
		Timestamp: time.Now(),
		HostDir:   hostCwd,
		Session:   s.getSessionData(),
		Doctor:    s.getDoctorData(),
		Tabs:      s.getTabsData(),
		Events:    s.getEventsData(),
		Analytics: s.getAnalyticsData(),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.getSessionData())
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.getDoctorData())
}

func (s *Server) handleTabs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.getTabsData())
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.getEventsData())
}

func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	data := s.getAnalyticsData()
	if data == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no active transcript found"}`))
		return
	}
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) getAnalyticsData() *analytics.SessionAnalytics {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	geminiDir := filepath.Join(home, ".gemini")
	tInfo, err := watcher.FindActiveTranscript(geminiDir)
	if err != nil {
		return nil
	}

	fi, err := os.Stat(tInfo.Path)
	if err != nil {
		return nil
	}

	s.analyticsMu.Lock()
	defer s.analyticsMu.Unlock()

	if s.cachedAnalytics != nil && s.cachedPath == tInfo.Path && fi.ModTime().Equal(s.cachedMtime) && fi.Size() == s.cachedSize {
		return s.cachedAnalytics
	}

	sa, err := analytics.AnalyzeTranscript(tInfo.Path)
	if err != nil {
		return nil
	}
	sa.ConversationID = tInfo.ConversationID
	// Cap tool calls for dashboard payload to latest 100 (newest first) while keeping full session aggregate metrics
	if len(sa.ToolCalls) > 100 {
		sa.ToolCalls = sa.ToolCalls[len(sa.ToolCalls)-100:]
	}
	for i, j := 0, len(sa.ToolCalls)-1; i < j; i, j = i+1, j-1 {
		sa.ToolCalls[i], sa.ToolCalls[j] = sa.ToolCalls[j], sa.ToolCalls[i]
	}

	s.cachedAnalytics = sa
	s.cachedMtime = fi.ModTime()
	s.cachedSize = fi.Size()
	s.cachedPath = tInfo.Path

	return sa
}

func (s *Server) getSessionData() map[string]interface{} {
	resp := map[string]interface{}{
		"active": false,
	}

	sum, err := session.GetSummary(s.cfg.TargetDir)
	if err != nil {
		resp["error"] = err.Error()
		return resp
	}

	pct := 0
	if sum.TotalTasks > 0 {
		pct = (sum.DoneTasks * 100) / sum.TotalTasks
	}

	resp["active"] = true
	resp["objective"] = sum.Objective
	resp["status"] = sum.Status
	resp["doneTasks"] = sum.DoneTasks
	resp["totalTasks"] = sum.TotalTasks
	resp["progress"] = pct
	resp["pendingList"] = sum.Pending
	return resp
}

func (s *Server) getDoctorData() map[string]interface{} {
	rep := doctor.Run(s.cfg.PlatformInfo)
	checks := make([]map[string]interface{}, 0, len(rep.Checks))
	for _, c := range rep.Checks {
		checks = append(checks, map[string]interface{}{
			"name":    c.Name,
			"status":  c.Status,
			"message": c.Details,
		})
	}
	return map[string]interface{}{
		"os":         s.cfg.PlatformInfo.OS,
		"arch":       s.cfg.PlatformInfo.Arch,
		"hasDisplay": s.cfg.PlatformInfo.HasDisplay,
		"checks":     checks,
	}
}

func (s *Server) getTabsData() []profile.Tab {
	tabs, err := profile.ListTabs(profile.DefaultDebugPort)
	if err != nil {
		return []profile.Tab{}
	}
	return tabs
}

func (s *Server) getEventsData() []string {
	var events []string
	home, err := os.UserHomeDir()
	if err != nil {
		return events
	}
	geminiDir := filepath.Join(home, ".gemini")
	tInfo, err := watcher.FindActiveTranscript(geminiDir)
	if err != nil {
		return events
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	opts := watcher.WatchOptions{
		Follow:       false,
		InitialSteps: 8,
		OSName:       s.cfg.PlatformInfo.OS,
	}

	_ = watcher.Stream(ctx, tInfo.Path, opts, func(evt *watcher.Event) {
		sm := evt.Summary()
		if sm != "" {
			events = append(events, sm)
		}
	})

	return events
}
