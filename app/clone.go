package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const zeroHash = "0000000000000000000000000000000000000000"

// pktLine encodes s as a pkt-line: a 4-digit hex length (including the
// length itself) followed by the payload.
func pktLine(s string) string {
	return fmt.Sprintf("%04x%s", len(s)+4, s)
}

// readPktLine reads one pkt-line. Special packets (flush "0000", delimiter
// "0001", response-end "0002") are reported with flush set and no payload.
func readPktLine(r io.Reader) (payload []byte, flush bool, err error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, false, err
	}
	n, err := strconv.ParseUint(string(head[:]), 16, 16)
	if err != nil {
		return nil, false, fmt.Errorf("malformed pkt-line length %q", head[:])
	}
	if n < 4 {
		return nil, true, nil
	}
	payload = make([]byte, n-4)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, false, fmt.Errorf("truncated pkt-line: %w", err)
	}
	return payload, false, nil
}

// remoteRefs is the ref advertisement of a remote repository.
type remoteRefs struct {
	Refs         map[string]string // ref name -> hash, including "HEAD"
	Capabilities []string
}

// headSymref returns the branch HEAD points to (e.g. "refs/heads/main"), as
// advertised by the server, or "" if unknown.
func (r *remoteRefs) headSymref() string {
	for _, c := range r.Capabilities {
		if target, ok := strings.CutPrefix(c, "symref=HEAD:"); ok {
			return target
		}
	}
	return ""
}

func (r *remoteRefs) hasCapability(name string) bool {
	for _, c := range r.Capabilities {
		if c == name {
			return true
		}
	}
	return false
}

// parseRefAdvertisement decodes the body of a smart HTTP
// "info/refs?service=git-upload-pack" response.
func parseRefAdvertisement(r io.Reader) (*remoteRefs, error) {
	refs := &remoteRefs{Refs: map[string]string{}}

	line, flush, err := readPktLine(r)
	if err != nil {
		return nil, fmt.Errorf("reading ref advertisement: %w", err)
	}
	if !flush && bytes.HasPrefix(line, []byte("# service=")) {
		// Skip the service announcement and the flush packet after it.
		if _, flush, err = readPktLine(r); err != nil {
			return nil, fmt.Errorf("reading ref advertisement: %w", err)
		} else if !flush {
			return nil, fmt.Errorf("malformed ref advertisement: missing flush after service line")
		}
		if line, flush, err = readPktLine(r); err != nil {
			return nil, fmt.Errorf("reading ref advertisement: %w", err)
		}
	}

	for first := true; !flush; first = false {
		text := strings.TrimSuffix(string(line), "\n")
		if first {
			// The first ref carries the capability list after a NUL byte.
			if ref, caps, ok := strings.Cut(text, "\x00"); ok {
				text = ref
				refs.Capabilities = strings.Fields(caps)
			}
		}
		hash, name, ok := strings.Cut(text, " ")
		if !ok || !isHexHash(hash) {
			return nil, fmt.Errorf("malformed ref advertisement line %q", text)
		}
		// "capabilities^{}" is a placeholder sent by empty repositories and
		// "<tag>^{}" lines are peeled tags; neither is a real ref.
		if !strings.HasSuffix(name, "^{}") && hash != zeroHash {
			refs.Refs[name] = hash
		}

		if line, flush, err = readPktLine(r); err != nil {
			return nil, fmt.Errorf("reading ref advertisement: %w", err)
		}
	}
	return refs, nil
}

const userAgent = "git/2.0 (codecrafters-git-go)"

// discoverRefs asks the remote which refs it has.
func discoverRefs(repoURL string) (*remoteRefs, error) {
	req, err := http.NewRequest(http.MethodGet, repoURL+"/info/refs?service=git-upload-pack", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("repository %s not found or not accessible: %s", repoURL, resp.Status)
	}
	return parseRefAdvertisement(bufio.NewReader(resp.Body))
}

// uploadPackRequest builds the body of a git-upload-pack request asking for
// the complete history of the given hashes.
func uploadPackRequest(wants, capabilities []string) string {
	var body strings.Builder
	for i, want := range wants {
		line := "want " + want
		if i == 0 && len(capabilities) > 0 {
			line += " " + strings.Join(capabilities, " ")
		}
		body.WriteString(pktLine(line + "\n"))
	}
	body.WriteString("0000")
	body.WriteString(pktLine("done\n"))
	return body.String()
}

// fetchPack downloads a packfile containing the wanted objects and
// everything reachable from them.
func fetchPack(repoURL string, wants, capabilities []string) ([]byte, error) {
	body := uploadPackRequest(wants, capabilities)
	req, err := http.NewRequest(http.MethodPost, repoURL+"/git-upload-pack", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	req.Header.Set("Accept", "application/x-git-upload-pack-result")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching pack from %s: %s", repoURL, resp.Status)
	}

	// The pack is preceded by acknowledgement pkt-lines (normally "NAK").
	r := bufio.NewReader(resp.Body)
	for {
		head, err := r.Peek(4)
		if err != nil {
			return nil, fmt.Errorf("reading upload-pack response: %w", err)
		}
		if string(head) == "PACK" {
			break
		}
		line, _, err := readPktLine(r)
		if err != nil {
			return nil, fmt.Errorf("reading upload-pack response: %w", err)
		}
		if msg, ok := strings.CutPrefix(string(line), "ERR "); ok {
			return nil, fmt.Errorf("remote error: %s", strings.TrimSpace(msg))
		}
	}
	return io.ReadAll(r)
}

