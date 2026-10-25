package attestpublication

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/smithy-go"
)

type Publisher struct {
	Control  *ControlClient
	Transfer Multipart
	Sleep    func(time.Duration)
}
type PublishResult struct {
	Run         Run
	Package     Package
	JournalPath string
}

// Publish serializes the entire journal lifecycle across CLI processes.
func (publisher Publisher) Publish(ctx context.Context, repoID, journalPath string, archive Package) (PublishResult, error) {
	return withPublicationLock(ctx, journalPath, func(state *publicationState) (PublishResult, error) {
		root := state.root
		if filepath.Clean(filepath.Dir(archive.ArchivePath)) != filepath.Clean(filepath.Dir(journalPath)) {
			var err error
			root, err = openPublicationRoot(filepath.Dir(archive.ArchivePath))
			if err != nil {
				return PublishResult{}, err
			}
			defer root.Close()
		}
		var err error
		state.archive, err = root.Open(filepath.Base(archive.ArchivePath))
		if err != nil {
			return PublishResult{}, err
		}
		defer state.archive.Close()
		return publisher.publish(ctx, repoID, journalPath, archive, state)
	})
}

func (publisher Publisher) publish(ctx context.Context, repoID, journalPath string, archive Package, state *publicationState) (PublishResult, error) {
	if publisher.Control == nil {
		return PublishResult{}, errors.New("attest control client is required")
	}
	if publisher.Transfer == nil {
		publisher.Transfer = S3Multipart{}
	}
	journal, err := loadJournal(journalPath, state)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return PublishResult{}, fmt.Errorf("read attest upload journal: %w", err)
	}
	var run Run
	if err == nil {
		if err := journal.Matches(archive); err != nil {
			return PublishResult{}, err
		}
		if journal.RunID == "" {
			if journal.IdempotencyKey == "" {
				return PublishResult{}, errors.New("pending attest journal has no create idempotency key")
			}
			request := createRequest(archive)
			run, err = publisher.createWithRecovery(ctx, repoID, journal.IdempotencyKey, request)
			if err != nil {
				return PublishResult{}, err
			}
			if run.Upload == nil {
				journal.RunID, journal.Completed = run.RunID, true
				if err := SaveJournal(journalPath, journal); err != nil {
					return PublishResult{}, err
				}
				run, err = publisher.poll(ctx, repoID, run)
				return PublishResult{Run: run, Package: archive, JournalPath: journalPath}, err
			}
			journal, err = journal.Bind(run)
			if err != nil {
				return PublishResult{}, err
			}
			if err := SaveJournal(journalPath, journal); err != nil {
				return PublishResult{}, err
			}
		} else {
			if journal.Completed {
				run, err = publisher.Control.Get(ctx, repoID, journal.RunID)
				if err != nil {
					return PublishResult{}, err
				}
				if run.Status == "uploading" {
					// The multipart object is journaled before the control-plane
					// completion. A process loss in that gap must retry the idempotent
					// completion rather than polling an upload that cannot advance.
					run, err = publisher.completeAndPoll(ctx, repoID, journal.RunID)
				} else {
					run, err = publisher.poll(ctx, repoID, run)
				}
				return PublishResult{Run: run, Package: archive, JournalPath: journalPath}, err
			}
			run.RunID = journal.RunID
			run.SourceRunID = journal.SourceRunID
			run.Status = "uploading"
			grant, renewErr := publisher.Control.Renew(ctx, repoID, journal.RunID)
			if renewErr != nil {
				return PublishResult{}, renewErr
			}
			if err := validateRenewal(journal, grant); err != nil {
				return PublishResult{}, err
			}
			run.Upload = &grant
		}
	} else {
		journal, err = NewPendingJournal(archive)
		journal.state = state
		if err != nil {
			return PublishResult{}, err
		}
		if err = SaveJournal(journalPath, journal); err != nil {
			return PublishResult{}, err
		}
		run, err = publisher.createWithRecovery(ctx, repoID, journal.IdempotencyKey, createRequest(archive))
		if err != nil {
			return PublishResult{}, err
		}
		if run.Upload == nil {
			journal.RunID, journal.Completed = run.RunID, true
			if err := SaveJournal(journalPath, journal); err != nil {
				return PublishResult{}, err
			}
			run, err = publisher.poll(ctx, repoID, run)
			return PublishResult{Run: run, Package: archive, JournalPath: journalPath}, err
		}
		journal, err = journal.Bind(run)
		if err != nil {
			return PublishResult{}, err
		}
		if err = SaveJournal(journalPath, journal); err != nil {
			return PublishResult{}, err
		}
	}
	if run.Upload == nil {
		return PublishResult{Run: run, Package: archive, JournalPath: journalPath}, nil
	}
	if err := UploadArchive(ctx, publisher.Transfer, *run.Upload, journalPath, &journal); err != nil {
		if expiredCredentials(err) {
			grant, renewErr := publisher.Control.Renew(ctx, repoID, journal.RunID)
			if renewErr != nil {
				return PublishResult{}, renewErr
			}
			if err := validateRenewal(journal, grant); err != nil {
				return PublishResult{}, err
			}
			if retryErr := UploadArchive(ctx, publisher.Transfer, grant, journalPath, &journal); retryErr == nil {
				run, retryErr = publisher.completeAndPoll(ctx, repoID, journal.RunID)
				if retryErr != nil {
					return PublishResult{}, retryErr
				}
				return PublishResult{Run: run, Package: archive, JournalPath: journalPath}, nil
			} else {
				err = retryErr
			}
		}
		// A multipart completion can commit after its response is lost. The scoped
		// credentials cannot read staging, so only the API completion/status path
		// can distinguish that case from an unfinished upload without overwriting.
		if reconciled, completeErr := publisher.Control.Complete(ctx, repoID, journal.RunID); completeErr == nil {
			journal.Completed = true
			if saveErr := SaveJournal(journalPath, journal); saveErr != nil {
				return PublishResult{}, saveErr
			}
			reconciled, completeErr = publisher.poll(ctx, repoID, reconciled)
			return PublishResult{Run: reconciled, Package: archive, JournalPath: journalPath}, completeErr
		}
		return PublishResult{}, err
	}
	run, err = publisher.completeAndPoll(ctx, repoID, journal.RunID)
	if err != nil {
		return PublishResult{}, err
	}
	return PublishResult{Run: run, Package: archive, JournalPath: journalPath}, nil
}

