# Mini-S3 Gap Closure Plan

This document identifies bugs and gaps in the mini-s3 server and provides a plan for fixing them.

## Status: COMPLETED

All critical and high-priority issues have been fixed. The server is now fully functional with AWS CLI v2.

---

## Completed Fixes

### 1. Object Storage Data Loss Bug (CRITICAL) - FIXED

**Problem:** Object data was overwritten by metadata.

**Solution:** Separated object data and metadata paths:
- Object data: `<bucket>/<object>`
- Metadata: `<bucket>/.metadata/<object>.meta`

### 2. SigV4 Signature Calculation Issues - FIXED

**Problems fixed:**
- `host` header was not found because Go stores it in `r.Host`, not `r.Header`
- Added support for `STREAMING-UNSIGNED-PAYLOAD-TRAILER` (AWS CLI v2)
- Added `aws-chunked` Content-Encoding decoder

### 3. Empty Const Block - FIXED

Removed the empty const block.

### 4. ListObjectsV2 Nested Paths - FIXED

Changed from `os.ReadDir` to `filepath.WalkDir` for recursive metadata discovery.

### 5. deleteObjectHandler - FIXED

Now properly reads metadata, deletes object data, deletes metadata, and cleans up empty directories.

### 6. Inconsistent Error Responses - FIXED

All handlers now use `errorToXML()` with proper Content-Type headers.

### 7. Content-Length Handling - FIXED

Now uses `int64(len(body))` instead of `r.ContentLength`.

### 8. Multipart Storage Alignment - FIXED

Storage locations are now consistent between PUT and multipart upload handlers.

### 9. Bucket Name Validation - FIXED

Added `validateBucketName()` function enforcing S3 naming rules.

### 10. Object Key Validation - FIXED

Added `validateObjectKey()` function.

---

## Remaining Medium Priority Issues (Not Critical)

### 11. Race Conditions

**Problem:** No file locking. Concurrent operations on the same object could corrupt data.

**Status:** Not implemented - acceptable for single-user/development scenarios.

---

## Testing

### Unit Tests - COMPLETED

Created `main_test.go` with 17 passing tests:
- `TestHashSHA256`
- `TestHmacSHA256`
- `TestGetSigningKey`
- `TestErrorToXML`
- `TestParseInt`
- `TestGetEnvOrDefault`
- `TestValidateBucketName`
- `TestValidateObjectKey`
- `TestCreateBucketValidation`
- `TestObjectMetadataJSON`
- `TestListAllMyBucketsResultXML`
- `TestListBucketResultXML`
- `TestGetCanonicalURI`
- `TestGetCanonicalQueryString`
- `TestMultipartUploadJSON`
- `TestCleanupEmptyDirs`
- `TestCompleteMultipartUploadXML`

Run with: `go test -v ./...`

### Integration Test Script - COMPLETED

Created `scripts/test-s3-operations.sh` which tests:
- Create bucket
- List buckets
- Upload object
- List objects
- Download object
- Content integrity verification
- Head object
- Nested path objects
- Delete object
- Delete nested objects
- Delete bucket
- Optional: Multipart upload (set `RUN_MULTIPART_TEST=1`)

Run with: `./scripts/test-s3-operations.sh`

---

## Success Criteria - ALL MET

1. `aws s3 cp localfile s3://bucket/key` followed by `aws s3 cp s3://bucket/key localfile2` produces identical files
2. All standard aws-cli operations work without signature errors
3. Objects with nested paths (`folder/subfolder/file.txt`) work correctly
4. Multipart uploads produce valid, retrievable objects
5. All unit and integration tests pass
