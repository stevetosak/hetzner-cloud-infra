# core/redis — the shared cache

One Redis, shared by every project. It holds sessions and cached values, and
it holds **nothing that must survive a restart**.

| File | What it is |
|---|---|
| `namespace.yaml` | The `redis` namespace. Nothing else declares it |
| `credentials.yaml` | Template for the `redis` Secret. Values are never committed |
| `deployment.yaml` | One replica, `redis:8.10.2-alpine`, no persistence |
| `service.yaml` | `redis-master`, ClusterIP 6379 |

Address for consumers:

```
redis-master.redis.svc.cluster.local:6379
```

## Who uses it

| Consumer | Reads the password from |
|---|---|
| `authos-api` | `credentials`/`REDIS_PASS` in namespace `authos` |
| `duster` | the same key, the same Secret |

🔴 **The password therefore lives in two Secrets in two namespaces.** Rotating
`redis`/`password` without rotating `credentials`/`REDIS_PASS` leaves both
applications failing with `NOAUTH`, in a namespace nothing here points at.
Rotate both in one step, then restart Redis and both consumers.

Duster namespaces its keys `duster:*` and shares the instance deliberately.

## This Redis does not persist anything

`--save ''` and `--appendonly no`. There is no volume and no snapshot. That is
a decision, not an omission: this instance is a cache and a session store, and
the cluster has no backup of it and needs none.

**The consequence is that any restart signs every user out** — a node drain, an
image bump, an eviction. That is acceptable for what it holds. It would not be
acceptable for anything else, so do not put anything else here.

Because there is no persistence, `redis-server` never forks to write a
snapshot, which is what makes the memory headroom below as small as it is.

## Two memory settings that must move together

`--maxmemory 384mb` against `limits.memory: 512Mi`.

`maxmemory` bounds **the dataset only**. Client output buffers, replication
buffers and allocator fragmentation sit on top of it and the container limit
covers all of them. The file used to set both to 512, which leaves no headroom
at all: Redis fills to its own limit, the kernel then kills the container, and
the pod restarts empty. Raise the two together or not at all.

`--maxmemory-policy volatile-lru` evicts only keys that carry a TTL. **A key
written without a TTL is never evicted**, so an application that forgets to set
one can fill the instance, after which writes fail with an OOM error instead of
making room. This is the correct policy for a session store — it never silently
drops a live session — but it moves the failure into the application. If that
ever happens, the fix is a TTL on the offending key, not `allkeys-lru`.

## Why the securityContext is not optional here

`command: ['redis-server']` **replaces the image entrypoint.** The official
image drops privileges inside `docker-entrypoint.sh` with `setpriv`; overriding
the entrypoint skips that, so without `runAsUser` Redis runs as **root**.

`runAsUser: 999` / `runAsGroup: 1000` are the `redis` user of the official
image, read out of the image's own `/etc/passwd` rather than assumed.

Overriding the entrypoint has a second effect worth knowing. The entrypoint
walks `/usr/local/lib/redis/modules/` and appends a `--loadmodule` for every
`.so` it finds; skipping it means the four shipped modules — `redisearch.so`
(20 MB), `rejson.so` (45 MB), `redistimeseries.so`, `redisbloom.so` — are
**present in the image and not loaded**. `MODULE LIST` shows one entry,
`vectorset`, which is built into the Redis 8 binary itself and is there either
way. Resident memory with nothing loaded is about 10 MB.

Not loading them is wanted — nothing here uses them and they would eat the
memory headroom set out above. But it is a side effect of a line that looks
like it only chooses which binary to run, so restoring the entrypoint would
quietly change the memory budget.

## The password is the only access control

Flannel enforces no NetworkPolicy (ADR 0001), so **every pod in this cluster
can reach port 6379**. That is measured, not assumed: a PostgreSQL pod in
`pg-cluster` resolves `redis-master.redis.svc.cluster.local` and opens a TCP
connection to it. `--requirepass` is the whole boundary. This is the
standing argument for Cilium, recorded in ADR 0001; until then, treat the Redis
password as a cluster-wide credential.

## Apply

The Secret must exist before the Deployment, or the pod stops at
`CreateContainerConfigError` with the cause named nowhere.

```
kubectl apply -f core/redis/namespace.yaml
# create the `redis` Secret — see credentials.yaml
kubectl apply -f core/redis/service.yaml -f core/redis/deployment.yaml
```

Then check the password is 40 bytes and nothing else, which the base64 in the
Secret cannot tell you. `credentials.yaml` gives the two commands; neither
prints the value. The first build of this Redis stored a 41-byte password and
went `Ready` anyway.
