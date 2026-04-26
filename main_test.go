package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// ─── hasLatestTag ─────────────────────────────────────────────────────────────

func TestHasLatestTag(t *testing.T) {
	t.Parallel()

	cases := []struct {
		image string
		want  bool
	}{
		// Should be BLOCKED
		{"nginx", true},                    // no tag → implicit latest
		{"nginx:latest", true},             // explicit latest
		{"nginx:", true},                   // empty tag → latest
		{"myrepo/app:latest", true},        // scoped image + latest
		{"registry.io/ns/app:latest", true},// full registry + latest

		// Should be ALLOWED
		{"nginx:1.27.0", false},
		{"nginx:1.27.0-alpine", false},
		{"myrepo/app:v2.1.3", false},
		{"registry.io/app:stable", false},
		{"nginx@sha256:abc123def456", false},      // digest → always allowed
		{"myrepo/app@sha256:deadbeefcafe", false}, // digest → always allowed
	}

	for _, tc := range cases {
		tc := tc // capture loop variable (pre-Go 1.22 safety)
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

	t.Run("no violations when all images are pinned", func(t *testing.T) {
		t.Parallel()
		containers := []corev1.Container{
			{Name: "app", Image: "nginx:1.27.0"},
			{Name: "sidecar", Image: "envoy:v1.30.1"},
		}
		if v := validateContainers(containers, "app"); len(v) != 0 {
			t.Errorf("expected 0 violations, got %d: %v", len(v), v)
		}
	})

	t.Run("violation for each container using latest", func(t *testing.T) {
		t.Parallel()
		containers := []corev1.Container{
			{Name: "app", Image: "nginx:latest"},
			{Name: "sidecar", Image: "envoy:latest"},
		}
		if v := validateContainers(containers, "app"); len(v) != 2 {
			t.Errorf("expected 2 violations, got %d: %v", len(v), v)
		}
	})

	t.Run("violation for container with no tag", func(t *testing.T) {
		t.Parallel()
		containers := []corev1.Container{
			{Name: "app", Image: "nginx"},
		}
		if v := validateContainers(containers, "app"); len(v) != 1 {
			t.Errorf("expected 1 violation, got %d", len(v))
		}
	})

	t.Run("mixed containers — only flags the bad one", func(t *testing.T) {
		t.Parallel()
		containers := []corev1.Container{
			{Name: "good", Image: "nginx:1.27.0"},
			{Name: "bad", Image: "nginx:latest"},
		}
		if v := validateContainers(containers, "app"); len(v) != 1 {
			t.Errorf("expected 1 violation, got %d: %v", len(v), v)
		}
	})

	t.Run("empty container list returns no violations", func(t *testing.T) {
		t.Parallel()
		if v := validateContainers(nil, "app"); len(v) != 0 {
			t.Errorf("expected 0 violations for empty list, got %d", len(v))
		}
	})
}

// ─── HTTP handler (no real K8s needed) ────────────────────────────────────────

// buildReview builds a fake AdmissionReview with the given containers.
func buildReview(t *testing.T, containers, initContainers []corev1.Container) []byte {
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

	review := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admission.k8s.io/v1",
			Kind:       "AdmissionReview",
		},
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

// postReview fires the review body at the handler and returns the parsed response.
func postReview(t *testing.T, body []byte) *admissionv1.AdmissionReview {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	reviewHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handler returned status %d, want 200. body: %s", rec.Code, rec.Body.String())
	}

	var review admissionv1.AdmissionReview
	if err := json.NewDecoder(rec.Body).Decode(&review); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return &review
}

func TestReviewHandler_AllowsPinnedImage(t *testing.T) {
	body := buildReview(t,
		[]corev1.Container{{Name: "app", Image: "nginx:1.27.0"}},
		nil,
	)
	review := postReview(t, body)
	if !review.Response.Allowed {
		t.Errorf("expected allowed=true, got false. message: %s", review.Response.Result.Message)
	}
}

func TestReviewHandler_AllowsDigest(t *testing.T) {
	body := buildReview(t,
		[]corev1.Container{{Name: "app", Image: "nginx@sha256:abc123def456"}},
		nil,
	)
	review := postReview(t, body)
	if !review.Response.Allowed {
		t.Errorf("expected allowed=true for digest image, got false")
	}
}

func TestReviewHandler_BlocksLatestTag(t *testing.T) {
	body := buildReview(t,
		[]corev1.Container{{Name: "app", Image: "nginx:latest"}},
		nil,
	)
	review := postReview(t, body)
	if review.Response.Allowed {
		t.Error("expected allowed=false, got true")
	}
	if review.Response.Result == nil || review.Response.Result.Code != http.StatusForbidden {
		t.Errorf("expected 403 in result, got %+v", review.Response.Result)
	}
}

func TestReviewHandler_BlocksImplicitLatest(t *testing.T) {
	body := buildReview(t,
		[]corev1.Container{{Name: "app", Image: "nginx"}}, // no tag
		nil,
	)
	review := postReview(t, body)
	if review.Response.Allowed {
		t.Error("expected allowed=false for image with no tag")
	}
}

func TestReviewHandler_BlocksLatestInInitContainer(t *testing.T) {
	body := buildReview(t,
		[]corev1.Container{{Name: "app", Image: "nginx:1.27.0"}},      // good
		[]corev1.Container{{Name: "init", Image: "busybox:latest"}},   // bad
	)
	review := postReview(t, body)
	if review.Response.Allowed {
		t.Error("expected allowed=false when initContainer uses latest")
	}
}

func TestReviewHandler_BlocksMultipleViolations(t *testing.T) {
	body := buildReview(t,
		[]corev1.Container{
			{Name: "app", Image: "nginx:latest"},
			{Name: "sidecar", Image: "envoy:latest"},
		},
		nil,
	)
	review := postReview(t, body)
	if review.Response.Allowed {
		t.Error("expected allowed=false when multiple containers use latest")
	}
}

func TestReviewHandler_RejectsNonPost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/validate", nil)
	rec := httptest.NewRecorder()
	reviewHandler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestReviewHandler_RejectsMalformedJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/validate", bytes.NewBufferString("not-json{{"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	reviewHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHealthzHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	healthzHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("expected body 'ok', got %q", rec.Body.String())
	}
}
