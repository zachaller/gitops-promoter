# Aggregation API Server

The aggregation API server provides a read-only aggregated view of `PromotionStrategy` resources and all their related resources. It exposes a virtual resource `PromotionStrategyView` that combines data from multiple promoter resources into a single view.

## Overview

The `PromotionStrategyView` resource aggregates:

- **PromotionStrategy** - The base strategy spec and status
- **GitRepository** - The referenced Git repository
- **ChangeTransferPolicies** - Policies for each environment
- **CommitStatuses** - All commit status types (ArgoCD, Git, Timed, and low-level CommitStatus)
- **PullRequests** - Active pull requests for promotions

This is a **virtual resource** - it doesn't exist in etcd. Data is computed on-demand when you query the API.

## API Access

Once installed, access the aggregated view using kubectl:

```bash
# Get aggregated view for a specific PromotionStrategy
kubectl get promotionstrategyview -n <namespace> <name>

# Short name: psv
kubectl get psv -n <namespace> <name>

# List all aggregated views in a namespace
kubectl get promotionstrategyviews -n <namespace>

# Get detailed output
kubectl get psv -n <namespace> <name> -o yaml
```

Or via the REST API:

```bash
# Get a specific view
GET /apis/aggregation.promoter.argoproj.io/v1alpha1/namespaces/<namespace>/promotionstrategyviews/<name>

# List all views in a namespace
GET /apis/aggregation.promoter.argoproj.io/v1alpha1/namespaces/<namespace>/promotionstrategyviews
```

## Installation

### Prerequisites

- Kubernetes cluster with the promoter CRDs installed
- cert-manager (recommended) or manual TLS certificate management

### Using Kustomize

1. **Create TLS certificates**

   The aggregation API server requires TLS certificates. You can use cert-manager to generate them:

   ```yaml
   # cert-manager Certificate resource
   apiVersion: cert-manager.io/v1
   kind: Certificate
   metadata:
     name: aggregation-server-cert
     namespace: promoter-system
   spec:
     secretName: aggregation-server-cert
     duration: 8760h # 1 year
     renewBefore: 720h # 30 days
     subject:
       organizations:
         - promoter
     isCA: false
     privateKey:
       algorithm: RSA
       size: 2048
     usages:
       - server auth
     dnsNames:
       - aggregation-server
       - aggregation-server.promoter-system
       - aggregation-server.promoter-system.svc
       - aggregation-server.promoter-system.svc.cluster.local
     issuerRef:
       name: your-cluster-issuer  # Replace with your issuer
       kind: ClusterIssuer
   ```

2. **Update the APIService with CA bundle**

   After the certificate is created, update the APIService with the CA bundle:

   ```bash
   # Get the CA bundle from cert-manager or your CA
   CA_BUNDLE=$(kubectl get secret aggregation-server-cert -n promoter-system -o jsonpath='{.data.ca\.crt}')
   
   # Patch the APIService
   kubectl patch apiservice v1alpha1.aggregation.promoter.argoproj.io \
     --type='json' \
     -p="[{\"op\": \"replace\", \"path\": \"/spec/caBundle\", \"value\": \"${CA_BUNDLE}\"}]"
   ```

3. **Deploy the aggregation server**

   ```bash
   kubectl apply -k config/aggregation/
   ```

### Manual Installation

Apply the manifests individually:

```bash
# Create the namespace if it doesn't exist
kubectl create namespace promoter-system --dry-run=client -o yaml | kubectl apply -f -

# Apply RBAC
kubectl apply -f config/aggregation/rbac.yaml
kubectl apply -f config/aggregation/rbac-kube-system.yaml

# Apply Service and Deployment
kubectl apply -f config/aggregation/service.yaml
kubectl apply -f config/aggregation/deployment.yaml

# Apply APIService (after setting up TLS)
kubectl apply -f config/aggregation/apiservice.yaml
```

## Running Locally

### For Development/Testing

You can run the aggregation server locally for development and testing purposes.

#### 1. Generate TLS Certificates

```bash
# Create a directory for certificates
mkdir -p tmp/apiserver-certs

# Generate a self-signed certificate
openssl req -x509 -newkey rsa:4096 \
  -keyout tmp/apiserver-certs/tls.key \
  -out tmp/apiserver-certs/tls.crt \
  -days 365 -nodes \
  -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"
```

#### 2. Run the Server

```bash
# Build the binary
go build -o bin/promoter ./cmd/main.go

# Run the aggregation server
./bin/promoter aggregation-server \
  --secure-port=6443 \
  --tls-cert-file=tmp/apiserver-certs/tls.crt \
  --tls-private-key-file=tmp/apiserver-certs/tls.key \
  --kubeconfig=$HOME/.kube/config
```

#### 3. Test the API

```bash
# Test with curl (skip TLS verification for self-signed certs)
curl -k https://localhost:6443/apis/aggregation.promoter.argoproj.io/v1alpha1

# List PromotionStrategyViews in a namespace
curl -k https://localhost:6443/apis/aggregation.promoter.argoproj.io/v1alpha1/namespaces/default/promotionstrategyviews

# Get a specific PromotionStrategyView
curl -k https://localhost:6443/apis/aggregation.promoter.argoproj.io/v1alpha1/namespaces/default/promotionstrategyviews/my-app
```

### Running with kubectl proxy

For easier local testing without TLS concerns:

1. **Start the aggregation server** (as shown above)

