# Minimalist S3 Server in Go

This project implements a minimalist S3-compatible server in Go. It supports basic bucket and object operations, including multipart uploads, and uses AWS Signature Version 4 for authentication.

## Why mini-s3?

**Serve any directory as an S3 bucket.** Unlike other S3-compatible servers that manage their own opaque storage, mini-s3 maps buckets directly to filesystem directories. This means you can:

- Point a bucket at an existing directory and immediately access its contents via S3 API
- Use symlinks to expose directories from anywhere on your system
- Mount network shares, NFS volumes, or any filesystem and serve them as S3 buckets
- Mix auto-discovered buckets with explicitly configured custom paths

This makes mini-s3 ideal for:
- Exposing existing file archives via S3-compatible APIs
- Creating S3 gateways to legacy storage systems
- Development and testing against real directory structures
- Serving static content from arbitrary locations

## Features

*   **Bucket Operations**:
    *   Create Bucket (`PUT /<bucket>`)
    *   List Buckets (`GET /`)
    *   Delete Bucket (`DELETE /<bucket>`)
    *   Get Bucket Location (`GET /<bucket>?location`)
    *   HEAD Bucket (`HEAD /<bucket>`)
*   **Object Operations**:
    *   Upload Object (`PUT /<bucket>/<object>`)
    *   Get Object (`GET /<bucket>/<object>`)
    *   Delete Object (`DELETE /<bucket>/<object>`)
    *   List Objects (`GET /<bucket>?list-type=2`)
        *   Supports `prefix`, `delimiter`, `continuation-token`, `start-after`, `max-keys`.
    *   HEAD Object (`HEAD /<bucket>/<object>`)
*   **Multipart Uploads**:
    *   Initiate (`POST /<bucket>/<object>?uploads`)
    *   Upload Part (`PUT /<bucket>/<object>?partNumber=N&uploadId=XYZ`)
    *   Complete (`POST /<bucket>/<object>?uploadId=XYZ`)
    *   Abort (`DELETE /<bucket>/<object>?uploadId=XYZ`)
*   **Authentication**: AWS Signature Version 4 (HMAC-SHA256).
*   **Flexible Storage**: Maps buckets directly to filesystem directories. Auto-discovers buckets from a configurable root directory, plus supports explicit bucket-to-path mappings for serving arbitrary directories. Follows symlinks.
*   **HTTPS**: Enforces HTTPS-only access.
*   **ACLs**: Stubbed (returns "Not Implemented").

## Prerequisites

*   Go (version 1.18 or later recommended)
*   OpenSSL (for generating SSL certificates)
*   `make` (for using the Makefile)

## Credentials Configuration

The server uses **AWS Signature Version 4** for authentication. You must configure matching credentials on both the server and client.

### Server-Side Credentials

Credentials can be set via environment variables (recommended) or will fall back to defaults:

| Environment Variable | Default Value | Description |
|---------------------|---------------|-------------|
| `MINIS3_ACCESS_KEY` | `minioadmin`  | Access Key ID |
| `MINIS3_SECRET_KEY` | `minioadmin`  | Secret Access Key |

**Option 1: Use default credentials (quickstart)**

The server ships with default credentials `minioadmin`/`minioadmin`. Just start the server and configure your client to match.

**Option 2: Set custom credentials via environment**

```bash
export MINIS3_ACCESS_KEY="myaccesskey"
export MINIS3_SECRET_KEY="mysecretkey"
./mini-s3-server
```

Or inline:
```bash
MINIS3_ACCESS_KEY="myaccesskey" MINIS3_SECRET_KEY="mysecretkey" ./mini-s3-server
```

### Client-Side Configuration (AWS CLI)

**Step 1: Create an AWS CLI profile**

```bash
aws configure --profile minis3
```

Enter the following when prompted:
```
AWS Access Key ID [None]: minioadmin
AWS Secret Access Key [None]: minioadmin
Default region name [None]: us-east-1
Default output format [None]: json
```

**Step 2: Test the connection**

```bash
aws s3 ls --profile minis3 --endpoint-url https://localhost:8443 --no-verify-ssl
```

### Quick Test with Inline Credentials

For quick testing without creating a profile:

