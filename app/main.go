package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Usage: your_program.sh <command> <arg1> <arg2> ...
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: mygit <command> [<args>...]\n")
		os.Exit(1)
	}

	if err := run(os.Args[1], os.Args[2:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %s\n", err)
		os.Exit(1)
	}
}

// run executes a single git command in the current working directory,
// writing its output to stdout.
func run(command string, args []string, stdout io.Writer) error {
	switch command {
	case "init":
		return cmdInit(stdout)
	case "cat-file":
		return cmdCatFile(args, stdout)
	case "hash-object":
		return cmdHashObject(args, stdout)
	case "ls-tree":
		return cmdLsTree(args, stdout)
	case "write-tree":
		return cmdWriteTree(args, stdout)
	case "commit-tree":
		return cmdCommitTree(args, stdout)
	case "clone":
		return cmdClone(args)
	default:
		return fmt.Errorf("unknown command %s", command)
	}
}

// initRepo creates the skeleton of a git directory at gitDir.
func initRepo(gitDir string) error {
	for _, dir := range []string{gitDir, filepath.Join(gitDir, "objects"), filepath.Join(gitDir, "refs")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating directory: %w", err)
		}
	}

	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0644); err != nil {
		return fmt.Errorf("writing HEAD: %w", err)
	}
	return nil
}

func cmdInit(stdout io.Writer) error {
	if err := initRepo(".git"); err != nil {
		return err
	}

	fmt.Fprintln(stdout, "Initialized git directory")
	return nil
}

func cmdCatFile(args []string, stdout io.Writer) error {
	if len(args) != 2 || args[0] != "-p" {
		return fmt.Errorf("usage: mygit cat-file -p <object>")
	}

	_, content, err := readObject(".git", args[1])
	if err != nil {
		return err
	}

	_, err = stdout.Write(content)
	return err
}

func cmdHashObject(args []string, stdout io.Writer) error {
	write := false
	var files []string
	for _, arg := range args {
		if arg == "-w" {
			write = true
		} else {
			files = append(files, arg)
		}
	}
	if len(files) != 1 {
		return fmt.Errorf("usage: mygit hash-object [-w] <file>")
	}

	content, err := os.ReadFile(files[0])
	if err != nil {
		return err
	}

	var hash string
	if write {
		if hash, err = writeObject(".git", "blob", content); err != nil {
			return err
		}
	} else {
		hash, _ = hashObject("blob", content)
	}

	fmt.Fprintln(stdout, hash)
	return nil
}

func cmdLsTree(args []string, stdout io.Writer) error {
	nameOnly := false
	var hashes []string
	for _, arg := range args {
		if arg == "--name-only" {
			nameOnly = true
		} else {
			hashes = append(hashes, arg)
		}
	}
	if len(hashes) != 1 {
		return fmt.Errorf("usage: mygit ls-tree [--name-only] <tree>")
	}

	objType, content, err := readObject(".git", hashes[0])
	if err != nil {
		return err
	}
	if objType != "tree" {
		return fmt.Errorf("not a tree object")
	}

	entries, err := parseTree(content)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if nameOnly {
			fmt.Fprintln(stdout, e.Name)
		} else {
			fmt.Fprintf(stdout, "%06s %s %s\t%s\n", e.Mode, e.Type(), e.Hash, e.Name)
		}
	}
	return nil
}

func cmdWriteTree(args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: mygit write-tree")
	}

	hash, _, err := writeTree(".git", ".")
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, hash)
	return nil
}

func cmdCommitTree(args []string, stdout io.Writer) error {
	const usage = "usage: mygit commit-tree <tree> [-p <parent>]... -m <message>"

	var tree string
	var parents, messages []string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "-p", "-m":
			if i+1 >= len(args) {
				return fmt.Errorf("option %s requires a value\n%s", arg, usage)
			}
			i++
			if arg == "-p" {
				parents = append(parents, args[i])
			} else {
				messages = append(messages, args[i])
			}
		default:
			if tree != "" {
				return fmt.Errorf("%s", usage)
			}
			tree = arg
		}
	}
	if tree == "" || len(messages) == 0 {
		return fmt.Errorf("%s", usage)
	}

	for _, hash := range append([]string{tree}, parents...) {
		if !isHexHash(hash) {
			return fmt.Errorf("not a valid object name %s", hash)
		}
	}

	// Like git, multiple -m values become separate paragraphs.
	message := strings.Join(messages, "\n\n")
	content := encodeCommit(tree, parents, defaultSignature("AUTHOR"), defaultSignature("COMMITTER"), message)
	hash, err := writeObject(".git", "commit", content)
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, hash)
	return nil
}

func cmdClone(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: mygit clone <url> <directory>")
	}
	return clone(args[0], args[1])
}
