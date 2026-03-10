# Plan: Crush-Lite Distributed ZFS Storage Backend

## Context

mini-s3 currently uses a local filesystem storage model where objects are stored directly in `<dataDir>/<bucket>/<object>`. This plan describes implementing a "crush-lite" storage backend algorithm that can distribute objects across multiple ZFS storage servers for redundancy, capacity scaling, and geographic distribution.

**Why "crush-lite"?** Inspired by Ceph's CRUSH (Controlled Replication Under Scalable Hashing) algorithm, but simplified for mini-s3's scope. It provides deterministic object placement using consistent hashing with virtual nodes, without the full complexity of CRUSH maps.

---

## High-Level Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    S3 Request Handlers                       │
│     (putObjectHandler, getObjectHandler, etc.)              │
└──────────────────────────┬──────────────────────────────────┘
                           │
┌──────────────────────────▼──────────────────────────────────┐
│                  PlacementEngine                             │
│  - Deterministic object → server(s) mapping                 │
│  - Replication factor enforcement                           │
│  - Failure domain awareness                                 │
└──────────────────────────┬──────────────────────────────────┘
                           │
┌──────────────────────────▼──────────────────────────────────┐
│                  StorageBackend Interface                    │
│  - Read/Write/Delete/Stat operations                        │
│  - Health checking                                          │
│  - ZFS-aware operations                                     │
└───────┬──────────┬──────────┬──────────┬───────────────────┘
        │          │          │          │
   ┌────▼────┐ ┌───▼───┐ ┌───▼───┐ ┌────▼────┐
   │ Local   │ │  NFS  │ │ SSH/  │ │ HTTP    │
   │ ZFS     │ │ Mount │ │ SFTP  │ │ Remote  │
   └─────────┘ └───────┘ └───────┘ └─────────┘
```

---

## Placement Algorithm

### Virtual Node Ring

Each storage server is assigned multiple "virtual nodes" (vnodes) on a hash ring, proportional to its weight. This provides:

1. **Deterministic placement**: `Hash(bucket/key)` always maps to the same server(s)
2. **Stable mapping**: Adding/removing servers only moves ~1/N of objects
3. **Weighted distribution**: Higher-weight servers receive proportionally more data
4. **Failure domain spread**: Replicas placed in different racks/zones

```go
type VirtualNode struct {
    Hash       uint64    // Position on ring (0 to 2^64-1)
    ServerID   string    // Physical server identifier
    FailDomain string    // Rack/zone for failure domain separation
}

