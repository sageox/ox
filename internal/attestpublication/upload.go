package attestpublication

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/sageox/ox/internal/fileutil"
	"golang.org/x/sync/errgroup"
)

const (
	partSize    = int64(16 * 1024 * 1024)
	partWorkers = 4
)

type CompletedPart struct {
	Number int32  `json:"number"`
	ETag   string `json:"etag"`
	Size   int64  `json:"size"`
}
type Multipart interface {
	Create(context.Context, Grant) (string, error)
	List(context.Context, Grant, string) ([]CompletedPart, error)
	Upload(context.Context, Grant, string, int32, io.Reader, int64) (CompletedPart, error)
	Complete(context.Context, Grant, string, []CompletedPart) error
	Completed(context.Context, Grant) (bool, error)
}

// Journal intentionally contains only the immutable binding and multipart
// bookkeeping. STS credentials are short-lived secrets and must never survive
// the process that received them.
type Journal struct {
	state *publicationState

	Version         int             `json:"version"`
	RunID           string          `json:"run_id"`
	SourceRunID     string          `json:"source_run_id"`
	ArchivePath     string          `json:"archive_path"`
	Binding         Binding         `json:"binding"`
	UploadID        string          `json:"upload_id"`
	Parts           []CompletedPart `json:"parts"`
	IntentExpiresAt time.Time       `json:"intent_expires_at"`
	Completed       bool            `json:"completed"`
	IdempotencyKey  string          `json:"idempotency_key"`
}

func LoadJournal(path string) (Journal, error) { return loadJournal(path, nil) }

func loadJournal(path string, state *publicationState) (Journal, error) {
	var raw []byte
	var err error
	if state == nil {
		raw, err = os.ReadFile(path)
	} else {
		raw, err = state.root.ReadFile(filepath.Base(path))
	}
	if err != nil {
		return Journal{}, err
	}
	journal := Journal{state: state}
	if err := json.Unmarshal(raw, &journal); err != nil {
		return Journal{}, err
	}
	if journal.Version != 1 {
		return Journal{}, fmt.Errorf("unsupported attest upload journal version %d", journal.Version)
	}
	return journal, nil
}
func SaveJournal(path string, journal Journal) error {
	if journal.state != nil {
		return journal.state.save(path, journal)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Use the shared exclusive-temp writer; predictable .tmp names can be symlinks.
	return fileutil.AtomicWriteJSON(path, journal, 0o600)
}

func NewJournal(run Run, archive Package) (Journal, error) {
	journal, err := NewPendingJournal(archive)
	if err != nil {
		return Journal{}, err
	}
	return journal.Bind(run)
}

// NewPendingJournal persists the create idempotency key before the first HTTP
// attempt. A process loss can then retry the same create without a global key
// collision between principals publishing identical archive bytes.
func NewPendingJournal(archive Package) (Journal, error) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return Journal{}, fmt.Errorf("generate create idempotency key: %w", err)
	}
	return Journal{Version: 1, SourceRunID: archive.Run.SourceRunID, ArchivePath: archive.ArchivePath,
		Binding:        Binding{ManifestSHA256: archive.ManifestSHA256, ArchiveSHA256: archive.ArchiveSHA256, ArchiveSizeBytes: archive.ArchiveSizeBytes},
		IdempotencyKey: hex.EncodeToString(key)}, nil
}

func (journal Journal) Bind(run Run) (Journal, error) {
	if run.Upload == nil {
		return Journal{}, fmt.Errorf("run %s has no upload grant", run.RunID)
	}
	if journal.Binding != run.Upload.Binding {
		return Journal{}, fmt.Errorf("server upload grant does not match frozen package binding")
	}
	journal.RunID = run.RunID
	journal.IntentExpiresAt = run.Upload.IntentExpiresAt
	return journal, nil
}

func (journal Journal) Matches(archive Package) error {
	if journal.SourceRunID != archive.Run.SourceRunID || journal.Binding != (Binding{ManifestSHA256: archive.ManifestSHA256, ArchiveSHA256: archive.ArchiveSHA256, ArchiveSizeBytes: archive.ArchiveSizeBytes}) {
		return fmt.Errorf("frozen package changed; refusing to reuse upload journal")
	}
	return nil
}

