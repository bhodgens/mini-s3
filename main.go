package main

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	dataDir          = "./data/"
	awsAlgorithm     = "AWS4-HMAC-SHA256"
	defaultRegion    = "us-east-1" // Default region for our S3 server
	serviceName      = "s3"
	unsignedPayload  = "UNSIGNED-PAYLOAD"
	streamingPayload = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	iso8601Format    = "20060102T150405Z"
	shortDateFormat  = "20060102"
)

// Credentials store (simple hardcoded version)
// TODO: Load from environment variables or config file
var serverCredentials = struct {
	AccessKeyID     string
	SecretAccessKey string
}{
	AccessKeyID:     getEnvOrDefault("MINIS3_ACCESS_KEY", "minioadmin"),
	SecretAccessKey: getEnvOrDefault("MINIS3_SECRET_KEY", "minioadmin"),
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// decodeAWSChunked decodes aws-chunked Content-Encoding used by AWS CLI v2.
// Format: <hex-size>;chunk-signature=...\r\n<data>\r\n, ending with 0\r\n<trailers>\r\n\r\n
func decodeAWSChunked(body []byte) ([]byte, error) {
	var result bytes.Buffer
	reader := bufio.NewReader(bytes.NewReader(body))

	for {
		// Read the chunk header line
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("error reading chunk header: %w", err)
		}

		// Parse chunk size (format: "<hex>;chunk-signature=..." or just "<hex>")
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, ";", 2)
		sizeStr := strings.TrimSpace(parts[0])

		if sizeStr == "" {
			continue // Skip empty lines
		}

		chunkSize, err := strconv.ParseInt(sizeStr, 16, 64)
		if err != nil {
			// Might be a trailer line, skip it
			continue
		}

		if chunkSize == 0 {
			// Final chunk - read remaining trailers
			break
		}

		// Read the chunk data
		chunkData := make([]byte, chunkSize)
		n, err := io.ReadFull(reader, chunkData)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("error reading chunk data: %w", err)
		}
		result.Write(chunkData[:n])

		// Read the trailing \r\n after chunk data
		reader.ReadString('\n')
	}

	return result.Bytes(), nil
}

// Regex for parsing the AWS V4 Authorization header
var authHeaderRegex = regexp.MustCompile(
	`^AWS4-HMAC-SHA256 Credential=([^/]+)/([^/]+)/([^/]+)/s3/aws4_request, SignedHeaders=([^,]+), Signature=(.+)$`,
)


// S3Error defines the structure for S3 compatible XML error responses
type S3Error struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	RequestID string   `xml:"RequestId,omitempty"` // Optional
	HostID    string   `xml:"HostId,omitempty"`    // Optional
}

// errorToXML converts an error code and message to S3 XML error format
func errorToXML(code, message string) string {
	s3Err := S3Error{
		Code:    code,
		Message: message,
	}
	x, err := xml.MarshalIndent(s3Err, "", "  ")
	if err != nil {
		log.Printf("Error marshalling S3Error to XML: %v", err)
		return "<Error><Code>InternalError</Code><Message>Failed to generate error XML.</Message></Error>"
	}
	return string(x)
}

func main() {
	// Ensure data directory exists
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			log.Fatalf("Failed to create data directory: %v", err)
		}
	}

	http.HandleFunc("/", rootHandler)
	log.Println("Starting S3 server on :8443 (HTTPS)")
	// Assumes certs/cert.pem and certs/key.pem exist
	err := http.ListenAndServeTLS(":8443", "certs/cert.pem", "certs/key.pem", nil)
	if err != nil {
		log.Fatalf("ListenAndServeTLS failed: %v. Please ensure certs/cert.pem and certs/key.pem are correctly generated and in place.", err)
	}
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	// Basic path parsing to differentiate between service-level and bucket-level requests
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	bucketName := ""
	objectName := ""

	if len(pathParts) >= 1 && pathParts[0] != "" {
		bucketName = pathParts[0]
	}
	if len(pathParts) >= 2 {
		objectName = strings.Join(pathParts[1:], "/")
	}

	log.Printf("Request: %s %s, Bucket: '%s', Object: '%s'", r.Method, r.URL.Path, bucketName, objectName)

	// Authenticate request (placeholder - to be implemented with AWS SigV4)
	if !authenticateRequest(w, r) {
		// authenticateRequest will write the error response if authentication fails
		return
	}

	// ACL specific handling (stubbed)
	if _, aclPresent := r.URL.Query()["acl"]; aclPresent {
		handleACL(w, r, bucketName, objectName)
		return
	}

	if bucketName == "" { // Service-level operations
		switch r.Method {
		case "GET":
			listBucketsHandler(w, r)
		default:
			http.Error(w, "Method Not Allowed at service level", http.StatusMethodNotAllowed)
		}
	} else if objectName == "" { // Bucket-level operations
		// Check if location parameter is present for GetBucketLocation
		if _, ok := r.URL.Query()["location"]; ok && r.Method == "GET" {
			getBucketLocationHandler(w, r, bucketName)
			return
		}
		// Check if list-type=2 parameter is present for ListObjectsV2
		if val, ok := r.URL.Query()["list-type"]; ok && val[0] == "2" && r.Method == "GET" {
			listObjectsV2Handler(w, r, bucketName)
			return
		}

		switch r.Method {
		case "PUT":
			createBucketHandler(w, r, bucketName)
		case "GET": // This would be ListObjectsV1 or GetBucketACL etc.
			// For now, assume ListObjectsV2 is the primary way to list.
			// If no specific query params for listing, could be GetBucketACL or other bucket specific GETs.
			// We'll default to a simple "Not Implemented" or treat as ListObjectsV2 if query params match.
			listObjectsV2Handler(w, r, bucketName) // Or a more specific handler based on query params
		case "DELETE":
			deleteBucketHandler(w, r, bucketName)
		case "HEAD":
			headBucketHandler(w, r, bucketName)
		default:
			http.Error(w, "Method Not Allowed for bucket", http.StatusMethodNotAllowed)
		}
	} else { // Object-level operations
		// Check for multipart upload query parameters
		if _, ok := r.URL.Query()["uploads"]; ok && r.Method == "POST" {
			initiateMultipartUploadHandler(w, r, bucketName, objectName)
			return
		}
		if partNumber, ok := r.URL.Query()["partNumber"]; ok && r.Method == "PUT" {
			if uploadID, ok := r.URL.Query()["uploadId"]; ok {
				uploadPartHandler(w, r, bucketName, objectName, partNumber[0], uploadID[0])
				return
			}
		}
		if uploadID, ok := r.URL.Query()["uploadId"]; ok && r.Method == "POST" {
			completeMultipartUploadHandler(w, r, bucketName, objectName, uploadID[0])
			return
		}
		if uploadID, ok := r.URL.Query()["uploadId"]; ok && r.Method == "DELETE" {
			abortMultipartUploadHandler(w, r, bucketName, objectName, uploadID[0])
			return
		}

		switch r.Method {
		case "PUT":
			putObjectHandler(w, r, bucketName, objectName)
		case "GET":
			getObjectHandler(w, r, bucketName, objectName)
		case "DELETE":
			deleteObjectHandler(w, r, bucketName, objectName)
		case "HEAD":
			headObjectHandler(w, r, bucketName, objectName)
		default:
			http.Error(w, "Method Not Allowed for object", http.StatusMethodNotAllowed)
		}
	}
}

// XML Structures for S3 Responses

