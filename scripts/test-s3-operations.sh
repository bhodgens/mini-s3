#!/bin/bash
# Manual test script for mini-s3 server
# This script tests basic S3 operations using AWS CLI

set -e

# Configuration
ENDPOINT="https://localhost:8443"
AWS_ACCESS_KEY_ID="${MINIS3_ACCESS_KEY:-minioadmin}"
AWS_SECRET_ACCESS_KEY="${MINIS3_SECRET_KEY:-minioadmin}"
REGION="us-east-1"
BUCKET="test-bucket-$(date +%s)"
TEST_FILE="/tmp/test-upload-$$.txt"
DOWNLOAD_FILE="/tmp/test-download-$$.txt"

# Export credentials
export AWS_ACCESS_KEY_ID
export AWS_SECRET_ACCESS_KEY

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Helper functions
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_test() {
    echo -e "\n${YELLOW}=== TEST: $1 ===${NC}"
}

cleanup() {
    log_info "Cleaning up..."
    rm -f "$TEST_FILE" "$DOWNLOAD_FILE" 2>/dev/null || true
    # Try to delete the test bucket (may fail if already deleted)
    aws s3 rb "s3://$BUCKET" --force --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>/dev/null || true
}

trap cleanup EXIT

# Check prerequisites
check_prerequisites() {
    log_test "Prerequisites Check"

    if ! command -v aws &> /dev/null; then
        log_error "AWS CLI not found. Please install it first."
        exit 1
    fi

    # Check if server is running
    if ! curl -k -s "$ENDPOINT" > /dev/null 2>&1; then
        log_error "Mini-S3 server is not running at $ENDPOINT"
        log_info "Start the server with: make run"
        exit 1
    fi

    log_info "Prerequisites OK"
}

# Test 1: Create bucket
test_create_bucket() {
    log_test "Create Bucket"
    aws s3 mb "s3://$BUCKET" --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1
    log_info "Bucket created: $BUCKET"
}

# Test 2: List buckets
test_list_buckets() {
    log_test "List Buckets"
    aws s3 ls --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1
    log_info "Buckets listed successfully"
}

# Test 3: Upload object
test_upload_object() {
    log_test "Upload Object"

    # Create test file
    echo "Hello, Mini-S3! This is a test file." > "$TEST_FILE"

    aws s3 cp "$TEST_FILE" "s3://$BUCKET/test.txt" --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1
    log_info "Object uploaded: test.txt"
}

# Test 4: List objects
test_list_objects() {
    log_test "List Objects"
    aws s3 ls "s3://$BUCKET" --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1
    log_info "Objects listed successfully"
}

# Test 5: Download object
test_download_object() {
    log_test "Download Object"
    aws s3 cp "s3://$BUCKET/test.txt" "$DOWNLOAD_FILE" --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1
    log_info "Object downloaded"
}

# Test 6: Verify content integrity
test_verify_content() {
    log_test "Verify Content Integrity"

    if diff "$TEST_FILE" "$DOWNLOAD_FILE" > /dev/null 2>&1; then
        log_info "Content verification PASSED - files match!"
    else
        log_error "Content verification FAILED - files don't match!"
        echo "Original file:"
        cat "$TEST_FILE"
        echo "Downloaded file:"
        cat "$DOWNLOAD_FILE"
        exit 1
    fi
}

# Test 7: Head object
test_head_object() {
    log_test "Head Object (Get Metadata)"
    aws s3api head-object --bucket "$BUCKET" --key "test.txt" --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1
    log_info "Object metadata retrieved"
}

# Test 8: Upload object with nested path
test_nested_object() {
    log_test "Upload Object with Nested Path"

    aws s3 cp "$TEST_FILE" "s3://$BUCKET/folder/subfolder/nested.txt" --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1
    log_info "Nested object uploaded"

    # List with prefix
    log_info "Listing objects with prefix 'folder/':"
    aws s3 ls "s3://$BUCKET/folder/" --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1
}

# Test 9: Delete object
test_delete_object() {
    log_test "Delete Object"
    aws s3 rm "s3://$BUCKET/test.txt" --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1
    log_info "Object deleted"
}

# Test 10: Delete nested objects
test_delete_nested() {
    log_test "Delete Nested Objects"
    aws s3 rm "s3://$BUCKET" --recursive --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1
    log_info "All objects deleted"
}

# Test 11: Delete bucket
test_delete_bucket() {
    log_test "Delete Bucket"
    aws s3 rb "s3://$BUCKET" --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1
    log_info "Bucket deleted"
}

# Test 12: Multipart upload (for larger files)
test_multipart_upload() {
    log_test "Multipart Upload"

    # Create a larger test file (10MB)
    dd if=/dev/urandom of="$TEST_FILE.large" bs=1M count=10 2>/dev/null

    # Create bucket again for this test
    aws s3 mb "s3://$BUCKET" --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1 || true

    # Upload (AWS CLI will automatically use multipart for larger files)
    aws s3 cp "$TEST_FILE.large" "s3://$BUCKET/large-file.bin" --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1
    log_info "Large file uploaded (multipart)"

    # Download and verify
    aws s3 cp "s3://$BUCKET/large-file.bin" "$DOWNLOAD_FILE.large" --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1

    if diff "$TEST_FILE.large" "$DOWNLOAD_FILE.large" > /dev/null 2>&1; then
        log_info "Large file content verification PASSED"
    else
        log_error "Large file content verification FAILED"
    fi

    # Cleanup
    rm -f "$TEST_FILE.large" "$DOWNLOAD_FILE.large"
    aws s3 rm "s3://$BUCKET/large-file.bin" --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1
    aws s3 rb "s3://$BUCKET" --endpoint-url "$ENDPOINT" --no-verify-ssl --region "$REGION" 2>&1 || true
}

# Main execution
main() {
    echo "============================================"
    echo "  Mini-S3 Server Integration Test Suite"
    echo "============================================"
    echo ""
    log_info "Endpoint: $ENDPOINT"
    log_info "Test Bucket: $BUCKET"
    echo ""

    check_prerequisites

    test_create_bucket
    test_list_buckets
    test_upload_object
    test_list_objects
    test_download_object
    test_verify_content
    test_head_object
    test_nested_object
    test_delete_object
    test_delete_nested
    test_delete_bucket

    # Optional: Run multipart test
    if [ "${RUN_MULTIPART_TEST:-0}" = "1" ]; then
        test_multipart_upload
    else
        log_info "Skipping multipart upload test (set RUN_MULTIPART_TEST=1 to enable)"
    fi

    echo ""
    echo "============================================"
    echo -e "  ${GREEN}All tests passed!${NC}"
    echo "============================================"
}

# Run main
main "$@"