func UploadArchive(ctx context.Context, transfer Multipart, grant Grant, journalPath string, journal *Journal) error {
	if journal.Completed {
		return nil
	}
	if err := expired(journal.IntentExpiresAt); err != nil {
		return err
	}
	if journal.UploadID == "" {
		id, err := transfer.Create(ctx, grant)
		if err != nil {
			return err
		}
		journal.UploadID = id
		if err := SaveJournal(journalPath, *journal); err != nil {
			return err
		}
	}
	remote, err := transfer.List(ctx, grant, journal.UploadID)
	if err != nil {
		if isS3Code(err, "NoSuchUpload") {
			if completed, probeErr := transfer.Completed(ctx, grant); probeErr == nil && completed {
				journal.Completed = true
				return SaveJournal(journalPath, *journal)
			}
		}
		return err
	}
	journal.Parts = reconcileParts(journal.Parts, remote)
	if err := SaveJournal(journalPath, *journal); err != nil {
		return err
	}
	var file *os.File
	if journal.state != nil {
		file = journal.state.archive
	} else {
		file, err = os.Open(journal.ArchivePath)
		if err != nil {
			return err
		}
		defer file.Close()
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != journal.Binding.ArchiveSizeBytes {
		return fmt.Errorf("archive changed after journal was created")
	}
	missing := missingParts(info.Size(), journal.Parts)
	if err := uploadMissing(ctx, transfer, grant, file, missing, journal, journalPath); err != nil {
		return err
	}
	parts := append([]CompletedPart(nil), journal.Parts...)
	slices.SortFunc(parts, func(a, b CompletedPart) int { return int(a.Number - b.Number) })
	if err := transfer.Complete(ctx, grant, journal.UploadID, parts); err != nil {
		if isS3Code(err, "NoSuchUpload", "PreconditionFailed") {
			if completed, probeErr := transfer.Completed(ctx, grant); probeErr == nil && completed {
				journal.Completed = true
				return SaveJournal(journalPath, *journal)
			}
		}
		return err
	}
	journal.Completed = true
	return SaveJournal(journalPath, *journal)
}

type missingPart struct {
	number       int32
	offset, size int64
}

func missingParts(size int64, completed []CompletedPart) []missingPart {
	have := map[int32]bool{}
	for _, part := range completed {
		have[part.Number] = true
	}
	var result []missingPart
	for number, offset := int32(1), int64(0); offset < size; number, offset = number+1, offset+partSize {
		if have[number] {
			continue
		}
		n := partSize
		if size-offset < n {
			n = size - offset
		}
		result = append(result, missingPart{number, offset, n})
	}
	return result
}
func uploadMissing(ctx context.Context, transfer Multipart, grant Grant, file *os.File, work []missingPart, journal *Journal, journalPath string) error {
	group, uploadCtx := errgroup.WithContext(ctx)
	jobs := make(chan missingPart)
	var journalMu sync.Mutex
	group.Go(func() error {
		defer close(jobs)
		for _, part := range work {
			select {
			case jobs <- part:
			case <-uploadCtx.Done():
				return uploadCtx.Err()
			}
		}
		return nil
	})
	for range partWorkers {
		group.Go(func() error {
			for {
				select {
				case <-uploadCtx.Done():
					return uploadCtx.Err()
				case part, ok := <-jobs:
					if !ok {
						return nil
					}
					reader := io.NewSectionReader(file, part.offset, part.size)
					completed, err := transfer.Upload(uploadCtx, grant, journal.UploadID, part.number, reader, part.size)
					if err != nil {
						return err
					}
					journalMu.Lock()
					journal.Parts = reconcileParts(journal.Parts, []CompletedPart{completed})
					err = SaveJournal(journalPath, *journal)
					journalMu.Unlock()
					if err != nil {
						return err
					}
				}
			}
		})
	}
	// Wait includes the producer and every reader. A credential retry cannot start
	// (and the caller cannot close the archive) until the old attempt has stopped.
	return group.Wait()
}

func reconcileParts(local, remote []CompletedPart) []CompletedPart {
	values := map[int32]CompletedPart{}
	for _, part := range local {
		values[part.Number] = part
	}
	for _, part := range remote {
		values[part.Number] = part
	}
	result := make([]CompletedPart, 0, len(values))
	for _, part := range values {
		result = append(result, part)
	}
	slices.SortFunc(result, func(a, b CompletedPart) int { return int(a.Number - b.Number) })
	return result
}
func expired(deadline time.Time) error {
	if deadline.IsZero() || time.Now().After(deadline) {
		return fmt.Errorf("attest upload intent expired; package identity is immutable and needs a new source run")
	}
	return nil
}

type S3Multipart struct{}

func s3Client(ctx context.Context, grant Grant, options ...func(*s3.Options)) (*s3.Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(grant.Region), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(grant.Credentials.AccessKeyID, grant.Credentials.SecretAccessKey, grant.Credentials.SessionToken)))
	if err != nil {
		return nil, err
	}
	options = append([]func(*s3.Options){s3AttemptClientOption}, options...)
	return s3.NewFromConfig(cfg, options...), nil
}
func (S3Multipart) Create(ctx context.Context, grant Grant) (string, error) {
	client, err := s3Client(ctx, grant)
	if err != nil {
		return "", err
	}
	out, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(grant.Bucket), Key: aws.String(grant.Key)})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.UploadId), nil
}
func (S3Multipart) List(ctx context.Context, grant Grant, uploadID string) ([]CompletedPart, error) {
	client, err := s3Client(ctx, grant)
	if err != nil {
		return nil, err
	}
	var result []CompletedPart
	var marker *string
	for {
		out, err := client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(grant.Bucket), Key: aws.String(grant.Key), UploadId: aws.String(uploadID), PartNumberMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, part := range out.Parts {
			result = append(result, CompletedPart{Number: aws.ToInt32(part.PartNumber), ETag: aws.ToString(part.ETag), Size: aws.ToInt64(part.Size)})
		}
		if !aws.ToBool(out.IsTruncated) {
			return result, nil
		}
		marker = out.NextPartNumberMarker
	}
}
func (S3Multipart) Upload(ctx context.Context, grant Grant, uploadID string, number int32, body io.Reader, size int64) (CompletedPart, error) {
	client, err := s3Client(ctx, grant)
	if err != nil {
		return CompletedPart{}, err
	}
	out, err := client.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(grant.Bucket), Key: aws.String(grant.Key), UploadId: aws.String(uploadID), PartNumber: aws.Int32(number), Body: body, ContentLength: aws.Int64(size)})
	if err != nil {
		return CompletedPart{}, err
	}
	return CompletedPart{Number: number, ETag: aws.ToString(out.ETag), Size: size}, nil
}
func (S3Multipart) Complete(ctx context.Context, grant Grant, uploadID string, parts []CompletedPart) error {
	client, err := s3Client(ctx, grant)
	if err != nil {
		return err
	}
	completed := make([]types.CompletedPart, 0, len(parts))
	for _, part := range parts {
		completed = append(completed, types.CompletedPart{PartNumber: aws.Int32(part.Number), ETag: aws.String(part.ETag)})
	}
	_, err = client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String(grant.Bucket), Key: aws.String(grant.Key), UploadId: aws.String(uploadID), IfNoneMatch: aws.String("*"), MpuObjectSize: aws.Int64(grant.Binding.ArchiveSizeBytes), MultipartUpload: &types.CompletedMultipartUpload{Parts: completed}})
	return err
}

func (S3Multipart) Completed(ctx context.Context, grant Grant) (bool, error) {
	client, err := s3Client(ctx, grant)
	if err != nil {
		return false, err
	}
	out, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(grant.Bucket), Key: aws.String(grant.Key)})
	if err != nil {
		if isS3Code(err, "NoSuchKey", "NotFound") {
			return false, nil
		}
		return false, err
	}
	if aws.ToInt64(out.ContentLength) != grant.Binding.ArchiveSizeBytes {
		return false, fmt.Errorf("completed upload size does not match immutable archive binding")
	}
	// Size is only a recovery signal. The worker remains the authority for the
	// archive SHA-256 before it makes any source or evidence visible.
	return true, nil
}

func isS3Code(err error, codes ...string) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return slices.Contains(codes, apiErr.ErrorCode())
}