```bash
AWS_ACCESS_KEY_ID=minioadmin AWS_SECRET_ACCESS_KEY=minioadmin \
  aws s3 ls --endpoint-url https://localhost:8443 --no-verify-ssl --region us-east-1
```

## Configuration File

mini-s3 uses a JSON configuration file to define storage locations. By default, it looks for `config.json` in the current directory.

### Configuration Options

Create a `config.json` file (see `config.json.example`):

```json
{
  "dataDir": "./data/",
  "buckets": {
    "photos": "/mnt/storage/photos",
    "backups": "/var/backups/s3-mirror",
    "home": "/home/username"
  }
}
```

| Option | Description | Default |
|--------|-------------|---------|
| `dataDir` | Root directory for auto-discovered buckets. Any subdirectory (including symlinks) becomes a bucket. | `./data/` |
| `buckets` | Map of bucket names to custom filesystem paths. These buckets can point anywhere on the system. | `{}` |

### How Bucket Discovery Works

1. **Custom buckets** (from `config.json` `buckets` map) are loaded first
2. **Auto-discovered buckets** are found by scanning `dataDir` for subdirectories
3. Symlinks are followed - a symlink to a directory is treated as a valid bucket
4. Custom buckets take precedence if there's a name collision

### Custom Bucket Behavior

Buckets defined in `config.json`:
- Appear in `ListBuckets` alongside auto-discovered buckets
- **Cannot be deleted** via the S3 API (protected)
- **Cannot be created** via the S3 API (already exist)
- Can point to any readable directory on the filesystem

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `MINIS3_CONFIG` | Path to configuration file | `config.json` |
| `MINIS3_ACCESS_KEY` | Access Key ID for authentication | `minioadmin` |
| `MINIS3_SECRET_KEY` | Secret Access Key for authentication | `minioadmin` |

### Example: Serving Existing Directories

To expose `/var/log` as an S3 bucket named `logs` and `/home/user/documents` as `docs`:

```json
{
  "dataDir": "./data/",
  "buckets": {
    "logs": "/var/log",
    "docs": "/home/user/documents"
  }
}
```

Then access via S3:
```bash
aws s3 ls s3://logs/ --profile minis3 --endpoint-url https://localhost:8443 --no-verify-ssl
aws s3 cp s3://docs/report.pdf ./report.pdf --profile minis3 --endpoint-url https://localhost:8443 --no-verify-ssl
```

## Setup and Installation

1.  **Clone the Repository**:
    ```bash
    git clone <repository-url>
    cd mini-s3-server
    ```