type HashRing struct {
    vnodes          []VirtualNode  // Sorted by Hash
    servers         map[string]*StorageServer
    vnodesPerWeight int            // Base vnodes per weight unit (default: 10000)
}
```

### Placement Selection

```go
func (ring *HashRing) GetPlacement(key string, replicationFactor int) []StorageServer {
    hash := xxhash64(key)
    startIdx := ring.findVnodeIndex(hash)

    var selected []StorageServer
    seenServers := make(map[string]bool)
    seenFailDomains := make(map[string]int)

    // Walk ring clockwise until we have enough replicas
    for i := 0; len(selected) < replicationFactor && i < len(ring.vnodes); i++ {
        vnode := ring.vnodes[(startIdx+i) % len(ring.vnodes)]

        if seenServers[vnode.ServerID] {
            continue
        }

        // Enforce failure domain spread if configured
        if ring.requireFailDomainSpread {
            if seenFailDomains[vnode.FailDomain] >= ring.maxPerFailDomain {
                continue
            }
        }

        server := ring.servers[vnode.ServerID]
        if server.IsHealthy() {
            selected = append(selected, *server)
            seenServers[vnode.ServerID] = true
            seenFailDomains[vnode.FailDomain]++
        }
    }

    return selected
}
```

---

## Configuration Schema

### Extended config.json

```json
{
  "dataDir": "./data/",
  "buckets": {
    "local-only": "/tank/local-data"
  },

  "distributed": {
    "enabled": true,
    "defaultReplicationFactor": 2,
    "requireFailDomainSpread": true,
    "maxPerFailDomain": 1,
    "writeQuorum": 2,
    "readQuorum": 1,
    "placementSeed": 12345,

    "servers": [
      {
        "id": "zfs-node-1",
        "type": "local",
        "path": "/tank/s3data",
        "weight": 2.0,
        "failDomain": "rack-a",
        "zfs": {
          "dataset": "tank/s3data",
          "compression": "lz4",
          "autoSnapshot": true
        }
      },
      {
        "id": "nfs-node-1",
        "type": "nfs",
        "path": "/mnt/nfs-zfs-server1/s3data",
        "weight": 1.5,
        "failDomain": "rack-b",
        "healthCheck": {
          "path": "/.health",
          "intervalSec": 30
        }
      },
      {
        "id": "ssh-node-1",
        "type": "ssh",
        "host": "storage2.local",
        "port": 22,
        "user": "s3sync",
        "keyFile": "/etc/mini-s3/ssh/id_ed25519",
        "remotePath": "/tank/s3data",
        "weight": 1.0,
        "failDomain": "rack-c",
        "zfs": {
          "dataset": "tank/s3data",
          "remoteZfsCommand": "/usr/sbin/zfs"
        }
      },
      {
        "id": "http-node-1",
        "type": "http",
        "endpoint": "https://storage3.local:8443",
        "accessKey": "remote-admin",
        "secretKey": "${HTTP_NODE_1_SECRET}",
        "weight": 1.0,
        "failDomain": "zone-2",
        "timeout": "30s"
      }
    ],

    "bucketOverrides": {
      "critical-data": {
        "replicationFactor": 3,
        "writeQuorum": 3
      },
      "ephemeral": {
        "replicationFactor": 1,
        "serverFilter": ["local-*"]
      }
    }
  }
}
```

### Server Types

| Type | Description | Use Case |
|------|-------------|----------|
| `local` | Local filesystem path | Primary storage on same host |
| `nfs` | NFS-mounted ZFS | Remote ZFS via NFS export |
| `ssh` | SSH/SFTP access | Remote ZFS with full ZFS command access |
| `http` | Remote mini-s3 instance | Federated S3-compatible nodes |

---

## Interface Definitions

### StorageBackend Interface

```go
// storage/backend.go

type BackendType int
const (
    BackendLocal BackendType = iota
    BackendNFS
    BackendSSH
    BackendHTTP
)

type StorageBackend interface {
    // Identity
    ID() string
    Type() BackendType

    // Health & Status
    IsHealthy() bool
    CheckHealth(ctx context.Context) error
    GetCapacity() (total, used, available int64, err error)

    // Object Operations
    WriteObject(ctx context.Context, bucket, key string, data io.Reader, size int64) error
    ReadObject(ctx context.Context, bucket, key string) (io.ReadCloser, error)
    DeleteObject(ctx context.Context, bucket, key string) error
    StatObject(ctx context.Context, bucket, key string) (*ObjectStat, error)

    // Metadata Operations
    WriteMetadata(ctx context.Context, bucket, key string, meta *ObjectMetadata) error
    ReadMetadata(ctx context.Context, bucket, key string) (*ObjectMetadata, error)
    DeleteMetadata(ctx context.Context, bucket, key string) error

    // Bucket Operations
    CreateBucket(ctx context.Context, bucket string) error
    DeleteBucket(ctx context.Context, bucket string) error
    ListObjects(ctx context.Context, bucket, prefix string) ([]ObjectStat, error)

    // ZFS-Specific (optional, returns nil if not supported)
    CreateSnapshot(ctx context.Context, name string) error
    ListSnapshots(ctx context.Context) ([]string, error)

    Close() error
}

type ObjectStat struct {
    Key          string
    Size         int64
    LastModified time.Time
    ETag         string
}
```

### PlacementEngine Interface

```go
// storage/placement.go

