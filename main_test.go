package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// noopLogger discards all log output during tests — keeps output clean.
var noopLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
	Level: slog.Level(100), // above all levels → nothing printed
}))

// ─── DefaultConfig ────────────────────────────────────────────────────────────

func TestDefaultConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	if cfg.Addr != ":8443" {
		t.Errorf("expected addr :8443, got %s", cfg.Addr)
	}
	if cfg.CertFile == "" {
		t.Error("expected non-empty CertFile")
	}
	if cfg.KeyFile == "" {
		t.Error("expected non-empty KeyFile")
	}
	if cfg.DevMode {
		t.Error("expected DevMode=false by default")
	}
}

// ─── hasLatestTag ─────────────────────────────────────────────────────────────

func TestHasLatestTag(t *testing.T) {
	t.Parallel()

	cases := []struct {
		image string
		want  bool
	}{
		// BLOCKED — latest or no tag
		{"nginx", true},
		{"nginx:latest", true},
		{"nginx:", true},
		{"myrepo/app:latest", true},
		{"registry.io/ns/app:latest", true},

		// ALLOWED — pinned version or digest
		{"nginx:1.27.0", false},
		{"nginx:1.27.0-alpine", false},
		{"myrepo/app:v2.1.3", false},
		{"registry.io/app:stable", false},
		{"nginx@sha256:abc123def456", false},
		{"myrepo/app@sha256:deadbeefcafe", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.image, func(t *testing.T) {
			t.Parallel()
			if got := hasLatestTag(tc.image); got != tc.want {
				t.Errorf("hasLatestTag(%q) = %v, want %v", tc.image, got, tc.want)
			}
		})
	}
}

// ─── validateContainers ───────────────────────────────────────────────────────

func TestValidateContainers(t *testing.T) {
	t.Parallel()

	t.Run("all pinned — no violations", func(t *testing.T) {
		t.Parallel()
		containers := []corev1.Container{
			{Name: "app", Image: "nginx:1.27.0"},
			{Name: "sidecar", Image: "envoy:v1.30.1"},
		}
		if v := validateContainers(containers, "app"); len(v) != 0 {
			t.Errorf("expected 0 violations, got %d: %v", len(v), v)
		}
	})

	t.Run("all latest — violation per container", func(t *testing.T) {
		t.Parallel()
		containers := []corev1.Container{
			{Name: "app", Image: "nginx:latest"},
			{Name: "sidecar", Image: "envoy:latest"},
		}
		if v := validateContainers(containers, "app"); len(v) != 2 {
			t.Errorf("expected 2 violations, got %d: %v", len(v), v)
		}
	})

	t.Run("no tag — violation", func(t *testing.T) {
		t.Parallel()
		containers := []corev1.Container{{Name: "app", Image: "nginx"}}
		if v := validateContainers(containers, "app"); len(v) != 1 {
			t.Errorf("expected 1 violation, got %d", len(v))
		}
	})

	t.Run("mixed — only bad one flagged", func(t *testing.T) {
		t.Parallel()
		containers := []corev1.Container{
			{Name: "good", Image: "nginx:1.27.0"},
			{Name: "bad", Image: "nginx:latest"},
		}
		if v := validateContainers(containers, "app"); len(v) != 1 {
			t.Errorf("expected 1 violation, got %d: %v", len(v), v)
		}
	})

	t.Run("empty list — no violations", func(t *testing.T) {
		t.Parallel()
		if v := validateContainers(nil, "app"); len(v) != 0 {
			t.Errorf("expected 0 violations for nil list, got %d", len(v))
		}
	})
}

// ─── admit (pure admission logic) ────────────────────────────────────────────

func marshalPod(t *testing.T, containers, initContainers []corev1.Container) []byte {
	t.Helper()
	pod := corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers:     containers,
			InitContainers: initContainers,
		},
	}
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatalf("marshal pod: %v", err)
	}
	return raw
}

func TestAdmit_NilRequest(t *testing.T) {
	t.Parallel()
	_, err := admit(nil)
	if err == nil {
		t.Error("expected error for nil request, got nil")
	}
	if err != ErrNilRequest {
		t.Errorf("expected ErrNilRequest, got %v", err)
	}
}

