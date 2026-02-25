package lfs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/sageox/ox/internal/useragent"
)

// ComputeOID computes the SHA256 hex digest of content (the LFS OID).
func ComputeOID(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

// UploadResult tracks the outcome of a single upload.
type UploadResult struct {
	OID   string
	Error error
}

// UploadObject uploads a single blob using the action href from the batch response.
func UploadObject(action *Action, content []byte) error {
	if action == nil || action.Href == "" {
		return fmt.Errorf("no upload action provided")
	}

	client := &http.Client{Timeout: 5 * time.Minute}

	req, err := http.NewRequest("PUT", action.Href, nil)
	if err != nil {
		return fmt.Errorf("create upload request: %w", err)
	}

	// only User-Agent for external Git host; no X-Orchestrator
	req.Header.Set("User-Agent", useragent.String())

	// set headers from action
	for k, v := range action.Header {
		req.Header.Set(k, v)
	}

	req.Body = io.NopCloser(io.NewSectionReader(newBytesReaderAt(content), 0, int64(len(content))))
	req.ContentLength = int64(len(content))

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// DownloadObject downloads a single blob using the action href.
func DownloadObject(action *Action) ([]byte, error) {
	if action == nil || action.Href == "" {
		return nil, fmt.Errorf("no download action provided")
	}

	client := &http.Client{Timeout: 5 * time.Minute}

	req, err := http.NewRequest("GET", action.Href, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}

	// only User-Agent for external Git host; no X-Orchestrator
	req.Header.Set("User-Agent", useragent.String())

	// set headers from action
	for k, v := range action.Header {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("download returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read download body: %w", err)
	}

	return data, nil
}

// UploadAll uploads multiple blobs in parallel.
// files maps OID -> content. Uses objects from the batch response to find upload actions.
func UploadAll(resp *BatchResponse, files map[string][]byte, maxConcurrent int) []UploadResult {
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}

	var results []UploadResult
	var mu sync.Mutex
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	for _, obj := range resp.Objects {
		if obj.Error != nil {
			mu.Lock()
			results = append(results, UploadResult{
				OID:   obj.OID,
				Error: fmt.Errorf("server error %d: %s", obj.Error.Code, obj.Error.Message),
			})
			mu.Unlock()
			continue
		}

		if obj.Actions == nil || obj.Actions.Upload == nil {
			// object already exists on server (no upload needed)
			mu.Lock()
			results = append(results, UploadResult{OID: obj.OID})
			mu.Unlock()
			continue
		}

		content, ok := files[obj.OID]
		if !ok {
			mu.Lock()
			results = append(results, UploadResult{
				OID:   obj.OID,
				Error: fmt.Errorf("no content for OID %s", obj.OID),
			})
			mu.Unlock()
			continue
		}

		wg.Add(1)
		sem <- struct{}{} // acquire semaphore

		go func(obj BatchResponseObject, content []byte) {
			defer wg.Done()
			defer func() { <-sem }() // release semaphore

			err := UploadObject(obj.Actions.Upload, content)

			mu.Lock()
			results = append(results, UploadResult{OID: obj.OID, Error: err})
			mu.Unlock()
		}(obj, content)
	}

	wg.Wait()
	return results
}

// DownloadResult tracks the outcome of a single download.
type DownloadResult struct {
	OID     string
	Content []byte
	Error   error
}

// DownloadAll downloads multiple blobs in parallel.
// Returns results for every object so callers can see all errors, not just the first.
func DownloadAll(resp *BatchResponse, maxConcurrent int) []DownloadResult {
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}

	var results []DownloadResult
	var mu sync.Mutex
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	for _, obj := range resp.Objects {
		if obj.Error != nil {
			mu.Lock()
			results = append(results, DownloadResult{
				OID:   obj.OID,
				Error: fmt.Errorf("server error %d: %s", obj.Error.Code, obj.Error.Message),
			})
			mu.Unlock()
			continue
		}

		if obj.Actions == nil || obj.Actions.Download == nil {
			mu.Lock()
			results = append(results, DownloadResult{
				OID:   obj.OID,
				Error: fmt.Errorf("no download action for OID %s", obj.OID),
			})
			mu.Unlock()
			continue
		}

		wg.Add(1)
		sem <- struct{}{}

		go func(obj BatchResponseObject) {
			defer wg.Done()
			defer func() { <-sem }()

			data, err := DownloadObject(obj.Actions.Download)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				results = append(results, DownloadResult{
					OID:   obj.OID,
					Error: fmt.Errorf("download %s: %w", obj.OID, err),
				})
				return
			}
			results = append(results, DownloadResult{OID: obj.OID, Content: data})
		}(obj)
	}

	wg.Wait()
	return results
}

// bytesReaderAt wraps a byte slice to implement io.ReaderAt.
type bytesReaderAt struct {
	data []byte
}

func newBytesReaderAt(data []byte) *bytesReaderAt {
	return &bytesReaderAt{data: data}
}

func (r *bytesReaderAt) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n = copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
