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
	"mime"
	"net/http"
	"slices"
)

// AllowedContentTypes lists every content-type the service accepts.
// Customer-support attachments cluster around screenshots (PNG/JPG),
// PDFs, Word docs, plain text, and CSVs. Anything else needs an
// explicit add here -- the deny-by-default posture is intentional.
//
// We list both `text/plain; charset=utf-8` and the parameter-less
// `text/plain` because http.DetectContentType usually returns the
// charset-qualified form but some inputs hit the bare one.
var AllowedContentTypes = []string{
	"application/pdf",
	"application/msword",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"image/png",
	"image/jpeg",
	"image/gif",
	"image/webp",
	"text/plain",
	"text/plain; charset=utf-8",
	"text/csv",
	"text/csv; charset=utf-8",
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
	// Compare claim against detected on the bare media-type only;
	// charset / boundary parameters drift between browsers, CLIs and
	// stdlib's detector, and equality there isn't load-bearing for
	// security -- the magic-byte sniff already pinned the format.
	if claim != "" && !mediaTypeMatches(claim, detected) {
		return detected, ErrContentTypeMismatch
	}
	return detected, nil
}

// mediaTypeMatches strips ;charset=...; etc. and compares the
// type/subtype only. Unparseable inputs fall back to exact equality.
func mediaTypeMatches(claim, detected string) bool {
	c, _, cerr := mime.ParseMediaType(claim)
	d, _, derr := mime.ParseMediaType(detected)
	if cerr != nil || derr != nil {
		return claim == detected
	}
	return c == d
}
