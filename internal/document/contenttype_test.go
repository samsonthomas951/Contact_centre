package document

import (
	"errors"
	"testing"
)

// pdfHead is the minimal "%PDF-" magic the stdlib detector recognises.
var pdfHead = []byte("%PDF-1.7\n%mock pdf body just to clear the sniff")

// docxHead mimics a ZIP container's local-file-header magic that DOCX
// uses (PK\x03\x04). DetectContentType returns application/zip.
var docxHead = []byte{
	0x50, 0x4b, 0x03, 0x04, 0x14, 0x00, 0x06, 0x00,
	'M', 'O', 'C', 'K', '_', 'D', 'O', 'C',
}

func TestSniff_AllowedTypes(t *testing.T) {
	cases := []struct {
		name  string
		head  []byte
		claim string
		want  string
	}{
		{"pdf, no claim", pdfHead, "", "application/pdf"},
		{"pdf, matching claim", pdfHead, "application/pdf", "application/pdf"},
		{
			"docx, claim resolves zip-magic",
			docxHead,
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Sniff(tc.head, tc.claim)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestSniff_Rejects(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		claim string
		err  error
	}{
		{"empty body", nil, "application/pdf", errors.New("document: empty body")},
		{
			// PNG bytes claiming to be a PDF -- the detector returns
			// image/png (now allow-listed); the claim mismatch is the
			// rejection reason. (Before image/png was allow-listed
			// this surfaced as NotAllowed; now Mismatch wins.)
			"png pretending to be pdf",
			[]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
			"application/pdf",
			ErrContentTypeMismatch,
		},
		{
			"pdf with mismatched docx claim",
			pdfHead,
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			ErrContentTypeMismatch,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Sniff(tc.head, tc.claim)
			if err == nil {
				t.Fatal("want error")
			}
			if tc.err != nil && !errors.Is(tc.err, ErrContentTypeNotAllowed) && !errors.Is(tc.err, ErrContentTypeMismatch) {
				if err.Error() != tc.err.Error() {
					t.Errorf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if !errors.Is(err, tc.err) {
				t.Errorf("err = %v, want %v", err, tc.err)
			}
		})
	}
}
