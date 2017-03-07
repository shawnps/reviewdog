package reviewdog

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/haya14busa/reviewdog/diff"
)

// Reviewdog represents review dog application which parses result of compiler
// or linter, get diff and filter the results by diff, and report filtered
// results.
type Reviewdog struct {
	toolname string
	p        Parser
	c        CommentService
	d        DiffService
}

// NewReviewdog returns a new Reviewdog.
func NewReviewdog(toolname string, p Parser, c CommentService, d DiffService) *Reviewdog {
	return &Reviewdog{p: p, c: c, d: d, toolname: toolname}
}

// CheckResult represents a checked result of static analysis tools.
// :h error-file-format
type CheckResult struct {
	Path    string   // relative file path
	Lnum    int      // line number
	Col     int      // column number (1 <tab> == 1 character column)
	Message string   // error message
	Lines   []string // Original error lines (often one line)
}

// Parser is an interface which parses compilers, linters, or any tools
// results.
type Parser interface {
	Parse(r io.Reader) ([]*CheckResult, error)
}

// Comment represents a reported result as a comment.
type Comment struct {
	*CheckResult
	Body     string
	LnumDiff int
	ToolName string
}

// CommentService is an interface which posts Comment.
 type CommentService interface {
	Post(context.Context, *Comment) error
}

// BulkCommentService posts comments all at once when Flash() is called.
// Flash() will be called at the end of reviewdog run.
type BulkCommentService interface {
	CommentService
	Flash(context.Context) error
}

// DiffService is an interface which get diff.
type DiffService interface {
	Diff(context.Context) ([]byte, error)
	Strip() int
}

// Run runs Reviewdog application.
func (w *Reviewdog) Run(ctx context.Context, r io.Reader) error {
	results, err := w.p.Parse(r)
	if err != nil {
		return fmt.Errorf("parse error: %v", err)
	}

	d, err := w.d.Diff(ctx)
	if err != nil {
		return fmt.Errorf("fail to get diff: %v", err)
	}

	filediffs, err := diff.ParseMultiFile(bytes.NewReader(d))
	if err != nil {
		return fmt.Errorf("fail to parse diff: %v", err)
	}
	addedlines := addedDiffLines(filediffs, w.d.Strip())

	wd, err := os.Getwd()
	if err != nil {
		return err
	}

	for _, result := range results {
		addedline := addedlines.Get(result.Path, result.Lnum)
		if filepath.IsAbs(result.Path) {
			relpath, err := filepath.Rel(wd, result.Path)
			if err != nil {
				return err
			}
			result.Path = relpath
		}
		result.Path = filepath.Clean(result.Path)
		if addedline != nil {
			comment := &Comment{
				CheckResult: result,
				Body:        result.Message, // TODO: format message
				LnumDiff:    addedline.LnumDiff,
				ToolName:    w.toolname,
			}
			if err := w.c.Post(ctx, comment); err != nil {
				return err
			}
		}
	}

	if bulk, ok := w.c.(BulkCommentService); ok {
		return bulk.Flash(ctx)
	}

	return nil
}

// AddedLine represents added line in diff.
type AddedLine struct {
	Path     string // path to new file
	Lnum     int    // the line number in the new file
	LnumDiff int    // the line number of the diff (Same as Lnumdiff of diff.Line)
	Content  string // line content
}

// posToAddedLine is a hash table of normalized path to line number to AddedLine.
type posToAddedLine map[string]map[int]*AddedLine

func (p posToAddedLine) Get(path string, lnum int) *AddedLine {
	npath, err := normalizePath(path)
	if err != nil {
		return nil
	}
	ltodiff, ok := p[npath]
	if !ok {
		return nil
	}
	diffline, ok := ltodiff[lnum]
	if !ok {
		return nil
	}
	return diffline
}

// addedDiffLines traverse []*diff.FileDiff and returns posToAddedLine.
func addedDiffLines(filediffs []*diff.FileDiff, strip int) posToAddedLine {
	r := make(posToAddedLine)
	for _, filediff := range filediffs {
		path := filediff.PathNew
		ltodiff := make(map[int]*AddedLine)
		if strip > 0 {
			ps := strings.Split(filepath.ToSlash(filediff.PathNew), "/")
			if len(ps) > strip {
				path = filepath.Join(ps[strip:]...)
			}
		}
		np, err := normalizePath(path)
		if err != nil {
			// FIXME(haya14busa): log or return error?
			continue
		}
		path = np

		for _, hunk := range filediff.Hunks {
			for _, line := range hunk.Lines {
				if line.Type == diff.LineAdded {
					ltodiff[line.LnumNew] = &AddedLine{
						Path:     path,
						Lnum:     line.LnumNew,
						LnumDiff: line.LnumDiff,
						Content:  line.Content,
					}
				}
			}
		}
		r[path] = ltodiff
	}
	return r
}

func normalizePath(p string) (string, error) {
	if !filepath.IsAbs(p) {
		path, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		p = path
	}
	return filepath.ToSlash(p), nil
}