// writeRef stores hash as the loose ref name (e.g. "refs/heads/main").
func writeRef(gitDir, name, hash string) error {
	if !strings.HasPrefix(name, "refs/") || !filepath.IsLocal(filepath.FromSlash(name)) {
		return fmt.Errorf("refusing to write unsafe ref name %q", name)
	}
	path := filepath.Join(gitDir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(hash+"\n"), 0644)
}

// clone copies the repository at repoURL into the new directory dir.
func clone(repoURL, dir string) error {
	repoURL = strings.TrimRight(repoURL, "/")

	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("destination path '%s' already exists and is not an empty directory", dir)
	}

	// Talk to the remote before touching the disk so a bad URL leaves
	// nothing behind.
	remote, err := discoverRefs(repoURL)
	if err != nil {
		return err
	}

	gitDir := filepath.Join(dir, ".git")
	if err := initRepo(gitDir); err != nil {
		return err
	}
	config := fmt.Sprintf("[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n"+
		"[remote \"origin\"]\n\turl = %s\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n", repoURL)
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0644); err != nil {
		return err
	}

	// Ask for HEAD plus every branch and tag.
	head := remote.Refs["HEAD"]
	var wants []string
	seen := map[string]bool{}
	names := make([]string, 0, len(remote.Refs))
	for name := range remote.Refs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		hash := remote.Refs[name]
		wanted := name == "HEAD" || strings.HasPrefix(name, "refs/heads/") || strings.HasPrefix(name, "refs/tags/")
		if wanted && !seen[hash] {
			seen[hash] = true
			wants = append(wants, hash)
		}
	}
	if len(wants) == 0 {
		return nil // an empty repository: nothing to fetch or check out
	}

	var capabilities []string
	if remote.hasCapability("ofs-delta") {
		capabilities = append(capabilities, "ofs-delta")
	}
	pack, err := fetchPack(repoURL, wants, capabilities)
	if err != nil {
		return err
	}
	if _, err := unpackPack(gitDir, pack); err != nil {
		return err
	}

	// Record the remote's branches and tags.
	for _, name := range names {
		hash := remote.Refs[name]
		if branch, ok := strings.CutPrefix(name, "refs/heads/"); ok {
			err = writeRef(gitDir, "refs/remotes/origin/"+branch, hash)
		} else if strings.HasPrefix(name, "refs/tags/") {
			err = writeRef(gitDir, name, hash)
		}
		if err != nil {
			return err
		}
	}

	if head == "" {
		return nil
	}

	// Point HEAD at the remote's default branch, falling back to any branch
	// at the same commit, or a detached HEAD.
	branch := remote.headSymref()
	if branch == "" || remote.Refs[branch] != head {
		branch = ""
		for _, name := range names {
			if strings.HasPrefix(name, "refs/heads/") && remote.Refs[name] == head {
				branch = name
				break
			}
		}
	}
	headContent := head + "\n"
	if branch != "" {
		if err := writeRef(gitDir, branch, head); err != nil {
			return err
		}
		headContent = "ref: " + branch + "\n"
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(headContent), 0644); err != nil {
		return err
	}

	return checkoutCommit(gitDir, head, dir)
}

// commitTree returns the tree hash recorded in a commit object's content.
func commitTree(content []byte) (string, error) {
	line, _, _ := bytes.Cut(content, []byte("\n"))
	tree, ok := bytes.CutPrefix(line, []byte("tree "))
	if !ok || !isHexHash(string(tree)) {
		return "", fmt.Errorf("malformed commit: missing tree")
	}
	return string(tree), nil
}

// checkoutCommit writes the files of the given commit into dir.
func checkoutCommit(gitDir, commit, dir string) error {
	objType, content, err := readObject(gitDir, commit)
	if err != nil {
		return err
	}
	if objType != "commit" {
		return fmt.Errorf("object %s is a %s, not a commit", commit, objType)
	}
	tree, err := commitTree(content)
	if err != nil {
		return err
	}
	return checkoutTree(gitDir, tree, dir)
}

// checkoutTree recursively writes the contents of a tree object into dir.
func checkoutTree(gitDir, tree, dir string) error {
	objType, content, err := readObject(gitDir, tree)
	if err != nil {
		return err
	}
	if objType != "tree" {
		return fmt.Errorf("object %s is a %s, not a tree", tree, objType)
	}
	entries, err := parseTree(content)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	for _, e := range entries {
		// Never let a hostile tree write outside dir or into .git.
		if e.Name == "" || e.Name == "." || e.Name == ".." || strings.EqualFold(e.Name, ".git") ||
			strings.ContainsAny(e.Name, "/\\\x00") {
			return fmt.Errorf("refusing to check out unsafe path %q", e.Name)
		}
		path := filepath.Join(dir, e.Name)

		switch e.Mode {
		case modeDir:
			if err := checkoutTree(gitDir, e.Hash, path); err != nil {
				return err
			}
		case modeSubmodule:
			// Submodules are not fetched; leave an empty directory like git.
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
		default:
			objType, data, err := readObject(gitDir, e.Hash)
			if err != nil {
				return err
			}
			if objType != "blob" {
				return fmt.Errorf("object %s is a %s, not a blob", e.Hash, objType)
			}
			if e.Mode == modeSymlink {
				err = os.Symlink(string(data), path)
			} else {
				perm := os.FileMode(0644)
				if e.Mode == modeExecutable {
					perm = 0755
				}
				err = os.WriteFile(path, data, perm)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}