// ListAllMyBucketsResult is the top-level structure for listing buckets
type ListAllMyBucketsResult struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	Owner   Owner    `xml:"Owner"`
	Buckets Buckets  `xml:"Buckets"`
}

// Owner defines the owner of the buckets
type Owner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

// Buckets is a slice of Bucket
type Buckets struct {
	Bucket []Bucket `xml:"Bucket"`
}

// Bucket defines a single bucket entry
type Bucket struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"` // S3 format: 2006-02-03T16:45:09.000Z
}

// ObjectMetadata holds metadata for an object
// This will be stored as a JSON file in the .metadata directory
// for each object.
type ObjectMetadata struct {
	ContentType    string            `json:"contentType"`
	ContentLength  int64             `json:"contentLength"`
	ETag           string            `json:"eTag"`
	CustomMetadata map[string]string `json:"customMetadata"` // For x-amz-meta-* headers
	LastModified   time.Time         `json:"lastModified"`
	StoragePath    string            `json:"storagePath"` // Actual path to the object data on disk
}

// ListBucketResult is the S3 response structure for listing objects (ListObjectsV2)
// See: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectsV2.html
type ListBucketResult struct {
	XMLName               xml.Name       `xml:"ListBucketResult"`
	IsTruncated           bool           `xml:"IsTruncated"`
	Contents              []Object       `xml:"Contents,omitempty"`
	Name                  string         `xml:"Name"`
	Prefix                string         `xml:"Prefix"`
	Delimiter             string         `xml:"Delimiter,omitempty"`
	MaxKeys               int            `xml:"MaxKeys"`
	CommonPrefixes        []CommonPrefix `xml:"CommonPrefixes,omitempty"`
	EncodingType          string         `xml:"EncodingType,omitempty"` // Not implementing URL encoding for now
	KeyCount              int            `xml:"KeyCount"`
	ContinuationToken     string         `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string         `xml:"NextContinuationToken,omitempty"`
	StartAfter            string         `xml:"StartAfter,omitempty"`
}

// Object represents a single object in the ListBucketResult
type Object struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"` // Format: 2006-01-02T15:04:05.000Z
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`    // Placeholder, e.g., "STANDARD"
	Owner        *Owner `xml:"Owner,omitempty"` // Optional, can be omitted for simplicity
}

// CommonPrefix represents a prefix rolled up by a delimiter
type CommonPrefix struct {
	Prefix string `xml:"Prefix"`
}

// LocationConstraint is for GetBucketLocation
type LocationConstraint struct {
	XMLName  xml.Name `xml:"LocationConstraint"`
	Location string   `xml:",chardata"`
}

// MultipartUpload represents an active multipart upload session
type MultipartUpload struct {
	UploadID  string    `json:"uploadId"`
	Key       string    `json:"key"`
	Initiated time.Time `json:"initiated"`
	// Parts will store metadata about each uploaded part
	Parts map[int]PartMetadata `json:"parts"` // Keyed by PartNumber
}

// PartMetadata stores information about a single uploaded part
type PartMetadata struct {
	PartNumber int    `json:"partNumber"`
	ETag       string `json:"eTag"`
	Size       int64  `json:"size"`
	StoredPath string `json:"storedPath"` // Path to the temporary file for this part
}

// InitiateMultipartUploadResult is the XML response for initiating a multipart upload
type InitiateMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

// CompleteMultipartUpload is the structure for the request body of CompleteMultipartUpload
type CompleteMultipartUpload struct {
	XMLName xml.Name       `xml:"CompleteMultipartUpload"`
	Parts   []PartToUpload `xml:"Part"`
}

// PartToUpload represents a part in the CompleteMultipartUpload request
type PartToUpload struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

// CompletedMultipartUploadResult is the XML response for completing a multipart upload
type CompletedMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	Location string   `xml:"Location"` // URL of the created object
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"` // ETag of the assembled object (often MD5 of part ETags + count)
}

