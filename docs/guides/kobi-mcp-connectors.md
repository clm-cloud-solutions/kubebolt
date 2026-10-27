# Cloud connectors for Kobi

Kobi sees inside the cluster. When a workload fails because of something
outside it (the database is stopped, a firewall rule is gone, a queue is
full of dead letters, a node pool cannot grow because of a quota), the
evidence in Kubernetes stops at "cannot connect". Cloud connectors let Kobi
**read** those resources in your cloud account through curated MCP servers:

| Connector | Server | Credentials |
|---|---|---|
| Amazon Web Services | AWS MCP Server, hosted by AWS | an IAM role KubeBolt assumes with an external ID it generates, or the AWS identity KubeBolt already runs with |
| Microsoft Azure | Azure MCP Server, running next to the KubeBolt API (a sidecar) | a read-only service principal held by the sidecar |
| Google Cloud | Google's remote MCP servers | not offered yet: the Connectors page lists it as **Soon** |

Connectors are set up in **Administration › AI (Kobi) › Connectors**. They
keep their settings in KubeBolt's settings store, so they need authentication
on (`KUBEBOLT_AUTH_ENABLED`, the default); with it off there is no
Connectors tab.

## What "read-only" means here

It is enforced in layers, not promised:

- **Only curated tools exist.** Each connector exposes an allowlist. A tool
  that is not on it is not in the model's catalogue, and a call to it is
  refused again at call time.
