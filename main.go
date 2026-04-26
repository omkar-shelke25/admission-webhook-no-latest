package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// logger uses Go 1.21+ structured logging (slog) with JSON output.
var logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
	Level: slog.LevelInfo,
}))

// hasLatestTag returns true when the image uses ":latest" or has no tag at all.
// Images pinned by digest (e.g. nginx@sha256:abc…) are always allowed.
func hasLatestTag(image string) bool {
	// Digest references are immutable — allow them.
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

// validateContainers returns a violation string for every container whose
// image uses an unpinned tag.
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

// reviewHandler is the ValidatingWebhook admission endpoint.
func reviewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "only POST is accepted", http.StatusMethodNotAllowed)
		return
	}

	var review admissionv1.AdmissionReview
	if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
		logger.Error("failed to decode admission review", "error", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	req := review.Request
	if req == nil {
		http.Error(w, "admission request is nil", http.StatusBadRequest)
		return
	}

	logger.Info("reviewing pod",
		"name", req.Name,
		"namespace", req.Namespace,
		"operation", req.Operation,
	)

	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		logger.Error("failed to unmarshal pod", "error", err)
		http.Error(w, "failed to decode pod", http.StatusInternalServerError)
		return
	}

	// Check both init containers and regular containers.
	var allViolations []string
	allViolations = append(allViolations, validateContainers(pod.Spec.InitContainers, "init")...)
	allViolations = append(allViolations, validateContainers(pod.Spec.Containers, "app")...)

	response := &admissionv1.AdmissionResponse{UID: req.UID}

	if len(allViolations) == 0 {
		response.Allowed = true
		logger.Info("pod allowed", "name", req.Name, "namespace", req.Namespace)
	} else {
		response.Allowed = false
		message := "Image policy violation:\n" + strings.Join(allViolations, "\n")
		response.Result = &metav1.Status{
			Code:    http.StatusForbidden,
			Message: message,
		}
		logger.Warn("pod denied",
			"name", req.Name,
			"namespace", req.Namespace,
			"violations", allViolations,
		)
	}

	review.Response = response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(review); err != nil {
		logger.Error("failed to encode response", "error", err)
	}
}

func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func main() {
	devMode := flag.Bool("dev", false, "run HTTP instead of HTTPS (local testing only — never in production)")
	certFile := flag.String("cert", "/etc/webhook/certs/tls.crt", "path to TLS certificate")
	keyFile  := flag.String("key",  "/etc/webhook/certs/tls.key", "path to TLS private key")
	addr     := flag.String("addr", ":8443", "listen address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/validate", reviewHandler)
	mux.HandleFunc("/healthz", healthzHandler)

	server := &http.Server{
		Addr:    *addr,
		Handler: mux,
	}

	if *devMode {
		logger.Warn("running in HTTP dev mode — NEVER use in production", "addr", *addr)
		if err := server.ListenAndServe(); err != nil {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
		return
	}

	logger.Info("starting no-latest-tag webhook (TLS)", "addr", *addr)
	if err := server.ListenAndServeTLS(*certFile, *keyFile); err != nil {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}
