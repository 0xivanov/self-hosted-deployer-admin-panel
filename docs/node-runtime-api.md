# Node runtime observation API

`internal/noderuntimeapi` connects the portal recovery worker to a single assigned
Node runtime using authenticated HTTPS. The client implements `NodeRuntimeReader`
and can be passed directly to `ReconcileNodeDeployment`. This API currently exposes
only status reads; it cannot launch code, upload artifacts or change a route.

## Management access

The handler must run on a private management listener separate from customer
content, with listener/header timeouts and a bounded header size. It requires TLS,
an exact configured Host, a 32-byte hex bearer secret, and exact project/runtime
headers. Browser Origin and fetch-site headers are rejected. Only GET requests to
`/v1/operations/<64-hex-operation-id>` are accepted; query strings, encoded paths,
request bodies and mutation methods are rejected. Authentication errors and
provider failures return generic messages without credential or exception details.

Each request includes a fresh random 32-byte observation nonce. The handler echoes
that nonce in a bounded JSON envelope after invoking its assigned provider. Both
requests and responses prohibit caching. A cached/replayed response with a previous
nonce is rejected by the client. This does not excuse a provider from taking a
fresh runtime observation: the nonce validates the exchange, not process identity.
At most four provider reads run concurrently, each with a ten-second context bound.
Providers must honor cancellation and must not turn reads into deployment actions.

## Client verification

The client requires a bare operator-assigned HTTPS origin, with certificate
verification enabled and optional dedicated CA roots. It disables environment
proxies, redirects and automatic decompression, limits the response to 16 KiB,
requires JSON and rejects unknown fields/trailing data. It checks the response
nonce, project/runtime/operation identities, candidate shapes, toolchain digest,
architecture and consistency between the active route and recorded newest attempt.
Store reconciliation additionally checks the expected deployment, revision, artifact,
settlement, cleanup and health requirements.

The client stamps `ObservedAt` on its own clock only after validating the response.
The handler clears the remote timestamp, so clock skew cannot turn a cached remote
time into worker-local freshness evidence. Provider implementations must verify
current listener/artifact/toolchain binding, active health and operation settlement.
An old router snapshot or customer app output alone is not a valid observation.
Credentials belong in private worker/runtime configuration and must not be placed
in source archives, customer environments, URLs or logs.

## Verification and remaining work

Race-enabled integration tests use real TLS listeners to verify certificate trust,
scoped bearer authentication, identity checks, cached nonce rejection, malformed/
oversized/encoded responses, redirects, browser/request guards and concurrency
bounds. A connected portal test creates a retained fixture release and deployment,
reads its explicit runtime evidence fixture over TLS, reconciles the active record
and verifies archive retention. This tests the persistence/transport connection;
it does not claim to launch a Node app or prove a production process identity.

The production launcher/provider, credential provisioning/rotation, private
listener entrypoint, mutation protocol, worker scheduling and end-to-end deployment
qualification are still required before customer runtime activation can go live.