- **No writes, no secret values, no database queries.** AWS: the model never
  writes code. It calls a typed `call {service, operation, params}`, KubeBolt
  renders a fixed script for read operations only, then checks that exactly
  that API ran. Azure: a `(namespace, command)` allowlist; every value must
  have its declared type, and values the sidecar would read as a file
  (`@…`), an option (`--…`) or a path out of the subscription are refused
  (see [Limits of the Azure connector](#limits-of-the-azure-connector)).
- **Credentials you control.** The IAM role or service principal carries
  only read roles; the setup guide in each connector's drawer generates them.
- **Output is data.** Every result reaches the model redacted (keys, tokens,
  passwords, private keys), capped at 32 KiB and wrapped in
  `<external_tool_output>`; the prompt tells Kobi it is never an instruction.
- **Audited.** Every call leaves an access record (`mcp_tool_call`:
  connector, tool, operation or command, duration, result; never arguments
  or responses) in the action history, `GET /api/v1/admin/actions?class=access`.

Limits: 12 cloud calls per question, 120 per minute for the whole install,
4 at a time per connector, 30 s per call (`KUBEBOLT_MCP_TOOL_TIMEOUT`).

## Who can use a connector

Each connector has a minimum role, set in its drawer under *Who can use it
in Kobi*: *Editors and admins* (the default), *Everyone* or *Admins only*.
A user below it does not see the connector's tools, and Kobi's prompt does
not mention it; a call made anyway is refused and recorded as denied.
Changing it does not affect the verification or the connector's session.

A connector reads the same cloud account for every user who clears the
minimum role. The public MCP endpoint (`/api/v1/mcp`) never serves connector
tools.

## Configuration

| Variable | Helm value | Default | Meaning |
|---|---|---|---|
| `KUBEBOLT_MCP_CONNECTORS` | `mcpConnectors.enabled` | on | `false` removes the whole surface: the Connectors tab, Kobi's cloud tools and their lines in its prompt. Accepts `0`/`f`/`false`/`no`/`n`/`off` (any case); anything else leaves it on |
| `KUBEBOLT_MCP_AWS_PRINCIPAL_ARN` | `mcpConnectors.aws.principalArn` | — | The AWS identity KubeBolt runs as, printed as the principal of the trust policy in the AWS setup guide. Empty leaves a placeholder there |
| `KUBEBOLT_MCP_AZMCP_URL` | — | `http://127.0.0.1:5001/` | Where the Azure sidecar listens (the MCP endpoint is the root path). Loopback only: any other host is refused, because the sidecar runs without sign-in. The chart's sidecar listens on the default |
| `KUBEBOLT_MCP_TOOL_TIMEOUT` | `extraEnv` | `30s` | Per-call timeout (Go duration or seconds). The AWS server's first call in a session takes 9–12 s |

The Helm values for the Azure sidecar are under
[Azure › Run the Azure MCP Server](#run-the-azure-mcp-server).

Outbound traffic, for installs behind an egress firewall: the API calls
`aws-mcp.us-east-1.api.aws` or `aws-mcp.eu-central-1.api.aws` (the connector's
*AWS MCP Server location*) and AWS STS; the Azure sidecar calls
`login.microsoftonline.com`, `management.azure.com` and, for Service Bus and
Key Vault reads, the namespace and vault hosts.

## AWS

Two ways to sign in, picked in the connector's drawer:

- **IAM role (recommended).** KubeBolt assumes a read-only role in your
  account, protected by an external ID that KubeBolt generates for your
  install. The drawer's setup guide prints the permissions policy (reads
  only, with explicit denies for secrets, instance user data, object
  contents and item reads, and a deny of anything else that comes through
  the AWS MCP Server), the trust policy with the external ID condition, and
  the CLI commands. Paste the role ARN and the region. The external ID is
  generated the first time an admin opens the Connectors page, is already
  in the printed trust policy, and is shown read-only in the drawer;
  generating it does not set the connector up. It is kept when the
  connector is removed, so setting it up again prints the same ID and the
  role keeps trusting KubeBolt.
- **This machine's credentials.** KubeBolt signs with the AWS identity it
  already runs with (environment, a config profile, IRSA, EKS Pod Identity
  or an instance role). KubeBolt only sends the read operations it lists,
  but IAM does not limit what that identity could do, so give it a
  read-only policy. A passing test carries a warning when that identity is
  the account's root user.

Both modes start from the AWS identity the API runs with: the IAM role mode
uses it to assume your role. Where it comes from:

- **EKS:** give the chart's ServiceAccount an IAM role, through IRSA
  (`serviceAccount.annotations` with `eks.amazonaws.com/role-arn`) or an EKS
  Pod Identity association, and set `mcpConnectors.aws.principalArn` to that
  role's ARN so the trust policy names it. For the IAM role mode across
  accounts, that role also needs `sts:AssumeRole` on the reader role.
- **Docker Compose:** the compose file mounts `~/.aws` into the API
  container. The IAM role mode signs with its default profile; *This
  machine's credentials* can name a profile in the drawer.
- **On a host:** the API process's environment or profile.

The first call in a session takes 9–12 s while AWS starts its sandbox;
later calls take 2–3 s.

## Azure

The Azure MCP Server runs next to the KubeBolt API and holds the Azure
identity, a read-only service principal. KubeBolt calls it on loopback and
never sees the service principal's secret. Because one server holds one
identity, an install reads one Azure tenant.

The drawer's setup guide walks through these steps with your subscription
filled in.

### Create a read-only service principal

```
SUB=<subscription-id>
az ad sp create-for-rbac --name kubebolt-azure-reader --role Reader --scopes /subscriptions/$SUB
APP_ID=<appId from the output>
az role assignment create --assignee $APP_ID --role "Monitoring Reader" --scope /subscriptions/$SUB
az role assignment create --assignee $APP_ID --role "Log Analytics Reader" --scope /subscriptions/$SUB
```

Keep the `appId`, `password` and `tenant` it prints. Optional roles, scoped
to the resources Kobi should read:

- *Azure Service Bus Data Receiver* on a namespace, for queue and topic
  backlog and dead-letter counts.
- *Key Vault Reader* on a vault, for key and certificate expiry. Never grant
  *Key Vault Secrets User*: KubeBolt does not read secret values, and the
  identity should not be able to either.

### Register the Resource Health provider (recommended)

Resource Health says whether Azure itself reports a resource available or
degraded. It answers only in a subscription that has registered its resource
provider. Run this once as an Owner or Contributor of the subscription (the
read-only service principal cannot):

```
SUB=<subscription-id>
az provider register --namespace Microsoft.ResourceHealth --subscription $SUB
az provider show --namespace Microsoft.ResourceHealth --subscription $SUB --query registrationState -o tsv
```

Wait until the second command prints `Registered`; it can take about ten
minutes, with intermittent 409s from `resourcehealth` along the way.

- **What it does.** Registering turns the Resource Health API on in the
  subscription. It creates no resource and gives none of your identities new
  permissions. Resource Health is free. Microsoft adds the provider's app to
  your Microsoft Entra tenant when you register it.
- **Least privilege.** Microsoft advises registering a resource provider only
  when you use it. A provider can be unregistered once no resources of its
  types remain.
- **Without it.** Kobi still reads a resource's health events from its
  activity log (for example an event titled *Stopped* with cause
  *UserInitiated*), but not its current availability status (`Available`,
  `Degraded`, `Unavailable`, `Unknown`): `resourcehealth` answers 409 and
  Kobi says so.

Sources: Microsoft Learn,
[Azure resource providers and types](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/resource-providers-and-types)
and the
[Resource Health FAQ](https://learn.microsoft.com/en-us/azure/service-health/resource-health-faq).

### Run the Azure MCP Server

The server runs read-only, with response caching off (an answer during an
incident is never a cached one), telemetry off, and its own sign-in off,
which it allows only on loopback. KubeBolt pins the release its allowlist was
reviewed against, `3.0.0-beta.46`: the betas rename commands, so the pin
moves only with a KubeBolt release.

**Helm.** The chart runs it as a second container of the API pod
(`mcr.microsoft.com/azure-sdk/azure-mcp`, pinned by digest). It reads the
service principal from a Secret only that container is given, listens on the
pod's loopback, runs as a non-root user with a read-only root filesystem and
no capabilities, and does not get the pod's ServiceAccount token.

```
NAMESPACE=kubebolt
read -rs AZURE_CLIENT_SECRET   # paste the password; it stays out of your shell history
kubectl create secret generic kubebolt-azure-reader -n $NAMESPACE \
  --from-literal=tenant-id=<tenant> \
  --from-literal=client-id=<appId> \
  --from-literal=client-secret="$AZURE_CLIENT_SECRET"
```

```yaml
mcpConnectors:
  azure:
    sidecar:
      enabled: true
      existingSecret: kubebolt-azure-reader
```

| Value | Default | Meaning |
|---|---|---|
| `mcpConnectors.azure.sidecar.enabled` | `false` | Run the sidecar in the API pod |
| `mcpConnectors.azure.sidecar.existingSecret` | — | Secret with `tenant-id`, `client-id`, `client-secret`. Required: rendering fails without it |
| `mcpConnectors.azure.sidecar.image.*` | the reviewed release, by digest | Change only with a KubeBolt release |
| `mcpConnectors.azure.sidecar.resources` | 20m / 128Mi requested, 500m / 512Mi limit | |
| `mcpConnectors.azure.sidecar.securityContext` | non-root (uid 1654), read-only root filesystem, no capabilities | On OpenShift unset `runAsUser`: restricted-v2 assigns one from the namespace's range |

The sidecar has no probes: the kubelet probes the pod IP, which it does not
listen on. The connector's **Test connection** reports when nothing answers.

**Docker Compose.** Put `AZURE_TENANT_ID`, `AZURE_CLIENT_ID` and
`AZURE_CLIENT_SECRET` in `deploy/.env`, then start the `azure` profile. The
`azure-mcp` service joins the `api` container's network and listens on its
loopback:

```
cd deploy && docker compose --profile azure up -d
```

**Anywhere else** (the API on a host, or a container you run yourself): the
drawer's setup guide gives the `npx -y @azure/mcp@3.0.0-beta.46 server start …`
command with the same options and environment. Run it on the API's host, or
in a container that shares the API's network; set `KUBEBOLT_MCP_AZMCP_URL` on
the API if it listens on another loopback port.

### Connect it

In **Administration › AI (Kobi) › Connectors › Microsoft Azure › Set up**,
enter the subscription ID (and the tenant ID only if the service principal
sees several directories), **Test connection** (it lists the subscriptions
the server's identity can see and checks that this one is among them), then
**Save and enable**.

## What Kobi reads

**Azure:**

- **Activity log:** who changed what and when on a resource, found by name,
  including its Resource Health events.
- **Platform metrics:** any resource's Azure Monitor metrics (CPU,
  connections, sessions, dead-lettered messages…).
- **Resource Health:** availability and reason per resource; service health
  events.
- **PostgreSQL and MySQL flexible servers, Azure SQL:** inventory,
  configuration, parameters, SQL firewall rules and elastic pools; server
  level only.
- **AKS:** cluster and node pool state, autoscaling bounds.
- **Inventory and quotas:** subscriptions, resource groups, resources; quota
  usage per region.
- **Messaging, registries, Key Vault, storage:** Service Bus backlog and
  dead letters, Event Hubs, Container Registry and role assignments, key and
  certificate expiry, storage account settings.
- **Logs:** KQL over a Log Analytics workspace or one resource's logs, when
  diagnostic settings send them there.
- **Advisor and AppLens:** recommendations and guided diagnosis.

**AWS:** EKS and the EC2 and Auto Scaling behind its nodes; RDS, Aurora and
DocumentDB (status, events, maintenance, parameter groups, Performance
Insights); CloudWatch metrics, alarms and logs; load balancers and target
health, security groups, network ACLs, route tables, NAT, endpoints and
Route 53; ElastiCache, MemoryDB, SQS, SNS and MSK; DynamoDB and Redshift
status; EBS, EFS and S3 bucket names; CloudTrail management events; AWS
Health and Service Quotas; IAM roles (IRSA and Pod Identity bindings) and
ECR; Cost Explorer; Secrets Manager and SSM metadata. Each drawer lists the
families it reads and what it never does.

**What it does not see:**

- **Database contents:** sessions, locks, grants and passwords stay out of
  reach (no SQL); Kobi finds them only through the errors in the
  application's logs.
- **Secret values and object contents**, in any cloud.
- **Azure networking** (NSGs, route tables, Private DNS, load balancers,
  Application Gateway, private endpoints): the Azure MCP Server has no tools
  for them. Kobi finds a network change through the activity log but cannot
  read the current rules. AWS can read them.
- **Azure Functions, App Service and App Configuration**, beyond their
  activity log and metrics; **Key Vault secrets**, even their metadata;
  **Microsoft Entra ID** (app registrations, expired client secrets): Kobi
  sees an expired secret only as the pod's `AADSTS…` error.
- **AWS limits:** AWS allows 10 authenticated requests per second per
  account and region to its MCP Server, and the first call in a session is
  slow (above).

## Using it

Ask as usual ("orders-api is not ready — why?"). When the evidence points
outside the cluster, Kobi calls `get_external_connections` (Hubble flows, or
the workload's environment when there is no Hubble), classifies the
destination (`*.postgres.database.azure.com` → Azure PostgreSQL, an RDS
endpoint → RDS…) and uses the matching connector, at most a handful of
calls. Tool cards read "Azure · postgres" or "AWS · call"; an answer that
used a connector ends with "Read from <cloud> · read-only · credentials
never shared with the model".

### A dependency in another cluster

An address a workload calls (a flow destination, or a configured host such
as `ORDERS_URL=http://203.0.113.10`) may be the load balancer of another
cluster in KubeBolt. `get_external_connections` matches each address against
the addresses those clusters expose: the load-balancer status of
`LoadBalancer` Services and the status of Ingresses and Gateways. It never
uses `spec.externalIPs` or spec hostnames, which anyone who can create a
Service or an Ingress could write. A match adds `inFleet` to the row
(cluster, kind, namespace/name, ports), and Kobi offers to switch to that
cluster when the failing calls go to that address, naming what to look at
there.

The clusters searched are exactly the ones `GET /clusters` shows the person
asking; without an authenticated request behind the call, nothing is
searched. Only clusters with a live connection are read; the rest count as
skipped and are never connected for this. Each cluster's addresses are
cached for 60 s. The lookup reads at most 8 clusters at a time, 3 s each and
3.5 s in total, and matches a hostname only against load-balancer hostnames,
never through DNS.

### How Azure metric queries are made

A model asks for several Azure Monitor metrics at once, at one interval, with
no dimension filter. Azure refuses a query whose metrics share no time grain
(a PostgreSQL server's `max_connections` is published every 30 minutes or
more, its active connections every minute), and without a filter it folds a
metric's dimensions into one unlabeled series. KubeBolt reads the metric
definitions Azure publishes for the resource (cached for an hour) and, when
anything needs it, queries each metric on its own:

- at the interval asked for when the metric offers it, otherwise the nearest
  coarser one it offers;
- split by every dimension the metric has, when the model gave no filter
  (`sessions_by_wait_event_type` comes back as `Lock = 10`, not `10`). A split
  Azure refuses, or one too large for the tool output, comes back unsplit.

When nothing needs changing, the query goes as asked, in one call. When the
definitions cannot be read, Azure's own time-grain refusal (it lists the
grains it takes) sends the metrics again one at a time.

## Limits of the Azure connector

Every value is checked before it reaches the sidecar, which would otherwise
read it as a path or a command line. Reads that go through Azure Resource
Manager stay in the configured subscription; the few addressed by name or
host (last bullet) are bounded by the identity's role assignments instead:

- Every value must have the JSON type the sidecar declares for its
  parameter: a string, a number, `true`/`false`, or a list of strings for a
  list parameter. Objects are refused everywhere, and so are a list for any
  other parameter and a list inside a list. A number or `true`/`false` may
  also be written as a string when it is exactly that value (`"5"`, `"1.5"`,
  `"true"`). A single string for a list parameter is taken as a one-item
  list. A `null` is treated as unset and is not forwarded.
- KQL (`query` and `table`) that calls `workspace()`, `app()`, `resource()`,
  `adx()`, `cluster()`, `database()`, `arg()` or `externaldata` is refused:
  those read outside the workspace or resource named in the call. Case,
  bracket quoting (`['workspace']`), whitespace and comments before the `(`
  do not get around it, and neither does splitting the call across the items
  of a list. A call written inside a string literal is refused too; the model
  rephrases.
- No value may contain a control character (tabs and line breaks are allowed
  only in free text: KQL, filters, questions), and names, paths and IDs
  cannot contain a character that looks like a slash or a backslash
  (`／`, `∕`, `⁄`, `⧸`, `＼`…).
- Plain names (resource group, server, vault, workspace…) cannot contain
  `/`, `\`, `%`, `?` or `#`, and cannot be `.` or `..`; a Log Analytics
  workspace is given by name or ID, not by its resource ID. Resource types,
  metric namespaces and times may contain `/`, but not an empty, `.` or `..`
  part between slashes.
- Resource IDs must be a bare resource name (resolved inside the
  subscription) or `/subscriptions/<the configured subscription>/…` with no
  empty, `.` or `..` segment and none of `\`, `%`, `?`, `#`; role scopes must
  be the latter. One trailing slash is dropped. Advisor's `resource` filter
  also takes the part of an ID after `/subscriptions/<id>/`, with the same
  rules, because it only filters results already read in the subscription.
- The Service Bus `namespace` must be `<name>.servicebus.windows.net` (or
  `.servicebus.usgovcloudapi.net` / `.servicebus.chinacloudapi.cn`).
- A value that starts with `@` or `-` is refused anywhere: the sidecar would
  read it as a file or a command-line option.
- Each refusal names the parameter and the reason, never the value.
- Key Vault keys and certificates, blob containers and Service Bus queues and
  topics are addressed by vault name, storage account name or namespace host,
  not through the subscription: the sidecar reaches any vault, account or
  namespace of that name its identity can read, in any subscription. Give
  the service principal no roles outside the configured subscription, and
  add Key Vault Reader and Azure Service Bus Data Receiver only on the vaults
  and namespaces Kobi should read.

### What the Azure MCP Server leaves out

Some commands return less than Azure Resource Manager does. Seen with
`@azure/mcp` 3.0.0-beta.46 on a stopped PostgreSQL flexible server:

- **Lists give names only.** `postgres_list` returns
  `{"Servers": ["<name>", …]}`, with no resource group, ID or state. The
  commands that read one server require the resource group, and a made-up
  one answers 404 `ResourceGroupNotFound`, so Kobi must not guess it. Every
  activity log entry of the resource carries it (`resourceGroupName`).
- **`postgres_server_config_get` has no state and no network.** It returns
  the server's name, location, version, SKU, storage and backup settings;
  Azure Resource Manager also returns the state (`Ready`, `Stopped`…) and the
  network settings, but the sidecar drops them. A configuration that answers
  does not mean the server is up.
- **How Kobi answers "is it down?":** from the resource's activity log, with
  the evidence inside the cluster (the readiness probe's "no response"):
  - `monitor_activitylog_list` needs only the resource name (add
    `resource-type` when two resources share it). It shows stop and start
    requests with their status and caller, firewall rule changes, and the
    resource's Resource Health events, even without the provider registered.
    For the stopped server, the *Activated* event had the title *Stopped*,
    `currentHealthStatus` *Degraded*, `cause` *UserInitiated*; an *Updated*
    event in the same second reported *Available*, so read the title and the
    *Activated* event, not the latest status.
  - It returns the newest 10 events of the last 24 hours unless `top` and
    `hours` say otherwise, and does not say when more exist. The tool
    descriptions tell the model to ask for `top` 20 when it looks for what
    changed.
  - `monitor_metrics_query` (`is_db_alive`, `active_connections`) also takes
    the resource by name. A query returns at most `max-buckets` time buckets
    (50 by default): over 24 hours `PT15M` is refused with a 400, and at
    `PT1H` the hour of the stop still reads 1. Start one or two hours back at
    `PT5M`: a stopped server reads 0 or has no data.
  - Once the Resource Health provider is registered, `resourcehealth` adds
    the current availability; for the stopped server it answered
    `availabilityState` *Degraded*, title *Stopped*, `reasonType`
    *Customer Initiated*.

## Troubleshooting

| Symptom | Cause |
|---|---|
| No Connectors tab under AI (Kobi) | `KUBEBOLT_MCP_CONNECTORS=false` (`mcpConnectors.enabled: false`), or authentication is off |
| Azure test: "Nothing answers at <host>: the Azure MCP Server is not running." | the sidecar is not running (`mcpConnectors.azure.sidecar.enabled`, the compose `azure` profile, or your own process), or `KUBEBOLT_MCP_AZMCP_URL` points elsewhere |
| Azure test: "The Azure MCP Server at <host> closed the connection: it is not running or restarting." | the sidecar stopped or is restarting; check `kubectl logs <api-pod> -c azure-mcp`, then test again |
| `helm install` fails: "mcpConnectors.azure.sidecar.enabled=true requires mcpConnectors.azure.sidecar.existingSecret" | create the Secret and set `existingSecret` |
| The `azure-mcp` container fails with `CreateContainerConfigError` | the Secret is missing, or lacks `tenant-id`, `client-id` or `client-secret` |
| Pod rejected on OpenShift: the sidecar's `runAsUser` is outside the namespace's range | unset `mcpConnectors.azure.sidecar.securityContext.runAsUser` |
| AWS test: cannot assume the role | the trust policy does not name KubeBolt's identity (`mcpConnectors.aws.principalArn`), or lacks the external ID shown in the drawer |
| AWS test fails in the API pod with no credentials | the ServiceAccount has no IAM role: add the IRSA annotation or a Pod Identity association |
| A user's Kobi does not use a connector the admin's does | the user's role is below the connector's minimum for Kobi |
| Kobi does not use a connector | it is not enabled or verified, or the question is answered by evidence inside the cluster first (by design) |
| Some of a connector's tools are missing (Azure namespaces) | they did not answer when the connector was first used. They are listed again in the background, from 3 up to 30 minutes apart; a passing **Test connection** of the saved settings makes the next call rebuild the connector at once |
| AWS: the external ID is the same after removing and re-adding the connector | by design: it is already in the role's trust policy |
| `resourcehealth` 409 | `Microsoft.ResourceHealth` is not registered in the subscription, or is still registering. Until then Kobi reads the health events from the activity log |
| An Azure call answers 404 `ResourceGroupNotFound` | the model guessed the resource group, for example after `postgres_list`, which gives names only. The resource's activity log entries carry the real one |
| The test button reads "Test again in Ns" | tests are paced per connector (the API answers 429 `rate_limited` with `Retry-After`) |

## Live tests (contributors)

The catalogue's live tests talk to the real providers. Each is skipped unless
its switch is exactly `1`, CI never sets them, and they only read:

| Variable | Test | What it does and needs |
|---|---|---|
| `KUBEBOLT_MCP_LIVE_AWS=1` | `TestAWSLive` | AWS credentials on this machine. A fixed list of reads (`sts GetCallerIdentity`, `ec2 DescribeRegions`, `rds DescribeDBInstances`) plus the test's canary |
| `KUBEBOLT_MCP_LIVE_AWS_REGION` | `TestAWSLive` | Region of the resources (default `us-east-1`) |
| `KUBEBOLT_MCP_LIVE_AWS_HOST_REGION` | `TestAWSLive` | AWS MCP Server host (default `us-east-1`) |
| `KUBEBOLT_MCP_LIVE_AWS_PROFILE` | `TestAWSLive` | A named profile (default: the credential chain) |
| `KUBEBOLT_MCP_LIVE_AWS_DUMP` | `TestAWSLive` | A file that receives the reply shapes, redacted (keys and types, never values) |
| `KUBEBOLT_MCP_LIVE_AZURE=1` | `TestAzureLiveSidecar`, `TestAzureLiveHardening` | The sidecar running on this machine (the drawer's `npx` command, or `KUBEBOLT_MCP_AZMCP_URL`). The hardening test replays known escapes aimed only at the all-zero subscription and checks that none leaves the process |
| `KUBEBOLT_MCP_LIVE_AZURE_SUBSCRIPTION` | both Azure tests | The subscription to read (default: the identity's default subscription) |
| `KUBEBOLT_MCP_LIVE_GCP=1` | `TestGCPLiveCatalog` | Network only: lists Google's public catalogues and fails when the allowlist has drifted from them |
| `KUBEBOLT_MCP_LIVE_GCP_UPDATE=1` | `TestGCPLiveCatalog` | Also rewrites `testdata/gcp_tools_snapshot.json` |

```
cd apps/api && KUBEBOLT_MCP_LIVE_AZURE=1 go test -count=1 -run 'TestAzureLive' -v ./internal/mcpcatalog/
```

## Not covered yet

- Google Cloud: built and tested, offered once an authenticated call has
  been validated against a real project.
- More than one Azure tenant per install (one sidecar holds one identity).
- Network policies for the chart's egress (the hosts above).
- Observability connectors (Datadog, Grafana, New Relic).
