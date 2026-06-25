// Package cty parses AD1C cty.dat and resolves a callsign to its DXCC entity
// (name + primary prefix + continent). It is shared by the HorstReporter backend
// (to label DX-cluster spots with country + flag) and is independent of QRZ.
//
// cty.dat has no ISO/flag data; the primary-prefix → ISO-3166 mapping lives in
// iso.go (used to render a country flag).
package cty

import (
	"bufio"
	"io"
	"strings"
)

// Entity is a DXCC entity.
type Entity struct {
	Name          string
	PrimaryPrefix string
	Continent     string
}

// Resolver resolves callsigns to DXCC entities. Exact-callsign overrides (the
// "=CALL" entries) win; otherwise the longest matching prefix wins.
type Resolver struct {
	exact    map[string]Entity
	prefixes map[string]Entity
	maxPfx   int
}

// Resolve returns the entity for a callsign (and its ISO-3166 alpha-2 code, ""
// if the entity has no national flag), or ok=false if unknown.
func (r *Resolver) Resolve(call string) (ent Entity, iso string, ok bool) {
	if r == nil {
		return Entity{}, "", false
	}
	cs := strings.ToUpper(strings.TrimSpace(call))
	if cs == "" {
		return Entity{}, "", false
	}
	if e, found := r.exact[cs]; found {
		return e, prefixISO[e.PrimaryPrefix], true
	}
	hi := r.maxPfx
	if hi > len(cs) {
		hi = len(cs)
	}
	for i := hi; i >= 1; i-- {
		if e, found := r.prefixes[cs[:i]]; found {
			return e, prefixISO[e.PrimaryPrefix], true
		}
	}
	return Entity{}, "", false
}

// Parse reads AD1C cty.dat. Header line: 8 colon-separated fields ending with the
// primary prefix; a comma-separated prefix list (possibly across continuation
// lines) follows, terminated by ';'. Per-prefix override annotations
// ((cq),[itu],<lat/lon>,{cont},~tz~) are stripped; '=' marks an exact callsign.
func Parse(r io.Reader) (*Resolver, error) {
	res := &Resolver{exact: map[string]Entity{}, prefixes: map[string]Entity{}}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var header []string
	var prefixBuf strings.Builder
	inPrefixes := false

	flush := func() {
		if len(header) < 8 {
			return
		}
		ent := Entity{
			Name:          strings.TrimSpace(header[0]),
			Continent:     strings.TrimSpace(header[3]),
			PrimaryPrefix: strings.TrimPrefix(strings.TrimSpace(header[7]), "*"), // AD1C marks some with '*'
		}
		for _, raw := range strings.Split(prefixBuf.String(), ",") {
			tok := strings.TrimSpace(raw)
			if tok == "" {
				continue
			}
			exact := false
			if strings.HasPrefix(tok, "=") {
				exact = true
				tok = tok[1:]
			}
			tok = stripOverrides(tok)
			if tok == "" {
				continue
			}
			tok = strings.ToUpper(tok)
			if exact {
				res.exact[tok] = ent
			} else {
				res.prefixes[tok] = ent
				if len(tok) > res.maxPfx {
					res.maxPfx = len(tok)
				}
			}
		}
		header = nil
		prefixBuf.Reset()
		inPrefixes = false
	}

	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !inPrefixes && len(line) > 0 && line[0] != ' ' && line[0] != '\t' && strings.Contains(line, ":") {
			flush()
			header = strings.SplitN(line, ":", 9)
			if len(header) >= 9 {
				prefixBuf.WriteString(header[8])
			}
			inPrefixes = true
		} else if inPrefixes {
			prefixBuf.WriteString(line)
		}
		if strings.Contains(trimmed, ";") {
			flush()
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

func stripOverrides(tok string) string {
	tok = strings.TrimSuffix(strings.TrimSpace(tok), ";")
	if i := strings.IndexAny(tok, "([<{~"); i >= 0 {
		tok = tok[:i]
	}
	return strings.TrimSpace(tok)
}
