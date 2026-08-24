package lfs

import "fmt"

// UploadedRef is evidence that a FileRef's content is present in the LFS store.
//
// Its whole reason to exist is a compile-time invariant: WritePointerFile /
// WritePointerFiles take an UploadedRef, not a bare FileRef, so a caller holding
// only a hash (NewFileRef's output — sha256 + size, no I/O) CANNOT write a
// pointer. The only ways to obtain an UploadedRef are:
//
//  1. UploadBlob / UploadSessionFiles — actually talked to the Batch API and got
//     a success response, so the blob is provably in the store; or
//  2. AssertUploaded / AssertUploadedManifest — the explicitly-named, audited
//     escape hatch for a ref whose blob a PRIOR run already uploaded (a persisted
//     meta.json manifest being re-pointerized after a successful push).
//
// This closes the GH #810 bug class by construction, for the plan path, the
// session path, and every future caller: minting a pointer for a blob that was
// never uploaded is no longer expressible. The field is unexported precisely so
// no code outside this package can fabricate the proof.
type UploadedRef struct {
	ref FileRef
}

// Ref returns the underlying FileRef (OID + size + storage).
func (u UploadedRef) Ref() FileRef { return u.ref }

// BareOID returns the underlying ref's hex digest without the "sha256:" prefix.
func (u UploadedRef) BareOID() string { return u.ref.BareOID() }

// AssertUploaded wraps a FileRef whose blob is ALREADY in the LFS store into the
// proof type WritePointerFile requires. Use ONLY where a prior successful upload
// is proven — a persisted meta.json manifest re-pointerized after its push
// succeeded, or an import of blobs already confirmed present. NEVER call it on a
// fresh NewFileRef whose content was not uploaded: that is exactly the #810 bug,
// and the whole point of the type is to make that call stand out in review.
func AssertUploaded(ref FileRef) UploadedRef { return UploadedRef{ref: ref} }

// AssertUploadedManifest is the bulk form of AssertUploaded for a persisted
// filename->FileRef manifest (meta.Files). Same contract: every entry's blob
// MUST already be in the store. Grep for AssertUploaded to audit every site that
// asserts (rather than proves) upload.
func AssertUploadedManifest(files map[string]FileRef) map[string]UploadedRef {
	out := make(map[string]UploadedRef, len(files))
	for name, ref := range files {
		out[name] = UploadedRef{ref: ref}
	}
	return out
}

// UploadBlob uploads a single content blob to the LFS store and returns proof of
// upload. It is the single-object counterpart to UploadSessionFiles, sharing the
// same upload-then-return-proof core: hash, request an upload action via the
// Batch API, PUT the bytes, and surface any per-object failure. Only a nil error
// yields a usable UploadedRef.
//
// A blob the server reports as already present (UploadAll skips it) is a success
// — it is in the store, which is all UploadedRef attests.
func UploadBlob(client *Client, content []byte) (UploadedRef, error) {
	ref := NewFileRef(content)
	resp, err := client.BatchUpload([]BatchObject{{OID: ref.BareOID(), Size: ref.Size}})
	if err != nil {
		return UploadedRef{}, fmt.Errorf("LFS batch upload: %w", err)
	}
	for _, r := range UploadAll(resp, map[string][]byte{ref.BareOID(): content}, 1) {
		if r.Error != nil {
			return UploadedRef{}, fmt.Errorf("LFS upload OID %s: %w", r.OID, r.Error)
		}
	}
	return UploadedRef{ref: ref}, nil
}
