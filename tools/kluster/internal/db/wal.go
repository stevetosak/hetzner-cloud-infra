package db

import (
	"context"
	"path"
	"strconv"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/r2"
)

// walFile is one object under <generation>/wals/: a segment
// (TTTTTTTTXXXXXXXXYYYYYYYY, the timeline and the position) or a timeline
// history file (TTTTTTTT.history). barman adds a compression suffix.
type walFile struct {
	Key      string
	Timeline uint32
	// Pos is the segment's position, 16 upper-case hex digits; it sorts as
	// a string. Empty for a history file.
	Pos     string
	History bool
	// Partial is the last, unfinished segment of a timeline a promotion
	// left behind; it is not a segment of the new timeline.
	Partial bool
}

func isHex(s string) bool {
	_, err := strconv.ParseUint(s, 16, 64)
	return err == nil
}

// parseWAL reads a key; ok is false for anything else under wals/ (a
// .backup label, a marker).
func parseWAL(key string) (walFile, bool) {
	name, rest, _ := strings.Cut(path.Base(key), ".")
	f := walFile{Key: key}
	switch {
	case len(name) == 8 && isHex(name) && strings.HasPrefix(rest, "history"):
		f.History = true
	case len(name) == 24 && isHex(name) && !strings.Contains(rest, "backup"):
		f.Pos = strings.ToUpper(name[8:])
		f.Partial = strings.Contains(rest, "partial")
	default:
		return walFile{}, false
	}
	tl, _ := strconv.ParseUint(name[:8], 16, 32)
	f.Timeline = uint32(tl)
	return f, true
}

// wals is what one generation's wals/ holds.
type wals struct {
	segments  []walFile // full segments, partials left out
	histories []uint32
}

func readWALs(ctx context.Context, b r2.Bucket, g Generation) (wals, error) {
	objs, err := b.List(ctx, g.Prefix()+"wals/")
	if err != nil {
		return wals{}, err
	}
	var w wals
	for _, o := range objs {
		f, ok := parseWAL(o.Key)
		switch {
		case !ok || f.Partial:
		case f.History:
			w.histories = append(w.histories, f.Timeline)
		default:
			w.segments = append(w.segments, f)
		}
	}
	return w, nil
}

// newest is the segment at the highest position.
func (w wals) newest() (walFile, bool) {
	var n walFile
	for _, s := range w.segments {
		if s.Pos > n.Pos {
			n = s
		}
	}
	return n, n.Pos != ""
}

// latestTimeline is the highest timeline a history file opens, or 1.
func (w wals) latestTimeline() uint32 {
	tl := uint32(1)
	for _, h := range w.histories {
		tl = max(tl, h)
	}
	return tl
}

// lowestTimeline is the timeline of the first segment this generation
// archived: the one its Cluster started on.
func (w wals) lowestTimeline() (uint32, bool) {
	var tl uint32
	for _, s := range w.segments {
		if tl == 0 || s.Timeline < tl {
			tl = s.Timeline
		}
	}
	return tl, tl != 0
}

// firstOn is the lowest segment on timeline tl.
func (w wals) firstOn(tl uint32) (walFile, bool) {
	var f walFile
	for _, s := range w.segments {
		if s.Timeline == tl && (f.Pos == "" || s.Pos < f.Pos) {
			f = s
		}
	}
	return f, f.Pos != ""
}
