package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

// helper to create a wsl policy rooted at dir
func wslPolicy(dir string) sandbox.Policy {
	return sandbox.Policy{Mode: sandbox.ModeWSL, Workspace: dir}
}

func TestReadFile_WSL_InsideSuccess(t *testing.T) {
	ws := t.TempDir()
	p := wslPolicy(ws)
	// create file via native write first, then read via wsl policy
	native := NewWriteFile()
	path := filepath.Join(ws, "hello.txt")
	if res, err := native.Execute(context.Background(), map[string]any{"path": path, "content": "hi"}); err != nil || res.IsError {
		t.Fatalf("setup write failed: %v %v", res, err)
	}
	rf := NewReadFileWithPolicy(p)
	res, err := rf.Execute(context.Background(), map[string]any{"path": "hello.txt"})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", res.Content)
	}
	if res.Content != "hi" {
		t.Fatalf("content = %q, want %q", res.Content, "hi")
	}
	// absolute inside also should succeed
	res, err = rf.Execute(context.Background(), map[string]any{"path": path})
	if err != nil || res.IsError {
		t.Fatalf("absolute inside failed: err=%v res=%+v", err, res)
	}
}

func TestReadFile_WSL_TraversalDenied(t *testing.T) {
	ws := t.TempDir()
	p := wslPolicy(ws)
	rf := NewReadFileWithPolicy(p)
	// create file outside workspace to ensure we don't read it via traversal
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// traversal via ..
	res, err := rf.Execute(context.Background(), map[string]any{"path": filepath.Join("..", filepath.Base(filepath.Dir(outside)), filepath.Base(outside))})
	if err != nil {
		t.Fatalf("Execute returned Go error = %v, want domain error", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for traversal, got success")
	}
	if !strings.Contains(strings.ToLower(res.Content), "outside") && !strings.Contains(res.Content, "workspace") {
		t.Errorf("traversal error message should mention workspace boundary, got %q", res.Content)
	}
	// direct relative traversal
	res, err = rf.Execute(context.Background(), map[string]any{"path": "../escape.txt"})
	if err != nil || !res.IsError {
		t.Fatalf("expected traversal denied for ../escape.txt, got err=%v res=%+v", err, res)
	}
}

func TestReadFile_WSL_AbsoluteOutsideDenied(t *testing.T) {
	ws := t.TempDir()
	p := wslPolicy(ws)
	rf := NewReadFileWithPolicy(p)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := rf.Execute(context.Background(), map[string]any{"path": outside})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !res.IsError {
		t.Fatal("expected absolute outside to be denied")
	}
	if !strings.Contains(res.Content, "outside") && !strings.Contains(res.Content, "workspace") {
		t.Errorf("error should mention workspace, got %q", res.Content)
	}
}

func TestReadFile_WSL_SymlinkOutsideDenied(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	p := wslPolicy(ws)
	rf := NewReadFileWithPolicy(p)
	// reading via symlink should be denied
	res, err := rf.Execute(context.Background(), map[string]any{"path": filepath.Join("link", "secret.txt")})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected symlink escape to be denied, got success with %q", res.Content)
	}
	if !strings.Contains(res.Content, "outside") && !strings.Contains(res.Content, "workspace") {
		t.Errorf("symlink escape error should mention workspace, got %q", res.Content)
	}
	// symlink to file inside should succeed
	insideFile := filepath.Join(ws, "inside.txt")
	if err := os.WriteFile(insideFile, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkIn := filepath.Join(ws, "inlink")
	// On Windows, symlink to file may need different handling; try dir link
	insideDir := filepath.Join(ws, "inner")
	if err := os.MkdirAll(insideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(insideDir, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(insideDir, linkIn); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	res, err = rf.Execute(context.Background(), map[string]any{"path": filepath.Join("inlink", "a.txt")})
	if err != nil || res.IsError {
		t.Fatalf("symlink inside should be allowed, err=%v res=%+v", err, res)
	}
	if res.Content != "a" {
		t.Fatalf("content = %q, want %q", res.Content, "a")
	}
}

func TestWriteFile_WSL_InsideSuccess(t *testing.T) {
	ws := t.TempDir()
	p := wslPolicy(ws)
	wf := NewWriteFileWithPolicy(p)
	res, err := wf.Execute(context.Background(), map[string]any{"path": "a/b/c.txt", "content": "hello"})
	if err != nil || res.IsError {
		t.Fatalf("write inside failed: err=%v res=%+v", err, res)
	}
	// verify file exists at workspace location
	data, err := os.ReadFile(filepath.Join(ws, "a", "b", "c.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("file not written correctly: %v %q", err, string(data))
	}
	// absolute inside
	absPath := filepath.Join(ws, "abs.txt")
	res, err = wf.Execute(context.Background(), map[string]any{"path": absPath, "content": "abs"})
	if err != nil || res.IsError {
		t.Fatalf("absolute inside write failed: %v %+v", err, res)
	}
}

func TestWriteFile_WSL_TraversalDenied(t *testing.T) {
	ws := t.TempDir()
	p := wslPolicy(ws)
	wf := NewWriteFileWithPolicy(p)
	res, err := wf.Execute(context.Background(), map[string]any{"path": "../escape.txt", "content": "bad"})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !res.IsError {
		t.Fatal("expected traversal write to be denied")
	}
	if !strings.Contains(res.Content, "outside") && !strings.Contains(res.Content, "workspace") {
		t.Errorf("error should mention workspace, got %q", res.Content)
	}
	// ensure no file outside was created
	outsidePath := filepath.Join(filepath.Dir(ws), "escape.txt")
	if _, err := os.Stat(outsidePath); err == nil {
		t.Errorf("traversal write created file outside workspace: %s", outsidePath)
	}
	// also test nested traversal
	res, err = wf.Execute(context.Background(), map[string]any{"path": "a/../../outside.txt", "content": "bad"})
	if err != nil || !res.IsError {
		t.Fatalf("nested traversal should be denied, got %v %+v", err, res)
	}
}

func TestWriteFile_WSL_AbsoluteOutsideDenied(t *testing.T) {
	ws := t.TempDir()
	p := wslPolicy(ws)
	wf := NewWriteFileWithPolicy(p)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	res, err := wf.Execute(context.Background(), map[string]any{"path": outside, "content": "evil"})
	if err != nil || !res.IsError {
		t.Fatalf("absolute outside should be denied, got %v %+v", err, res)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("absolute outside write should not create file")
	}
}

func TestWriteFile_WSL_SymlinkOutsideDenied(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(ws, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	p := wslPolicy(ws)
	wf := NewWriteFileWithPolicy(p)
	res, err := wf.Execute(context.Background(), map[string]any{"path": filepath.Join("link", "evil.txt"), "content": "bad"})
	if err != nil || !res.IsError {
		t.Fatalf("symlink write should be denied, got %v %+v", err, res)
	}
	// ensure no file created outside
	if _, err := os.Stat(filepath.Join(outside, "evil.txt")); err == nil {
		t.Error("symlink escape write created file outside")
	}
	// ensure parent MkdirAll didn't create outside dir
	// test nested mkdir traversal: a path that would create outside via symlink parent not yet existent
}

func TestWriteFile_WSL_MkdirAllNotEscaping(t *testing.T) {
	ws := t.TempDir()
	p := wslPolicy(ws)
	wf := NewWriteFileWithPolicy(p)
	// attempt to create directory via traversal in MkdirAll
	res, err := wf.Execute(context.Background(), map[string]any{"path": "a/../..//outside2.txt", "content": "bad"})
	if err != nil || !res.IsError {
		t.Fatalf("mkdir traversal should be denied, got %v %+v", err, res)
	}
}

func TestListFiles_WSL_InsideSuccessAndTraversalDenied(t *testing.T) {
	ws := t.TempDir()
	// create file inside
	if err := os.WriteFile(filepath.Join(ws, "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(ws, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "inner.txt"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := wslPolicy(ws)
	lf := NewListFilesWithPolicy(p)

	// default "." should resolve to workspace root
	res, err := lf.Execute(context.Background(), map[string]any{})
	if err != nil || res.IsError {
		t.Fatalf("default list failed: %v %+v", err, res)
	}
	if !strings.Contains(res.Content, "file.txt") {
		t.Errorf("default list should contain file.txt, got %q", res.Content)
	}

	// list subdir via relative
	res, err = lf.Execute(context.Background(), map[string]any{"path": "sub"})
	if err != nil || res.IsError {
		t.Fatalf("list sub failed: %v %+v", err, res)
	}
	if !strings.Contains(res.Content, "inner.txt") {
		t.Errorf("list sub should contain inner.txt, got %q", res.Content)
	}

	// traversal denied
	res, err = lf.Execute(context.Background(), map[string]any{"path": ".."})
	if err != nil || !res.IsError {
		t.Fatalf("list traversal should be denied, got %v %+v", err, res)
	}
	if !strings.Contains(res.Content, "outside") && !strings.Contains(res.Content, "workspace") {
		t.Errorf("traversal error should mention workspace, got %q", res.Content)
	}

	// absolute outside denied
	outside := t.TempDir()
	res, err = lf.Execute(context.Background(), map[string]any{"path": outside})
	if err != nil || !res.IsError {
		t.Fatalf("absolute outside list should be denied, got %v %+v", err, res)
	}

	// symlink outside denied
	link := filepath.Join(ws, "linkdir")
	if err := os.Symlink(outside, link); err == nil {
		res, err = lf.Execute(context.Background(), map[string]any{"path": "linkdir"})
		if err != nil || !res.IsError {
			t.Fatalf("symlink list should be denied, got %v %+v", err, res)
		}
	}
}

func TestFilesystem_NativeMode_AllowsOutside(t *testing.T) {
	// native mode with no policy should allow outside access (historical behavior)
	ws := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "native.txt")
	if err := os.WriteFile(outsideFile, []byte("native-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	// read via native (no policy) should succeed
	rf := NewReadFile()
	res, err := rf.Execute(context.Background(), map[string]any{"path": outsideFile})
	if err != nil || res.IsError {
		t.Fatalf("native read should allow outside, got err=%v res=%+v", err, res)
	}
	if res.Content != "native-secret" {
		t.Fatalf("native read content = %q, want %q", res.Content, "native-secret")
	}

	// native with explicit native policy also allows
	nativePolicy := sandbox.Policy{Mode: sandbox.ModeNative, Workspace: ws}
	rf2 := NewReadFileWithPolicy(nativePolicy)
	res, err = rf2.Execute(context.Background(), map[string]any{"path": outsideFile})
	if err != nil || res.IsError {
		t.Fatalf("native policy read should allow outside, got %v %+v", err, res)
	}

	// write outside via native
	wf := NewWriteFile()
	out2 := filepath.Join(outside, "write_native.txt")
	res, err = wf.Execute(context.Background(), map[string]any{"path": out2, "content": "hello"})
	if err != nil || res.IsError {
		t.Fatalf("native write outside should succeed, got %v %+v", err, res)
	}
	data, err := os.ReadFile(out2)
	if err != nil || string(data) != "hello" {
		t.Fatalf("native write not persisted")
	}

	// list outside via native
	lf := NewListFiles()
	res, err = lf.Execute(context.Background(), map[string]any{"path": outside})
	if err != nil || res.IsError {
		t.Fatalf("native list outside should succeed, got %v %+v", err, res)
	}
	if !strings.Contains(res.Content, "native.txt") {
		t.Errorf("native list should contain file, got %q", res.Content)
	}

	// also ensure wsl policy with empty mode (default) behaves as native
	emptyPolicy := sandbox.Policy{}
	rf3 := NewReadFileWithPolicy(emptyPolicy)
	res, err = rf3.Execute(context.Background(), map[string]any{"path": outsideFile})
	if err != nil || res.IsError {
		t.Fatalf("empty policy should be native and allow outside, got %v %+v", err, res)
	}
}

func TestWriteFile_WSL_RelativeToWorkspaceNotCWD(t *testing.T) {
	ws := t.TempDir()
	otherCwd := t.TempDir()
	// change cwd to other directory, but workspace is ws, relative paths should resolve to ws not cwd
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(otherCwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	p := wslPolicy(ws)
	wf := NewWriteFileWithPolicy(p)
	res, err := wf.Execute(context.Background(), map[string]any{"path": "rel.txt", "content": "rel"})
	if err != nil || res.IsError {
		t.Fatalf("relative write should resolve to workspace, got %v %+v", err, res)
	}
	// file should be in workspace, not cwd
	if _, err := os.Stat(filepath.Join(ws, "rel.txt")); err != nil {
		t.Fatalf("file not in workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(otherCwd, "rel.txt")); err == nil {
		t.Error("file incorrectly created in cwd instead of workspace")
	}

	rf := NewReadFileWithPolicy(p)
	res, err = rf.Execute(context.Background(), map[string]any{"path": "rel.txt"})
	if err != nil || res.IsError {
		t.Fatalf("relative read should resolve to workspace, got %v %+v", err, res)
	}
	if res.Content != "rel" {
		t.Fatalf("content = %q, want %q", res.Content, "rel")
	}
}