type PlacementEngine interface {
    // GetWriteTargets returns backends for writing (replication factor servers)
    GetWriteTargets(bucket, key string) ([]StorageBackend, error)

    // GetReadTargets returns backends for reading (ordered by preference)
    GetReadTargets(bucket, key string) ([]StorageBackend, error)

    // GetAllReplicas returns all backends currently storing an object
    GetAllReplicas(bucket, key string) ([]StorageBackend, error)

    // GetBucketConfig returns bucket-specific placement config
    GetBucketConfig(bucket string) *BucketPlacementConfig

    // RebuildRing rebuilds the hash ring (after config change)
    RebuildRing() error

    // GetRebalancePlan returns objects that need to move after ring change
    GetRebalancePlan(oldRing, newRing *HashRing) (*RebalancePlan, error)
}

type BucketPlacementConfig struct {
    ReplicationFactor int
    WriteQuorum       int
    ReadQuorum        int
    ServerFilter      func(ServerConfig) bool
}
```

### DistributedStorage Coordinator

```go
// storage/distributed.go

type DistributedStorage struct {
    config    *DistributedConfig
    backends  map[string]StorageBackend
    placement PlacementEngine
    mu        sync.RWMutex
}

// WriteObject writes to all replica targets, requiring quorum success.
func (ds *DistributedStorage) WriteObject(ctx context.Context, bucket, key string,
    data []byte, meta *ObjectMetadata) error

// ReadObject reads from the first healthy replica.
func (ds *DistributedStorage) ReadObject(ctx context.Context, bucket, key string) (
    []byte, *ObjectMetadata, error)

// DeleteObject removes from all replicas (best effort after quorum).
func (ds *DistributedStorage) DeleteObject(ctx context.Context, bucket, key string) error

// RepairObject checks and repairs inconsistent replicas.
func (ds *DistributedStorage) RepairObject(ctx context.Context, bucket, key string) error
```

---

## Read/Write Paths

### Write Path

```
PUT /bucket/key
       │
       ▼
┌──────────────────────┐
│ putObjectHandler     │
│ - Validate request   │
│ - Read body          │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────┐
│ PlacementEngine      │
│ - Hash bucket/key    │
│ - Get N targets      │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────────────────────┐
│ DistributedStorage.WriteObject       │
│ - Write to all targets in parallel  │
│ - Wait for quorum (W) successes     │
│ - Rollback failed backends          │
└──────────┬───────────────────────────┘
           │
    ┌──────┼──────┬──────┐
    ▼      ▼      ▼      ▼
  [B1]   [B2]   [B3]   [B4]
    │      │      │      │
    └──────┴──────┴──────┘
           │
           ▼
   Return success if quorum achieved
```

### Read Path

```
GET /bucket/key
       │
       ▼
┌──────────────────────┐
│ getObjectHandler     │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────┐
│ PlacementEngine      │
│ - Get ordered list   │
│   of replica backends│
└──────────┬───────────┘
           │
           ▼
┌──────────────────────────────────────┐
│ DistributedStorage.ReadObject        │
│ - Try backend[0]                     │
│ - If fail, try backend[1], etc.      │
│ - Optional: async repair if mismatch │
└──────────┬───────────────────────────┘
           │
           ▼
   Stream response to client
