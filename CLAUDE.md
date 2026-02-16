# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and Run Commands

```bash
make build        # Compile the Go server to ./mini-s3-server
make run          # Build and start the server (HTTPS on :8443)
make certs        # Generate self-signed SSL certificates in certs/
make clean        # Remove the compiled binary
```

## Architecture

This is a single-file Go S3-compatible server (`main.go`) implementing core S3 operations with AWS Signature Version 4 authentication.

### Storage Layout

```
<dataDir>/                  # Configurable via config.json (default: ./data/)
  <bucket>/                 # Auto-discovered bucket (or symlink to dir)
    <object-file>           # Actual object data for simple PUT
    .metadata/
      <object>.meta         # JSON metadata file per object
      .uploads/
        <uploadId>.json     # Multipart upload session metadata
        <uploadId>_parts/   # Temporary part files during multipart

<custom-bucket-path>/       # Custom bucket from config.json "buckets" map
  <object-file>
  .metadata/
    ...
```

### Request Flow

`rootHandler` is the single entry point that:
1. Parses path into bucket/object names
2. Calls `authenticateRequest` for SigV4 validation
3. Routes to operation handlers based on method and query params

### Key Components

- **SigV4 Authentication**: Full implementation including canonical request construction, signing key derivation, signature comparison. Uses `authHeaderRegex` to parse Authorization header.
- **Object Metadata**: `ObjectMetadata` struct stored as JSON, tracks content type, ETag (MD5), custom x-amz-meta-* headers, and storage path.
- **Multipart Uploads**: Three-phase flow (initiate/upload parts/complete) with `MultipartUpload` tracking part ETags and temporary storage.

### Configuration

The server loads configuration from `config.json` (or path specified by `MINIS3_CONFIG` env var).

```json
{
  "dataDir": "./data/",
  "buckets": {
    "photos": "/mnt/storage/photos",
    "backups": "/var/backups/s3-mirror"
  }
}
```

- **dataDir**: Root directory for auto-discovered buckets (default: `./data/`)
- **buckets**: Map of bucket names to custom filesystem paths. These buckets:
  - Appear in ListBuckets alongside auto-discovered buckets
  - Cannot be created or deleted via the S3 API
  - Can point to any directory (including symlinks)

**Symlink support**: Both the `dataDir` scan and custom bucket paths follow symlinks correctly.

### Credentials

Set via environment variables or defaults to `minioadmin`/`minioadmin`:
- `MINIS3_ACCESS_KEY` - Access Key ID
- `MINIS3_SECRET_KEY` - Secret Access Key
- `MINIS3_CONFIG` - Path to config file (default: `config.json`)

### Testing with AWS CLI

```bash
aws s3 ls --profile minis3 --endpoint-url https://localhost:8443 --no-verify-ssl
```

Configure profile with credentials `minioadmin`/`minioadmin` and region `us-east-1`.

## Known Issues

See `docs/gap-closure.md` for the full list. Critical issues:
- **Object storage bug**: `putObjectHandler` writes data then overwrites with metadata (lines 532, 574)
- **SigV4 verification**: May fail with standard AWS CLI due to canonical header/URI handling
