package filesystem

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// EditSpec represents a single search-and-replace block.
type EditSpec struct {
	Search  string `json:"search"`
	Replace string `json:"replace"`
}

// EditFile performs surgical, verified search-and-replace edits on existing files.
type EditFile struct {
	policy sandbox.Policy

	// preCommitHook is an optional hook invoked right before concurrent
	// modification check and commit (for testing race conditions).
	preCommitHook func(path string)
}

// NewEditFile returns a ready-to-register EditFile tool.
func NewEditFile() *EditFile {
	return &EditFile{}
}

// NewEditFileWithPolicy returns an EditFile confined to policy.Workspace.
func NewEditFileWithPolicy(p sandbox.Policy) *EditFile {
	return &EditFile{policy: p}
}

func (EditFile) Name() string { return "edit_file" }

func (EditFile) Description() string {
	return "Surgically edit an existing text file by replacing exact code blocks. " +
		"Each edit replaces an exact, unique occurrence of 'search' with 'replace'. " +
		"Edits are applied sequentially in memory and committed atomically. " +
		"For Go files, syntax is verified before changes are written to disk. " +
		"Use edit_file to modify existing files; use write_file only for new files or intentional full rewrites."
}

func (EditFile) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to edit, absolute or relative to the workspace.",
			},
			"edits": map[string]any{
				"type":        "array",
				"description": "Sequential list of search and replace operations to apply atomically.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"search": map[string]any{
							"type":        "string",
							"description": "Exact text to find and replace. Must match uniquely in the file. Cannot be empty.",
						},
						"replace": map[string]any{
							"type":        "string",
							"description": "Replacement text. Can be empty to delete the matched text.",
						},
					},
					"required": []string{"search", "replace"},
				},
			},
		},
		"required": []string{"path", "edits"},
	}
}

// CheckBoundary implements tools.BoundaryChecker: it dry-runs the
// workspace boundary decision for an edit (canonicalize + resolve, no
// writes, nothing created) and returns the canonical path the edit
// would target.
func (e EditFile) CheckBoundary(args map[string]any) (string, error) {
	path, err := tools.StringArg(args, "path")
	if err != nil {
		return "", err
	}
	return sandbox.EnsureWithinWorkspace(e.policy.Workspace, path)
}

func parseEdits(args map[string]any) ([]EditSpec, error) {
	raw, ok := args["edits"]
	if !ok {
		return nil, &tools.ArgumentError{Field: "edits", Reason: "is required"}
	}

	switch v := raw.(type) {
	case []EditSpec:
		if len(v) == 0 {
			return nil, &tools.ArgumentError{Field: "edits", Reason: "must contain at least one edit"}
		}
		for i, ed := range v {
			if ed.Search == "" {
				return nil, &tools.ArgumentError{Field: fmt.Sprintf("edits[%d].search", i), Reason: "cannot be empty"}
			}
		}
		return v, nil

	case []any:
		if len(v) == 0 {
			return nil, &tools.ArgumentError{Field: "edits", Reason: "must contain at least one edit"}
		}
		edits := make([]EditSpec, len(v))
		for i, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, &tools.ArgumentError{Field: fmt.Sprintf("edits[%d]", i), Reason: "must be an object with 'search' and 'replace' fields"}
			}
			searchVal, ok := m["search"]
			if !ok {
				return nil, &tools.ArgumentError{Field: fmt.Sprintf("edits[%d].search", i), Reason: "is required"}
			}
			searchStr, ok := searchVal.(string)
			if !ok {
				return nil, &tools.ArgumentError{Field: fmt.Sprintf("edits[%d].search", i), Reason: "must be a string"}
			}
			if searchStr == "" {
				return nil, &tools.ArgumentError{Field: fmt.Sprintf("edits[%d].search", i), Reason: "cannot be empty"}
			}

			replaceVal, ok := m["replace"]
			if !ok {
				return nil, &tools.ArgumentError{Field: fmt.Sprintf("edits[%d].replace", i), Reason: "is required"}
			}
			replaceStr, ok := replaceVal.(string)
			if !ok {
				return nil, &tools.ArgumentError{Field: fmt.Sprintf("edits[%d].replace", i), Reason: "must be a string"}
			}

			edits[i] = EditSpec{Search: searchStr, Replace: replaceStr}
		}
		return edits, nil

	default:
		return nil, &tools.ArgumentError{Field: "edits", Reason: "must be an array"}
	}
}

