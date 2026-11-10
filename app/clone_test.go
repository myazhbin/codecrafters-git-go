package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPktLine(t *testing.T) {
	if got := pktLine("done\n"); got != "0009done\n" {
		t.Errorf("pktLine = %q", got)
	}

	r := strings.NewReader("0009done\n00000006a\n")
	payload, flush, err := readPktLine(r)
	if err != nil || flush || string(payload) != "done\n" {
		t.Errorf("first = (%q, %v, %v)", payload, flush, err)
	}
	if _, flush, err = readPktLine(r); err != nil || !flush {
		t.Errorf("expected a flush packet, got (%v, %v)", flush, err)
	}
	payload, flush, err = readPktLine(r)
	if err != nil || flush || string(payload) != "a\n" {
		t.Errorf("third = (%q, %v, %v)", payload, flush, err)
	}
	if _, _, err = readPktLine(r); err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}

	for name, in := range map[string]string{"bad length": "zzzzabc", "truncated": "0010abc"} {
		if _, _, err := readPktLine(strings.NewReader(in)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseRefAdvertisement(t *testing.T) {
	const (
		h1 = "1111111111111111111111111111111111111111"
		h2 = "2222222222222222222222222222222222222222"
		h3 = "3333333333333333333333333333333333333333"
	)
	body := pktLine("# service=git-upload-pack\n") + "0000" +
		pktLine(h1+" HEAD\x00multi_ack ofs-delta symref=HEAD:refs/heads/main agent=git/x\n") +
		pktLine(h1+" refs/heads/main\n") +
		pktLine(h2+" refs/tags/v1\n") +
		pktLine(h3+" refs/tags/v1^{}\n") +
		"0000"

	refs, err := parseRefAdvertisement(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"HEAD": h1, "refs/heads/main": h1, "refs/tags/v1": h2}
	if len(refs.Refs) != len(want) {
		t.Errorf("refs = %v, want %v", refs.Refs, want)
	}
	for name, hash := range want {
		if refs.Refs[name] != hash {
			t.Errorf("ref %s = %q, want %q", name, refs.Refs[name], hash)
		}
	}
	if refs.headSymref() != "refs/heads/main" {
		t.Errorf("headSymref = %q", refs.headSymref())
	}
	if !refs.hasCapability("ofs-delta") || refs.hasCapability("side-band") {
		t.Errorf("capabilities = %v", refs.Capabilities)
	}
}

func TestParseRefAdvertisementEmptyRepo(t *testing.T) {
	body := pktLine("# service=git-upload-pack\n") + "0000" +
		pktLine(zeroHash+" capabilities^{}\x00multi_ack\n") + "0000"
	refs, err := parseRefAdvertisement(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(refs.Refs) != 0 {
		t.Errorf("refs = %v, want none", refs.Refs)
	}
}

func TestParseRefAdvertisementMalformed(t *testing.T) {
	for name, body := range map[string]string{
		"empty":          "",
		"no flush":       pktLine("# service=git-upload-pack\n") + pktLine("x y\n"),
		"bad hash":       pktLine("# service=git-upload-pack\n") + "0000" + pktLine("nothex HEAD\n") + "0000",
		"missing name":   pktLine("# service=git-upload-pack\n") + "0000" + pktLine(zeroHash+"\n") + "0000",
		"unterminated":   pktLine("# service=git-upload-pack\n") + "0000" + pktLine(zeroHash+" HEAD\n"),
		"not a pkt-line": "<html>not git</html>",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRefAdvertisement(strings.NewReader(body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestUploadPackRequest(t *testing.T) {
	const (
		h1 = "1111111111111111111111111111111111111111"
		h2 = "2222222222222222222222222222222222222222"
	)
	got := uploadPackRequest([]string{h1, h2}, []string{"ofs-delta"})
	want := "003cwant " + h1 + " ofs-delta\n" + "0032want " + h2 + "\n" + "0000" + "0009done\n"
	if got != want {
		t.Errorf("request = %q, want %q", got, want)
	}
}

func TestWriteRefRejectsUnsafeNames(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	for _, name := range []string{"refs/../../evil", "HEAD", "/etc/passwd", "refs/heads/../../../x"} {
		if err := writeRef(gitDir, name, zeroHash); err == nil {
			t.Errorf("writeRef(%q) should fail", name)
		}
	}
	if err := writeRef(gitDir, "refs/heads/feature/x", zeroHash); err != nil {
		t.Errorf("nested branch names must be allowed: %v", err)
	}
}

func TestCheckoutRejectsUnsafePaths(t *testing.T) {
	inTempRepo(t)
	blob := writeRawObject(t, "blob", []byte("x"))
	for _, name := range []string{"..", ".git", ".GIT", "a/b", "."} {
		tree := writeRawObject(t, "tree", rawTree(t, []treeEntry{{modeFile, name, blob}}))
		if err := checkoutTree(".git", tree, filepath.Join(t.TempDir(), "out")); err == nil {
			t.Errorf("checking out entry %q should fail", name)
		}
	}
}

// serveRepo exposes the git repository at repoDir over the smart HTTP
// protocol, backed by the official git implementation.
func serveRepo(t *testing.T, repoDir string) *httptest.Server {
	t.Helper()
	uploadPack := func(w http.ResponseWriter, stdin io.Reader, args ...string) {
		cmd := exec.Command("git", append([]string{"upload-pack", "--stateless-rpc"}, append(args, repoDir)...)...)
		cmd.Stdin = stdin
		cmd.Stdout = w
		if err := cmd.Run(); err != nil {
			t.Errorf("git upload-pack: %v", err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repo/info/refs" && r.URL.Query().Get("service") == "git-upload-pack":
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			io.WriteString(w, pktLine("# service=git-upload-pack\n")+"0000")
			uploadPack(w, nil, "--advertise-refs")
		case r.Method == http.MethodPost && r.URL.Path == "/repo/git-upload-pack":
			w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
			uploadPack(w, r.Body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestCloneEndToEnd clones a repository built and served by the official
// git over smart HTTP, then has official git verify the result.
func TestCloneEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "Test User")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "test@example.com")
	}

	// Build a source repository with history, so the pack contains deltas.
	src := t.TempDir()
	t.Chdir(src)
	realGit(t, src, "init", "-q", "-b", "trunk")
	big := strings.Repeat("the quick brown fox jumps over the lazy dog\n", 500)
	writeFiles(t, map[string]string{
		"README.md":         "# hello\n",
		"big.txt":           big,
		"dir/nested/a.txt":  "nested file\n",
		"scripts/run.sh":    "#!/bin/sh\necho hi\n",
		"data/binary.bin":   "\x00\x01\x02\xff\xfe",
		"data/empty":        "",
		"with space/f i le": "spaces\n",
	})
	if err := os.Chmod("scripts/run.sh", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README.md", "link"); err != nil {
		t.Fatal(err)
	}
	realGit(t, src, "add", "-A")
	realGit(t, src, "commit", "-q", "-m", "first")
	realGit(t, src, "tag", "-a", "v1", "-m", "annotated tag")
	for i, extra := range []string{"second change\n", "third change\n"} {
		big = big[:1000*(i+1)] + extra + big[1000*(i+1):]
		writeFiles(t, map[string]string{"big.txt": big, "README.md": "# hello\n" + extra})
		realGit(t, src, "commit", "-q", "-am", extra)
	}
	realGit(t, src, "branch", "feature", "HEAD~1")
	realGit(t, src, "tag", "light")
	realGit(t, src, "repack", "-adfq")

	srv := serveRepo(t, src)
	work := t.TempDir()
	t.Chdir(work)

	var out bytes.Buffer
	if err := run("clone", []string{srv.URL + "/repo", "dest"}, &out); err != nil {
		t.Fatalf("clone: %v", err)
	}
	dest := filepath.Join(work, "dest")

	// Working tree matches the source.
	for _, path := range []string{"README.md", "big.txt", "dir/nested/a.txt", "scripts/run.sh", "data/binary.bin", "data/empty", "with space/f i le"} {
		want, err := os.ReadFile(filepath.Join(src, path))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dest, path))
		if err != nil {
			t.Errorf("missing file in clone: %v", err)
		} else if !bytes.Equal(got, want) {
			t.Errorf("%s differs from the source", path)
		}
	}
	if info, err := os.Stat(filepath.Join(dest, "scripts/run.sh")); err != nil || info.Mode()&0111 == 0 {
		t.Errorf("scripts/run.sh should be executable")
	}
	if info, err := os.Stat(filepath.Join(dest, "README.md")); err != nil || info.Mode()&0111 != 0 {
		t.Errorf("README.md should not be executable")
	}
	if target, err := os.Readlink(filepath.Join(dest, "link")); err != nil || target != "README.md" {
		t.Errorf("link = (%q, %v), want a symlink to README.md", target, err)
	}

	// History, refs and object integrity, as judged by official git.
	if got, want := realGit(t, dest, "rev-parse", "HEAD"), realGit(t, src, "rev-parse", "HEAD"); got != want {
		t.Errorf("HEAD = %s, want %s", got, want)
	}
	if got := realGit(t, dest, "symbolic-ref", "HEAD"); got != "refs/heads/trunk" {
		t.Errorf("HEAD points to %q, want refs/heads/trunk", got)
	}
	if got, want := realGit(t, dest, "log", "--format=%H %an <%ae> %s"), realGit(t, src, "log", "--format=%H %an <%ae> %s"); got != want {
		t.Errorf("log differs:\n%s\nwant\n%s", got, want)
	}
	for ref, srcRef := range map[string]string{
		"refs/remotes/origin/trunk":   "refs/heads/trunk",
		"refs/remotes/origin/feature": "refs/heads/feature",
		"refs/tags/v1":                "refs/tags/v1",
		"refs/tags/light":             "refs/tags/light",
	} {
		if got, want := realGit(t, dest, "rev-parse", ref), realGit(t, src, "rev-parse", srcRef); got != want {
			t.Errorf("%s = %s, want %s", ref, got, want)
		}
	}
	realGit(t, dest, "fsck", "--strict", "--no-dangling")
	if got := realGit(t, dest, "config", "remote.origin.url"); got != srv.URL+"/repo" {
		t.Errorf("remote.origin.url = %q", got)
	}

	// Our own commands work on the clone too.
	t.Chdir(dest)
	out.Reset()
	if err := run("ls-tree", []string{"--name-only", realGit(t, dest, "rev-parse", "HEAD^{tree}")}, &out); err != nil {
		t.Fatalf("ls-tree: %v", err)
	}
	if want := "README.md\nbig.txt\ndata\ndir\nlink\nscripts\nwith space\n"; out.String() != want {
		t.Errorf("ls-tree = %q, want %q", out.String(), want)
	}
	if got, want := runWriteTree(t), realGit(t, dest, "rev-parse", "HEAD^{tree}"); got != want {
		t.Errorf("write-tree of the checkout = %s, want %s", got, want)
	}
}

func TestCloneEmptyRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	src := t.TempDir()
	realGit(t, src, "init", "-q")
	srv := serveRepo(t, src)

	t.Chdir(t.TempDir())
	if err := run("clone", []string{srv.URL + "/repo/", "dest"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if _, err := os.Stat("dest/.git/HEAD"); err != nil {
		t.Errorf("expected an initialized repository: %v", err)
	}
}

func TestCloneErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	t.Chdir(t.TempDir())
	if err := run("clone", []string{srv.URL + "/missing", "dest"}, &bytes.Buffer{}); err == nil {
		t.Error("expected an error for a missing repository")
	}
	if _, err := os.Stat("dest"); !os.IsNotExist(err) {
		t.Error("a failed clone of a missing repository should not create the directory")
	}

	writeFiles(t, map[string]string{"occupied/file": "x"})
	if err := run("clone", []string{srv.URL + "/missing", "occupied"}, &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "not an empty directory") {
		t.Errorf("expected a non-empty destination error, got %v", err)
	}

	for name, args := range map[string][]string{"no args": {}, "no dir": {"http://x"}, "too many": {"a", "b", "c"}} {
		if err := run("clone", args, &bytes.Buffer{}); err == nil {
			t.Errorf("%s: expected a usage error", name)
		}
	}
}