func TestAdmit_InvalidPodJSON(t *testing.T) {
	t.Parallel()
	req := &admissionv1.AdmissionRequest{
		UID:    "uid-bad",
		Object: runtime.RawExtension{Raw: []byte("not-valid-json{{")},
	}
	_, err := admit(req)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestAdmit_AllowsPinnedImage(t *testing.T) {
	t.Parallel()
	req := &admissionv1.AdmissionRequest{
		UID:    "uid-1",
		Object: runtime.RawExtension{Raw: marshalPod(t, []corev1.Container{{Name: "app", Image: "nginx:1.27.0"}}, nil)},
	}
	resp, err := admit(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Allowed {
		t.Errorf("expected allowed=true, got false: %s", resp.Result.Message)
	}
}

func TestAdmit_AllowsDigest(t *testing.T) {
	t.Parallel()
	req := &admissionv1.AdmissionRequest{
		UID:    "uid-digest",
		Object: runtime.RawExtension{Raw: marshalPod(t, []corev1.Container{{Name: "app", Image: "nginx@sha256:abc123"}}, nil)},
	}
	resp, err := admit(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Allowed {
		t.Error("expected allowed=true for digest image")
	}
}

func TestAdmit_BlocksLatestTag(t *testing.T) {
	t.Parallel()
	req := &admissionv1.AdmissionRequest{
		UID:    "uid-latest",
		Object: runtime.RawExtension{Raw: marshalPod(t, []corev1.Container{{Name: "app", Image: "nginx:latest"}}, nil)},
	}
	resp, err := admit(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Allowed {
		t.Error("expected allowed=false for :latest image")
	}
	if resp.Result == nil || resp.Result.Code != http.StatusForbidden {
		t.Errorf("expected 403 result, got %+v", resp.Result)
	}
}

func TestAdmit_BlocksImplicitLatest(t *testing.T) {
	t.Parallel()
	req := &admissionv1.AdmissionRequest{
		UID:    "uid-notag",
		Object: runtime.RawExtension{Raw: marshalPod(t, []corev1.Container{{Name: "app", Image: "nginx"}}, nil)},
	}
	resp, err := admit(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Allowed {
		t.Error("expected allowed=false for image with no tag")
	}
}

func TestAdmit_BlocksLatestInInitContainer(t *testing.T) {
	t.Parallel()
	req := &admissionv1.AdmissionRequest{
		UID: "uid-init",
		Object: runtime.RawExtension{Raw: marshalPod(t,
			[]corev1.Container{{Name: "app", Image: "nginx:1.27.0"}},
			[]corev1.Container{{Name: "init", Image: "busybox:latest"}},
		)},
	}
	resp, err := admit(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Allowed {
		t.Error("expected allowed=false when initContainer uses :latest")
	}
}

func TestAdmit_BlocksMultipleViolations(t *testing.T) {
	t.Parallel()
	req := &admissionv1.AdmissionRequest{
		UID: "uid-multi",
		Object: runtime.RawExtension{Raw: marshalPod(t,
			[]corev1.Container{
				{Name: "app", Image: "nginx:latest"},
				{Name: "sidecar", Image: "envoy:latest"},
			},
			nil,
		)},
	}
	resp, err := admit(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Allowed {
		t.Error("expected allowed=false when multiple containers use :latest")
	}
}

func TestAdmit_UIDPropagated(t *testing.T) {
	t.Parallel()
	req := &admissionv1.AdmissionRequest{
		UID:    types.UID("my-specific-uid"),
		Object: runtime.RawExtension{Raw: marshalPod(t, []corev1.Container{{Name: "app", Image: "nginx:1.27.0"}}, nil)},
	}
	resp, err := admit(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.UID != req.UID {
		t.Errorf("UID not propagated: got %q, want %q", resp.UID, req.UID)
	}
}

// ─── HTTP handler tests ───────────────────────────────────────────────────────

func newTestServer() *Server {
	return NewServer(noopLogger)
}

func buildReview(t *testing.T, containers, initContainers []corev1.Container) []byte {
	t.Helper()
	raw := marshalPod(t, containers, initContainers)
	review := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Request: &admissionv1.AdmissionRequest{
			UID:       types.UID("test-uid-001"),
			Operation: admissionv1.Create,
			Resource:  metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
	body, err := json.Marshal(review)
	if err != nil {
		t.Fatalf("marshal review: %v", err)
	}
	return body
}

func postReview(t *testing.T, srv *Server, body []byte) *admissionv1.AdmissionReview {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handler returned %d, want 200. body: %s", rec.Code, rec.Body.String())
	}
	var review admissionv1.AdmissionReview
	if err := json.NewDecoder(rec.Body).Decode(&review); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return &review
}

func TestHandleValidate_AllowsPinnedImage(t *testing.T) {
	body := buildReview(t, []corev1.Container{{Name: "app", Image: "nginx:1.27.0"}}, nil)
	review := postReview(t, newTestServer(), body)
	if !review.Response.Allowed {
		t.Errorf("expected allowed=true, got false: %s", review.Response.Result.Message)
	}
}

func TestHandleValidate_AllowsDigest(t *testing.T) {
	body := buildReview(t, []corev1.Container{{Name: "app", Image: "nginx@sha256:abc123def456"}}, nil)
	review := postReview(t, newTestServer(), body)
	if !review.Response.Allowed {
		t.Error("expected allowed=true for digest image")
	}
}

func TestHandleValidate_BlocksLatestTag(t *testing.T) {
	body := buildReview(t, []corev1.Container{{Name: "app", Image: "nginx:latest"}}, nil)
	review := postReview(t, newTestServer(), body)
	if review.Response.Allowed {
		t.Error("expected allowed=false")
	}
	if review.Response.Result == nil || review.Response.Result.Code != http.StatusForbidden {
		t.Errorf("expected 403 result, got %+v", review.Response.Result)
	}
}

func TestHandleValidate_BlocksImplicitLatest(t *testing.T) {
	body := buildReview(t, []corev1.Container{{Name: "app", Image: "nginx"}}, nil)
	review := postReview(t, newTestServer(), body)
	if review.Response.Allowed {
		t.Error("expected allowed=false for image with no tag")
	}
}

func TestHandleValidate_BlocksLatestInInitContainer(t *testing.T) {
	body := buildReview(t,
		[]corev1.Container{{Name: "app", Image: "nginx:1.27.0"}},
		[]corev1.Container{{Name: "init", Image: "busybox:latest"}},
	)
	review := postReview(t, newTestServer(), body)
	if review.Response.Allowed {
		t.Error("expected allowed=false when initContainer uses :latest")
	}
}

func TestHandleValidate_BlocksMultipleViolations(t *testing.T) {
	body := buildReview(t,
		[]corev1.Container{
			{Name: "app", Image: "nginx:latest"},
			{Name: "sidecar", Image: "envoy:latest"},
		},
		nil,
	)
	review := postReview(t, newTestServer(), body)
	if review.Response.Allowed {
		t.Error("expected allowed=false for multiple :latest containers")
	}
}

func TestHandleValidate_RejectsNonPost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/validate", nil)
	rec := httptest.NewRecorder()
	newTestServer().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleValidate_RejectsMalformedJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/validate", bytes.NewBufferString("not-json{{"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newTestServer().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleValidate_RejectsNilRequest(t *testing.T) {
	// Build a review with no Request field
	review := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
	}
	body, _ := json.Marshal(review)
	req := httptest.NewRequest(http.MethodPost, "/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newTestServer().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for nil request, got %d", rec.Code)
	}
}

func TestHandleValidate_RejectsBadPodJSON(t *testing.T) {
	// Build a review whose Object.Raw is not a valid Pod
	review := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Request: &admissionv1.AdmissionRequest{
			UID:       "uid-badpod",
			Operation: admissionv1.Create,
			Object:    runtime.RawExtension{Raw: []byte("{{not a pod")},
		},
	}
	body, _ := json.Marshal(review)
	req := httptest.NewRequest(http.MethodPost, "/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newTestServer().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for bad pod JSON, got %d", rec.Code)
	}
}

func TestHandleHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	newTestServer().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("expected body 'ok', got %q", rec.Body.String())
	}
}

// ─── Config / env var tests ───────────────────────────────────────────────────

func TestDefaultConfig_Values(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.DevMode {
		t.Error("DevMode should be false by default")
	}
	if cfg.Addr == "" {
		t.Error("Addr should not be empty")
	}
}

func TestNewServer_RoutesRegistered(t *testing.T) {
	srv := NewServer(noopLogger)

	for _, path := range []string{"/healthz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Errorf("route %s not registered", path)
		}
	}
}
