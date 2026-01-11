package sqlgen

import (
	"fmt"
	"strings"
)

// SQLBuilder builds SQL with automatic indentation management.
// Use this for complex multi-line SQL construction where managing
// indentation manually would be error-prone.
type SQLBuilder struct {
	lines     []string
	indent    int
	indentStr string
}

// NewBuilder creates a new SQLBuilder with 4-space indentation.
func NewBuilder() *SQLBuilder {
	return &SQLBuilder{
		indentStr: "    ",
	}
}

// NewBuilderWith creates a new SQLBuilder with custom indentation.
func NewBuilderWith(indentStr string) *SQLBuilder {
	return &SQLBuilder{
		indentStr: indentStr,
	}
}

// Line adds a line at the current indentation level.
func (b *SQLBuilder) Line(format string, args ...any) *SQLBuilder {
	line := fmt.Sprintf(format, args...)
	if b.indent > 0 && line != "" {
		line = strings.Repeat(b.indentStr, b.indent) + line
	}
	b.lines = append(b.lines, line)
	return b
}

// LineIf adds a line only if the condition is true.
func (b *SQLBuilder) LineIf(cond bool, format string, args ...any) *SQLBuilder {
	if cond {
		return b.Line(format, args...)
	}
	return b
}

// Raw adds a raw string without any indentation modification.
// Useful for multi-line strings that have their own formatting.
func (b *SQLBuilder) Raw(s string) *SQLBuilder {
	if s != "" {
		b.lines = append(b.lines, s)
	}
	return b
}

// Indent increases the indentation level.
func (b *SQLBuilder) Indent() *SQLBuilder {
	b.indent++
	return b
}

// Dedent decreases the indentation level.
func (b *SQLBuilder) Dedent() *SQLBuilder {
	if b.indent > 0 {
		b.indent--
	}
	return b
}

// Block executes a function with increased indentation.
// Automatically handles indent/dedent around the callback.
func (b *SQLBuilder) Block(fn func(*SQLBuilder)) *SQLBuilder {
	b.Indent()
	fn(b)
	b.Dedent()
	return b
}

// Lines adds multiple lines at the current indentation level.
func (b *SQLBuilder) Lines(lines ...string) *SQLBuilder {
	for _, line := range lines {
		b.Line("%s", line)
	}
	return b
}

// Empty returns true if no lines have been added.
func (b *SQLBuilder) Empty() bool {
	return len(b.lines) == 0
}

// String returns the built SQL as a single string.
func (b *SQLBuilder) String() string {
	return strings.Join(b.lines, "\n")
}

// Joiner accumulates clauses and joins them with a separator,
// automatically filtering out empty strings.
type Joiner struct {
	sep   string
	parts []string
}

// NewJoiner creates a Joiner with the given separator.
func NewJoiner(sep string) *Joiner {
	return &Joiner{sep: sep}
}

// Add adds a part to the joiner if it's non-empty.
func (j *Joiner) Add(parts ...string) *Joiner {
	for _, p := range parts {
		if p != "" {
			j.parts = append(j.parts, p)
		}
	}
	return j
}

// AddIf adds a part only if the condition is true.
func (j *Joiner) AddIf(cond bool, part string) *Joiner {
	if cond && part != "" {
		j.parts = append(j.parts, part)
	}
	return j
}

// AddFunc adds the result of a function if non-empty.
func (j *Joiner) AddFunc(fn func() string) *Joiner {
	if result := fn(); result != "" {
		j.parts = append(j.parts, result)
	}
	return j
}

// Empty returns true if no parts have been added.
func (j *Joiner) Empty() bool {
	return len(j.parts) == 0
}

// String joins all non-empty parts with the separator.
func (j *Joiner) String() string {
	return strings.Join(j.parts, j.sep)
}

// Count returns the number of parts.
func (j *Joiner) Count() int {
	return len(j.parts)
}
