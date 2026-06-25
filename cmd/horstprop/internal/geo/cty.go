package geo

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// Entity is a DXCC entity resolved from a callsign via cty.dat.
type Entity struct {
	Name          string
	PrimaryPrefix string
	Continent     string
	CQ            int
	ITU           int
	Loc           LatLon // entity centroid, East-positive longitude
}

// CtyResolver resolves callsigns to DXCC entities using AD1C cty.dat data.
// Exact-callsign overrides (the "=CALL" entries) win over prefix matches; among
// prefix matches the longest prefix wins.
type CtyResolver struct {
	exact    map[string]Entity
	prefixes map[string]Entity
	maxPfx   int
}

// Resolve returns the entity for a callsign, or ok=false if unknown.
func (c *CtyResolver) Resolve(call string) (Entity, bool) {
	if c == nil {
		return Entity{}, false
	}
	cs := strings.ToUpper(strings.TrimSpace(call))
	if cs == "" {
		return Entity{}, false
	}
	if e, ok := c.exact[cs]; ok {
		return e, true
	}
	// longest-prefix match
	hi := c.maxPfx
	if hi > len(cs) {
		hi = len(cs)
	}
	for i := hi; i >= 1; i-- {
		if e, ok := c.prefixes[cs[:i]]; ok {
			return e, true
		}
	}
	return Entity{}, false
}

// ParseCty parses AD1C cty.dat. The format is a header line of 8 colon-separated
// fields followed by a comma-separated prefix list terminated by ';'. Longitude
// in cty.dat is WEST-positive; we negate it to East-positive. Per-prefix
// override annotations ((cq), [itu], <lat/lon>, {cont}, ~tz~) are stripped; an
// "=" marks an exact callsign.
func ParseCty(r io.Reader) (*CtyResolver, error) {
	res := &CtyResolver{exact: map[string]Entity{}, prefixes: map[string]Entity{}}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var header []string // accumulates header fields for the current record
	var prefixBuf strings.Builder
	inPrefixes := false

	flush := func() {
		if len(header) < 8 {
			return
		}
		ent := Entity{
			Name:          strings.TrimSpace(header[0]),
			Continent:     strings.TrimSpace(header[3]),
			PrimaryPrefix: strings.TrimSpace(header[7]),
		}
		ent.CQ, _ = strconv.Atoi(strings.TrimSpace(header[1]))
		ent.ITU, _ = strconv.Atoi(strings.TrimSpace(header[2]))
		lat, _ := strconv.ParseFloat(strings.TrimSpace(header[4]), 64)
		lonWest, _ := strconv.ParseFloat(strings.TrimSpace(header[5]), 64)
		ent.Loc = LatLon{Lat: lat, Lon: -lonWest}

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
			// header line
			flush() // close any prior record defensively
			header = strings.SplitN(line, ":", 9)
			if len(header) >= 9 {
				prefixBuf.WriteString(header[8]) // prefixes may begin on the same line
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

// stripOverrides removes AD1C per-prefix annotations and the trailing ';'.
func stripOverrides(tok string) string {
	tok = strings.TrimSuffix(strings.TrimSpace(tok), ";")
	if i := strings.IndexAny(tok, "([<{~"); i >= 0 {
		tok = tok[:i]
	}
	return strings.TrimSpace(tok)
}