// Placeholder handlers - to be implemented in handlers.go or similar
func listBucketsHandler(w http.ResponseWriter, r *http.Request) {
	dirs, err := os.ReadDir(dataDir)
	if err != nil {
		log.Printf("Error reading data directory %s: %v", dataDir, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var s3Buckets []Bucket
	for _, dir := range dirs {
		if dir.IsDir() && !strings.HasPrefix(dir.Name(), ".") { // Exclude hidden dirs like .metadata if any at root
			// For CreationDate, we need to get it from the bucket's .metadata or the dir itself.
			// For simplicity, using directory modification time for now.
			// A more accurate way would be to store creation date in a metadata file inside the bucket.
			info, err := dir.Info()
			var creationDate string
			if err == nil {
				creationDate = info.ModTime().UTC().Format("2006-01-02T15:04:05.000Z")
			} else {
				creationDate = time.Now().UTC().Format("2006-01-02T15:04:05.000Z") // Fallback
				log.Printf("Warning: Could not get info for bucket %s: %v", dir.Name(), err)
			}
			s3Buckets = append(s3Buckets, Bucket{Name: dir.Name(), CreationDate: creationDate})
		}
	}

	result := ListAllMyBucketsResult{
		Owner:   Owner{ID: "minis3-user-id", DisplayName: "minis3-user"}, // Placeholder owner
		Buckets: Buckets{Bucket: s3Buckets},
	}

	x, err := xml.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Printf("Error marshalling ListAllMyBucketsResult to XML: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	w.Write(x)
}

func createBucketHandler(w http.ResponseWriter, r *http.Request, bucketName string) {
	// Validate bucket name
	if err := validateBucketName(bucketName); err != nil {
		log.Printf("Invalid bucket name %s: %v", bucketName, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(errorToXML("InvalidBucketName", err.Error())))
		return
	}

	bucketPath := filepath.Join(dataDir, bucketName)
	metadataPath := filepath.Join(bucketPath, ".metadata")

	// Check if bucket already exists
	if _, err := os.Stat(bucketPath); !os.IsNotExist(err) {
		log.Printf("Bucket %s already exists.", bucketName)
		w.WriteHeader(http.StatusOK) // S3 PUT Bucket is idempotent
		return
	}

	// Create bucket directory
	if err := os.Mkdir(bucketPath, 0755); err != nil {
		log.Printf("Error creating bucket directory %s: %v", bucketPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error creating bucket.")))
		return
	}

	// Create .metadata directory within the bucket
	if err := os.Mkdir(metadataPath, 0755); err != nil {
		log.Printf("Error creating metadata directory %s for bucket %s: %v", metadataPath, bucketName, err)
		os.RemoveAll(bucketPath)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error creating bucket metadata storage.")))
		return
	}

	log.Printf("Successfully created bucket: %s", bucketName)
	w.WriteHeader(http.StatusOK)
}
func deleteBucketHandler(w http.ResponseWriter, r *http.Request, bucketName string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	metadataPath := filepath.Join(bucketPath, ".metadata")

	// Check if bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Attempted to delete non-existent bucket: %s", bucketName)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(errorToXML("NoSuchBucket", "The specified bucket does not exist.")))
		return
	}

	// Check if bucket is empty (excluding .metadata directory)
	files, err := os.ReadDir(bucketPath)
	if err != nil {
		log.Printf("Error reading bucket directory %s during delete: %v", bucketPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error reading bucket.")))
		return
	}

	for _, file := range files {
		if file.Name() != ".metadata" {
			log.Printf("Attempted to delete non-empty bucket: %s", bucketName)
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(errorToXML("BucketNotEmpty", "The bucket you tried to delete is not empty.")))
			return
		}
	}

	// Delete .metadata directory first
	if err := os.RemoveAll(metadataPath); err != nil {
		log.Printf("Error deleting metadata directory %s for bucket %s: %v", metadataPath, bucketName, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error deleting bucket.")))
		return
	}

	// Delete bucket directory
	if err := os.RemoveAll(bucketPath); err != nil {
		log.Printf("Error deleting bucket directory %s: %v", bucketPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error deleting bucket.")))
		return
	}

	log.Printf("Successfully deleted bucket: %s", bucketName)
	w.WriteHeader(http.StatusNoContent)
}
func getBucketLocationHandler(w http.ResponseWriter, r *http.Request, bucketName string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	// Check if bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Bucket %s does not exist for GetBucketLocation", bucketName)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(errorToXML("NoSuchBucket", "The specified bucket does not exist.")))
		return
	}

	// S3 returns an empty LocationConstraint for US Standard (us-east-1)
	location := LocationConstraint{Location: ""}
	x, err := xml.MarshalIndent(location, "", "  ")
	if err != nil {
		log.Printf("Error marshalling LocationConstraint to XML for bucket %s: %v", bucketName, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error formatting response.")))
		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	w.Write(x)
	log.Printf("Successfully served GetBucketLocation for %s", bucketName)
}

func headBucketHandler(w http.ResponseWriter, r *http.Request, bucketName string) {
	bucketPath := filepath.Join(dataDir, bucketName)

	// Check if bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Bucket %s does not exist for HeadBucket", bucketName)
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
	log.Printf("Successfully served HeadBucket for %s", bucketName)
}

func putObjectHandler(w http.ResponseWriter, r *http.Request, bucketName, objectName string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	// Object data is stored directly in the bucket directory
	objectDataPath := filepath.Join(bucketPath, objectName)
	// Metadata is stored in .metadata subdirectory
	objectMetadataDir := filepath.Join(bucketPath, ".metadata")
	objectMetadataPath := filepath.Join(objectMetadataDir, objectName+".meta")

	// Validate object key
	if err := validateObjectKey(objectName); err != nil {
		log.Printf("Invalid object key %s: %v", objectName, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(errorToXML("InvalidArgument", err.Error())))
		return
	}

	// Ensure bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Bucket %s does not exist for PutObject", bucketName)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(errorToXML("NoSuchBucket", "The specified bucket does not exist.")))
		return
	}

	// Read the request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error reading request body for %s/%s: %v", bucketName, objectName, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error reading request body.")))
		return
	}
	defer r.Body.Close()

	// Handle aws-chunked Content-Encoding (used by AWS CLI v2)
	contentEncoding := r.Header.Get("Content-Encoding")
	if strings.Contains(contentEncoding, "aws-chunked") {
		decodedBody, err := decodeAWSChunked(body)
		if err != nil {
			log.Printf("Error decoding aws-chunked body for %s/%s: %v", bucketName, objectName, err)
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(errorToXML("InvalidArgument", "Failed to decode chunked body.")))
			return
		}
		body = decodedBody
		log.Printf("Decoded aws-chunked body: %d bytes", len(body))
	}

	// Calculate ETag (MD5 hash of the content)
	hash := md5.Sum(body)
	eTag := hex.EncodeToString(hash[:])

	// Create parent directories for the object data if they don't exist
	objectDataParentDir := filepath.Dir(objectDataPath)
	if err := os.MkdirAll(objectDataParentDir, 0755); err != nil {
		log.Printf("Error creating parent directories for object data %s: %v", objectDataPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error creating object storage.")))
		return
	}

	// Write the object data
	if err := os.WriteFile(objectDataPath, body, 0644); err != nil {
		log.Printf("Error writing object data to %s: %v", objectDataPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error writing object data.")))
		return
	}

	// Create parent directories for the metadata file if they don't exist
	metadataParentDir := filepath.Dir(objectMetadataPath)
	if err := os.MkdirAll(metadataParentDir, 0755); err != nil {
		log.Printf("Error creating parent directories for metadata %s: %v", objectMetadataPath, err)
		// Clean up the object data file since metadata creation failed
		os.Remove(objectDataPath)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error creating metadata storage.")))
		return
	}

	// Store metadata - use actual body length, not Content-Length header
	meta := ObjectMetadata{
		ContentType:    r.Header.Get("Content-Type"),
		ContentLength:  int64(len(body)), // Use actual body length
		ETag:           eTag,
		CustomMetadata: make(map[string]string),
		LastModified:   time.Now().UTC(),
		StoragePath:    objectDataPath, // Points to actual object data
	}

	for headerName, headerValues := range r.Header {
		if strings.HasPrefix(strings.ToLower(headerName), "x-amz-meta-") {
			meta.CustomMetadata[headerName] = strings.Join(headerValues, ", ")
		}
	}

	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		log.Printf("Error marshalling metadata for %s/%s: %v", bucketName, objectName, err)
		os.Remove(objectDataPath) // Clean up object data
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error creating metadata.")))
		return
	}

	if err := os.WriteFile(objectMetadataPath, metaJSON, 0644); err != nil {
		log.Printf("Error writing metadata file %s: %v", objectMetadataPath, err)
		os.Remove(objectDataPath) // Clean up object data
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error writing metadata.")))
		return
	}

	log.Printf("Successfully put object %s/%s, ETag: %s", bucketName, objectName, eTag)
	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", eTag))
	w.WriteHeader(http.StatusOK)
}

func getObjectHandler(w http.ResponseWriter, r *http.Request, bucketName, objectName string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	objectMetadataPath := filepath.Join(bucketPath, ".metadata", objectName+".meta")

	// Check if bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Bucket %s does not exist for GetObject", bucketName)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(errorToXML("NoSuchBucket", "The specified bucket does not exist.")))
		return
	}

	// Read metadata
	metaJSON, err := os.ReadFile(objectMetadataPath)
	if os.IsNotExist(err) {
		log.Printf("Object metadata %s not found for %s/%s", objectMetadataPath, bucketName, objectName)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(errorToXML("NoSuchKey", "The specified key does not exist.")))
		return
	}
	if err != nil {
		log.Printf("Error reading metadata file %s: %v", objectMetadataPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error reading object metadata.")))
		return
	}

	var meta ObjectMetadata
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		log.Printf("Error unmarshalling metadata from %s: %v", objectMetadataPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error parsing object metadata.")))
		return
	}

	// Check if actual object data file exists
	objectDataPath := meta.StoragePath
	if _, err := os.Stat(objectDataPath); os.IsNotExist(err) {
		log.Printf("Object data file %s not found for %s/%s", objectDataPath, bucketName, objectName)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(errorToXML("NoSuchKey", "The specified key does not exist.")))
		return
	}

	// Set headers from metadata
	if meta.ContentType != "" {
		w.Header().Set("Content-Type", meta.ContentType)
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.ContentLength))
	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", meta.ETag))
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	for k, v := range meta.CustomMetadata {
		w.Header().Set(k, v)
	}

	// Stream the object data
	file, err := os.Open(objectDataPath)
	if err != nil {
		log.Printf("Error opening object data file %s: %v", objectDataPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error reading object data.")))
		return
	}
	defer file.Close()

	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, file); err != nil {
		log.Printf("Error streaming object %s/%s to client: %v", bucketName, objectName, err)
	}
	log.Printf("Successfully served object %s/%s", bucketName, objectName)
}

func deleteObjectHandler(w http.ResponseWriter, r *http.Request, bucketName, objectName string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	objectMetadataPath := filepath.Join(bucketPath, ".metadata", objectName+".meta")
	objectDataPath := filepath.Join(bucketPath, objectName)

	// Check if bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Bucket %s does not exist for DeleteObject", bucketName)
		// S3 returns 204 No Content for delete even if object doesn't exist
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Try to read metadata to get actual storage path
	var actualDataPath string
	metaJSON, err := os.ReadFile(objectMetadataPath)
	if err == nil {
		var meta ObjectMetadata
		if jsonErr := json.Unmarshal(metaJSON, &meta); jsonErr == nil && meta.StoragePath != "" {
			actualDataPath = meta.StoragePath
		} else {
			actualDataPath = objectDataPath
		}
	} else {
		actualDataPath = objectDataPath
	}

	// Delete the object data file
	dataDeleted := false
	if err := os.Remove(actualDataPath); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Error deleting object data file %s: %v", actualDataPath, err)
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(errorToXML("InternalError", "Error deleting object data.")))
			return
		}
	} else {
		dataDeleted = true
	}

	// Delete the metadata file
	metaDeleted := false
	if err := os.Remove(objectMetadataPath); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Error deleting metadata file %s: %v", objectMetadataPath, err)
			// Don't fail - data is already deleted
		}
	} else {
		metaDeleted = true
	}

	// Clean up empty parent directories (best effort)
	cleanupEmptyDirs(filepath.Dir(actualDataPath), bucketPath)
	cleanupEmptyDirs(filepath.Dir(objectMetadataPath), filepath.Join(bucketPath, ".metadata"))

	if dataDeleted || metaDeleted {
		log.Printf("Successfully deleted object %s/%s", bucketName, objectName)
	} else {
		log.Printf("Object %s/%s did not exist for deletion", bucketName, objectName)
	}

	w.WriteHeader(http.StatusNoContent) // S3 spec: 204 No Content
}

