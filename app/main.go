package main

import (
	"fmt"
	"io"
	"os"
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
	default:
		return fmt.Errorf("unknown command %s", command)
	}
}

func cmdInit(stdout io.Writer) error {
	for _, dir := range []string{".git", ".git/objects", ".git/refs"} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating directory: %w", err)
		}
	}

	if err := os.WriteFile(".git/HEAD", []byte("ref: refs/heads/main\n"), 0644); err != nil {
		return fmt.Errorf("writing HEAD: %w", err)
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
