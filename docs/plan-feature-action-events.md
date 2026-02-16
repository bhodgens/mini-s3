# Plan: Event-Based Bucket Actions System

## Overview

Add a configurable system for time- and event-based actions that execute shell commands in response to S3 operations. Actions are configured via `.bucket-actions` files (JSON5 format) placed in bucket directories, with child directories able to override parent behavior.

## Use Cases

- **Post-upload processing**: thumbnail generation, video transcoding, backup notifications
- **Delete-after-download**: ephemeral file cleanup
- **Inactivity snapshots**: ZFS snapshots after periods of no activity
- **Audit logging**: track all downloads/uploads

## Deliverables

1. **Go implementation** (`actions.go`) - Action loading, execution, and inactivity tracking
2. **Handler integration** (`main.go`) - Trigger hooks in PUT/GET/DELETE handlers
3. **Sample config** (`bucket-actions.sample.json5`) - Documented examples
4. **Discovery script** (`scripts/show-bucket-actions.sh`) - Show all actions in a subtree
5. **Documentation** (`README.md` update) - Feature documentation
6. **Plan archive** (`docs/plan-feature-action-events.md`) - Copy of this plan

---

## Configuration Format (`.bucket-actions`)

```json5
{
  "version": "1.0",

  "after_upload": [
    {
      "name": "thumbnail-generator",
      "description": "Generate thumbnails for images",
      "patterns": ["*.jpg", "*.png"],           // Glob patterns (empty = all)
      "command": "/scripts/thumb.sh \"$FILE_PATH\"",
      "async": true,                             // Non-blocking (default: true)
      "timeout": 60,                             // Seconds (0 = no limit)
      "enabled": true
    }
  ],

  "after_download": [
    {
      "name": "ephemeral-cleanup",
      "patterns": ["ephemeral/*"],
      "command": "rm -f \"$FILE_PATH\""
    }
  ],

  "after_delete": [
    {
      "name": "thumbnail-cleanup",
      "patterns": ["*.jpg", "*.png"],
      "command": "rm -f \"$BUCKET_PATH/.thumbs/$(basename \"$OBJECT_KEY\")\""
    }
  ],

  "inactivity_timeout": {
    "duration": "30m",                          // Supports: s, m, h, d
    "command": "zfs snapshot tank/data@$(date +%s)",
    "reset_on": ["upload", "delete"],           // Activity types that reset timer
    "enabled": true
  },

  "inheritance": {
    "mode": "merge"                             // merge | override | disable
  }
}
```

## Available Variables

| Variable | Description |
|----------|-------------|
| `$FILE_PATH` | Full path to object data file |
| `$METADATA_PATH` | Path to `.metadata/<obj>.meta` |
| `$BUCKET_NAME` | S3 bucket name |
| `$BUCKET_PATH` | Filesystem path to bucket root |
| `$OBJECT_KEY` | Object key within bucket |
| `$CONTENT_TYPE` | MIME type |
| `$ETAG` | Object ETag (MD5) |
| `$SIZE` | Size in bytes |

---

## Implementation Steps

### Step 1: Create `actions.go` (~400 lines)

**Data Structures:**
```go
type BucketActions struct {
    Version           string             `json:"version"`
    AfterUpload       []ActionConfig     `json:"after_upload,omitempty"`
    AfterDownload     []ActionConfig     `json:"after_download,omitempty"`
    AfterDelete       []ActionConfig     `json:"after_delete,omitempty"`
    InactivityTimeout *InactivityConfig  `json:"inactivity_timeout,omitempty"`
    Inheritance       *InheritanceConfig `json:"inheritance,omitempty"`
}

type ActionConfig struct {
    Name        string   `json:"name"`
    Description string   `json:"description,omitempty"`
    Patterns    []string `json:"patterns,omitempty"`
    Command     string   `json:"command"`
    Async       *bool    `json:"async,omitempty"`
    Timeout     int      `json:"timeout,omitempty"`
    Enabled     *bool    `json:"enabled,omitempty"`
}

type InactivityConfig struct {
    Duration    string   `json:"duration"`
    Command     string   `json:"command"`
    Description string   `json:"description,omitempty"`
    Enabled     *bool    `json:"enabled,omitempty"`
    ResetOn     []string `json:"reset_on,omitempty"`
}

type ActionContext struct {
    FilePath, MetadataPath, BucketName string
    BucketPath, ObjectKey, ContentType string
    ETag string
    Size int64
}
```

**Key Functions:**
- `stripJSON5Comments(data []byte) []byte` - Remove `//` and `/* */` comments
- `loadActionsFile(path string) (*BucketActions, error)` - Load and parse
- `loadActionsForPath(bucketPath, objectKey string) *BucketActions` - Merge hierarchy
- `mergeActions(parent, child *BucketActions) *BucketActions` - Handle inheritance
- `triggerActions(eventType string, ctx ActionContext)` - Entry point for handlers
- `executeAction(action ActionConfig, ctx ActionContext)` - Run single action
- `runCommand(name, cmd string, timeout int)` - Execute with logging
- `substituteVariables(cmd string, ctx ActionContext) string` - Replace $VAR
- `matchesAnyPattern(key string, patterns []string) bool` - Glob matching