func (e EditFile) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	path, err := tools.StringArg(args, "path")
	if err != nil {
		return tools.Result{}, err
	}
	edits, err := parseEdits(args)
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	if err := sandbox.CheckCtx(ctx); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: %v", path, err)}, nil
	}

	// Refuse symlinks up front before resolving links.
	wsDir := e.policy.Workspace
	if strings.TrimSpace(wsDir) == "" {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			wsDir = wd
		}
	}
	checkPath := path
	if !filepath.IsAbs(checkPath) {
		checkPath = filepath.Join(wsDir, checkPath)
	}
	if info, err := os.Lstat(checkPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: path is a symlink", path)}, nil
		}
	}

	// Workspace confinement: resolve inside the root before touching the file.
	resolved, err := sandbox.ResolveWithinWorkspace(e.policy.Workspace, path)
	if err != nil {
		if strings.Contains(err.Error(), "escapes") || strings.Contains(err.Error(), "outside") {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: path escapes the authorized workspace", path)}, nil
		}
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: file does not exist; use write_file to create new files", path)}, nil
	}

	// Double-check resolved path is not a symlink.
	if info, err := os.Lstat(resolved); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: path is a symlink", path)}, nil
		}
	} else if os.IsNotExist(err) {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: file does not exist; use write_file to create new files", path)}, nil
	} else {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: %v", path, err)}, nil
	}

	// Hardened open for reading.
	f, err := sandbox.OpenNoFollowRead(resolved)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: %v", path, err)}, nil
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: %v", path, err)}, nil
	}
	if err := sandbox.AssertRegular(info); err != nil {
		f.Close()
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: %v", path, err)}, nil
	}
	if err := sandbox.AssertWriteLinkCount(info); err != nil {
		f.Close()
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: %v", path, err)}, nil
	}

	// Record initial metadata for concurrency detection and permission preservation.
	originalPerm := info.Mode().Perm()
	originalModTime := info.ModTime()
	originalSize := info.Size()

	if err := sandbox.AssertReadSize(info, tools.DefaultReadMaxBytes); err != nil {
		f.Close()
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: %v", path, err)}, nil
	}

	rawBytes, err := sandbox.ReadCapped(ctx, f, tools.DefaultReadMaxBytes)
	f.Close()
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: %v", path, err)}, nil
	}

	originalContent := string(rawBytes)
	currentBuffer := originalContent
	fileEnding := detectLineEnding(originalContent)

	// Apply edits sequentially in memory.
	for i, edit := range edits {
		search := edit.Search
		replace := edit.Replace

		count := strings.Count(currentBuffer, search)
		if count == 0 {
			// Try line-ending normalized search if line endings differ (e.g. CRLF in file vs LF in search).
			searchNorm := normalizeLineEndings(search, fileEnding)
			if searchNorm != search {
				normCount := strings.Count(currentBuffer, searchNorm)
				if normCount == 1 {
					search = searchNorm
					replace = normalizeLineEndings(replace, fileEnding)
					count = 1
				} else if normCount > 1 {
					count = normCount
					search = searchNorm
				}
			}
		}

		if count == 0 {
			// Conservative indentation-aware fallback: only attempted when exact matching
			// returns zero occurrences. Normalizes leading indentation only, requiring exactly
			// one unambiguous match.
			newBuf, matchCount, matchedLines, err := applyIndentationAwareEdit(currentBuffer, search, replace, fileEnding)
			if err == nil && matchCount == 1 {
				currentBuffer = newBuf
				continue
			}
			if matchCount > 1 {
				lineList := formatLineNumbers(matchedLines)
				return tools.Result{
					IsError: true,
					Content: fmt.Sprintf("edit %d of %d failed: search block matched %d times in %s (at lines %s).\nThe search block must be unique. Please include more surrounding context lines in 'search' to uniquely identify the replacement site.", i+1, len(edits), matchCount, path, lineList),
				}, nil
			}

			hint := findNearMatchHint(currentBuffer, edit.Search)
			return tools.Result{
				IsError: true,
				Content: fmt.Sprintf("edit %d of %d failed: search block not found in %s.%s\nEnsure the search text matches the existing code exactly, including leading spaces, tabs, and indentation.", i+1, len(edits), path, hint),
			}, nil
		}

		if count > 1 {
			lines := findMatchLineNumbers(currentBuffer, search)
			lineList := formatLineNumbers(lines)
			return tools.Result{
				IsError: true,
				Content: fmt.Sprintf("edit %d of %d failed: search block matched %d times in %s (at lines %s).\nThe search block must be unique. Please include more surrounding context lines in 'search' to uniquely identify the replacement site.", i+1, len(edits), count, path, lineList),
			}, nil
		}

		currentBuffer = strings.Replace(currentBuffer, search, replace, 1)
	}

	// Syntax validation before committing.
	if err := validateSyntax(resolved, []byte(currentBuffer)); err != nil {
		return tools.Result{
			IsError: true,
			Content: fmt.Sprintf("cannot edit %s: syntax validation failed: %v\nChanges were rejected to prevent corrupting the file. Please review the syntax and try again.", path, err),
		}, nil
	}

	// Optional test hook before commit.
	if e.preCommitHook != nil {
		e.preCommitHook(resolved)
	}

	// Concurrent modification check: re-stat the file to ensure no other process altered it.
	postStat, err := os.Stat(resolved)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: %v", path, err)}, nil
	}
	if !postStat.ModTime().Equal(originalModTime) || postStat.Size() != originalSize {
		return tools.Result{
			IsError: true,
			Content: fmt.Sprintf("cannot edit %s: file was modified concurrently by another process since it was read; please re-read the file before editing", path),
		}, nil
	}

	if err := sandbox.CheckCtx(ctx); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: %v", path, err)}, nil
	}

	// Atomic commit via temporary file in the same directory.
	dir := filepath.Dir(resolved)
	tmpFile, err := os.CreateTemp(dir, ".edit_file_tmp_*")
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: failed to create temporary file: %v", path, err)}, nil
	}
	tmpPath := tmpFile.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if err := sandbox.WriteCapped(ctx, tmpFile, []byte(currentBuffer)); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: write error: %v", path, err)}, nil
	}
	if err := tmpFile.Sync(); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: sync error: %v", path, err)}, nil
	}
	if err := tmpFile.Chmod(originalPerm); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: chmod error: %v", path, err)}, nil
	}
	if err := tmpFile.Close(); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: close error: %v", path, err)}, nil
	}

	// Atomic rename to target path.
	if err := os.Rename(tmpPath, resolved); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot edit %s: commit error: %v", path, err)}, nil
	}
	committed = true

	origLineCount := strings.Count(originalContent, "\n")
	newLineCount := strings.Count(currentBuffer, "\n")
	lineDelta := newLineCount - origLineCount
	deltaStr := fmt.Sprintf("%+d lines", lineDelta)
	if lineDelta == 0 {
		deltaStr = "0 line change"
	}

	return tools.Result{
		Content: fmt.Sprintf("successfully applied %d edit(s) to %s (%s)", len(edits), path, deltaStr),
	}, nil
}

