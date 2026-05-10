[![CI — Test · Build · Scan · SBOM · Sign](https://github.com/omkar-shelke25/admission-webhook-no-latest/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/omkar-shelke25/admission-webhook-no-latest/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/omkar-shelke25/admission-webhook-no-latest)](https://goreportcard.com/report/github.com/omkar-shelke25/admission-webhook-no-latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](https://golang.org)
[![Helm](https://img.shields.io/badge/Helm-3.12+-0F1689?logo=helm)](https://helm.sh)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.26+-326CE5?logo=kubernetes)](https://kubernetes.io)
[![cert-manager](https://img.shields.io/badge/cert--manager-1.13+-00AEEF?logo=letsencrypt)](https://cert-manager.io)
[![ArgoCD](https://img.shields.io/badge/ArgoCD-2.9+-EF7B4D?logo=argo)](https://argoproj.github.io/cd)
[![GHCR](https://img.shields.io/badge/GHCR-ghcr.io-24292e?logo=github)](https://ghcr.io/omkar-shelke25/admission-webhook-no-latest)
[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/admission-webhook-no-latest)](https://artifacthub.io/packages/helm/admission-webhook-no-latest/no-latest-tag-webhook)

# 🛡️ kube-image-guard

> Kubernetes ValidatingAdmissionWebhook written in Go that blocks any pod using `:latest` or untagged container images — enforcing reproducible and auditable deployments cluster-wide.

---

## 💡 Project Idea

In Kubernetes, using `:latest` image tags is a common anti-pattern. It causes:

- **Non-reproducible deployments** — the same tag can point to different images over time
- **Silent rollouts** — pods restart with new code without any deliberate deploy action
- **Audit failures** — no way to trace which exact image version is running

**kube-image-guard** solves this by installing a `ValidatingAdmissionWebhook` that intercepts every Pod `CREATE` and `UPDATE` request. If any container (including init containers) uses `:latest` or has no tag at all, the request is rejected before the pod is ever scheduled.

```
Developer runs:  kubectl run app --image=nginx:latest
                        │
                        ▼
              Kubernetes API Server
                        │
                        ▼
              ValidatingWebhook (kube-image-guard)
                        │
                        ▼
              ❌ REJECTED — "Image policy violation:
                 app container uses forbidden tag (nginx:latest)
                 — pin to a specific version or digest"
```

Images pinned to a digest (e.g. `nginx@sha256:abc123`) are always allowed.

---

## ✅ Prerequisites

| Tool | Version | Purpose |
|---|---|---|
| Kubernetes | v1.26+ | Target cluster |
| kubectl | v1.26+ | Cluster interaction |
| Helm | v3.12+ | Chart installation |
| cert-manager | v1.13+ | Automatic TLS certificate management |
| ArgoCD | v2.9+ | GitOps continuous deployment |

---

## 🧪 Playground (No Local Setup Required)

You can run this entire setup for free using the iximiuz Kubernetes playground:

👉 **https://labs.iximiuz.com/playgrounds/k8s-omni**

The playground gives you a fully working multi-node Kubernetes cluster in your browser — no installation needed. Follow the steps below directly in the playground terminal.

> **What's already available in the playground:**
> - ✅ Kubernetes cluster (multi-node)
> - ✅ kubectl
> - ✅ Helm v3
>
> **What you need to install inside the playground:**
> - ⚙️ cert-manager — Step 1 below
> - ⚙️ ArgoCD — Step 2 below

---

## 🚀 Local Setup

### Step 1 — Install cert-manager

cert-manager automatically provisions and renews the TLS certificate the webhook needs. A `ClusterIssuer` (not a namespace-scoped `Issuer`) is required because `ValidatingWebhookConfiguration` is a cluster-scoped resource.

```bash
# Install cert-manager
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml

# Wait until all cert-manager pods are ready
kubectl wait --for=condition=Available deployment/cert-manager \
  -n cert-manager \
  --timeout=120s

kubectl wait --for=condition=Available deployment/cert-manager-webhook \
  -n cert-manager \
  --timeout=120s

# Verify
kubectl get pods -n cert-manager
```

Expected output:
```
NAME                                      READY   STATUS    RESTARTS   AGE
cert-manager-xxxx                         1/1     Running   0          60s
cert-manager-cainjector-xxxx              1/1     Running   0          60s
cert-manager-webhook-xxxx                 1/1     Running   0          60s
```

---

### Step 2 — Install ArgoCD

```bash
# Create namespace and install ArgoCD
kubectl create namespace argocd

kubectl apply -n argocd \
  -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml

# Wait for ArgoCD to be ready
kubectl wait --for=condition=Available deployment/argocd-server \
  -n argocd \
  --timeout=120s

# Get the initial admin password
kubectl get secret argocd-initial-admin-secret \
  -n argocd \
  -o jsonpath="{.data.password}" | base64 -d && echo

# Port-forward to access the UI
kubectl port-forward svc/argocd-server -n argocd 8080:443
# Open https://localhost:8080  (username: admin)
```

---

### Step 3 — Add Helm Repo

```bash
helm repo add admission-webhook-no-latest \
  https://omkar-shelke25.github.io/admission-webhook-no-latest/

helm repo update

# Verify chart is available
helm search repo admission-webhook-no-latest
```

---

### Step 4 — Install via Helm (for local testing without ArgoCD)

> **Note:** In production we deploy using ArgoCD (see ArgoCD section below).
> The Helm commands below are for local testing and verification only.

```bash
helm install no-latest-tag-webhook \
  admission-webhook-no-latest/no-latest-tag-webhook \
  --version 1.0.14 \
  --namespace webhook-system \
  --create-namespace
```

Verify the installation:

```bash
# Pods should be Running
kubectl get pods -n webhook-system

# Certificate should be Ready
kubectl get certificate -n webhook-system

# Webhook should be registered
kubectl get validatingwebhookconfiguration no-latest-tag-webhook

# Test — should be BLOCKED
kubectl run test-bad --image=nginx:latest -n default
# Expected: Error from server (Forbidden) ...

# Test — should be ALLOWED
kubectl run test-good --image=nginx:1.27.0 -n default
# Expected: pod/test-good created
```

---

## 🔄 CI Pipeline Structure

The GitHub Actions pipeline runs automatically on every push to `main` (documentation changes are ignored).

```
push to main
    │
    ▼
┌─────────────────────────────────────────────────────────────┐
│  JOB 1 — test                                               │
│  Go unit tests + coverage report                            │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│  JOB 2 — build                                              │
│  Docker build + push to GHCR                                │
│  Outputs: image-tag (sha-xxxxxxx)                           │
└──────────┬─────────────────────────┬────────────────────────┘
           │                         │                    │
           ▼                         ▼                    ▼
┌──────────────────┐  ┌──────────────────┐  ┌────────────────────┐
│  JOB 3 — sign    │  │  JOB 4 — trivy   │  │  JOB 5 — sbom      │
│  Cosign keyless  │  │  Vuln scan       │  │  Syft SPDX +       │
│  OIDC signing    │  │  SARIF → GitHub  │  │  CycloneDX         │
└────────┬─────────┘  └────────┬─────────┘  └────────┬───────────┘
         │                     │                      │
         │                     │                      ▼
         │                     │           ┌────────────────────┐
         │                     │           │  JOB 6 — grype     │
         │                     │           │  Scan from SBOM    │
         │                     │           └────────┬───────────┘
         │                     │                    │
         └─────────────────────┴────────────────────┘
                               │
                    ALL must pass ✅
                               │
                               ▼
┌─────────────────────────────────────────────────────────────┐
│  JOB 7 — helm-update                                        │
│  Inject image.tag into values.yaml                          │
│  Bump Chart.yaml patch version                              │
│  git commit [skip ci] → push to main                        │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│  JOB 8 — helm-package                                       │
│  helm package → .tgz                                        │
│  Upload as GitHub artifact                                  │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│  JOB 9 — helm-publish-ghpages                               │
│  Download artifact → gh-pages branch                        │
│  helm repo index --merge                                     │
│  Push index.yaml + .tgz to gh-pages                        │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│  ArgoCD (GitOps)                               [WIP]        │
│  Watches Helm repo on gh-pages                              │
│  Auto-syncs when new chart version is published             │
│  Deploys updated webhook to cluster                         │
└─────────────────────────────────────────────────────────────┘
```

> **Note:** The ArgoCD deployment section is work in progress and will be completed in a future update.

---

## 📁 Repository Structure

```
admission-webhook-no-latest/
├── main.go                        # Webhook server
├── main_test.go                   # Unit tests
├── go.mod                         # Go modules
├── Dockerfile                     # Multi-stage build
├── .github/
│   └── workflows/
│       └── ci.yml                 # CI pipeline
└── no-latest-tag-webhook/         # Helm chart
    ├── Chart.yaml
    ├── values.yaml
    └── templates/
        ├── _helpers.tpl
        ├── namespace.yaml
        ├── clusterissuer.yaml
        ├── certificate.yaml
        ├── deployment.yaml
        ├── service.yaml
        ├── validatingwebhookconfiguration.yaml
        └── NOTES.txt
```

---

## ⚙️ Configuration

Key values you can override during `helm install`:

```bash
helm install kube-image-guard kube-image-guard/no-latest-tag-webhook \
  --namespace webhook-system \
  --create-namespace \
  --set replicaCount=3 \
  --set admissionWebhook.failurePolicy=Ignore \
  --set namespaceSelector.excludedNamespaces="{kube-system,kube-public,webhook-system,monitoring}"
```

| Parameter | Default | Description |
|---|---|---|
| `replicaCount` | `2` | Number of webhook pods |
| `admissionWebhook.failurePolicy` | `Fail` | `Fail` blocks pods if webhook is down; `Ignore` allows them |
| `admissionWebhook.timeoutSeconds` | `5` | Webhook call timeout |
| `certManager.enabled` | `true` | Use cert-manager for TLS |
| `namespaceSelector.excludedNamespaces` | `[kube-system, kube-public, webhook-system]` | Namespaces not enforced |

---

## 📜 License

MIT
