package document

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// ClamAVScanner streams bytes to a clamd daemon over INSTREAM and
// returns the verdict.
//
// We speak the wire protocol directly (zero deps) — the message is:
//
//	zINSTREAM\0
//	<chunk_len uint32 BE><chunk bytes>...
//	<0 uint32 BE>      // terminator
//
// clamd replies with `stream: OK\0` or `stream: <SIG> FOUND\0`. Per
// §7 ("Virus scanning pipeline"), max chunk is 25 MiB; the daemon
// limits to StreamMaxLength which we don't need to enforce client-side.
type ClamAVScanner struct {
	Addr           string        // e.g. "clamav:3310"
	DialTimeout    time.Duration // default 5s
	OperationTimeout time.Duration // overall scan deadline; default 60s
}

// Verdict captures one scan result.
type Verdict struct {
	Clean     bool
	Signature string // populated when !Clean
	Raw       string // raw clamd line for debugging
}

// ErrNoClamAV is returned by Scan when Addr is empty. Useful in tests.
var ErrNoClamAV = errors.New("document: clamav not configured")

// Scan streams r into clamd. Caller is responsible for capping r at the
// server's StreamMaxLength; ClamAV will close the connection rather than
// return an error if that's exceeded.
func (s *ClamAVScanner) Scan(ctx context.Context, r io.Reader) (Verdict, error) {
	if s.Addr == "" {
		return Verdict{}, ErrNoClamAV
	}

	dialT := s.DialTimeout
	if dialT == 0 {
		dialT = 5 * time.Second
	}
	opT := s.OperationTimeout
	if opT == 0 {
		opT = 60 * time.Second
	}

	d := &net.Dialer{Timeout: dialT}
	conn, err := d.DialContext(ctx, "tcp", s.Addr)
	if err != nil {
		return Verdict{}, fmt.Errorf("document: clamav dial: %w", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(opT)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return Verdict{}, fmt.Errorf("document: clamav write header: %w", err)
	}

	buf := make([]byte, 64*1024)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			var hdr [4]byte
			binary.BigEndian.PutUint32(hdr[:], uint32(n))
			if _, err := conn.Write(hdr[:]); err != nil {
				return Verdict{}, fmt.Errorf("document: clamav write chunk header: %w", err)
			}
			if _, err := conn.Write(buf[:n]); err != nil {
				return Verdict{}, fmt.Errorf("document: clamav write chunk: %w", err)
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return Verdict{}, fmt.Errorf("document: read upload stream: %w", rerr)
		}
	}
	// Zero-length chunk terminates INSTREAM.
	var term [4]byte
	if _, err := conn.Write(term[:]); err != nil {
		return Verdict{}, fmt.Errorf("document: clamav write terminator: %w", err)
	}

	reply, err := bufio.NewReader(conn).ReadString(0)
	if err != nil {
		return Verdict{}, fmt.Errorf("document: clamav read reply: %w", err)
	}
	return parseVerdict(reply), nil
}

// parseVerdict turns clamd's "stream: <result>" line into a Verdict.
// Examples:
//
//	"stream: OK\x00"
//	"stream: Win.Test.EICAR_HDB-1 FOUND\x00"
//	"stream: clamd is not running\x00" (anomalous)
func parseVerdict(line string) Verdict {
	v := Verdict{Raw: strings.TrimRight(line, "\x00\n ")}
	switch {
	case strings.HasSuffix(v.Raw, " OK"):
		v.Clean = true
	case strings.HasSuffix(v.Raw, " FOUND"):
		// "stream: <sig> FOUND" -> extract <sig>
		body := strings.TrimPrefix(v.Raw, "stream: ")
		v.Signature = strings.TrimSuffix(body, " FOUND")
	default:
		// Treat unparseable replies as not-clean to err on the safe side.
	}
	return v
}
