package fleet

import (
	"path/filepath"
	"strings"
	"time"
)

// containerSessionDir is the fixed, user-agnostic mount point for an agent's
// host session-log dir. The launch wrapper writes container.log + status here,
// so it never needs the run user's home resolved.
const containerSessionDir = "/flotilla/session"

// logsRoot is the host dir holding per-session logs (default ~/.flotilla/logs).
func (f *Fleet) logsRoot() string {
	if f.LogRoot != "" {
		return f.LogRoot
	}
	return filepath.Join(homeDir(), ".flotilla", "logs")
}

// repoSlug turns a repo URL into a filesystem-safe "owner-repo" slug. It strips
// the scheme and a trailing .git, keeps the last two path segments, and replaces
// any char outside [A-Za-z0-9._-] with '-'. Falls back to "repo".
func repoSlug(repoURL string) string {
	s := strings.TrimSuffix(strings.TrimSpace(repoURL), ".git")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.ReplaceAll(s, ":", "/")
	var parts []string
	for _, p := range strings.Split(s, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	var tail []string
	switch {
	case len(parts) >= 2:
		tail = parts[len(parts)-2:]
	case len(parts) == 1:
		tail = parts
	default:
		return "repo"
	}
	slug := sanitizeSlug(strings.Join(tail, "-"))
	if slug == "" {
		return "repo"
	}
	return slug
}

func sanitizeSlug(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// sessionDirName builds "<YYYY-MM-DD-HHMM>-<agent>".
func sessionDirName(name string, t time.Time) string {
	return t.Format("2006-01-02-1504") + "-" + name
}

// transcriptTarget expands a profile's transcript_path against the run user's
// home (replacing a leading ~). Empty in → empty out (no transcript mount).
func transcriptTarget(transcriptPath, home string) string {
	p := strings.TrimSpace(transcriptPath)
	switch {
	case p == "":
		return ""
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return strings.TrimRight(home, "/") + "/" + p[2:]
	default:
		return p
	}
}