**Inactivity Tracker:**
```go
type InactivityTracker struct {
    mu           sync.RWMutex
    lastActivity map[string]time.Time
    timers       map[string]*time.Timer
}

var inactivityTracker *InactivityTracker

func (t *InactivityTracker) recordActivity(bucketPath, activityType string)
func (t *InactivityTracker) initializeForBucket(bucketPath string)
```

### Step 2: Integrate into `main.go`

**Add imports:**
```go
import (
    "context"
    "os/exec"
)
```

**Initialize tracker in main():**
```go
inactivityTracker = &InactivityTracker{
    lastActivity: make(map[string]time.Time),
    timers:       make(map[string]*time.Timer),
}
// Initialize timers for existing buckets with inactivity configs
initializeInactivityTimers()
```

**Hook into handlers:**

| Handler | Location | Code to Add |
|---------|----------|-------------|
| `putObjectHandler` | After line 805 | `go triggerActions("after_upload", ctx)` |
| `getObjectHandler` | After line 883 | `go triggerActions("after_download", ctx)` |
| `deleteObjectHandler` | After line 944 | `go triggerActions("after_delete", ctx)` |
| `completeMultipartUploadHandler` | After line 1576 | `go triggerActions("after_upload", ctx)` |

Each handler also calls `inactivityTracker.recordActivity(bucketPath, "upload"|"download"|"delete")`.

### Step 3: Create `bucket-actions.sample.json5`

Full example demonstrating:
- Multiple after_upload actions with different patterns
- Delete-after-download for ephemeral files
- Thumbnail cleanup on delete
- ZFS snapshot on inactivity
- Comments explaining each field

### Step 4: Create `scripts/show-bucket-actions.sh`

```bash
#!/bin/bash
# Usage: ./show-bucket-actions.sh [path]
# Finds all .bucket-actions files and displays summary

ROOT="${1:-.}"
find "$ROOT" -name ".bucket-actions" -print0 | while IFS= read -r -d '' f; do
    echo "=== $f ==="
    # Use jq if available, else just cat
    if command -v jq &>/dev/null; then
        sed 's|//.*||g' "$f" | jq -r '
          "after_upload: \(.after_upload // [] | map(.name) | join(", "))",
          "after_download: \(.after_download // [] | map(.name) | join(", "))",
          "after_delete: \(.after_delete // [] | map(.name) | join(", "))",
          "inactivity: \(.inactivity_timeout.duration // "none")"
        ' 2>/dev/null || cat "$f"
    else
        cat "$f"
    fi
    echo
done
```

### Step 5: Update `README.md`

Add new section "Event Actions" covering:
- Feature overview
- Configuration file format
- Available variables table
- Directory inheritance behavior
- Security notes (quoting, permissions, timeouts)
- Example use cases
- Link to sample config

### Step 6: Archive Plan

Copy this plan to `docs/plan-feature-action-events.md` for project documentation.

---

## Inheritance Behavior

When an object operation occurs at `bucket/a/b/c/file.txt`:

1. Load `bucket/.bucket-actions` (bucket root)
2. Load `bucket/a/.bucket-actions` if exists
3. Load `bucket/a/b/.bucket-actions` if exists
4. Load `bucket/a/b/c/.bucket-actions` if exists

**Merge modes:**
- `merge` (default): Child actions added; same-name actions override parent
- `override`: Child completely replaces parent
- `disable`: Only current directory's actions apply

---

## Security Considerations

1. **Variable quoting**: Users must quote variables (`"$FILE_PATH"`)
2. **Permissions**: Commands run as server process user
3. **Timeouts**: Enforce to prevent runaway processes
4. **Validation**: Check pattern syntax, required fields on load
5. **Logging**: All action execution logged with stdout/stderr capture

---

## Verification Plan

1. **Unit tests** (`actions_test.go`):
   - JSON5 comment stripping
   - Action loading and validation
   - Pattern matching
   - Variable substitution
   - Inheritance merging

2. **Integration tests**:
   - Create bucket with `.bucket-actions`
   - Upload file matching pattern -> verify action runs
   - Download file -> verify after_download action
   - Test inheritance with nested directories
   - Test inactivity timer fires correctly

3. **Manual testing**:
   ```bash
   # Create test bucket with actions
   mkdir -p data/test-bucket
   cat > data/test-bucket/.bucket-actions << 'EOF'
   {
     "after_upload": [{
       "name": "log-upload",
       "patterns": ["*"],
       "command": "echo \"Uploaded: $OBJECT_KEY\" >> /tmp/s3-actions.log"
     }]
   }
   EOF

   # Upload test file
   aws s3 cp test.txt s3://test-bucket/test.txt --endpoint-url https://localhost:8443 --no-verify-ssl

   # Check log
   cat /tmp/s3-actions.log
   ```

4. **Run show-bucket-actions.sh**:
   ```bash
   ./scripts/show-bucket-actions.sh data/
   ```

---

## Files Created/Modified

| File | Action | Lines |
|------|--------|-------|
| `actions.go` | Create | ~400 |
| `main.go` | Modify | +30 (imports, init, hook calls) |
| `bucket-actions.sample.json5` | Create | ~80 |
| `scripts/show-bucket-actions.sh` | Create | ~50 |
| `README.md` | Modify | +60 |
| `docs/plan-feature-action-events.md` | Create | Copy of this plan |
| `actions_test.go` | Create | ~200 |