func validateRenewal(journal Journal, grant Grant) error {
	if grant.Binding != journal.Binding || grant.IntentExpiresAt != journal.IntentExpiresAt {
		return errors.New("renewed upload grant changed immutable package binding or intent deadline")
	}
	return nil
}

func createRequest(archive Package) CreateRequest {
	return CreateRequest{SourceRunID: archive.Run.SourceRunID, CorpusKey: archive.Run.Corpus.Key, PackageVersion: packageVersion,
		ManifestSHA256: archive.ManifestSHA256, ArchiveSHA256: archive.ArchiveSHA256, ArchiveSizeBytes: archive.ArchiveSizeBytes}
}

func expiredCredentials(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "ExpiredToken", "ExpiredTokenException", "RequestExpired":
		return true
	default:
		return false
	}
}

func (publisher Publisher) createWithRecovery(ctx context.Context, repoID, key string, request CreateRequest) (Run, error) {
	var last error
	for attempt := 0; attempt != 3; attempt++ {
		run, err := publisher.Control.Create(ctx, repoID, key, request)
		if err == nil {
			return run, nil
		}
		last = err
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status != 503 {
			return Run{}, err
		}
		if err := ctx.Err(); err != nil {
			return Run{}, err
		}
		if publisher.Sleep != nil {
			publisher.Sleep(time.Second)
		} else {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return Run{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return Run{}, last
}
func (publisher Publisher) completeAndPoll(ctx context.Context, repoID, runID string) (Run, error) {
	run, err := publisher.Control.Complete(ctx, repoID, runID)
	if err != nil {
		return Run{}, err
	}
	return publisher.poll(ctx, repoID, run)
}

func (publisher Publisher) poll(ctx context.Context, repoID string, run Run) (Run, error) {
	for run.Status == "uploading" || run.Status == "processing" {
		wait := time.Second
		if publisher.Sleep != nil {
			publisher.Sleep(wait)
		} else {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return Run{}, ctx.Err()
			case <-timer.C:
			}
		}
		next, err := publisher.Control.Get(ctx, repoID, run.RunID)
		if err != nil {
			return Run{}, err
		}
		run = next
	}
	return run, nil
}
