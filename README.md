# Minimalist S3 Server in Go

This project implements a minimalist S3-compatible server in Go. It supports basic bucket and object operations, including multipart uploads, and uses AWS Signature Version 4 for authentication.

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
*   **Storage**: Uses the local filesystem. Object data is stored directly, and metadata is stored in a hidden `.metadata` subdirectory within each bucket.
*   **HTTPS**: Enforces HTTPS-only access.
*   **ACLs**: Stubbed (returns "Not Implemented").

## Prerequisites

*   Go (version 1.18 or later recommended)
*   OpenSSL (for generating SSL certificates)
*   `make` (for using the Makefile)

## Important Security Note
Important Security Note: The serverCredentials (Access Key ID and Secret Access Key) are still hardcoded. In a production system, these should be managed securely (e.g., via environment variables, a configuration file with restricted permissions, or a secrets management system).

## Setup and Configuration

1.  **Clone the Repository**:
    ```bash
    git clone <repository-url>
    cd mini-s3-server
    ```

2.  **Configure Credentials**:
    Open `main.go` and locate the `serverCredentials` struct:
    ```go
    // Credentials store (simple hardcoded version)
    var serverCredentials = struct {
    	AccessKeyID     string
    	SecretAccessKey string
    }{
    	AccessKeyID:     "YOUR_ACCESS_KEY_ID",     // Replace with your desired Access Key ID
    	SecretAccessKey: "YOUR_SECRET_ACCESS_KEY", // Replace with your desired Secret Access Key
    }
    ```
    Replace `"YOUR_ACCESS_KEY_ID"` and `"YOUR_SECRET_ACCESS_KEY"` with the Access Key ID and Secret AccessKey you wish to use for clients connecting to this server. **These are not your AWS account credentials.** They are specific to this mini S3 server instance.

3.  **Generate SSL Certificates**:
    The server requires SSL certificates (`cert.pem` and `key.pem`) to run over HTTPS. The Makefile can generate self-signed certificates for local development:
    ```bash
    make certs
    ```
    This will create a `certs` directory (if it doesn't exist) and place `cert.pem` and `key.pem` inside it. If you have your own certificates, you can place them in the `certs` directory with these names.

4.  **Create Data Directory**:
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
*   **Access Key ID**: Use the `AccessKeyID` you configured in `main.go`.
*   **Secret Access Key**: Use the `SecretAccessKey` you configured in `main.go`.
*   **Region**: Set this to `us-east-1` (or the `defaultRegion` configured in `main.go`).
*   **SSL Verification**: Since the server uses self-signed certificates by default, you might need to configure your client to trust these certificates or disable SSL verification for local testing (e.g., `--no-verify-ssl` with `aws-cli`).

**Example with `aws-cli`**:

First, configure a profile for your local S3 server (e.g., named `minis3local`):
```bash
aws configure --profile minis3local
AWS Access Key ID [None]: YOUR_ACCESS_KEY_ID
AWS Secret Access Key [None]: YOUR_SECRET_ACCESS_KEY
Default region name [None]: us-east-1
Default output format [None]: json
```

Then, you can run commands:

```bash
# List buckets
aws s3 ls --profile minis3local --endpoint-url https://localhost:8443 --no-verify-ssl

# Create a bucket
aws s3 mb s3://mytestbucket --profile minis3local --endpoint-url https://localhost:8443 --no-verify-ssl

# Upload a file
aws s3 cp test.txt s3://mytestbucket/test.txt --profile minis3local --endpoint-url https://localhost:8443 --no-verify-ssl

# List objects in a bucket
aws s3 ls s3://mytestbucket --profile minis3local --endpoint-url https://localhost:8443 --no-verify-ssl

# Download a file
aws s3 cp s3://mytestbucket/test.txt downloaded_test.txt --profile minis3local --endpoint-url https://localhost:8443 --no-verify-ssl
```

## Project Structure

```
.
├── main.go         # Main application code
├── Makefile        # Makefile for building, running, etc.
├── README.md       # This file
├── certs/          # Directory for SSL certificates
│   ├── cert.pem    # SSL certificate
│   └── key.pem     # SSL private key
└── data/           # Root directory for storing buckets and objects (created on run)
```

## TODO / Potential Enhancements

*   More robust error handling and S3 error code compliance.
*   Configuration of credentials and server settings via a config file or environment variables instead of hardcoding.
*   Full support for streaming payloads in SigV4.
*   Implementation of ACLs and Bucket Policies.
*   More comprehensive support for S3 features (versioning, lifecycle policies, etc.).
*   Listing multipart uploads.
*   More detailed logging options.
