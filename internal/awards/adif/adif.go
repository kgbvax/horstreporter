// Package adif is a small, dependency-free streaming reader for ADIF logs. ADIF
// is a trivial length-delimited tag format (<NAME:LEN[:TYPE]>value … <EOR>), so
// we hand-roll a scanner rather than pull a third-party library — matching the
// repo's style of small hand-written format parsers (internal/cty).
//
// Parsing is streaming and bounded-memory: fields are read tag-by-tag from a
// buffered reader and emitted one Record at a time, so a 100k-QSO export never
// has to be held in memory at once.
package adif

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// Record is one QSO: ADIF field names (UPPERCASED) → values. Values are kept
// verbatim (length-delimited, so they may contain spaces/newlines).
type Record map[string]string

// Get returns the trimmed value for an uppercased field name.
func (r Record) Get(field string) string {
	return strings.TrimSpace(r[field])
}

// Parse scans ADIF from r, invoking emit once per <EOR> record. The optional
// header (everything up to <EOH>) is skipped. emit must not retain the Record
// across calls (it is reused-safe only if copied). Returns the first read error,
// or nil at clean EOF.
func Parse(r io.Reader, emit func(Record)) error {
	br := bufio.NewReaderSize(r, 64*1024)
	rec := Record{}
	hasFields := false
	for {
		// Advance to the next '<' (tag start), discarding inter-field text.
		if err := skipToTag(br); err != nil {
			if err == io.EOF {
				if hasFields {
					emit(rec) // tolerate a final record with no trailing <EOR>
				}
				return nil
			}
			return err
		}
		name, length, err := readTag(br)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch name {
		case "EOR":
			if hasFields {
				emit(rec)
			}
			rec = Record{}
			hasFields = false
			continue
		case "EOH":
			// End of header: discard anything accumulated as "header app fields".
			rec = Record{}
			hasFields = false
			continue
		}
		// Field tag: read exactly length bytes of value (length may be 0).
		val, err := readN(br, length)
		if err != nil && err != io.EOF {
			return err
		}
		rec[name] = val
		hasFields = true
		if err == io.EOF {
			emit(rec)
			return nil
		}
	}
}

// skipToTag consumes bytes until the next '<' (left in place is the byte AFTER
// '<', i.e. the tag interior begins). Returns io.EOF if no further tag exists.
func skipToTag(br *bufio.Reader) error {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return err
		}
		if b == '<' {
			return nil
		}
	}
}

// readTag reads a tag interior up to '>' and returns the UPPERCASED field name
// and value length. Control tags (EOR/EOH) have length 0. The leading '<' has
// already been consumed by skipToTag.
func readTag(br *bufio.Reader) (name string, length int, err error) {
	var sb strings.Builder
	for {
		b, e := br.ReadByte()
		if e != nil {
			return "", 0, e
		}
		if b == '>' {
			break
		}
		sb.WriteByte(b)
	}
	parts := strings.Split(sb.String(), ":")
	name = strings.ToUpper(strings.TrimSpace(parts[0]))
	if len(parts) >= 2 {
		// parts[1] = length; parts[2] (optional) = type indicator, ignored.
		n, e := strconv.Atoi(strings.TrimSpace(parts[1]))
		if e == nil && n >= 0 {
			length = n
		}
	}
	return name, length, nil
}

// readN reads exactly n bytes (the field value). It may return a short value with
// io.EOF if the input is truncated.
func readN(br *bufio.Reader, n int) (string, error) {
	if n == 0 {
		return "", nil
	}
	buf := make([]byte, n)
	got, err := io.ReadFull(br, buf)
	if err == io.ErrUnexpectedEOF {
		return string(buf[:got]), io.EOF
	}
	if err != nil {
		return string(buf[:got]), err
	}
	return string(buf), nil
}
