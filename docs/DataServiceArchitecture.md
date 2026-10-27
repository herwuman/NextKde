# Data service architecture

`services/data-service` builds one Go executable from one `main` package.
Files separate responsibilities; package boundaries, goroutine ownership,
JSON field names and the versioned Socket protocol remain unchanged.
The JSONL envelope, state roots and operations are documented below.

| File | Responsibility |
| --- | --- |
| `main.go` | Instance lock, startup sequence and periodic scheduling |
| `service.go` | Shared `Service` ownership, defaults and state restoration |
| `types.go` | Persisted state and snapshot value types with JSON tags |
| `metrics.go` | CPU, memory, disk, frequency and temperature sampling |
| `activity.go` | Activity attribution, daily rollover and journal history |
| `desktop.go` | Desktop directory snapshots, reconciliation and filesystem watching |
| `desktop_placement.go` | Desktop screen assignment and placement persistence |
| `weather.go` | Weather provider, refresh policy and normalized weather state |
| `files.go` | Home-directory file index behind the island's Finder: walk bounds, fuzzy scoring and stale-reference resolution |
| `persistence.go` | Serialized, atomic state and snapshot writes |
| `subscriptions.go` | Subscriber lifetime, serialized JSONL writes and event publication |
| `protocol.go` | Request/response/error envelopes, operation dispatch and Socket serving |

## State and concurrency contracts

`Service.mu` protects mutable state and snapshot reads. Sampling reads procfs
and sysfs outside that lock, then publishes a coherent update under it. The
main scheduler owns CPU counter history and periodic sampling. Activity
settlement and day rollover remain under the same state ownership as before.

`persistMu` serializes persisted generations, while `desktopMu` serializes
directory refresh and placement changes. Subscriber membership has its own
mutex; each `subscriberConn` serializes response and event writes so concurrent
publishers cannot interleave JSONL bytes. File boundaries do not introduce new
goroutines, locks, callbacks or a second copy of state.

`main.go` still samples and publishes the initial desktop snapshot before
accepting clients. Feature code works with the shared typed state; only the
protocol and subscription layer marshals transport envelopes. Existing tests
cover concurrent snapshots and placement, subscriber framing, atomic writes,
daily activity, sensor selection and weather fallback. Run the data layer
through `tools/run-tests.sh`; Go's race detector can additionally check the
same package's concurrent tests in the verification environment.

## State and sockets

State remains at:

```text
$XDG_STATE_HOME/quickshell/shell-data-service/state.json
$XDG_STATE_HOME/quickshell/shell-data-service/snapshot.json
```

The runtime API is:

```text
$XDG_RUNTIME_DIR/kos-data.sock
```

The socket is mode `0600`. Requests are versioned JSON Lines:

```json
{"version":1,"requestId":"42","operation":"metrics.snapshot","payload":{}}
{"version":1,"requestId":"42","ok":true,"result":{"metrics":{}}}
```

Supported operations are:

- `metrics.snapshot`;
- `activity.snapshot` and `activity.active-app`;
- `desktop.snapshot`, `desktop.refresh`, `desktop.outputs` and `desktop.place`;
- `weather.snapshot`, `weather.search`, `weather.refresh`,
  `weather.set-location`, and `weather.set-units`;
- `files.search`, `files.resolve` and `files.refresh`.

Desktop changes are pushed as `desktop.changed` events after a complete
directory scan has been persisted. A completed weather state transition is
pushed as `weather.changed`; clients then request `weather.snapshot`, so every
response uses the same versioned envelope and request correlation.

Example weather requests:

```json
{"version":1,"requestId":"w1","operation":"weather.search","payload":{"query":"Changsha","language":"en","limit":8}}
{"version":1,"requestId":"w2","operation":"weather.set-units","payload":{"units":"metric"}}
```

The weather payload itself follows
[`shared/contracts/weather-v1.schema.json`](../shared/contracts/weather-v1.schema.json).
The service requests at most 48 hourly and seven daily points, refreshes after
one hour, and marks data stale after two hours. Failed refreshes preserve the
last complete forecast and expose the error alongside it.

## File index

`files.go` owns the index behind the island's Finder. The original app shells
out to `fd`; QML may not run commands, so the walk lives in the service and the
shell only sends a query.

- The walk happens once and produces a snapshot. It stops at 40 000 entries and
  12 directory levels, skips hidden files and directories, and skips
  `node_modules`, `venv`, `.venv`, `__pycache__`, `target` and `vendor`. Only
  regular files are indexed.
- `startFileIndexRefresh` rebuilds it in its own goroutine every ten minutes,
  and `files.refresh` asks for the same rebuild. The first build happens on
  demand: `files.search` and `files.resolve` build synchronously when the index
  has never been populated, so the first query after a service start waits for
  the walk while later ones answer from the snapshot. That walk runs on the
  connection's goroutine, so a client sharing one socket with other operations
  sees them queued behind it.
- `files.search` (`{query, limit}`, limit default 20 and clamped to 100) returns
  `{results, indexed, ready}`; an empty query returns the most recently
  modified files, and a non-empty one is scored as a subsequence match on the
  name, preferring consecutive hits and word boundaries. `indexed` is the size
  of the snapshot that answered, so a client can tell a truncated index from a
  complete one.
- `files.resolve` (`{path, name}`) backs a shelf entry that keeps a reference
  rather than a copy: it returns the original path while it still exists, and
  otherwise the most recently modified indexed file with the same name, as
  `{path, found, moved}`.
- `files.refresh` accepts a rebuild and returns `{accepted}` immediately; the
  list fills in once the background walk finishes.

The index never leaves the per-user socket and no path outside `$HOME` is
walked.


## Operations

Build/install/restart and verification run only in the VM or CI; see [Development](Development.md).
`kosctl uninstall` preserves the data state directory. Set `GOPROXY` to a reachable proxy or `off`
for a populated offline cache if the default module proxy is unavailable.
