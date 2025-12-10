# PromotionEventHook

## Overview

PromotionEventHook is a powerful automation feature that allows you to trigger actions based on the state of your PromotionStrategy. It continuously evaluates expressions against your PromotionStrategy and can execute webhooks or create/update Kubernetes resources when conditions are met.

Common use cases include:

- Opening Jira tickets when a promotion enters a specific environment
- Sending notifications to Slack/Teams/PagerDuty when promotions succeed or fail
- Creating Kubernetes Jobs for integration tests
- Updating ConfigMaps or Secrets based on promotion state
- Integrating with external CI/CD systems

## Key Concepts

### Expression-Based Triggering

PromotionEventHook uses the [expr](https://expr-lang.org/) language to evaluate conditions against your PromotionStrategy. The `triggerExpr` determines when an action should fire and can return custom data to track in the status.

### Action Types

PromotionEventHook supports two action types:

1. **Webhook**: Send HTTP requests to external services with full authentication support
2. **Resource**: Create or update Kubernetes resources using templated YAML

Both actions can be used together, executing sequentially (webhook first, then resource).

### Fire-Once Pattern

To ensure actions only fire once per condition (e.g., open one Jira ticket per environment), embed the logic in your `triggerExpr` using the `status.triggerData` to track what has already fired.

## Basic Example

Here's a simple example that sends a webhook when a promotion reaches the "production" environment:

```yaml
apiVersion: promoter.argoproj.io/v1alpha1
kind: PromotionEventHook
metadata:
  name: notify-prod-promotion
  namespace: default
spec:
  promotionStrategyRef:
    name: my-app
  
  # Fire when production environment (last in list) has a new SHA
  triggerExpr: |
    {
      "trigger": len(promotionStrategy.status.environments) > 0 &&
                    promotionStrategy.status.environments[len(promotionStrategy.status.environments)-1].active.hydrated.sha != "" &&
                    promotionStrategy.status.environments[len(promotionStrategy.status.environments)-1].branch == "environment/production" &&
                    status.triggerData["notified"] != "true",
      "notified": "true",
      "sha": promotionStrategy.status.environments[len(promotionStrategy.status.environments)-1].active.hydrated.sha
    }
  
  action:
    webhook:
      url: "https://hooks.slack.com/services/YOUR/WEBHOOK/URL"
      method: POST
      headers:
        Content-Type: application/json
      body: |
        {
          "text": "🚀 Production deployment complete! SHA: {{ .WebhookResponseData.sha }}"
        }
```

## Trigger Expression

The `triggerExpr` is evaluated on every reconciliation and must return a map with at least a `trigger` boolean field:

```yaml
triggerExpr: |
  {
    "trigger": true,  # Required: determines if action executes
    "customKey": "value" # Optional: stored in status.triggerData
  }
```

**Important**: Any fields you return (except `trigger`) are stored in `status.triggerData` and persist across reconciliations. This is how you implement fire-once patterns - by checking if a field exists and setting it when the action fires.

### Available Context

Your expression has access to:

- `promotionStrategy`: The full PromotionStrategy resource
- `status`: The current PromotionEventHook status (including `triggerData` from previous evaluations)

### Common Patterns

#### Fire Once Per SHA

Fire the action once for each unique SHA, useful for notifications:

```yaml
triggerExpr: |
  {
    "trigger": len(promotionStrategy.status.environments) > 0 &&
                  promotionStrategy.status.environments[0].active.hydrated.sha != "" &&
                  status.triggerData["lastSha"] != promotionStrategy.status.environments[0].active.hydrated.sha,
    "lastSha": promotionStrategy.status.environments[0].active.hydrated.sha  # Store to prevent re-firing
  }
```

**How it works**: The `lastSha` is stored in `status.triggerData` after firing. On the next reconciliation, the trigger will be `false` unless the SHA changes.

#### Fire Once Per Condition

Fire the action only once when a condition is met, never again:

```yaml
triggerExpr: |
  {
    "trigger": len(promotionStrategy.status.environments) > 0 &&
                  promotionStrategy.status.environments[len(promotionStrategy.status.environments)-1].branch == "environment/production" &&
                  status.triggerData["firedForProduction"] == "",
    "firedForProduction": "true"  # Set once, never fires again for this PromotionStrategy
  }
```

**How it works**: Once `firedForProduction` is set to `"true"`, it remains in `status.triggerData` forever (unless manually cleared), preventing the action from firing again.

#### Fire When a Specific Environment Changes

```yaml
triggerExpr: |
  {
    "trigger": len(promotionStrategy.status.environments) > 2 &&
                  status.triggerData["lastSha"] != promotionStrategy.status.environments[2].active.hydrated.sha,
    "lastSha": promotionStrategy.status.environments[2].active.hydrated.sha,
    "env": promotionStrategy.status.environments[2].branch
  }
```

#### Fire on Promotion Failure

```yaml
triggerExpr: |
  {
    "trigger": len(promotionStrategy.status.environments) > 0 &&
                  promotionStrategy.status.environments[0].lastFailureMessage != "" &&
                  status.triggerData["notifiedFailure"] != "true",
    "notifiedFailure": "true"
  }
```

**Note**: To reset and allow firing again after a failure is resolved, you would need to manually edit the PromotionEventHook status to clear `notifiedFailure`, or use a more sophisticated expression that checks if the failure message has changed.

## Webhook Actions

### Basic Webhook

```yaml
action:
  webhook:
    url: "https://api.example.com/notify"
    method: POST
    timeout: 30s
    headers:
      Content-Type: application/json
      body: |
        {
          "environment": "{{ (index .PromotionStrategy.Status.Environments 0).Branch }}",
          "sha": "{{ (index .PromotionStrategy.Status.Environments 0).Active.Hydrated.Sha }}"
        }
```

### Templating

Webhook URLs, headers, and bodies support Go templates with access to:

- `.PromotionStrategy`: The PromotionStrategy resource
- `.WebhookResponseData`: Data from `status.webhookResponseData` (empty when rendering webhook request, populated for resource templates after webhook execution)

Available template functions include all [Sprig functions](http://masterminds.github.io/sprig/) (except `env`, `expandenv`, `getHostByName`), plus:

- `toJson`: Convert data to JSON
- `fromJson`: Parse JSON string
- `now`: Current timestamp
- `urlQueryEscape`: URL-encode strings

### Authentication

#### Basic Authentication

```yaml
action:
  webhook:
    url: "https://api.example.com/notify"
    method: POST
    auth:
      basic:
        secretRef:
          name: api-credentials
          usernameKey: username  # defaults to "username"
          passwordKey: password  # defaults to "password"
```

#### Bearer Token

```yaml
action:
  webhook:
    url: "https://api.example.com/notify"
    method: POST
    auth:
      bearer:
        secretRef:
          name: api-token
          tokenKey: token  # defaults to "token"
```

#### OAuth2 Client Credentials

```yaml
action:
  webhook:
    url: "https://api.example.com/notify"
    method: POST
    auth:
      oauth2:
        tokenURL: "https://auth.example.com/oauth/token"
        secretRef:
          name: oauth-credentials
          clientIDKey: client_id      # defaults to "clientID"
          clientSecretKey: client_secret  # defaults to "clientSecret"
        scopes:
          - api.read
          - api.write
```

#### TLS Client Certificates

```yaml
action:
  webhook:
    url: "https://api.example.com/notify"
    method: POST
    auth:
      tls:
        secretRef:
          name: tls-credentials
          certKey: tls.crt  # defaults to "tls.crt"
          keyKey: tls.key   # defaults to "tls.key"
          caKey: ca.crt     # optional, defaults to "ca.crt"
```

#### Custom Authentication (Inline Secrets)

For custom auth patterns, reference secrets directly in headers or body:

```yaml
action:
  webhook:
    url: "https://api.example.com/notify"
    method: POST
    headers:
      X-API-Key: "secret://api-credentials/api-key"
      Authorization: "Bearer secret://api-token/token"
    body: |
      {
        "api_secret": "secret://api-credentials/secret"
      }
```

Secret references use the format: `secret://<secret-name>/<key-name>`

## Resource Actions

Resource actions create or update Kubernetes resources using templated YAML.

### Basic Resource

```yaml
action:
  resource:
    template: |
      apiVersion: v1
      kind: ConfigMap
      metadata:
        name: promotion-status
        namespace: default
      data:
        environment: "{{ (index .PromotionStrategy.Status.Environments 0).Branch }}"
        sha: "{{ (index .PromotionStrategy.Status.Environments 0).Active.Hydrated.Sha }}"
        updated: "{{ now }}"
```

### Resource with Owner Reference

Set the PromotionEventHook as the owner to automatically clean up resources:

```yaml
action:
  resource:
    setOwnerReference: true
    template: |
      apiVersion: batch/v1
      kind: Job
      metadata:
        name: integration-test-{{ .WebhookResponseData.sha | trunc 8 }}
        namespace: default
      spec:
        template:
          spec:
            containers:
            - name: test
              image: my-test-image:latest
                env:
                - name: ENVIRONMENT
                  value: "{{ (index .PromotionStrategy.Status.Environments 0).Branch }}"
            restartPolicy: Never
```

## Webhook Response Processing

### WebhookResponseExpr

After a webhook executes successfully, you can use `webhookResponseExpr` to extract and transform data from the webhook response. This data is stored in `status.webhookResponseData` and becomes available to:

1. **Future `triggerExpr` evaluations** - via `status.WebhookResponseData`
2. **Resource templates** - via `.WebhookResponseData` context

```yaml
spec:
  webhookResponseExpr: |
    {
      "deploymentId": webhookResponse.data.id,
      "status": webhookResponse.data.status,
      "timestamp": webhookResponse.data.created_at
    }
```

The `webhookResponseExpr` has access to:
- `promotionStrategy`: The full PromotionStrategy resource
- `webhookResponse`: Object with `statusCode`, `headers`, `body`, and `data` (parsed JSON)

**Important**: The result must be a map that can be converted to `map[string]string` for storage in the status.

### Using Webhook Response Data in Triggers

Once `webhookResponseData` is set, you can use it in future `triggerExpr` evaluations:

```yaml
spec:
  triggerExpr: |
    {
      "trigger": status.webhookResponseData["deploymentId"] != "" &&
                    status.webhookResponseData["status"] == "complete" &&
                    status.triggerData["notified"] == "",
      "notified": "true"
    }
```

## Combined Webhook and Resource Actions

When both webhook and resource actions are specified, they execute sequentially (webhook first, then resource). Use `webhookResponseExpr` to transform the webhook response, and the result becomes available in `.Data` for resource templates:

```yaml
spec:
  triggerExpr: |
    {
      "trigger": len(promotionStrategy.status.environments) > 0 &&
                    promotionStrategy.status.environments[len(promotionStrategy.status.environments)-1].branch == "environment/production",
      "env": "production"
    }
  
  webhookResponseExpr: |
    {
      "deploymentId": webhookResponse.data.id,
      "status": webhookResponse.data.status
    }
  
  action:
    webhook:
      url: "https://api.example.com/create-deployment"
      method: POST
      body: |
        {
          "environment": "{{ (index .PromotionStrategy.Status.Environments 0).Branch }}"
        }
    
    resource:
      template: |
        apiVersion: v1
        kind: ConfigMap
        metadata:
          name: deployment-tracking
          namespace: default
        data:
          deployment_id: "{{ .WebhookResponseData.deploymentId }}"
          status: "{{ .WebhookResponseData.status }}"
```

## Retry Policy

Configure how actions retry on failure:

```yaml
spec:
  retryPolicy:
    strategy: exponential  # or "fixed" or "none"
    maxAttempts: 5
    initialDelay: 5s
    maxDelay: 5m
```

**Strategies:**

- `none`: No retries
- `fixed`: Retry with constant `initialDelay`
- `exponential`: Exponential backoff starting at `initialDelay`, capped at `maxDelay`

## Status

The PromotionEventHook status tracks execution state:

```yaml
status:
  conditions:
    - type: Ready
      status: "True"
      reason: ActionsSucceeded
      message: All actions completed successfully
  
  lastTriggerTime: "2024-01-15T10:30:00Z"
  lastEvaluationTime: "2024-01-15T10:30:05Z"
  
  triggerData:
    lastSha: "abc123"
    notified: "true"
  
  webhookResponseData:
    deploymentId: "dep-12345"
    status: "complete"
  
  webhookStatus:
    success: true
    statusCode: 200
    message: "Request completed successfully"
    attempts: 1
  
  resourceStatus:
    success: true
    message: "Resource created successfully"
    apiVersion: "v1"
    kind: "ConfigMap"
    namespace: "default"
    name: "promotion-status"
    attempts: 1
```

**Key Status Fields:**

- **`triggerData`**: Custom data returned from `triggerExpr` (excluding the `trigger` boolean). Persists across reconciliations and enables fire-once patterns.
- **`webhookResponseData`**: Transformed webhook response data from `webhookResponseExpr`. Available to future `triggerExpr` evaluations and resource templates via `.Data`.
- **`webhookStatus`**: Tracks webhook execution success/failure, HTTP status code, and retry attempts.
- **`resourceStatus`**: Tracks resource creation/update success/failure and details about the managed resource.

## Complete Examples

### Example 1: Slack Notification on Production Deployment

```yaml
apiVersion: promoter.argoproj.io/v1alpha1
kind: PromotionEventHook
metadata:
  name: slack-prod-notification
  namespace: default
spec:
  promotionStrategyRef:
    name: my-app
  
  triggerExpr: |
    {
      "trigger": len(promotionStrategy.status.environments) > 0 &&
                    promotionStrategy.status.environments[len(promotionStrategy.status.environments)-1].branch == "environment/production" &&
                    status.triggerData["lastProdSha"] != promotionStrategy.status.environments[len(promotionStrategy.status.environments)-1].active.hydrated.sha,
      "lastProdSha": promotionStrategy.status.environments[len(promotionStrategy.status.environments)-1].active.hydrated.sha,
      "sha": promotionStrategy.status.environments[len(promotionStrategy.status.environments)-1].active.hydrated.sha,
      "env": "production"
    }
  
  action:
    webhook:
      url: "secret://slack-webhook/url"
      method: POST
      headers:
        Content-Type: application/json
      body: |
        {
          "text": "🚀 *Production Deployment Complete*",
          "blocks": [
            {
              "type": "section",
              "text": {
                "type": "mrkdwn",
                    "text": "*Application:* {{ .PromotionStrategy.metadata.name }}\n*SHA:* `{{ .WebhookResponseData.sha }}`\n*Environment:* {{ .WebhookResponseData.env }}"
              }
            }
          ]
        }
  
  retryPolicy:
    strategy: exponential
    maxAttempts: 3
    initialDelay: 5s
    maxDelay: 1m
---
apiVersion: v1
kind: Secret
metadata:
  name: slack-webhook
  namespace: default
type: Opaque
stringData:
  url: "https://hooks.slack.com/services/YOUR/WEBHOOK/URL"
```

### Example 2: Create Integration Test Job

```yaml
apiVersion: promoter.argoproj.io/v1alpha1
kind: PromotionEventHook
metadata:
  name: integration-test-staging
  namespace: default
spec:
  promotionStrategyRef:
    name: my-app
  
  triggerExpr: |
    {
      "trigger": len(promotionStrategy.status.environments) > 0 &&
                    promotionStrategy.status.environments[0].branch == "environment/staging" &&
                    status.triggerData["lastTestedSha"] != promotionStrategy.status.environments[0].active.hydrated.sha,
      "lastTestedSha": promotionStrategy.status.environments[0].active.hydrated.sha,
      "sha": promotionStrategy.status.environments[0].active.hydrated.sha
    }
  
  action:
    resource:
      setOwnerReference: true
      template: |
        apiVersion: batch/v1
        kind: Job
        metadata:
          name: integration-test-{{ .WebhookResponseData.sha | trunc 8 }}
          namespace: {{ .PromotionStrategy.metadata.namespace }}
        spec:
          ttlSecondsAfterFinished: 3600
          template:
            spec:
              containers:
              - name: test
                image: my-org/integration-tests:latest
                env:
                - name: TEST_ENVIRONMENT
                  value: "{{ (index .PromotionStrategy.Status.Environments 0).Branch }}"
                - name: GIT_SHA
                  value: "{{ .WebhookResponseData.sha }}"
                - name: APP_URL
                  value: "https://{{ (index .PromotionStrategy.Status.Environments 0).Branch }}.example.com"
              restartPolicy: Never
          backoffLimit: 0
```

### Example 3: Jira Ticket Creation with OAuth2

```yaml
apiVersion: promoter.argoproj.io/v1alpha1
kind: PromotionEventHook
metadata:
  name: jira-ticket-prod
  namespace: default
spec:
  promotionStrategyRef:
    name: my-app
  
  # Fire once when production is reached, by checking if we already have a ticket key
  triggerExpr: |
    {
      "trigger": len(promotionStrategy.status.environments) > 0 &&
                    promotionStrategy.status.environments[len(promotionStrategy.status.environments)-1].branch == "environment/production" &&
                    status.webhookResponseData["ticketKey"] == "",
      "sha": promotionStrategy.status.environments[len(promotionStrategy.status.environments)-1].active.hydrated.sha,
      "timestamp": now().Format("2006-01-02 15:04:05")
    }
  
  webhookResponseExpr: |
    {
      "ticketKey": webhookResponse.data.key,
      "ticketUrl": "https://your-domain.atlassian.net/browse/" + webhookResponse.data.key
    }
  
  action:
    webhook:
      url: "https://your-domain.atlassian.net/rest/api/3/issue"
      method: POST
      timeout: 30s
      auth:
        oauth2:
          tokenURL: "https://auth.atlassian.com/oauth/token"
          secretRef:
            name: jira-oauth
          scopes:
            - write:jira-work
      headers:
        Content-Type: application/json
      body: |
        {
          "fields": {
            "project": {
              "key": "DEPLOY"
            },
            "summary": "Production Deployment: {{ .PromotionStrategy.metadata.name }}",
            "description": {
              "type": "doc",
              "version": 1,
              "content": [
                {
                  "type": "paragraph",
                  "content": [
                    {
                      "type": "text",
                      "text": "Application: {{ .PromotionStrategy.metadata.name }}\nSHA: {{ .WebhookResponseData.sha }}\nTime: {{ .WebhookResponseData.timestamp }}"
                    }
                  ]
                }
              ]
            },
            "issuetype": {
              "name": "Task"
            }
          }
        }
    
    resource:
      template: |
        apiVersion: v1
        kind: ConfigMap
        metadata:
          name: {{ .PromotionStrategy.metadata.name }}-jira
          namespace: {{ .PromotionStrategy.metadata.namespace }}
        data:
          jira_ticket: "{{ .WebhookResponseData.ticketKey }}"
          jira_url: "{{ .WebhookResponseData.ticketUrl }}"
---
apiVersion: v1
kind: Secret
metadata:
  name: jira-oauth
  namespace: default
type: Opaque
stringData:
  clientID: "your-client-id"
  clientSecret: "your-client-secret"
```

### Example 4: PagerDuty Alert on Failure

```yaml
apiVersion: promoter.argoproj.io/v1alpha1
kind: PromotionEventHook
metadata:
  name: pagerduty-alert-failure
  namespace: default
spec:
  promotionStrategyRef:
    name: critical-app
  
  triggerExpr: |
    {
      "trigger": len(promotionStrategy.status.environments) > 0 &&
                    promotionStrategy.status.environments[0].lastFailureMessage != "" &&
                    status.triggerData["alertedFailure"] != "true",
      "alertedFailure": "true",
      "failureMessage": promotionStrategy.status.environments[0].lastFailureMessage,
      "environment": promotionStrategy.status.environments[0].name
    }
  
  action:
    webhook:
      url: "https://events.pagerduty.com/v2/enqueue"
      method: POST
      headers:
        Content-Type: application/json
      body: |
        {
          "routing_key": "secret://pagerduty-key/integration-key",
          "event_action": "trigger",
          "payload": {
            "summary": "Promotion failed for {{ .PromotionStrategy.metadata.name }} in {{ .WebhookResponseData.environment }}",
            "severity": "error",
            "source": "gitops-promoter",
            "custom_details": {
              "application": "{{ .PromotionStrategy.metadata.name }}",
              "environment": "{{ .WebhookResponseData.environment }}",
              "failure_message": "{{ .WebhookResponseData.failureMessage }}"
            }
          }
        }
  
  retryPolicy:
    strategy: exponential
    maxAttempts: 5
    initialDelay: 10s
    maxDelay: 5m
---
apiVersion: v1
kind: Secret
metadata:
  name: pagerduty-key
  namespace: default
type: Opaque
stringData:
  integration-key: "your-pagerduty-integration-key"
```

## Best Practices

1. **Use Fire-Once Logic**: Always include state tracking in `triggerData` to prevent duplicate actions.

2. **Handle Secrets Securely**: Store sensitive data in Kubernetes Secrets and reference them using `secret://` notation or `secretRef`.

3. **Set Appropriate Timeouts**: Webhook timeout defaults to 30 seconds. Adjust based on your external service's response time.

4. **Configure Retry Policies**: Use exponential backoff for external services to handle transient failures gracefully.

5. **Use Owner References**: When creating resources, consider setting `setOwnerReference: true` for automatic cleanup.

6. **Test Expressions**: Use the expr playground or write unit tests for complex trigger expressions.

7. **Monitor Status**: Check the `status` field regularly to ensure actions are executing as expected.

8. **Namespace Isolation**: PromotionEventHooks can only create resources in the same namespace as the PromotionStrategy.

## Troubleshooting

### Action Not Firing

- Check `status.lastEvaluationTime` to ensure the controller is reconciling
- Verify `triggerExpr` returns `trigger: true` using expr playground
- Check controller logs for expression evaluation errors

### Webhook Failures

- Check `status.webhookStatus` for HTTP status codes and error messages
- Verify authentication credentials in secrets
- Test webhook URL manually with curl
- Check timeout settings if requests are slow

### Resource Creation Failures

- Check `status.resourceStatus` for detailed error messages
- Verify RBAC permissions for the controller service account
- Test template rendering with sample data
- Ensure resource names don't exceed Kubernetes limits (63 characters)

### Retry Exhaustion

- Review `status.webhookStatus.attempts` and `status.resourceStatus.attempts`
- Check if `maxAttempts` is sufficient
- Investigate underlying cause of failures (network, auth, validation)

## RBAC Considerations

The GitOps Promoter controller needs appropriate RBAC permissions to create/update resources. Ensure the controller's ServiceAccount has permissions for any resource types your PromotionEventHooks will create.

Example ClusterRole:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: promoter-eventhook-resources
rules:
- apiGroups: ["batch"]
  resources: ["jobs"]
  verbs: ["create", "update", "patch", "get", "list"]
- apiGroups: [""]
  resources: ["configmaps", "secrets"]
  verbs: ["create", "update", "patch", "get", "list"]
```

## See Also

- [PromotionStrategy CRD](crd-specs.md#promotionstrategy)
- [Gating Promotions](gating-promotions.md)
- [Monitoring Events](monitoring/events.md)
- [expr Language Documentation](https://expr-lang.org/)
- [Sprig Template Functions](http://masterminds.github.io/sprig/)