func headObjectHandler(w http.ResponseWriter, r *http.Request, bucketName, objectName string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	objectMetadataPath := filepath.Join(bucketPath, ".metadata", objectName+".meta")

	// Check if bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Bucket %s does not exist for HeadObject", bucketName)
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Read metadata
	metaJSON, err := os.ReadFile(objectMetadataPath)
	if os.IsNotExist(err) {
		log.Printf("Object metadata %s not found for %s/%s for HeadObject", objectMetadataPath, bucketName, objectName)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("Error reading metadata file %s for HeadObject: %v", objectMetadataPath, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	var meta ObjectMetadata
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		log.Printf("Error unmarshalling metadata from %s for HeadObject: %v", objectMetadataPath, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Check if actual object data file exists
	if _, err := os.Stat(meta.StoragePath); os.IsNotExist(err) {
		log.Printf("Object data file %s not found for %s/%s during HeadObject", meta.StoragePath, bucketName, objectName)
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Set headers from metadata
	if meta.ContentType != "" {
		w.Header().Set("Content-Type", meta.ContentType)
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.ContentLength))
	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", meta.ETag))
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	for k, v := range meta.CustomMetadata {
		w.Header().Set(k, v)
	}

	w.WriteHeader(http.StatusOK)
	log.Printf("Successfully served HEAD for object %s/%s", bucketName, objectName)
}

// listObjectsV2Handler implementation
func listObjectsV2Handler(w http.ResponseWriter, r *http.Request, bucketName string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	metadataDir := filepath.Join(bucketPath, ".metadata")

	// Check if bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Bucket %s does not exist for ListObjectsV2", bucketName)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(errorToXML("NoSuchBucket", "The specified bucket does not exist.")))
		return
	}

	// Parse query parameters
	prefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")
	continuationToken := r.URL.Query().Get("continuation-token")
	startAfter := r.URL.Query().Get("start-after")
	maxKeysStr := r.URL.Query().Get("max-keys")
	maxKeys := 1000 // Default S3 maxKeys
	if maxKeysStr != "" {
		var tempMaxKeys int
		if n, err := fmt.Sscan(maxKeysStr, &tempMaxKeys); err != nil || n != 1 {
			log.Printf("Invalid max-keys value: '%s'. Using default %d.", maxKeysStr, 1000)
		} else {
			if tempMaxKeys < 0 {
				log.Printf("max-keys must be non-negative. Received %d. Using default %d.", tempMaxKeys, 1000)
			} else if tempMaxKeys > 1000 {
				maxKeys = 1000 // S3 caps at 1000
			} else {
				maxKeys = tempMaxKeys
			}
		}
	}

	var objects []Object
	var commonPrefixesMap = make(map[string]struct{})
	var allObjectKeys []string

	// Use filepath.WalkDir to handle nested paths
	err := filepath.WalkDir(metadataDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip directories and non-.meta files
		if d.IsDir() {
			// Skip .uploads directory
			if d.Name() == ".uploads" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".meta") {
			return nil
		}

		// Calculate object key from path relative to metadata dir
		relPath, err := filepath.Rel(metadataDir, path)
		if err != nil {
			return nil
		}
		objectKey := strings.TrimSuffix(relPath, ".meta")
		allObjectKeys = append(allObjectKeys, objectKey)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		log.Printf("Error walking metadata directory %s: %v", metadataDir, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error listing objects.")))
		return
	}
	sort.Strings(allObjectKeys)

	startKey := ""
	if continuationToken != "" {
		startKey = continuationToken
	} else if startAfter != "" {
		startKey = startAfter
	}

	processedCount := 0
	isTruncated := false
	var nextContinuationToken string

	for _, objectKey := range allObjectKeys {
		if startKey != "" && objectKey <= startKey {
			continue
		}

		if prefix != "" && !strings.HasPrefix(objectKey, prefix) {
			continue
		}

		if processedCount >= maxKeys && maxKeys > 0 {
			isTruncated = true
			nextContinuationToken = objectKey
			break
		}

		if delimiter != "" {
			keyPartAfterRequestPrefix := objectKey
			if strings.HasPrefix(objectKey, prefix) {
				keyPartAfterRequestPrefix = objectKey[len(prefix):]
			} else if prefix != "" {
				continue
			}

			if idx := strings.Index(keyPartAfterRequestPrefix, delimiter); idx != -1 {
				commonPrefixValue := prefix + keyPartAfterRequestPrefix[:idx+len(delimiter)]
				if _, exists := commonPrefixesMap[commonPrefixValue]; !exists {
					commonPrefixesMap[commonPrefixValue] = struct{}{}
				}
				continue
			}
		}

		if maxKeys == 0 {
			isTruncated = len(allObjectKeys) > 0
			if isTruncated && len(allObjectKeys) > 0 && (startKey == "" || allObjectKeys[0] > startKey) {
				for _, k := range allObjectKeys {
					if startKey != "" && k <= startKey {
						continue
					}
					if prefix != "" && !strings.HasPrefix(k, prefix) {
						continue
					}
					nextContinuationToken = k
					break
				}
			}
			break
		}

		metaJSON, err := os.ReadFile(filepath.Join(metadataDir, objectKey+".meta"))
		if err != nil {
			log.Printf("Error reading metadata for %s/%s: %v. Skipping.", bucketName, objectKey, err)
			continue
		}
		var meta ObjectMetadata
		if err := json.Unmarshal(metaJSON, &meta); err != nil {
			log.Printf("Error unmarshalling metadata for %s/%s: %v. Skipping.", bucketName, objectKey, err)
			continue
		}

		objects = append(objects, Object{
			Key:          objectKey,
			LastModified: meta.LastModified.UTC().Format("2006-01-02T15:04:05.000Z"),
			ETag:         fmt.Sprintf("\"%s\"", meta.ETag),
			Size:         meta.ContentLength,
			StorageClass: "STANDARD",
		})
		processedCount++
	}

	var commonPrefixEntries []CommonPrefix
	for cp := range commonPrefixesMap {
		commonPrefixEntries = append(commonPrefixEntries, CommonPrefix{Prefix: cp})
	}
	sort.Slice(commonPrefixEntries, func(i, j int) bool {
		return commonPrefixEntries[i].Prefix < commonPrefixEntries[j].Prefix
	})

	result := ListBucketResult{
		IsTruncated:           isTruncated,
		Contents:              objects,
		Name:                  bucketName,
		Prefix:                prefix,
		Delimiter:             delimiter,
		MaxKeys:               maxKeys,
		CommonPrefixes:        commonPrefixEntries,
		KeyCount:              len(objects) + len(commonPrefixEntries),
		ContinuationToken:     continuationToken,
		NextContinuationToken: nextContinuationToken,
		StartAfter:            startAfter,
	}

	x, err := xml.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Printf("Error marshalling ListBucketResult to XML for bucket %s: %v", bucketName, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error formatting object list response.")))
		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	w.Write(x)
	log.Printf("Successfully served ListObjectsV2 for bucket %s", bucketName)
}

// Multipart Handlers
func initiateMultipartUploadHandler(w http.ResponseWriter, r *http.Request, bucketName, objectName string) {
	bucketPath := filepath.Join(dataDir, bucketName)

	// Validate object key
	if err := validateObjectKey(objectName); err != nil {
		log.Printf("Invalid object key %s: %v", objectName, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(errorToXML("InvalidArgument", err.Error())))
		return
	}

	// Ensure bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Bucket %s does not exist for InitiateMultipartUpload", bucketName)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(errorToXML("NoSuchBucket", "The specified bucket does not exist.")))
		return
	}

	// Generate a unique UploadID
	uploadID := fmt.Sprintf("%d-%s", time.Now().UnixNano(), objectName)
	hash := md5.Sum([]byte(uploadID))
	uploadID = hex.EncodeToString(hash[:])

	uploadsDir := filepath.Join(bucketPath, ".metadata", ".uploads")
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		log.Printf("Error creating .uploads directory %s: %v", uploadsDir, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error creating upload storage.")))
		return
	}

	mpUpload := MultipartUpload{
		UploadID:  uploadID,
		Key:       objectName,
		Initiated: time.Now().UTC(),
		Parts:     make(map[int]PartMetadata),
	}
	mpUploadJSON, err := json.MarshalIndent(mpUpload, "", "  ")
	if err != nil {
		log.Printf("Error marshalling multipart upload metadata for %s: %v", uploadID, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error creating upload metadata.")))
		return
	}

	mpUploadMetaPath := filepath.Join(uploadsDir, uploadID+".json")
	if err := os.WriteFile(mpUploadMetaPath, mpUploadJSON, 0644); err != nil {
		log.Printf("Error writing multipart upload metadata file %s: %v", mpUploadMetaPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error writing upload metadata.")))
		return
	}

	result := InitiateMultipartUploadResult{
		Bucket:   bucketName,
		Key:      objectName,
		UploadID: uploadID,
	}

	x, err := xml.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Printf("Error marshalling InitiateMultipartUploadResult to XML: %v", err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error formatting response.")))
		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	w.Write(x)
	log.Printf("Initiated multipart upload for %s/%s with UploadID: %s", bucketName, objectName, uploadID)
}

func uploadPartHandler(w http.ResponseWriter, r *http.Request, bucketName, objectName, partNumberStr, uploadID string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	mpUploadMetaPath := filepath.Join(bucketPath, ".metadata", ".uploads", uploadID+".json")

	partNumber, err := parseInt(partNumberStr, "partNumber")
	if err != nil {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(errorToXML("InvalidArgument", "Invalid part number.")))
		return
	}
	if partNumber < 1 || partNumber > 10000 {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(errorToXML("InvalidArgument", "Part number must be between 1 and 10000.")))
		return
	}

	// Read multipart upload metadata
	metaJSON, err := os.ReadFile(mpUploadMetaPath)
	if os.IsNotExist(err) {
		log.Printf("Multipart upload metadata %s not found for UploadID %s", mpUploadMetaPath, uploadID)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(errorToXML("NoSuchUpload", "The specified multipart upload does not exist.")))
		return
	}
	if err != nil {
		log.Printf("Error reading multipart upload metadata %s: %v", mpUploadMetaPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error reading upload metadata.")))
		return
	}

	var mpUpload MultipartUpload
	if err := json.Unmarshal(metaJSON, &mpUpload); err != nil {
		log.Printf("Error unmarshalling multipart upload metadata from %s: %v", mpUploadMetaPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error parsing upload metadata.")))
		return
	}

	if mpUpload.Key != objectName {
		log.Printf("Object name mismatch for UploadID %s. Expected %s, got %s", uploadID, mpUpload.Key, objectName)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(errorToXML("NoSuchUpload", "The specified multipart upload does not exist.")))
		return
	}

	// Read part data
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error reading request body for part %d of %s/%s (UploadID %s): %v", partNumber, bucketName, objectName, uploadID, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error reading part data.")))
		return
	}
	defer r.Body.Close()

	partSize := int64(len(body))

	// Calculate ETag for the part (MD5 hash)
	hash := md5.Sum(body)
	eTag := hex.EncodeToString(hash[:])

	// Store the part data
	partsDir := filepath.Join(bucketPath, ".metadata", ".uploads", uploadID+"_parts")
	if err := os.MkdirAll(partsDir, 0755); err != nil {
		log.Printf("Error creating directory for parts %s: %v", partsDir, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error creating part storage.")))
		return
	}
	partPath := filepath.Join(partsDir, fmt.Sprintf("part-%d", partNumber))
	if err := os.WriteFile(partPath, body, 0644); err != nil {
		log.Printf("Error writing part data to %s: %v", partPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error writing part data.")))
		return
	}

	// Update multipart upload metadata
	mpUpload.Parts[partNumber] = PartMetadata{
		PartNumber: partNumber,
		ETag:       eTag,
		Size:       partSize,
		StoredPath: partPath,
	}

	updatedMetaJSON, err := json.MarshalIndent(mpUpload, "", "  ")
	if err != nil {
		log.Printf("Error marshalling updated multipart upload metadata for %s: %v", uploadID, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error updating upload metadata.")))
		return
	}
	if err := os.WriteFile(mpUploadMetaPath, updatedMetaJSON, 0644); err != nil {
		log.Printf("Error writing updated multipart upload metadata file %s: %v", mpUploadMetaPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error saving upload metadata.")))
		return
	}

	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", eTag))
	w.WriteHeader(http.StatusOK)
	log.Printf("Successfully uploaded part %d for %s/%s (UploadID %s), ETag: %s", partNumber, bucketName, objectName, uploadID, eTag)
}

func completeMultipartUploadHandler(w http.ResponseWriter, r *http.Request, bucketName, objectName, uploadID string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	mpUploadMetaPath := filepath.Join(bucketPath, ".metadata", ".uploads", uploadID+".json")
	partsDir := filepath.Join(bucketPath, ".metadata", ".uploads", uploadID+"_parts")

	// Read multipart upload metadata
	metaJSON, err := os.ReadFile(mpUploadMetaPath)
	if os.IsNotExist(err) {
		log.Printf("Multipart upload metadata %s not found for UploadID %s (Complete)", mpUploadMetaPath, uploadID)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(errorToXML("NoSuchUpload", "The specified multipart upload does not exist.")))
		return
	}
	if err != nil {
		log.Printf("Error reading multipart upload metadata %s: %v", mpUploadMetaPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error reading upload metadata.")))
		return
	}

	var mpUpload MultipartUpload
	if err := json.Unmarshal(metaJSON, &mpUpload); err != nil {
		log.Printf("Error unmarshalling multipart upload metadata from %s: %v", mpUploadMetaPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error parsing upload metadata.")))
		return
	}

	if mpUpload.Key != objectName {
		log.Printf("Object name mismatch for UploadID %s during complete. Expected %s, got %s", uploadID, mpUpload.Key, objectName)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(errorToXML("NoSuchUpload", "The specified multipart upload does not exist.")))
		return
	}

	// Parse the XML body for part numbers and ETags
	var completeRequest CompleteMultipartUpload
	if err := xml.NewDecoder(r.Body).Decode(&completeRequest); err != nil {
		log.Printf("Error decoding CompleteMultipartUpload XML for %s/%s (UploadID %s): %v", bucketName, objectName, uploadID, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(errorToXML("MalformedXML", "The XML you provided was not well-formed.")))
		return
	}
	defer r.Body.Close()

	if len(completeRequest.Parts) == 0 {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(errorToXML("InvalidPart", "You must specify at least one part.")))
		return
	}

	// Verify parts and prepare for assembly
	// Object data stored directly in bucket (consistent with putObjectHandler)
	finalObjectPath := filepath.Join(bucketPath, objectName)

	// Create parent directories if needed
	if err := os.MkdirAll(filepath.Dir(finalObjectPath), 0755); err != nil {
		log.Printf("Error creating parent directories for final object %s: %v", finalObjectPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error creating object storage.")))
		return
	}
	finalObjectFile, err := os.OpenFile(finalObjectPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		log.Printf("Error creating final object file %s: %v", finalObjectPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error creating object file.")))
		return
	}
	defer finalObjectFile.Close()

	var totalSize int64
	var partETags []string // To calculate the final ETag

	for i, partToUpload := range completeRequest.Parts {
		// S3: Parts must be ordered by PartNumber
		if i > 0 && partToUpload.PartNumber <= completeRequest.Parts[i-1].PartNumber {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(errorToXML("InvalidPartOrder", "Parts must be ordered by part number.")))
			return
		}

		storedPartMeta, ok := mpUpload.Parts[partToUpload.PartNumber]
		if !ok {
			log.Printf("Part number %d not found in multipart upload %s", partToUpload.PartNumber, uploadID)
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(errorToXML("InvalidPart", fmt.Sprintf("Part number %d not found in upload.", partToUpload.PartNumber))))
			return
		}
		// Handle quoted ETags
		requestETag := strings.Trim(partToUpload.ETag, "\"")
		if storedPartMeta.ETag != requestETag {
			log.Printf("ETag mismatch for part %d of upload %s. Expected %s, got %s", partToUpload.PartNumber, uploadID, storedPartMeta.ETag, requestETag)
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(errorToXML("InvalidPart", fmt.Sprintf("ETag mismatch for part number %d.", partToUpload.PartNumber))))
			return
		}

		partFile, err := os.Open(storedPartMeta.StoredPath)
		if err != nil {
			log.Printf("Error opening part data %s for assembly: %v", storedPartMeta.StoredPath, err)
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(errorToXML("InternalError", "Could not access part data.")))
			return
		}
		written, err := io.Copy(finalObjectFile, partFile)
		partFile.Close()
		if err != nil {
			log.Printf("Error copying part %d data to final object: %v", storedPartMeta.PartNumber, err)
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(errorToXML("InternalError", "Error during object assembly.")))
			return
		}
		totalSize += written
		partETags = append(partETags, storedPartMeta.ETag)
	}

	// Calculate final ETag for the assembled object
	// S3's ETag for multipart uploads is MD5 of concatenated binary MD5s of parts, followed by "-<number of parts>"
	finalETagHash := md5.New()
	for _, partETag := range partETags {
		// Assuming partETag is hex string of MD5, decode it first
		decodedETag, _ := hex.DecodeString(partETag)
		finalETagHash.Write(decodedETag)
	}
	finalETag := fmt.Sprintf("\"%s-%d\"", hex.EncodeToString(finalETagHash.Sum(nil)), len(partETags))

	// Store metadata for the completed object (consistent with PutObject)
	objectMetadataDir := filepath.Join(bucketPath, ".metadata")
	objectMetadataPath := filepath.Join(objectMetadataDir, objectName+".meta")

	// Create parent directories for metadata
	if err := os.MkdirAll(filepath.Dir(objectMetadataPath), 0755); err != nil {
		log.Printf("Error creating metadata directories for %s: %v", objectMetadataPath, err)
		os.Remove(finalObjectPath) // Clean up assembled object
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error creating metadata storage.")))
		return
	}

	meta := ObjectMetadata{
		ContentType:    r.Header.Get("Content-Type"),
		ContentLength:  totalSize,
		ETag:           strings.Trim(finalETag, "\""),
		CustomMetadata: make(map[string]string),
		LastModified:   time.Now().UTC(),
		StoragePath:    finalObjectPath, // Points to actual object data
	}

	metaJSONOutput, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		log.Printf("Error marshalling final object metadata for %s/%s: %v", bucketName, objectName, err)
		os.Remove(finalObjectPath)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error creating metadata.")))
		return
	}
	if err := os.WriteFile(objectMetadataPath, metaJSONOutput, 0644); err != nil {
		log.Printf("Error writing final object metadata file %s: %v", objectMetadataPath, err)
		os.Remove(finalObjectPath)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error writing metadata.")))
		return
	}

	// Clean up: delete the multipart upload metadata file and the temporary parts directory
	if err := os.Remove(mpUploadMetaPath); err != nil {
		log.Printf("Warning: Error deleting multipart upload metadata file %s: %v", mpUploadMetaPath, err)
	}
	if err := os.RemoveAll(partsDir); err != nil {
		log.Printf("Warning: Error deleting temporary parts directory %s: %v", partsDir, err)
	}

	result := CompletedMultipartUploadResult{
		Location: r.Host + r.URL.Path, // Construct object URL
		Bucket:   bucketName,
		Key:      objectName,
		ETag:     finalETag,
	}
	x, err := xml.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Printf("Error marshalling CompletedMultipartUploadResult to XML: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	w.Write(x)
	log.Printf("Successfully completed multipart upload for %s/%s, UploadID: %s, Final ETag: %s", bucketName, objectName, uploadID, finalETag)
}

