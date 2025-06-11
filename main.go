package main

import (
	"bytes" // Added for getPayloadHash body handling
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
	"regexp" // Added for parsing Authorization header
	"sort"
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
var serverCredentials = struct {
	AccessKeyID     string
	SecretAccessKey string
}{
	AccessKeyID:     "YOUR_ACCESS_KEY_ID",     // Replace with your desired Access Key ID
	SecretAccessKey: "YOUR_SECRET_ACCESS_KEY", // Replace with your desired Secret Access Key
}

// Regex for parsing the AWS V4 Authorization header
var authHeaderRegex = regexp.MustCompile(
	`^AWS4-HMAC-SHA256 Credential=([^/]+)/([^/]+)/([^/]+)/s3/aws4_request, SignedHeaders=([^,]+), Signature=(.+)$`,
)

const (
// ... existing constants ...
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
	bucketPath := dataDir + bucketName
	metadataPath := bucketPath + "/.metadata" // Hidden subdirectory for metadata

	// Check if bucket already exists
	if _, err := os.Stat(bucketPath); !os.IsNotExist(err) {
		// Bucket exists, S3 PUT Bucket is idempotent, so 200 OK is fine.
		// Optionally, check ownership or handle "BucketAlreadyOwnedByYou" if implementing users.
		log.Printf("Bucket %s already exists.", bucketName)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Create bucket directory
	if err := os.Mkdir(bucketPath, 0755); err != nil {
		log.Printf("Error creating bucket directory %s: %v", bucketPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Create .metadata directory within the bucket
	if err := os.Mkdir(metadataPath, 0755); err != nil {
		log.Printf("Error creating metadata directory %s for bucket %s: %v", metadataPath, bucketName, err)
		// Attempt to clean up by removing the bucket directory if metadata creation fails
		os.RemoveAll(bucketPath)
		http.Error(w, "Internal Server Error creating metadata store", http.StatusInternalServerError)
		return
	}

	log.Printf("Successfully created bucket: %s with metadata dir: %s", bucketName, metadataPath)
	w.WriteHeader(http.StatusOK) // S3 spec: 200 OK for successful bucket creation
}
func deleteBucketHandler(w http.ResponseWriter, r *http.Request, bucketName string) {
	bucketPath := dataDir + bucketName
	metadataPath := bucketPath + "/.metadata"

	// Check if bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Attempted to delete non-existent bucket: %s", bucketName)
		// S3 behavior for deleting a non-existent bucket can vary.
		// Some might return 204 No Content, others 404 Not Found.
		// Let's go with 404 for clarity that it wasn't there to begin with.
		http.Error(w, "NoSuchBucket", http.StatusNotFound)
		return
	}

	// Check if bucket is empty (excluding .metadata directory)
	files, err := os.ReadDir(bucketPath)
	if err != nil {
		log.Printf("Error reading bucket directory %s during delete: %v", bucketPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	for _, file := range files {
		if file.Name() != ".metadata" {
			// If there's anything other than .metadata, the bucket is not empty.
			log.Printf("Attempted to delete non-empty bucket: %s", bucketName)
			http.Error(w, "BucketNotEmpty", http.StatusConflict) // S3 error code for non-empty bucket
			return
		}
	}

	// Delete .metadata directory first
	if err := os.RemoveAll(metadataPath); err != nil {
		log.Printf("Error deleting metadata directory %s for bucket %s: %v", metadataPath, bucketName, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Delete bucket directory
	if err := os.RemoveAll(bucketPath); err != nil {
		log.Printf("Error deleting bucket directory %s: %v", bucketPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	log.Printf("Successfully deleted bucket: %s", bucketName)
	w.WriteHeader(http.StatusNoContent) // S3 spec: 204 No Content for successful bucket deletion
}
func getBucketLocationHandler(w http.ResponseWriter, r *http.Request, bucketName string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	// Check if bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Bucket %s does not exist for GetBucketLocation", bucketName)
		http.Error(w, "NoSuchBucket", http.StatusNotFound)
		return
	}

	// S3 returns an empty LocationConstraint for US Standard (us-east-1)
	// or the region string. We can hardcode a default or leave it empty.
	// For simplicity, let's return an empty string, implying a default region.
	location := LocationConstraint{Location: ""} // Or a specific region string e.g., "us-west-1"
	x, err := xml.MarshalIndent(location, "", "  ")
	if err != nil {
		log.Printf("Error marshalling LocationConstraint to XML for bucket %s: %v", bucketName, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
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
		// S3 behavior for HEAD on a non-existent bucket is 404 Not Found.
		http.Error(w, "NoSuchBucket", http.StatusNotFound)
		return
	}

	// Bucket exists
	w.WriteHeader(http.StatusOK)
	log.Printf("Successfully served HeadBucket for %s", bucketName)
}

func putObjectHandler(w http.ResponseWriter, r *http.Request, bucketName, objectName string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	objectMetadataDir := filepath.Join(bucketPath, ".metadata")
	objectMetadataPath := filepath.Join(objectMetadataDir, objectName+".meta")

	// Ensure bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Bucket %s does not exist for PutObject", bucketName)
		http.Error(w, "NoSuchBucket", http.StatusNotFound)
		return
	}

	// Ensure .metadata directory exists within the bucket
	if _, err := os.Stat(objectMetadataDir); os.IsNotExist(err) {
		if err := os.MkdirAll(objectMetadataDir, 0755); err != nil {
			log.Printf("Failed to create metadata directory %s: %v", objectMetadataDir, err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}

	// Create/overwrite the object file
	// For simplicity, we read the whole object into memory first to calculate MD5.
	// For large files, streaming with tee to both file and hash would be better.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error reading request body for %s/%s: %v", bucketName, objectName, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	// Calculate ETag (MD5 hash of the content)
	hash := md5.Sum(body)
	eTag := hex.EncodeToString(hash[:])

	// Create parent directories for the object if they don't exist
	objectParentDir := filepath.Dir(objectMetadataPath)
	if err := os.MkdirAll(objectParentDir, 0755); err != nil {
		log.Printf("Error creating parent directories for object %s: %v", objectMetadataPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Write the object data
	if err := os.WriteFile(objectMetadataPath, body, 0644); err != nil {
		log.Printf("Error writing object data to %s: %v", objectMetadataPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Store metadata
	meta := ObjectMetadata{
		ContentType:    r.Header.Get("Content-Type"),
		ContentLength:  r.ContentLength, // This comes from the request header
		ETag:           eTag,
		CustomMetadata: make(map[string]string),
		LastModified:   time.Now().UTC(),
		StoragePath:    objectMetadataPath, // Storing for potential future use, not strictly S3
	}

	for headerName, headerValues := range r.Header {
		if strings.HasPrefix(strings.ToLower(headerName), "x-amz-meta-") {
			meta.CustomMetadata[headerName] = strings.Join(headerValues, ", ")
		}
	}

	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		log.Printf("Error marshalling metadata for %s/%s: %v", bucketName, objectName, err)
		// Object is written, but metadata failed. This is a partial failure state.
		// For simplicity, we'll still return success to the client but log the error.
		// A more robust system might try to clean up the object or retry metadata.
		w.Header().Set("ETag", fmt.Sprintf("\"%s\"", eTag)) // S3 ETag is usually quoted
		w.WriteHeader(http.StatusOK)
		return
	}

	// Create parent directories for the metadata file if they don't exist
	// (e.g. if objectName includes slashes like "folder/object.txt")
	metadataParentDir := filepath.Dir(objectMetadataPath)
	if err := os.MkdirAll(metadataParentDir, 0755); err != nil {
		log.Printf("Error creating parent directories for metadata %s: %v", objectMetadataPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if err := os.WriteFile(objectMetadataPath, metaJSON, 0644); err != nil {
		log.Printf("Error writing metadata file %s: %v", objectMetadataPath, err)
		// Similar to above, object is written, but metadata failed.
		w.Header().Set("ETag", fmt.Sprintf("\"%s\"", eTag))
		w.WriteHeader(http.StatusOK)
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
		http.Error(w, "NoSuchBucket", http.StatusNotFound)
		return
	}

	// Read metadata
	metaJSON, err := os.ReadFile(objectMetadataPath)
	if os.IsNotExist(err) {
		log.Printf("Object metadata %s not found for %s/%s", objectMetadataPath, bucketName, objectName)
		http.Error(w, "NoSuchKey", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("Error reading metadata file %s: %v", objectMetadataPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var meta ObjectMetadata
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		log.Printf("Error unmarshalling metadata from %s: %v", objectMetadataPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Check if actual object data file exists (as per metadata)
	// This is an important check if StoragePath in metadata is the source of truth
	actualObjectDataPath := meta.StoragePath
	if _, err := os.Stat(actualObjectDataPath); os.IsNotExist(err) {
		log.Printf("Object data file %s (from metadata) not found for %s/%s", actualObjectDataPath, bucketName, objectName)
		// This case might indicate an inconsistency, perhaps the object was deleted manually
		http.Error(w, "NoSuchKey", http.StatusNotFound)
		return
	}

	// Set headers from metadata
	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.ContentLength))
	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", meta.ETag))
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	for k, v := range meta.CustomMetadata {
		w.Header().Set(k, v)
	}

	// Stream the object data
	file, err := os.Open(actualObjectDataPath)
	if err != nil {
		log.Printf("Error opening object data file %s: %v", actualObjectDataPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, file); err != nil {
		log.Printf("Error streaming object %s/%s to client: %v", bucketName, objectName, err)
		// Client may have disconnected, too late to send an HTTP error status
	}
	log.Printf("Successfully served object %s/%s", bucketName, objectName)
}

func deleteObjectHandler(w http.ResponseWriter, r *http.Request, bucketName, objectName string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	objectMetadataPath := filepath.Join(bucketPath, ".metadata", objectName+".meta")

	// Check if bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Bucket %s does not exist for DeleteObject", bucketName)
		// S3 typically returns 204 No Content even if the bucket doesn't exist, as the goal is object deletion.
		// However, to be more explicit about the state, we can choose to return 404 if bucket is not found.
		// For now, let's align with a common interpretation of 204 for object deletion idempotency.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Attempt to read metadata to get the actual storage path, if it exists
	var meta ObjectMetadata
	metaJSON, err := os.ReadFile(objectMetadataPath)
	dataFileNotFound := false
	if err == nil {
		if jsonErr := json.Unmarshal(metaJSON, &meta); jsonErr == nil {
			objectMetadataPath = meta.StoragePath // Use path from metadata if available
		} else {
			log.Printf("Warning: could not unmarshal metadata %s for %s/%s: %v. Proceeding with default path.", objectMetadataPath, bucketName, objectName, jsonErr)
		}
	} else if !os.IsNotExist(err) {
		log.Printf("Error reading metadata file %s during delete: %v", objectMetadataPath, err)
		// Don't fail here, still attempt to delete the primary object file if metadata read fails for other reasons.
	}

	// Delete the object data file
	err = os.Remove(objectMetadataPath)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("Error deleting object data file %s: %v", objectMetadataPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if os.IsNotExist(err) {
		dataFileNotFound = true
	}

	// Delete the metadata file
	metaErr := os.Remove(objectMetadataPath)
	if metaErr != nil && !os.IsNotExist(metaErr) {
		log.Printf("Error deleting metadata file %s: %v", objectMetadataPath, metaErr)
		// If data file was deleted but metadata deletion fails, it's an inconsistent state.
		// However, S3 delete is idempotent. Client expects 204.
	}

	if dataFileNotFound && os.IsNotExist(metaErr) {
		log.Printf("Object %s/%s (and its metadata) did not exist for deletion.", bucketName, objectName)
	} else {
		log.Printf("Successfully deleted object %s/%s (and/or its metadata)", bucketName, objectName)
	}

	w.WriteHeader(http.StatusNoContent) // S3 spec: 204 No Content for successful deletion or if object didn't exist
}

func headObjectHandler(w http.ResponseWriter, r *http.Request, bucketName, objectName string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	objectMetadataPath := filepath.Join(bucketPath, ".metadata", objectName+".meta")

	// Check if bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Bucket %s does not exist for HeadObject", bucketName)
		http.Error(w, "NoSuchBucket", http.StatusNotFound) // S3 returns 404 if bucket doesn't exist for HEAD
		return
	}

	// Read metadata
	metaJSON, err := os.ReadFile(objectMetadataPath)
	if os.IsNotExist(err) {
		log.Printf("Object metadata %s not found for %s/%s for HeadObject", objectMetadataPath, bucketName, objectName)
		http.Error(w, "NoSuchKey", http.StatusNotFound) // S3 returns 404 if object doesn't exist for HEAD
		return
	}
	if err != nil {
		log.Printf("Error reading metadata file %s for HeadObject: %v", objectMetadataPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var meta ObjectMetadata
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		log.Printf("Error unmarshalling metadata from %s for HeadObject: %v", objectMetadataPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Check if actual object data file exists (as per metadata)
	// This ensures the metadata isn't stale.
	if _, err := os.Stat(meta.StoragePath); os.IsNotExist(err) {
		log.Printf("Object data file %s (from metadata) not found for %s/%s during HeadObject", meta.StoragePath, bucketName, objectName)
		http.Error(w, "NoSuchKey", http.StatusNotFound) // Treat as if the key doesn't exist if data is missing
		return
	}

	// Set headers from metadata
	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.ContentLength))
	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", meta.ETag))
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	for k, v := range meta.CustomMetadata {
		w.Header().Set(k, v)
	}

	w.WriteHeader(http.StatusOK) // HEAD requests return 200 OK with headers but no body
	log.Printf("Successfully served HEAD for object %s/%s", bucketName, objectName)
}

// listObjectsV2Handler implementation
func listObjectsV2Handler(w http.ResponseWriter, r *http.Request, bucketName string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	metadataDir := filepath.Join(bucketPath, ".metadata")

	// Check if bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Bucket %s does not exist for ListObjectsV2", bucketName)
		http.Error(w, errorToXML("NoSuchBucket", "The specified bucket does not exist."), http.StatusNotFound)
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

	metaFiles, err := os.ReadDir(metadataDir)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("Error reading metadata directory %s: %v", metadataDir, err)
		http.Error(w, errorToXML("InternalServerError", "Error listing objects."), http.StatusInternalServerError)
		return
	}

	for _, metaFile := range metaFiles {
		if !metaFile.IsDir() && strings.HasSuffix(metaFile.Name(), ".meta") {
			objectKey := strings.TrimSuffix(metaFile.Name(), ".meta")
			allObjectKeys = append(allObjectKeys, objectKey)
		}
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
		http.Error(w, errorToXML("InternalServerError", "Error formatting object list response."), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	w.Write(x)
	log.Printf("Successfully served ListObjectsV2 for bucket %s", bucketName)
}

// Placeholder Multipart Handlers
func initiateMultipartUploadHandler(w http.ResponseWriter, r *http.Request, bucketName, objectName string) {
	bucketPath := filepath.Join(dataDir, bucketName)
	// Ensure bucket exists
	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		log.Printf("Bucket %s does not exist for InitiateMultipartUpload", bucketName)
		http.Error(w, "NoSuchBucket", http.StatusNotFound)
		return
	}

	// Generate a unique UploadID (e.g., UUID or a sufficiently random string)
	// For simplicity, using timestamp + random number for now. Production systems need robust UUIDs.
	uploadID := fmt.Sprintf("%d-%s", time.Now().UnixNano(), objectName) // Basic unique ID
	hash := md5.Sum([]byte(uploadID))
	uploadID = hex.EncodeToString(hash[:])

	uploadsDir := filepath.Join(bucketPath, ".metadata", ".uploads")
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		log.Printf("Error creating .uploads directory %s: %v", uploadsDir, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Store information about this multipart upload
	mpUpload := MultipartUpload{
		UploadID:  uploadID,
		Key:       objectName,
		Initiated: time.Now().UTC(),
		Parts:     make(map[int]PartMetadata),
	}
	mpUploadJSON, err := json.MarshalIndent(mpUpload, "", "  ")
	if err != nil {
		log.Printf("Error marshalling multipart upload metadata for %s: %v", uploadID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	mpUploadMetaPath := filepath.Join(uploadsDir, uploadID+".json")
	if err := os.WriteFile(mpUploadMetaPath, mpUploadJSON, 0644); err != nil {
		log.Printf("Error writing multipart upload metadata file %s: %v", mpUploadMetaPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
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
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
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
		http.Error(w, "InvalidPartNumber", http.StatusBadRequest)
		return
	}
	// S3 part numbers are 1-based and up to 10000
	if partNumber < 1 || partNumber > 10000 {
		http.Error(w, "InvalidPartOrder: Part number must be an integer between 1 and 10000, inclusive.", http.StatusBadRequest)
		return
	}

	// Read multipart upload metadata
	metaJSON, err := os.ReadFile(mpUploadMetaPath)
	if os.IsNotExist(err) {
		log.Printf("Multipart upload metadata %s not found for UploadID %s", mpUploadMetaPath, uploadID)
		http.Error(w, "NoSuchUpload", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("Error reading multipart upload metadata %s: %v", mpUploadMetaPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var mpUpload MultipartUpload
	if err := json.Unmarshal(metaJSON, &mpUpload); err != nil {
		log.Printf("Error unmarshalling multipart upload metadata from %s: %v", mpUploadMetaPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if mpUpload.Key != objectName {
		log.Printf("Object name mismatch for UploadID %s. Expected %s, got %s", uploadID, mpUpload.Key, objectName)
		http.Error(w, "NoSuchUpload", http.StatusNotFound) // Or a more specific error
		return
	}

	// Read part data
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error reading request body for part %d of %s/%s (UploadID %s): %v", partNumber, bucketName, objectName, uploadID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	partSize := int64(len(body))
	// S3 part size limits (except for the last part)
	// For simplicity, we are not strictly enforcing the 5MB minimum here, but real S3 does.

	// Calculate ETag for the part (MD5 hash)
	hash := md5.Sum(body)
	eTag := hex.EncodeToString(hash[:])

	// Store the part data temporarily
	// Parts are stored in a subdirectory named after the UploadID
	partsDir := filepath.Join(bucketPath, ".metadata", ".uploads", uploadID+"_parts")
	if err := os.MkdirAll(partsDir, 0755); err != nil {
		log.Printf("Error creating directory for parts %s: %v", partsDir, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	partPath := filepath.Join(partsDir, fmt.Sprintf("part-%d", partNumber))
	if err := os.WriteFile(partPath, body, 0644); err != nil {
		log.Printf("Error writing part data to %s: %v", partPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Update multipart upload metadata with this part's info
	mpUpload.Parts[partNumber] = PartMetadata{
		PartNumber: partNumber,
		ETag:       eTag,
		Size:       partSize,
		StoredPath: partPath,
	}

	updatedMetaJSON, err := json.MarshalIndent(mpUpload, "", "  ")
	if err != nil {
		log.Printf("Error marshalling updated multipart upload metadata for %s: %v", uploadID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(mpUploadMetaPath, updatedMetaJSON, 0644); err != nil {
		log.Printf("Error writing updated multipart upload metadata file %s: %v", mpUploadMetaPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
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
		http.Error(w, "NoSuchUpload", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("Error reading multipart upload metadata %s: %v", mpUploadMetaPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var mpUpload MultipartUpload
	if err := json.Unmarshal(metaJSON, &mpUpload); err != nil {
		log.Printf("Error unmarshalling multipart upload metadata from %s: %v", mpUploadMetaPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if mpUpload.Key != objectName {
		log.Printf("Object name mismatch for UploadID %s during complete. Expected %s, got %s", uploadID, mpUpload.Key, objectName)
		http.Error(w, "NoSuchUpload", http.StatusNotFound) // Or a more specific error
		return
	}

	// Parse the XML body for part numbers and ETags
	var completeRequest CompleteMultipartUpload
	if err := xml.NewDecoder(r.Body).Decode(&completeRequest); err != nil {
		log.Printf("Error decoding CompleteMultipartUpload XML for %s/%s (UploadID %s): %v", bucketName, objectName, uploadID, err)
		http.Error(w, "InvalidRequest: Could not parse XML", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if len(completeRequest.Parts) == 0 {
		http.Error(w, "InvalidPart: You must specify at least one part.", http.StatusBadRequest)
		return
	}

	// Verify parts and prepare for assembly
	finalObjectPath := filepath.Join(bucketPath, objectName)
	finalObjectFile, err := os.OpenFile(finalObjectPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		log.Printf("Error creating final object file %s: %v", finalObjectPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer finalObjectFile.Close()

	var totalSize int64
	var partETags []string // To calculate the final ETag

	for i, partToUpload := range completeRequest.Parts {
		// S3: Parts must be ordered by PartNumber
		if i > 0 && partToUpload.PartNumber <= completeRequest.Parts[i-1].PartNumber {
			http.Error(w, "InvalidPartOrder: Parts must be ordered by part number.", http.StatusBadRequest)
			return
		}

		storedPartMeta, ok := mpUpload.Parts[partToUpload.PartNumber]
		if !ok {
			log.Printf("Part number %d not found in multipart upload %s", partToUpload.PartNumber, uploadID)
			http.Error(w, fmt.Sprintf("InvalidPart: Part number %d not found in upload.", partToUpload.PartNumber), http.StatusBadRequest)
			return
		}
		// S3 ETags are quoted, the request might send them quoted or not. Be flexible or strict.
		// Here, we assume the stored ETag is unquoted, and the request ETag might be quoted.
		requestETag := strings.Trim(partToUpload.ETag, "\"")
		if storedPartMeta.ETag != requestETag {
			log.Printf("ETag mismatch for part %d of upload %s. Expected %s, got %s", partToUpload.PartNumber, uploadID, storedPartMeta.ETag, requestETag)
			http.Error(w, fmt.Sprintf("InvalidPart: ETag mismatch for part number %s.", partToUpload.ETag), http.StatusBadRequest)
			return
		}

		partFile, err := os.Open(storedPartMeta.StoredPath)
		if err != nil {
			log.Printf("Error opening part data %s for assembly: %v", storedPartMeta.StoredPath, err)
			http.Error(w, "InternalServerError: Could not access part data.", http.StatusInternalServerError)
			return
		}
		written, err := io.Copy(finalObjectFile, partFile)
		partFile.Close() // Close immediately after copy
		if err != nil {
			log.Printf("Error copying part %d data to final object: %v", storedPartMeta.PartNumber, err)
			http.Error(w, "InternalServerError: Error during assembly.", http.StatusInternalServerError)
			return
		}
		totalSize += written
		partETags = append(partETags, storedPartMeta.ETag) // Use the unquoted ETag
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

	// Store metadata for the completed object (similar to PutObject)
	objectMetadataDir := filepath.Join(bucketPath, ".metadata")
	objectMetadataPath := filepath.Join(objectMetadataDir, objectName+".meta")

	meta := ObjectMetadata{
		ContentType:    r.Header.Get("Content-Type"), // Or determine from first part / user input
		ContentLength:  totalSize,
		ETag:           strings.Trim(finalETag, "\""), // Store unquoted ETag
		CustomMetadata: make(map[string]string),       // TODO: Handle x-amz-meta-* headers from initiate or complete?
		LastModified:   time.Now().UTC(),
		StoragePath:    finalObjectPath,
	}
	// Populate custom metadata if any were passed during Initiate (not implemented yet)

	metaJSONOutput, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		log.Printf("Error marshalling final object metadata for %s/%s: %v", bucketName, objectName, err)
		// Object assembled, but metadata failed. Critical error.
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(objectMetadataPath, metaJSONOutput, 0644); err != nil {
		log.Printf("Error writing final object metadata file %s: %v", objectMetadataPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
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
		http.Error(w, "NoSuchUpload", http.StatusNotFound)
		return
	}

	// Delete the multipart upload metadata file
	if err := os.Remove(mpUploadMetaPath); err != nil && !os.IsNotExist(err) {
		log.Printf("Error deleting multipart upload metadata file %s: %v", mpUploadMetaPath, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Delete the temporary parts directory
	if err := os.RemoveAll(partsDir); err != nil && !os.IsNotExist(err) {
		log.Printf("Error deleting temporary parts directory %s: %v", partsDir, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent) // S3 spec: 204 No Content for successful abort
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
	// Ensure 'host' header is always part of signed headers if not explicitly passed (it usually is)
	// For SigV4, the actual header names in signedHeaderNames must match what's in the request.
	// The list of signed headers comes from the Authorization header itself.

	// Create a map for quick lookup of signed headers
	signedHeadersMap := make(map[string]bool)
	for _, h := range signedHeaderNames {
		signedHeadersMap[strings.ToLower(h)] = true
	}

	var actualSignedHeadersForOutput []string // Store the headers that were actually found and signed
	var headerPairs [][2]string

	for name, values := range r.Header {
		lowerName := strings.ToLower(name)
		if signedHeadersMap[lowerName] {
			// S3: Multiple headers with the same name should be joined by a comma (after stripping whitespace)
			// However, http.Header canonicalizes to one entry with comma-separated values for most common headers.
			// For other headers, it might be a slice. We should handle this by joining.
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
	if xAmzContentSHA256 == streamingPayload {
		// Streaming payload is more complex and not handled in this basic version
		log.Println("Warning: STREAMING-AWS4-HMAC-SHA256-PAYLOAD is not fully supported for hashing in this version.")
		return streamingPayload, nil, nil // Or handle as an error
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

// TODO: Implement XML response structures
// TODO: Implement metadata storage (e.g., in a .metadata hidden folder within the bucket)
// TODO: Implement ETag calculation
// TODO: Implement ACLs/Policy (stubbed for now)
// TODO: Implement robust authentication (HMAC)