func detectLineEnding(s string) string {
	if strings.Contains(s, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func normalizeLineEndings(s, ending string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if ending == "\r\n" {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	return s
}

func findMatchLineNumbers(content, search string) []int {
	if len(search) == 0 {
		return nil
	}
	var lineNums []int
	idx := 0
	lineNum := 1
	lastIdx := 0
	contentLen := len(content)

	for {
		nextMatch := strings.Index(content[idx:], search)
		if nextMatch == -1 {
			break
		}
		absMatchIdx := idx + nextMatch
		lineNum += strings.Count(content[lastIdx:absMatchIdx], "\n")
		lineNums = append(lineNums, lineNum)
		lastIdx = absMatchIdx
		idx = absMatchIdx + 1
		if idx >= contentLen {
			break
		}
	}
	return lineNums
}

func formatLineNumbers(lines []int) string {
	if len(lines) == 0 {
		return ""
	}
	strs := make([]string, len(lines))
	for i, l := range lines {
		strs[i] = strconv.Itoa(l)
	}
	return strings.Join(strs, ", ")
}

func findNearMatchHint(content, search string) string {
	searchTrimmed := strings.TrimSpace(search)
	if searchTrimmed == "" {
		return ""
	}
	if strings.Contains(content, searchTrimmed) {
		lines := findMatchLineNumbers(content, searchTrimmed)
		if len(lines) > 0 {
			return fmt.Sprintf(" (note: a match with different surrounding whitespace exists at line %d)", lines[0])
		}
	}

	searchLines := strings.Split(search, "\n")
	for _, sl := range searchLines {
		slTrimmed := strings.TrimSpace(sl)
		if len(slTrimmed) >= 6 && strings.Contains(content, slTrimmed) {
			lines := findMatchLineNumbers(content, slTrimmed)
			if len(lines) > 0 {
				return fmt.Sprintf(" (note: line %q was found at line %d; check surrounding lines and indentation)", slTrimmed, lines[0])
			}
		}
	}
	return ""
}

func validateSyntax(path string, content []byte) error {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		fset := token.NewFileSet()
		_, err := parser.ParseFile(fset, path, content, parser.AllErrors)
		if err != nil {
			return fmt.Errorf("Go syntax error: %w", err)
		}
	}
	return nil
}

// lineRecord holds a single line with parsed leading indentation, content, and line ending.
type lineRecord struct {
	raw     string
	indent  string
	content string
	ending  string
}

func parseLines(text string) []lineRecord {
	if len(text) == 0 {
		return nil
	}
	rawLines := strings.SplitAfter(text, "\n")
	// If text ends with a newline, SplitAfter produces a trailing empty string
	// representing the EOF boundary. Trim it so a terminal newline is captured
	// by the final line's ending field rather than creating a phantom empty line.
	if len(rawLines) > 0 && rawLines[len(rawLines)-1] == "" {
		rawLines = rawLines[:len(rawLines)-1]
	}
	records := make([]lineRecord, len(rawLines))
	for i, raw := range rawLines {
		ending := ""
		core := raw
		if strings.HasSuffix(core, "\r\n") {
			ending = "\r\n"
			core = core[:len(core)-2]
		} else if strings.HasSuffix(core, "\n") {
			ending = "\n"
			core = core[:len(core)-1]
		}
		lead := 0
		for lead < len(core) && (core[lead] == ' ' || core[lead] == '\t') {
			lead++
		}
		records[i] = lineRecord{
			raw:     raw,
			indent:  core[:lead],
			content: core[lead:],
			ending:  ending,
		}
	}
	return records
}

func indentCol(indent string, tabWidth int) int {
	col := 0
	for _, ch := range indent {
		if ch == '\t' {
			col = (col/tabWidth + 1) * tabWidth
		} else if ch == ' ' {
			col++
		}
	}
	return col
}

type indentCandidate struct {
	startLine int
	tabWidth  int
}

func applyIndentationAwareEdit(buffer, search, replace, defaultEnding string) (string, int, []int, error) {
	fileLines := parseLines(buffer)
	searchLines := parseLines(search)
	if len(fileLines) == 0 || len(searchLines) == 0 || len(searchLines) > len(fileLines) {
		return buffer, 0, nil, nil
	}

	tabWidths := []int{4, 2, 8}
	var matchedCandidates []indentCandidate
	var matchedStartLines []int
	seenStarts := make(map[int]bool)

	for start := 0; start <= len(fileLines)-len(searchLines); start++ {
		// First pass: content equality (without leading indent or trailing line breaks)
		contentMatches := true
		for i := 0; i < len(searchLines); i++ {
			if fileLines[start+i].content != searchLines[i].content {
				contentMatches = false
				break
			}
		}
		if !contentMatches {
			continue
		}

		// Second pass: indentation structure check across candidate tab widths
		for _, tw := range tabWidths {
			firstNonEmpty := -1
			for i := 0; i < len(searchLines); i++ {
				if searchLines[i].content != "" {
					firstNonEmpty = i
					break
				}
			}

			indentMatches := true
			if firstNonEmpty != -1 {
				baseFileCol := indentCol(fileLines[start+firstNonEmpty].indent, tw)
				baseSearchCol := indentCol(searchLines[firstNonEmpty].indent, tw)
				diff := baseFileCol - baseSearchCol

				for i := 0; i < len(searchLines); i++ {
					if searchLines[i].content == "" {
						continue
					}
					fCol := indentCol(fileLines[start+i].indent, tw)
					sCol := indentCol(searchLines[i].indent, tw)
					if fCol != sCol+diff {
						indentMatches = false
						break
					}
				}
			} else {
				// All lines are blank; require exact column equality
				for i := 0; i < len(searchLines); i++ {
					if indentCol(fileLines[start+i].indent, tw) != indentCol(searchLines[i].indent, tw) {
						indentMatches = false
						break
					}
				}
			}

			if indentMatches {
				if !seenStarts[start] {
					seenStarts[start] = true
					matchedCandidates = append(matchedCandidates, indentCandidate{
						startLine: start,
						tabWidth:  tw,
					})
					matchedStartLines = append(matchedStartLines, start+1)
				}
				break
			}
		}
	}

	if len(matchedCandidates) != 1 {
		return buffer, len(matchedCandidates), matchedStartLines, nil
	}

	cand := matchedCandidates[0]
	start := cand.startLine
	tw := cand.tabWidth
	m := len(searchLines)

	// Determine file indentation style (tabs vs spaces)
	fileStyle := detectIndentationStyle(fileLines, start, m)

	// Format replacement lines using the file's indentation style and line endings
	replaceLines := parseLines(replace)
	firstNonEmpty := -1
	for i := 0; i < len(searchLines); i++ {
		if searchLines[i].content != "" {
			firstNonEmpty = i
			break
		}
	}
	baseSearchCol := 0
	baseFileCol := 0
	if firstNonEmpty != -1 {
		baseSearchCol = indentCol(searchLines[firstNonEmpty].indent, tw)
		baseFileCol = indentCol(fileLines[start+firstNonEmpty].indent, tw)
	}

	ending := fileLines[start].ending
	if ending == "" {
		ending = defaultEnding
	}

	var repBuffer strings.Builder
	for i, rLine := range replaceLines {
		if rLine.content == "" && rLine.indent == "" {
			// Blank line
			if i < len(replaceLines)-1 || strings.HasSuffix(replace, "\n") {
				repBuffer.WriteString(ending)
			}
			continue
		}

		rCol := indentCol(rLine.indent, tw)
		targetCol := baseFileCol + (rCol - baseSearchCol)
		if targetCol < 0 {
			targetCol = 0
		}

		var newIndent string
		if fileStyle == "\t" {
			numTabs := targetCol / tw
			remSpaces := targetCol % tw
			newIndent = strings.Repeat("\t", numTabs) + strings.Repeat(" ", remSpaces)
		} else {
			newIndent = strings.Repeat(" ", targetCol)
		}

		repBuffer.WriteString(newIndent)
		repBuffer.WriteString(rLine.content)
		if i < len(replaceLines)-1 || strings.HasSuffix(replace, "\n") || fileLines[start+m-1].ending != "" {
			repBuffer.WriteString(ending)
		}
	}

	// Reconstruct buffer atomically
	var finalBuf strings.Builder
	for i := 0; i < start; i++ {
		finalBuf.WriteString(fileLines[i].raw)
	}
	finalBuf.WriteString(repBuffer.String())
	for i := start + m; i < len(fileLines); i++ {
		finalBuf.WriteString(fileLines[i].raw)
	}

	return finalBuf.String(), 1, matchedStartLines, nil
}

func detectIndentationStyle(lines []lineRecord, start, count int) string {
	blockTabs := 0
	blockSpaces := 0
	for i := start; i < start+count && i < len(lines); i++ {
		if lines[i].content == "" {
			continue // skip blank lines
		}
		if strings.Contains(lines[i].indent, "\t") {
			blockTabs++
		} else if len(lines[i].indent) > 0 {
			blockSpaces++
		}
	}
	if blockTabs > blockSpaces {
		return "\t"
	}
	if blockSpaces > blockTabs {
		return " "
	}

	// Tied or no indentation in the matched block: inspect the entire file.
	tabCount := 0
	spaceCount := 0
	for _, l := range lines {
		if l.content == "" {
			continue
		}
		if strings.Contains(l.indent, "\t") {
			tabCount++
		} else if len(l.indent) > 0 {
			spaceCount++
		}
	}
	if tabCount >= spaceCount && tabCount > 0 {
		return "\t"
	}
	if spaceCount > 0 {
		return " "
	}
	return "\t"
}