func abortMultipartUploadHandler(w http.ResponseWriter, r *http.Request, bucketName, objectName, uploadID string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	mpUploadMetaPath := filepath.Join(bucketPath, ".metadata", ".uploads", uploadID+".json")
	partsDir := filepath.Join(bucketPath, ".metadata", ".uploads", uploadID+"_parts")

	// Check if the multipart upload metadata file exists
	_, err := os.Stat(mpUploadMetaPath)
	if os.IsNotExist(err) {
		log.Printf("Multipart upload metadata %s not found for UploadID %s (Abort)", mpUploadMetaPath, uploadID)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(errorToXML("NoSuchUpload", "The specified multipart upload does not exist.")))
		return
	}

	// Delete the multipart upload metadata file
	if err := os.Remove(mpUploadMetaPath); err != nil && !os.IsNotExist(err) {
		log.Printf("Error deleting multipart upload metadata file %s: %v", mpUploadMetaPath, err)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errorToXML("InternalError", "Error aborting upload.")))
		return
	}

	// Delete the temporary parts directory
	if err := os.RemoveAll(partsDir); err != nil && !os.IsNotExist(err) {
		log.Printf("Error deleting temporary parts directory %s: %v", partsDir, err)
		// Don't fail - metadata already deleted
	}

	w.WriteHeader(http.StatusNoContent)
	log.Printf("Successfully aborted multipart upload for %s/%s, UploadID: %s", bucketName, objectName, uploadID)
}