2.  **Generate SSL Certificates**:
    The server requires SSL certificates (`cert.pem` and `key.pem`) to run over HTTPS. The Makefile can generate self-signed certificates for local development:
    ```bash
    make certs
    ```
    This will create a `certs` directory (if it doesn't exist) and place `cert.pem` and `key.pem` inside it. If you have your own certificates, you can place them in the `certs` directory with these names.

3.  **Create Data Directory**:
    The server stores buckets and objects in a `data` directory. The Makefile can create this:
    ```bash
    make data_dir
    ```
    Alternatively, the server will attempt to create this directory on startup if it doesn't exist.

## Building and Running

*   **Build the Server**:
    ```bash
    make build
    ```
    This compiles the Go program and creates an executable named `mini-s3-server` in the project root.

*   **Run the Server**:
    ```bash
    make run
    ```
    This will first build the server (if needed) and then start it. By default, the server listens on `https://localhost:8443`.

*   **Clean Build Artifacts**:
    ```bash
    make clean
    ```
    This removes the `mini-s3-server` executable.

## Using with an S3 Client

To interact with the server, you can use an S3 client library (like AWS SDKs) or a command-line tool like `aws-cli` or `s3cmd`.

When configuring your client:

*   **Endpoint URL**: Set this to `https://localhost:8443`.
*   **Access Key ID**: Use `minioadmin` (default) or your custom `MINIS3_ACCESS_KEY`.
*   **Secret Access Key**: Use `minioadmin` (default) or your custom `MINIS3_SECRET_KEY`.
*   **Region**: Set this to `us-east-1`.
*   **SSL Verification**: Since the server uses self-signed certificates by default, you might need to configure your client to trust these certificates or disable SSL verification for local testing (e.g., `--no-verify-ssl` with `aws-cli`).

**Example with `aws-cli`**:

First, configure a profile for your local S3 server (e.g., named `minis3`):
```bash
aws configure --profile minis3
AWS Access Key ID [None]: minioadmin
AWS Secret Access Key [None]: minioadmin
Default region name [None]: us-east-1
Default output format [None]: json
```

Then, you can run commands:

```bash
# List buckets
aws s3 ls --profile minis3 --endpoint-url https://localhost:8443 --no-verify-ssl

# Create a bucket
aws s3 mb s3://mytestbucket --profile minis3 --endpoint-url https://localhost:8443 --no-verify-ssl

# Upload a file
aws s3 cp test.txt s3://mytestbucket/test.txt --profile minis3 --endpoint-url https://localhost:8443 --no-verify-ssl

# List objects in a bucket
aws s3 ls s3://mytestbucket --profile minis3 --endpoint-url https://localhost:8443 --no-verify-ssl

# Download a file
aws s3 cp s3://mytestbucket/test.txt downloaded_test.txt --profile minis3 --endpoint-url https://localhost:8443 --no-verify-ssl
```

## Project Structure

```
.
├── main.go              # Main application code
├── Makefile             # Makefile for building, running, etc.
├── README.md            # This file
├── config.json          # Server configuration (optional, see config.json.example)
├── config.json.example  # Example configuration file
├── certs/               # Directory for SSL certificates
│   ├── cert.pem         # SSL certificate
│   └── key.pem          # SSL private key
└── data/                # Default root for auto-discovered buckets
    └── <bucket>/        # Each subdirectory is a bucket
        ├── <objects>    # Object data files
        └── .metadata/   # Object metadata (JSON files)
```

## Testing

Run unit tests:
```bash
go test -v ./...
```

Run integration tests (requires server to be running):
```bash
./scripts/test-s3-operations.sh
```

## Development History

See [docs/gap-closure.md](docs/gap-closure.md) for the history of issues that were identified and fixed during development.

## Event Actions

mini-s3 supports configurable event-based actions that execute shell commands in response to S3 operations. Actions are configured via `.bucket-actions` files (JSON5 format) placed in bucket directories.

### Use Cases

- **Post-upload processing**: thumbnail generation, video transcoding, backup notifications
- **Delete-after-download**: ephemeral file cleanup
- **Inactivity snapshots**: ZFS snapshots after periods of no activity
- **Audit logging**: track all downloads/uploads

### Configuration

Place a `.bucket-actions` file in any bucket directory or subdirectory. Subdirectories inherit parent actions and can override them. See `bucket-actions.sample.json5` for a complete example.

```json5
{
  "version": "1.0",
  "after_upload": [
    {
      "name": "thumbnail-generator",
      "patterns": ["*.jpg", "*.png"],
      "command": "/scripts/thumb.sh \"$FILE_PATH\"",
      "async": true,
      "timeout": 60
    }
  ],
  "after_download": [...],
  "after_delete": [...],
  "inactivity_timeout": {
    "duration": "30m",
    "command": "zfs snapshot tank/data@$(date +%s)",
    "reset_on": ["upload", "delete"]
  }
}
```

### Available Variables

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

### Inheritance

When an object operation occurs, actions are loaded from the bucket root through all parent directories to the object's location:

| Mode | Behavior |
|------|----------|
| `merge` (default) | Child actions added; same-name actions override parent |
| `override` | Child completely replaces parent |
| `disable` | Only current directory's actions apply |

### Security Notes

1. **Variable quoting**: Always quote variables in commands (`"$FILE_PATH"`)
2. **Permissions**: Commands run as the server process user
3. **Timeouts**: Enforce timeouts to prevent runaway processes
4. **Async**: Actions run asynchronously by default (non-blocking)

### Discovery Script

Use `scripts/show-bucket-actions.sh` to view all configured actions:

```bash
./scripts/show-bucket-actions.sh data/
```

## TODO / Potential Enhancements

*   More robust error handling and S3 error code compliance.
*   Full support for streaming payloads in SigV4.
*   Implementation of ACLs and Bucket Policies.
*   More comprehensive support for S3 features (versioning, lifecycle policies, etc.).
*   Listing multipart uploads.
*   More detailed logging options.
