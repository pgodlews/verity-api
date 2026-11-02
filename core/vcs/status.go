package vcs

import (
	"bytes"
	"fmt"
	"strings"
)

// Entry is one changed path from `git status --porcelain=v2 -z`.
// Path and RenamedFrom are relative to the repository root, using forward
// slashes as git prints them. Index and Worktree are the two XY letters
// (".", "M", "A", "D", "R", "C", "T", "U"). Untracked and Conflicted are
// set for `?` and `u` records. Submodule marks gitlinks and dirty submodules.
// Ignored paths are omitted.
type Entry struct {
	Path        string
	Index       string
	Worktree    string
	Untracked   bool
	RenamedFrom string
	Conflicted  bool
	Submodule   bool
}

// Snapshot is a read-only status. Ahead and Behind come from the local
// upstream ref (porcelain `# branch.ab`); nothing is fetched. Short is the
// first 7 hex digits of the commit when there is one. Branch is empty when
// Detached is set.
//
// The type is not named Status: that identifier is the function below, and
// Go does not allow a type and a function to share a name.
type Snapshot struct {
	Root         string
	Branch       string
	Detached     bool
	OID          string
	Short        string
	Upstream     string
	Ahead        int
	Behind       int
	Tracking     bool
	UpstreamGone bool
	Merging      bool
	Rebasing     bool
	Initial      bool
	Entries      []Entry
}

// Status runs `git status --porcelain=v2 --branch -z` in dir.
// A directory that is not a repository returns *NotRepositoryError.
func Status(dir string) (*Snapshot, error) {
	if !Available() {
		return nil, ErrUnavailable
	}
	root, err := RepoRoot(dir)
	if err != nil {
		return nil, err
	}
	out, err := git(dir, "status", "--porcelain=v2", "--branch", "-z")
	if err != nil {
		if notRepository(err) {
			return nil, &NotRepositoryError{Dir: dir}
		}
		return nil, err
	}
	st, err := parseStatus(out)
	if err != nil {
		return nil, err
	}
	st.Root = root
	annotateUpstream(dir, st)
	st.Merging, _ = mergeHeadExists(dir)
	st.Rebasing = rebaseInProgress(dir)
	return st, nil
}

// annotateUpstream clears ahead/behind when the configured upstream ref does
// not resolve. Porcelain still prints branch.upstream in that case, which
// would otherwise look like ↑0 ↓0.
func annotateUpstream(dir string, st *Snapshot) {
	if st == nil || st.Detached || st.Upstream == "" || st.Initial {
		return
	}
	_, ok, err := SymbolicFullName(dir, "@{u}")
	if err != nil || ok {
		return
	}
	st.UpstreamGone = true
	st.Tracking = false
	st.Ahead = 0
	st.Behind = 0
}

// parseStatus reads porcelain v2 -z output. Records are split only on NUL.
// A rename's original path is the next record, even when a path contains
// tabs, CR, or LF. Those bytes are filename data. Headers are accepted
// NUL-terminated, or as a newline block in front of the first NUL, which is
// how some git versions mix the two. Header lines may drop a trailing CR;
// file records are never trimmed.
func parseStatus(out []byte) (*Snapshot, error) {
	st := &Snapshot{}
	recs := splitStatusRecords(out)
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		if rec == "" {
			continue
		}
		if strings.HasPrefix(rec, "#") {
			if strings.Contains(rec, "\n") {
				for _, line := range strings.Split(rec, "\n") {
					line = strings.TrimRight(line, "\r")
					if line == "" {
						continue
					}
					if strings.HasPrefix(line, "#") {
						applyHeader(st, line)
						continue
					}
					if err := addFile(st, line, ""); err != nil {
						return nil, err
					}
				}
				continue
			}
			applyHeader(st, strings.TrimRight(rec, "\r"))
			continue
		}
		orig := ""
		if strings.HasPrefix(rec, "2 ") {
			if i+1 >= len(recs) {
				return nil, fmt.Errorf("git status rename record is missing its original path")
			}
			i++
			orig = recs[i]
			if orig == "" {
				return nil, fmt.Errorf("git status rename record is missing its original path")
			}
		}
		if err := addFile(st, rec, orig); err != nil {
			return nil, err
		}
	}
	st.Short = shortOID(st.OID)
	return st, nil
}

// splitStatusRecords divides porcelain -z output on NUL only. A terminator
// after the last record is dropped; empty records in the middle are kept so
// a later step can tell a missing rename path from a short read.
// Path lists use splitNULPaths, which drops every empty record.
func splitStatusRecords(out []byte) []string {
	if len(out) == 0 {
		return nil
	}
	parts := bytes.Split(out, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	recs := make([]string, len(parts))
	for i, p := range parts {
		recs[i] = string(p)
	}
	return recs
}

func applyHeader(st *Snapshot, line string) {
	line = strings.TrimPrefix(line, "# ")
	key, val, ok := strings.Cut(line, " ")
	if !ok {
		return
	}
	switch key {
	case "branch.oid":
		if val == "(initial)" {
			st.Initial = true
			return
		}
		st.OID = val
	case "branch.head":
		if val == "(detached)" {
			st.Detached = true
			st.Branch = ""
			return
		}
		st.Branch = val
	case "branch.upstream":
		st.Upstream = val
		st.Tracking = true
	case "branch.ab":
		st.Tracking = true
		fmt.Sscanf(val, "+%d -%d", &st.Ahead, &st.Behind)
	}
}

func addFile(st *Snapshot, rec, orig string) error {
	switch {
	case strings.HasPrefix(rec, "1 "):
		e, err := parseXY(rec, 9)
		if err != nil {
			return err
		}
		st.Entries = append(st.Entries, e)
	case strings.HasPrefix(rec, "2 "):
		e, err := parseXY(rec, 10)
		if err != nil {
			return err
		}
		e.RenamedFrom = orig
		st.Entries = append(st.Entries, e)
	case strings.HasPrefix(rec, "u "):
		e, err := parseXY(rec, 11)
		if err != nil {
			return err
		}
		e.Conflicted = true
		st.Entries = append(st.Entries, e)
	case strings.HasPrefix(rec, "? "):
		st.Entries = append(st.Entries, Entry{Path: strings.TrimPrefix(rec, "? "), Untracked: true})
	case strings.HasPrefix(rec, "! ") || rec == "!":
		// Ignored paths are not changes.
	default:
		return fmt.Errorf("unrecognized git status record %q", truncate(rec, 120))
	}
	return nil
}

// parseXY splits a porcelain v2 file record. n is the number of space-separated
// fields including the path, which may itself contain spaces.
func parseXY(rec string, n int) (Entry, error) {
	fields := strings.SplitN(rec, " ", n)
	if len(fields) < n {
		return Entry{}, fmt.Errorf("short git status record %q", truncate(rec, 120))
	}
	xy := fields[1]
	if len(xy) != 2 {
		return Entry{}, fmt.Errorf("git status XY field %q", xy)
	}
	path := fields[n-1]
	return Entry{
		Path:       path,
		Index:      xy[:1],
		Worktree:   xy[1:],
		Submodule:  strings.HasPrefix(fields[2], "S") || fields[3] == "160000" || fields[4] == "160000",
		Conflicted: strings.ContainsRune(xy, 'U'),
	}, nil
}

func shortOID(oid string) string {
	if len(oid) < 7 {
		return ""
	}
	for _, r := range oid {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return ""
		}
	}
	return oid[:7]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