// SigV4 helper functions
func hashSHA256(data []byte) string {
	hasher := sha256.New()
	hasher.Write(data)
	return hex.EncodeToString(hasher.Sum(nil))
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func getSigningKey(secretKey, dateStamp, region, serviceName string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, serviceName)
	kSigning := hmacSHA256(kService, "aws4_request")
	return kSigning
}

func getCanonicalURI(r *http.Request) string {
	// Normalize path according to S3 rules (e.g. remove multiple slashes, handle dot segments if necessary)
	// For simplicity, using r.URL.Path. Clients should send a pre-normalized path.
	// S3 requires that the path be URI-encoded.
	// If r.URL.RawPath is available and correctly encoded by client, it might be better.
	// Otherwise, ensure r.URL.Path is what's expected.
	// For a bucket operation (e.g. /mybucket/), path is /mybucket/
	// For root (ListBuckets), path is /
	path := r.URL.Path
	if path == "" {
		return "/"
	}
	// Ensure leading slash
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// S3 URI encoding: No normalization of /./ or /../ segments in the path itself for signature.
	// However, query parameters are handled separately.
	// Go's http.Request.URL.Path is already decoded. For SigV4, we need the URI-encoded path as sent by client.
	// If r.URL.RawPath is empty, it means the path was not escaped or was "/"
	// This part can be tricky. AWS SDKs handle this. For a minimal server, we might assume client sends correctly escaped path.
	// Let's use r.URL.EscapedPath() if available and non-empty, otherwise r.URL.Path.
	escapedPath := r.URL.EscapedPath()
	if escapedPath == "" {
		escapedPath = "/"                          // Default for empty path
		if r.URL.Path != "" && r.URL.Path != "/" { // If path was not empty but RawPath was, re-escape (basic)
			escapedPath = (&url.URL{Path: r.URL.Path}).RequestURI() // This re-encodes based on Path
		}
	}
	return escapedPath
}