2. **Register the APIService pointing to localhost**

   Create a temporary APIService that points to your local server:

   ```yaml
   apiVersion: apiregistration.k8s.io/v1
   kind: APIService
   metadata:
     name: v1alpha1.aggregation.promoter.argoproj.io
   spec:
     group: aggregation.promoter.argoproj.io
     version: v1alpha1
     groupPriorityMinimum: 1000
     versionPriority: 100
     insecureSkipTLSVerify: true  # Only for local development!
     service:
       name: aggregation-server
       namespace: promoter-system
       port: 443
   ```

   Note: For local development, you may need to use a tool like `kubectl port-forward` or configure your cluster to reach your local machine.

### Command Line Options

```
Usage:
  promoter aggregation-server [flags]

Flags:
      --cert-dir string               Directory containing TLS certificates
  -h, --help                          help for aggregation-server
      --secure-port int               Port for HTTPS (default 6443)
      --tls-cert-file string          Path to TLS certificate file
      --tls-private-key-file string   Path to TLS private key file

Global Flags:
      --kubeconfig string             Path to kubeconfig file
      --context string                Kubeconfig context to use
      -n, --namespace string          Default namespace
```

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        kube-apiserver                            │
│                                                                  │
│  ┌──────────────┐                                               │
│  │  APIService  │ ──────────────────────────────────────────┐   │
│  │  (proxy)     │                                           │   │
│  └──────────────┘                                           │   │
└─────────────────────────────────────────────────────────────│───┘
                                                              │
                                                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                   Aggregation API Server                         │
│                                                                  │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐      │
│  │ REST Handler │ -> │   Lister     │ -> │  K8s Client  │      │
│  └──────────────┘    └──────────────┘    └──────────────┘      │
│                                                   │              │
└───────────────────────────────────────────────────│──────────────┘
                                                    │
                                                    ▼
┌─────────────────────────────────────────────────────────────────┐
│                     Promoter Resources                           │
│                                                                  │
│  ┌────────────────────┐  ┌─────────────────────────┐            │
│  │ PromotionStrategy  │  │ ChangeTransferPolicy    │            │
│  └────────────────────┘  └─────────────────────────┘            │
│  ┌────────────────────┐  ┌─────────────────────────┐            │
│  │ GitRepository      │  │ CommitStatus (all types)│            │
│  └────────────────────┘  └─────────────────────────┘            │
│  ┌────────────────────┐                                         │
│  │ PullRequest        │                                         │
│  └────────────────────┘                                         │
└─────────────────────────────────────────────────────────────────┘
```

## PromotionStrategyView Schema

```yaml
apiVersion: aggregation.promoter.argoproj.io/v1alpha1
kind: PromotionStrategyView
metadata:
  name: my-strategy
  namespace: my-namespace
spec:
  # Mirrors PromotionStrategy.Spec
  gitRepositoryRef:
    name: my-repo
  environments:
    - branch: environment/development
    - branch: environment/staging
    - branch: environment/production
status:
  # Mirrors PromotionStrategy.Status
  environments:
    - branch: environment/development
      active:
        dry:
          sha: abc123
        hydrated:
          sha: def456
aggregated:
  # GitRepository data
  gitRepository:
    name: my-repo
    namespace: my-namespace
    spec: { ... }
    status: { ... }
  
  # ChangeTransferPolicies for each environment
  changeTransferPolicies:
    - name: my-strategy-development
      namespace: my-namespace
      branch: environment/development
      spec: { ... }
      status: { ... }
  
  # All commit status types
  commitStatuses:
    argoCD:
      - name: my-argocd-status
        spec: { ... }
        status: { ... }
    git:
      - name: my-git-status
        spec: { ... }
        status: { ... }
    timed:
      - name: my-timed-status
        spec: { ... }
        status: { ... }
    commitStatuses:
      - name: my-commit-status
        spec: { ... }
        status: { ... }
  
  # Active PullRequests
  pullRequests:
    - name: my-pr
      namespace: my-namespace
      branch: environment/development
      spec: { ... }
      status: { ... }
```

## Troubleshooting

### APIService shows "False" for Available

Check the aggregation server logs:

```bash
kubectl logs -n promoter-system -l control-plane=aggregation-server
```

Common issues:
- TLS certificate not valid or not trusted
- Service not reachable from kube-apiserver
- RBAC permissions missing

### "unauthorized" errors

Ensure the auth delegation RBAC is properly configured:

```bash
kubectl get rolebinding -n kube-system promoter-aggregation-server-auth-reader
kubectl get clusterrolebinding aggregation-server:system:auth-delegator
```

### Connection refused

Verify the service and endpoints:

```bash
kubectl get svc -n promoter-system aggregation-server
kubectl get endpoints -n promoter-system aggregation-server
```

### Certificate errors

For development with self-signed certificates, you can temporarily set `insecureSkipTLSVerify: true` in the APIService. **Do not use this in production.**

## Security Considerations

1. **TLS Certificates**: Always use valid TLS certificates in production. Use cert-manager or your organization's PKI.

2. **RBAC**: The aggregation server needs read access to promoter resources. The provided RBAC is minimal and follows least-privilege principles.

3. **Network Policies**: Consider adding network policies to restrict which pods can communicate with the aggregation server.

4. **Authentication**: The aggregation server delegates authentication to the kube-apiserver. Ensure your cluster's authentication is properly configured.
