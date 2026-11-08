package clientgen

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// FileDrift is a generated file that differs from what generation produces.
type FileDrift struct {
	File string `json:"file"`
	// Status is "missing" or "differs".
	Status string `json:"status"`
	// Line is the first line of the file on disk that differs (compared
	// after Normalize); 0 when missing.
	Line int `json:"line,omitempty"`
	// OnDiskVersion is the melange version in the file's header, "" when
	// it has none.
	OnDiskVersion string `json:"on_disk_version,omitempty"`
}

// CompareFiles compares the generated files want, by name, with the files of
// the same names in dir, after Normalize. Other files in dir are ignored. The
// result is sorted by file name.
func CompareFiles(want map[string][]byte, dir string) ([]FileDrift, error) {
	var out []FileDrift
	for _, name := range slices.Sorted(maps.Keys(want)) {
		path := filepath.Join(dir, name)
		got, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			out = append(out, FileDrift{File: name, Status: "missing"})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		if line := firstDifference(Normalize(want[name]), Normalize(got)); line > 0 {
			out = append(out, FileDrift{File: name, Status: "differs", Line: onDiskLine(got, line), OnDiskVersion: HeaderVersion(got)})
		}
	}
	return out, nil
}

// Summary describes the drift without the file name: "missing", or where
// the file first differs.
func (f FileDrift) Summary() string {
	if f.Status == "missing" {
		return "missing"
	}
	return fmt.Sprintf("differs (first difference at line %d)", f.Line)
}

// firstDifference returns the 1-based line at which a and b first differ,
// or 0 when they are equal.
func firstDifference(a, b []byte) int {
	if bytes.Equal(a, b) {
		return 0
	}
	al, bl := bytes.Split(a, []byte("\n")), bytes.Split(b, []byte("\n"))
	shared := min(len(al), len(bl))
	for i := range shared {
		if !bytes.Equal(al[i], bl[i]) {
			return i + 1
		}
	}
	return shared + 1
}

// traceLines are the header lines that record how a file was generated
// rather than what it holds, in the Go (//) and TypeScript (/** */) headers.
var traceLines = []string{"// melange version: ", "// source: ", " * melange version: ", " * source: "}

// Normalize returns content as compared with a fresh generation: CRLF line
// endings converted to LF, the melange version and source lines of the
// leading comment block dropped (they change with the melange release and
// the directory generation ran from), and trailing whitespace at the end
// removed.
func Normalize(content []byte) []byte {
	lines, _ := keptLines(content)
	return bytes.TrimRight([]byte(strings.Join(lines, "")), " \t\r\n")
}

// keptLines returns the lines of content Normalize keeps, each with its
// line ending, and the 1-based line of content each comes from.
func keptLines(content []byte) (lines []string, origin []int) {
	content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	header := true
	for i, line := range strings.SplitAfter(string(content), "\n") {
		text := strings.TrimSuffix(line, "\n")
		if header && !isCommentLine(text) {
			header = false
		}
		if header && slices.ContainsFunc(traceLines, func(p string) bool { return strings.HasPrefix(text, p) }) {
			continue
		}
		lines = append(lines, line)
		origin = append(origin, i+1)
	}
	return lines, origin
}

// onDiskLine maps line n of Normalize(content) to its line in content, so a
// reported difference points into the file as written, header lines
// included.
func onDiskLine(content []byte, n int) int {
	_, origin := keptLines(content)
	switch {
	case n <= len(origin):
		return origin[n-1]
	case len(origin) > 0:
		return origin[len(origin)-1] + 1
	}
	return n
}

// isCommentLine reports whether line belongs to a leading // or /** */
// comment block.
func isCommentLine(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*")
}

// HeaderVersion returns the melange version a generated file's header
// records, or "".
func HeaderVersion(content []byte) string {
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !isCommentLine(line) {
			return ""
		}
		for _, p := range []string{"// melange version: ", " * melange version: "} {
			if v, ok := strings.CutPrefix(line, p); ok {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}