```

---

## Failure Handling

| Scenario | Write Behavior | Read Behavior |
|----------|---------------|---------------|
| 1 of 3 backends down | Succeeds (quorum=2) | Succeeds (failover) |
| 2 of 3 backends down | Fails (no quorum) | Succeeds (1 replica) |
| Backend slow (>timeout) | Treated as failed | Try next replica |
| Backend returns error | Rollback and retry | Try next replica |
| Network partition | Partial writes, needs repair | May get stale data |

---

## Implementation Phases

### Phase 1: Core Infrastructure
**Files to create:**
- `storage/backend.go` - StorageBackend interface and types
- `storage/local.go` - LocalBackend implementation
- `storage/placement.go` - PlacementEngine with hash ring
- `storage/config.go` - Configuration parsing

**Changes to existing files:**
- `main.go` - Add distributed storage initialization

**Deliverables:**
- Working local backend abstraction
- Hash ring with virtual nodes
- Deterministic placement for single-replica mode

### Phase 2: Multi-Replica Writes
**Files to create:**
- `storage/distributed.go` - DistributedStorage coordinator
- `storage/quorum.go` - Quorum handling logic

**Handler modifications:**
- `putObjectHandler` - Use distributed writes
- `completeMultipartUploadHandler` - Distributed assembly

**Deliverables:**
- Parallel writes to multiple backends
- Configurable write quorum
- Proper error handling for partial failures

### Phase 3: Remote Backends
**Files to create:**
- `storage/nfs.go` - NFSBackend with health checking
- `storage/ssh.go` - SSHBackend with SFTP operations
- `storage/http.go` - HTTPBackend with S3-compatible API

**Deliverables:**
- NFS mount support with connectivity monitoring
- SSH/SFTP backend with connection pooling
- HTTP backend with SigV4 authentication

### Phase 4: Read Path & Failover
**Handler modifications:**
- `getObjectHandler` - Try replicas in order
- `headObjectHandler` - Distributed stat
- Add read repair on inconsistency detection

**Deliverables:**
- Read from any available replica
- Automatic failover on backend failure
- Optional read repair

### Phase 5: ZFS Integration
**Files to create:**
- `storage/zfs.go` - ZFS command wrapper

**Features:**
- Automatic snapshot creation (integrates with `actions.go`)
- Compression settings propagation
- Dataset management for buckets

### Phase 6: Rebalancing & Repair
**Files to create:**
- `storage/rebalance.go` - Rebalancing coordinator
- `storage/repair.go` - Consistency checker

**Features:**
- Background rebalancing on ring changes
- Object repair for under-replicated data
- Admin CLI/API for rebalance operations

---

## Critical Files to Modify

| File | Lines | Changes |
|------|-------|---------|
| `main.go` | 688-825 | `putObjectHandler` - use StorageBackend |
| `main.go` | 827-915 | `getObjectHandler` - distributed reads |
| `main.go` | 917-989 | `deleteObjectHandler` - multi-replica delete |
| `main.go` | 195-232 | `main()` - initialize distributed storage |
| `actions.go` | 70-172 | Pattern for health monitoring to follow |

---

## Verification Plan

### Unit Tests
```go
// storage/placement_test.go
func TestHashRingDeterminism(t *testing.T)       // Same key → same servers
func TestHashRingStability(t *testing.T)         // Adding server moves minimal data
func TestFailDomainSpread(t *testing.T)          // Replicas span failure domains
func TestWeightedDistribution(t *testing.T)      // Distribution matches weights

// storage/distributed_test.go
func TestWriteQuorum(t *testing.T)               // Writes succeed with quorum
func TestWriteQuorumFailure(t *testing.T)        // Writes fail without quorum
func TestReadFailover(t *testing.T)              // Read falls back to next replica
func TestPartialWriteRollback(t *testing.T)      // Failed writes are cleaned up
```

### Integration Tests
```bash
#!/bin/bash
# Test replication
aws s3 cp testfile s3://mybucket/key --endpoint-url https://localhost:8443

# Verify file exists on multiple backends
ssh storage2 "ls /tank/s3data/mybucket/key"
ls /tank/s3data/mybucket/key

# Test failover: stop one backend
ssh storage2 "systemctl stop nfs-server"

# Read should still work
aws s3 cp s3://mybucket/key /tmp/retrieved

# Verify integrity
diff testfile /tmp/retrieved
```

### Chaos Testing Scenarios
1. Kill one backend mid-write
2. Network partition between backends
3. Disk full on one backend
4. Slow backend (add latency)
5. Concurrent writes to same key

---

## Backward Compatibility

The system maintains full backward compatibility:

1. If `distributed.enabled` is false or missing → use current `getBucketPath()` behavior
2. Custom `buckets` mappings continue to work (local-only)
3. New distributed buckets can coexist with legacy local buckets
4. Existing metadata format unchanged

---

## Dependencies

New Go dependencies:
- `github.com/cespare/xxhash/v2` - Fast hash function for placement
- `github.com/pkg/sftp` - SFTP client for SSH backend
- `golang.org/x/crypto/ssh` - SSH client

No changes to build system (existing `go.mod` pattern).
