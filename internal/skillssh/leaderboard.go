package skillssh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// Leaderboard views.
const (
	ViewAllTime  = ""
	ViewTrending = "trending"
	ViewHot      = "hot"
)

// Leaderboard returns a ranked view as skills.sh renders it. The public JSON API
// for leaderboards requires a Vercel token, but the leaderboard pages are public
// (robots.txt allows them) and embed the same rows for the client to hydrate, so
// this reads them from the page.
//
// ponytail: depends on the page embedding an "initialSkills" array - upgrade to
// the /api/v1/skills?view= endpoint if skills.sh offers it without a Vercel token.
func (c *Client) Leaderboard(ctx context.Context, view string) ([]Skill, error) {
	switch view {
	case ViewAllTime, ViewTrending, ViewHot:
	default:
		return nil, fmt.Errorf("unknown leaderboard view %q", view)
	}
	page, err := c.get(ctx, "/"+view, maxHTMLBody)
	if err != nil {
		return nil, err
	}
	raw, err := ParseLeaderboard(page)
	if err != nil {
		return nil, fmt.Errorf("reading the skills.sh %s page: %w", viewName(view), err)
	}
	out := make([]Skill, 0, len(raw))
	for _, s := range raw {
		if s, ok := sanitize(s); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

func viewName(view string) string {
	if view == "" {
		return "all-time"
	}
	return view
}

// flightChunk matches one React Server Components payload chunk in the page:
// self.__next_f.push([1,"<JSON string literal>"]).
var flightChunk = regexp.MustCompile(`self\.__next_f\.push\(\[1,("(?:[^"\\]|\\.)*")\]\)`)

// maxArrayTries bounds how many "initialSkills" arrays are tried. A real page
// has one or two.
const maxArrayTries = 16

var errNoRows = errors.New("no leaderboard rows found (the page format may have changed)")

// ParseLeaderboard extracts the leaderboard rows from a skills.sh page. It
// decodes the page's payload chunks, joins them, and decodes the longest
// "initialSkills" array. The rows are returned as-is; callers validate them.
func ParseLeaderboard(page []byte) ([]Skill, error) {
	var payload bytes.Buffer
	for _, m := range flightChunk.FindAllSubmatch(page, -1) {
		var s string
		if err := json.Unmarshal(m[1], &s); err != nil {
			continue // not a plain string chunk; skip it
		}
		payload.WriteString(s)
	}
	data := payload.Bytes()
	key := []byte(`"initialSkills":`)
	var best []Skill
	// Each attempt can scan the rest of the payload, so bound the attempts and
	// skip past an array once decoded; otherwise nested arrays cost quadratic time.
	for i, tries := 0, 0; tries < maxArrayTries; tries++ {
		j := bytes.Index(data[i:], key)
		if j < 0 {
			break
		}
		start := i + j + len(key)
		dec := json.NewDecoder(bytes.NewReader(data[start:]))
		var rows []Skill
		if err := dec.Decode(&rows); err == nil {
			if len(rows) > len(best) {
				best = rows
			}
			i = start + int(dec.InputOffset())
		} else {
			i = start
		}
	}
	if len(best) == 0 {
		return nil, errNoRows
	}
	return best, nil
}
