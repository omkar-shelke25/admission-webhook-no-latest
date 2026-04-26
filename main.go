package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ─── Config ───────────────────────────────────────────────────────────────────

// Config holds all runtime configuration for the webhook server.
// Keeping it as a struct makes it easy to construct in tests.
type Config struct {
	Addr     string
	CertFile string
	KeyFile  string
	DevMode  bool
}

// DefaultConfig returns a Config with production defaults.
func DefaultConfig() Config {
	return Config{
		Addr:     ":8443",
		CertFile: "/etc/webhook/certs/tls.crt",
		KeyFile:  "/etc/webhook/certs/tls.key",
		DevMode:  false,
	}
}

// ─── Image tag validation ─────────────────────────────────────────────────────

// hasLatestTag returns true when the image uses ":latest", has no tag at all,
// or has an empty tag — all of which resolve to "latest" at runtime.
// Images pinned by digest (e.g. nginx@sha256:abc…) are always allowed.
func hasLatestTag(image string) bool {
	// Digest references are immutable — always allow them.
	if strings.Contains(image, "@") {
		return false
	}
	parts := strings.SplitN(image, ":", 2)
	if len(parts) == 1 {
		// No tag → implicit "latest"
		return true
	}
	tag := parts[1]
	return tag == "latest" || tag == ""
}

// validateContainers returns a violation message for every container
// whose image uses an unpinned or "latest" tag.
func validateContainers(containers []corev1.Container, kind string) []string {
	var violations []string
	for _, c := range containers {
		if hasLatestTag(c.Image) {
			violations = append(violations,
				fmt.Sprintf(
					"%s container %q uses a forbidden image tag (image: %s) — pin to a specific version or digest",
					kind, c.Name, c.Image,
				),
			)
		}
	}
	return violations
}

// ─── Admission logic ──────────────────────────────────────────────────────────

// ErrNilRequest is returned when the AdmissionReview carries no Request.
var ErrNilRequest = errors.New("admission request is nil")

// admit performs the core admission decision and returns an AdmissionResponse.
// It is a pure function with no HTTP concerns — fully unit-testable.
func admit(req *admissionv1.AdmissionRequest) (*admissionv1.AdmissionResponse, error) {
	if req == nil {
		return nil, ErrNilRequest
	}

	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		return nil, fmt.Errorf("unmarshal pod: %w", err)
	}

	var allViolations []string
	allViolations = append(allViolations, validateContainers(pod.Spec.InitContainers, "init")...)
	allViolations = append(allViolations, validateContainers(pod.Spec.Containers, "app")...)

	resp := &admissionv1.AdmissionResponse{UID: req.UID}

	if len(allViolations) == 0 {
		resp.Allowed = true
	} else {
		resp.Allowed = false
		resp.Result = &metav1.Status{
			Code:    http.StatusForbidden,
			Message: "Image policy violation:\n" + strings.Join(allViolations, "\n"),
		}
	}

	return resp, nil
}

// ─── HTTP handlers ────────────────────────────────────────────────────────────

// Server wraps an http.ServeMux and a structured logger.
// Using a struct lets tests inject a custom logger and inspect behaviour.
type Server struct {
	mux    *http.ServeMux
	logger *slog.Logger
}

// NewServer wires up all routes and returns a ready-to-use Server.
func NewServer(logger *slog.Logger) *Server {
	s := &Server{
		mux:    http.NewServeMux(),
		logger: logger,
	}
	s.mux.HandleFunc("/validate", s.handleValidate)
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	return s
}

// ServeHTTP makes Server implement http.Handler so it works with httptest.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// handleValidate is the ValidatingWebhook admission endpoint.
func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "only POST is accepted", http.StatusMethodNotAllowed)
		return
	}

	var review admissionv1.AdmissionReview
	if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
		s.logger.Error("failed to decode admission review", "error", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if review.Request == nil {
		s.logger.Error("admission request is nil")
		http.Error(w, "admission request is nil", http.StatusBadRequest)
		return
	}

	s.logger.Info("reviewing pod",
		"name", review.Request.Name,
		"namespace", review.Request.Namespace,
		"operation", review.Request.Operation,
	)

	resp, err := admit(review.Request)
	if err != nil {
		s.logger.Error("admission error", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if resp.Allowed {
		s.logger.Info("pod allowed",
			"name", review.Request.Name,
			"namespace", review.Request.Namespace,
		)
	} else {
		s.logger.Warn("pod denied",
			"name", review.Request.Name,
			"namespace", review.Request.Namespace,
			"message", resp.Result.Message,
		)
	}

	review.Response = resp
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(review); err != nil {
		s.logger.Error("failed to encode response", "error", err)
	}
}

// handleHealthz responds to liveness/readiness probes.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// ─── Runner ───────────────────────────────────────────────────────────────────

// Run starts the HTTP(S) server according to cfg.
// Extracted from main() so it can be tested independently.
func Run(cfg Config, logger *slog.Logger) error {
	srv := NewServer(logger)

	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: srv,
	}

	if cfg.DevMode {
		logger.Warn("running in HTTP dev mode — NEVER use in production", "addr", cfg.Addr)
		return httpServer.ListenAndServe()
	}

	logger.Info("starting no-latest-tag webhook (TLS)", "addr", cfg.Addr)
	return httpServer.ListenAndServeTLS(cfg.CertFile, cfg.KeyFile)
}

// ─── Entry point ──────────────────────────────────────────────────────────────

func main() {
	cfg := DefaultConfig()

	// Parse env vars (simpler than flags for container deployments)
	if v := os.Getenv("WEBHOOK_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv("WEBHOOK_CERT"); v != "" {
		cfg.CertFile = v
	}
	if v := os.Getenv("WEBHOOK_KEY"); v != "" {
		cfg.KeyFile = v
	}
	if os.Getenv("WEBHOOK_DEV_MODE") == "true" {
		cfg.DevMode = true
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	if err := Run(cfg, logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