func getCanonicalQueryString(r *http.Request) string {
	queryParams := r.URL.Query()
	if len(queryParams) == 0 {
		return ""
	}

	var sortedKeys []string
	for k := range queryParams {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	var canonicalParams []string
	for _, k := range sortedKeys {
		values := queryParams[k]
		sort.Strings(values) // Sort values for the same key
		for _, v := range values {
			// S3 requires both key and value to be URI encoded.
			// r.URL.Query() gives decoded values. We need to re-encode them.
			canonicalParams = append(canonicalParams, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(canonicalParams, "&")
}

func getCanonicalHeaders(r *http.Request, signedHeaderNames []string) (string, string) {
	var canonicalHeaders strings.Builder

	// Create a map for quick lookup of signed headers
	signedHeadersMap := make(map[string]bool)
	for _, h := range signedHeaderNames {
		signedHeadersMap[strings.ToLower(h)] = true
	}

	var actualSignedHeadersForOutput []string
	var headerPairs [][2]string

	// Handle the 'host' header specially - Go stores it in r.Host, not r.Header
	if signedHeadersMap["host"] && r.Host != "" {
		headerPairs = append(headerPairs, [2]string{"host", r.Host})
		actualSignedHeadersForOutput = append(actualSignedHeadersForOutput, "host")
	}

	for name, values := range r.Header {
		lowerName := strings.ToLower(name)
		// Skip 'host' since we handled it above
		if lowerName == "host" {
			continue
		}
		if signedHeadersMap[lowerName] {
			var processedValues []string
			for _, v := range values {
				processedValues = append(processedValues, strings.TrimSpace(v))
			}
			headerPairs = append(headerPairs, [2]string{lowerName, strings.Join(processedValues, ",")})
			actualSignedHeadersForOutput = append(actualSignedHeadersForOutput, lowerName)
		}
	}

	// Sort headers by name
	sort.Slice(headerPairs, func(i, j int) bool {
		return headerPairs[i][0] < headerPairs[j][0]
	})

	for _, pair := range headerPairs {
		canonicalHeaders.WriteString(pair[0])
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(pair[1])
		canonicalHeaders.WriteString("\n")
	}

	// The SignedHeaders string is a semicolon-separated list of lowercase header names, sorted alphabetically.
	sort.Strings(actualSignedHeadersForOutput)
	return canonicalHeaders.String(), strings.Join(actualSignedHeadersForOutput, ";")
}

func getPayloadHash(r *http.Request) (string, []byte, error) {
	xAmzContentSHA256 := r.Header.Get("x-amz-content-sha256")
	if xAmzContentSHA256 == unsignedPayload {
		return unsignedPayload, nil, nil
	}
	// Handle various streaming payload types (AWS CLI v2 uses STREAMING-UNSIGNED-PAYLOAD-TRAILER)
	if strings.HasPrefix(xAmzContentSHA256, "STREAMING-") {
		// Streaming payloads are signed differently - treat as unsigned for basic implementation
		log.Printf("Note: Streaming payload type '%s' - accepting without body hash verification", xAmzContentSHA256)
		return xAmzContentSHA256, nil, nil
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read request body: %w", err)
	}
	// Replace r.Body so it can be read again by handlers
	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	payloadHash := hashSHA256(bodyBytes)

	// If x-amz-content-sha256 is provided and is not UNSIGNED_PAYLOAD, it MUST match the computed hash.
	if xAmzContentSHA256 != "" && xAmzContentSHA256 != payloadHash {
		return "", bodyBytes, fmt.Errorf("x-amz-content-sha256 mismatch. Provided: %s, Calculated: %s", xAmzContentSHA256, payloadHash)
	}
	return payloadHash, bodyBytes, nil // Return bodyBytes so it can be used if needed by caller
}

func authenticateRequest(w http.ResponseWriter, r *http.Request) bool {
	authHeader := r.Header.Get("Authorization")
	xAmzDate := r.Header.Get("x-amz-date")
	dateHeader := r.Header.Get("Date") // Fallback if x-amz-date is not present

	requestTimestamp := time.Now().UTC()
	var err error

	if xAmzDate != "" {
		requestTimestamp, err = time.Parse(iso8601Format, xAmzDate)
	} else if dateHeader != "" {
		requestTimestamp, err = time.Parse(http.TimeFormat, dateHeader)
	} else {
		log.Println("Authentication Error: Missing x-amz-date or Date header.")
		http.Error(w, errorToXML("AccessDenied", "AWS authentication requires a valid Date or x-amz-date header"), http.StatusForbidden)
		return false
	}
	if err != nil {
		log.Printf("Authentication Error: Invalid date format. x-amz-date: '%s', Date: '%s'. Error: %v", xAmzDate, dateHeader, err)
		http.Error(w, errorToXML("InvalidDate", "The date provided is invalid."), http.StatusBadRequest)
		return false
	}

	if time.Since(requestTimestamp).Abs() > 15*time.Minute {
		log.Printf("Authentication Error: Request timestamp %s is too skewed from server time %s.", requestTimestamp.Format(iso8601Format), time.Now().UTC().Format(iso8601Format))
		http.Error(w, errorToXML("RequestTimeTooSkewed", "The difference between the request time and the current time is too large."), http.StatusForbidden)
		return false
	}

	if authHeader == "" {
		// Enforce auth for all requests now. Remove temporary allowance for ListBuckets if any.
		log.Println("Authentication Error: Missing Authorization header.")
		http.Error(w, errorToXML("AuthorizationHeaderMissing", "The authorization header is missing."), http.StatusForbidden)
		return false
	}

	matches := authHeaderRegex.FindStringSubmatch(authHeader)
	if len(matches) != 6 {
		log.Printf("Authentication Error: Invalid Authorization header format: %s", authHeader)
		http.Error(w, errorToXML("AuthorizationHeaderMalformed", "The authorization header is malformed; it does not match the expected format."), http.StatusBadRequest)
		return false
	}

	accessKeyID := matches[1]
	dateStampFromCred := matches[2]
	regionFromCred := matches[3]
	signedHeadersFromAuth := strings.Split(matches[4], ";")
	clientSignature := matches[5]

	if accessKeyID != serverCredentials.AccessKeyID {
		log.Printf("Authentication Error: Unknown AccessKeyID: %s", accessKeyID)
		http.Error(w, errorToXML("InvalidAccessKeyId", "The AWS Access Key Id you provided does not exist in our records."), http.StatusForbidden)
		return false
	}

	// Validate dateStamp from credential scope matches the request date (short YYYYMMDD format)
	requestDateStamp := requestTimestamp.UTC().Format(shortDateFormat)
	if dateStampFromCred != requestDateStamp {
		log.Printf("Authentication Error: Date mismatch. Credential scope date: %s, Request date: %s", dateStampFromCred, requestDateStamp)
		http.Error(w, errorToXML("SignatureDoesNotMatch", "Credential scope date mismatch."), http.StatusForbidden)
		return false
	}

	if regionFromCred != defaultRegion {
		log.Printf("Authentication Error: Invalid region. Expected %s, got %s", defaultRegion, regionFromCred)
		http.Error(w, errorToXML("AuthorizationHeaderMalformed", "Region in credential scope ('"+regionFromCred+"') is incorrect; expected '"+defaultRegion+"'."), http.StatusForbidden)
		return false
	}

	// Step 1: Create a Canonical Request
	payloadHash, _, err := getPayloadHash(r) // bodyBytes might be needed if we re-calculate hash for some reason
	if err != nil {
		log.Printf("Authentication Error: Failed to get/verify payload hash: %v", err)
		http.Error(w, errorToXML("SignatureDoesNotMatch", "Payload hash mismatch or error reading body."), http.StatusForbidden)
		return false
	}

	canonicalURI := getCanonicalURI(r)
	canonicalQueryString := getCanonicalQueryString(r)
	canonicalHeaders, signedHeadersString := getCanonicalHeaders(r, signedHeadersFromAuth)

	// Verify that the signedHeadersString from our calculation matches what client sent in Authorization header
	// The client's list of signed headers (matches[4]) should be used to build our canonicalHeaders string.
	// Then, our re-calculated signedHeadersString (from getCanonicalHeaders) should match matches[4].
	if signedHeadersString != matches[4] {
		log.Printf("Authentication Error: SignedHeaders mismatch. Client sent: '%s', Server calculated based on found headers: '%s'", matches[4], signedHeadersString)
		// This might happen if client claims to sign a header that's not present, or if our sorting/joining is different.
		// For robustness, ensure getCanonicalHeaders uses the client's list of signed headers strictly.
		// The current getCanonicalHeaders already does this by taking signedHeaderNames as input.
	}

	canonicalRequest := strings.Join([]string{
		r.Method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders, // Already ends with a newline
		signedHeadersString,
		payloadHash,
	}, "\n")

	// Step 2: Create the String to Sign
	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStampFromCred, regionFromCred, serviceName)
	hashedCanonicalRequest := hashSHA256([]byte(canonicalRequest))

	stringToSign := strings.Join([]string{
		awsAlgorithm,
		requestTimestamp.UTC().Format(iso8601Format),
		credentialScope,
		hashedCanonicalRequest,
	}, "\n")

	// Step 3: Calculate the Signing Key
	signingKey := getSigningKey(serverCredentials.SecretAccessKey, dateStampFromCred, regionFromCred, serviceName)

	// Step 4: Calculate the Signature
	serverSignature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	// Step 5: Compare the Signatures
	if serverSignature != clientSignature {
		log.Printf("Authentication Error: Signature mismatch.\nServer Signature: %s\nClient Signature: %s\nString To Sign:\n%s\nCanonical Request:\n%s",
			serverSignature, clientSignature, stringToSign, canonicalRequest)
		http.Error(w, errorToXML("SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided."), http.StatusForbidden)
		return false
	}

	log.Println("Authentication Successful: SigV4 signature verified.")
	return true
}

// Placeholder ACL related requests.
func handleACL(w http.ResponseWriter, r *http.Request, bucketName, objectName string) {
	log.Printf("ACL request for Bucket: '%s', Object: '%s' - Not Implemented", bucketName, objectName)
	http.Error(w, "ACLs are not implemented.", http.StatusNotImplemented)
}

// Helper function to parse integers from string query parameters
func parseInt(valueStr string, paramName string) (int, error) {
	var val int
	_, err := fmt.Sscan(valueStr, &val)
	if err != nil {
		log.Printf("Invalid %s value: %s", paramName, valueStr)
		return 0, err
	}
	return val, nil
}

// validateBucketName validates S3 bucket naming rules
func validateBucketName(name string) error {
	if len(name) < 3 || len(name) > 63 {
		return fmt.Errorf("bucket name must be between 3 and 63 characters")
	}
	// Must start with lowercase letter or number
	if !((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= '0' && name[0] <= '9')) {
		return fmt.Errorf("bucket name must start with a lowercase letter or number")
	}
	// Must end with lowercase letter or number
	last := name[len(name)-1]
	if !((last >= 'a' && last <= 'z') || (last >= '0' && last <= '9')) {
		return fmt.Errorf("bucket name must end with a lowercase letter or number")
	}
	// Check valid characters and no consecutive periods
	prevChar := byte(0)
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.') {
			return fmt.Errorf("bucket name can only contain lowercase letters, numbers, hyphens, and periods")
		}
		if c == '.' && prevChar == '.' {
			return fmt.Errorf("bucket name cannot have consecutive periods")
		}
		prevChar = c
	}
	// Cannot be formatted as IP address
	if regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`).MatchString(name) {
		return fmt.Errorf("bucket name cannot be formatted as an IP address")
	}
	return nil
}

// validateObjectKey validates S3 object key constraints
func validateObjectKey(key string) error {
	if len(key) == 0 {
		return fmt.Errorf("object key cannot be empty")
	}
	if len(key) > 1024 {
		return fmt.Errorf("object key cannot exceed 1024 characters")
	}
	// Check for null bytes
	if strings.ContainsRune(key, 0) {
		return fmt.Errorf("object key cannot contain null bytes")
	}
	return nil
}

// cleanupEmptyDirs removes empty directories up to stopAt directory
func cleanupEmptyDirs(dir, stopAt string) {
	for dir != stopAt && dir != "." && dir != "/" {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(dir); err != nil {
			break
		}
		dir = filepath.Dir(dir)
	}
}
