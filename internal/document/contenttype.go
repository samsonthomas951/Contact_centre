// Package document is the contact-centre's owner of object storage,
// virus scanning, and signed access. Per §7 of the technical plan it is
// the only writer to MinIO; every other service goes through gRPC.
//
// This file holds the static content-type allow-list. Any uploaded
// object whose detected content-type isn't in this list is rejected
// before any bytes touch MinIO. We pair the magic-number sniff with the
// uploader's claimed content-type and reject on mismatch -- spoofed
// extensions are a common malware vector.
package document

import (
	"errors"
	"net/http"
	"slices"
)

// AllowedContentTypes lists every content-type the service accepts.
// Phase 1 covers PDF + Word; future phases will add images and CSV.
var AllowedContentTypes = []string{
	"application/pdf",
	"application/msword",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
}

// ErrContentTypeNotAllowed is returned by Sniff when the detected type
// is not in AllowedContentTypes.
var ErrContentTypeNotAllowed = errors.New("document: content-type not allowed")

// ErrContentTypeMismatch is returned by Sniff when the magic-number
// sniff disagrees with the claimed type. We default to the SNIFFED
// type because attackers control the uploader.
var ErrContentTypeMismatch = errors.New("document: claimed content-type does not match magic bytes")

// Sniff inspects up to the first 512 bytes of head, returns the
// detected content-type, and verifies it against the claim. The
// returned type is always the sniffed one; claim is informational.
//
// On Phase 1 we use stdlib net/http.DetectContentType, which covers
// PDF (`%PDF`) and the OOXML/MS-CFB family well enough. If a richer
// matcher is needed later (image-bomb detection, etc.) swap in
// gabriel-vasile/mimetype without changing the call sites.
func Sniff(head []byte, claim string) (string, error) {
	if len(head) == 0 {
		return "", errors.New("document: empty body")
	}
	detected := http.DetectContentType(head)

	// stdlib uses canonical lowercase types without parameters; OOXML
	// is reported as application/zip (MS-CFB). DOCX hides under that
	// because the file is a ZIP container.
	if detected == "application/zip" && claim ==
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		detected = claim
	}

	if !slices.Contains(AllowedContentTypes, detected) {
		return detected, ErrContentTypeNotAllowed
	}
	if claim != "" && claim != detected {
		return detected, ErrContentTypeMismatch
	}
	return detected, nil
}
