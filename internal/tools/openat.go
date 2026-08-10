package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file holds the containment primitive the filesystem tool opens every
// path through: a walk from a trusted root that resolves each component with
// openat(2) relative to the previous one and refuses any component that would
// leave the root.
//
// The previous scheme — resolve the path, check containment, then open the
// final component with O_NOFOLLOW — was not sufficient. O_NOFOLLOW constrains
// only the last component of the path handed to open(2); the kernel still
// resolves every intermediate component through symlinks. So an attacker who
// can create files in the workspace (the shell tool can) could let resolvePath
// bless ws/sub/file.txt, replace ws/sub with a symlink pointing outside, and
// the open that followed would land outside the workspace with no error. Read
// paths were worse still: they used os.ReadFile with no O_NOFOLLOW at all, so
// swapping the leaf for a symlink to ~/.ssh/id_rsa was enough.
//
// os.Root (Go 1.24) implements the walk, so this is a thin adapter over it
// rather than hand-rolled syscalls: it holds the root open as a descriptor and
// performs each lookup with openat, so component names cannot be re-pointed
// between the check and the open, and it is maintained and fuzzed upstream on
// every platform this builds for. Its escape error is "path escapes from
// parent".
//
// The root itself is opened by name. It is operator configuration, not attacker
// input, and a workspace that legitimately lives behind a symlink (macOS /tmp
// -> /private/tmp) has to keep working.

// openDirFlag is OR-ed into openInRoot's flags by callers that want a directory
// and nothing else. os.Root already refuses to traverse out of the root, so it
// is only a type assertion on the leaf; it is a named constant so the intent
// survives.
const openDirFlag = 0

// relInRoot returns path expressed relative to root, erroring if it is not
// lexically inside. os.Root would reject an escaping name itself, but doing it
// here first gives the caller the containment wording rather than a syscall
// error, and keeps ".." out of the name handed to os.Root at all.
func relInRoot(root, path string) (string, error) {
	// Both sides are made absolute first: a caller may hold a workspace or a
	// path relative to the process working directory, and filepath.Rel cannot
	// relate a relative path to an absolute one at all.
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("path %s is outside %s", path, root)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %s is outside %s", path, root)
	}
	return rel, nil
}

// openInRoot opens path with the given flags, contained under root. Every
// component below root is traversed with openat, so a component that has become
// a symlink out of the root fails instead of being followed.
func openInRoot(root, path string, flags int, perm os.FileMode) (*os.File, error) {
	rel, err := relInRoot(root, path)
	if err != nil {
		return nil, err
	}

	r, err := os.OpenRoot(filepath.Clean(root))
	if err != nil {
		return nil, err
	}
	defer r.Close()

	return r.OpenFile(rel, flags, perm)
}

// mkdirAllIn creates dir and any missing parents, contained under root. It
// exists because os.MkdirAll resolves intermediate components through symlinks,
// which is precisely the escape: MkdirAll on ws/sub/deep where ws/sub is a
// symlink to /outside creates /outside/deep and reports success.
func mkdirAllIn(root, dir string) error {
	rel, err := relInRoot(root, dir)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}

	r, err := os.OpenRoot(filepath.Clean(root))
	if err != nil {
		return err
	}
	defer r.Close()

	// Walk down creating each level. os.Root.MkdirAll is Go 1.25; this module
	// targets 1.24, so the loop stands in for it. An EEXIST is fine — but only
	// after Lstat confirms the existing entry is a directory and not a symlink,
	// because os.Root.Mkdir on a name that is already a symlink returns EEXIST
	// too, and treating that as "already there" would hand the next level
	// straight back to the link.
	cur := ""
	for _, comp := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, comp)
		if err := r.Mkdir(cur, 0o755); err != nil {
			if !os.IsExist(err) {
				return err
			}
			fi, lerr := r.Lstat(cur)
			if lerr != nil {
				return lerr
			}
			if !fi.IsDir() {
				return fmt.Errorf("mkdir %s: not a directory", filepath.Join(root, cur))
			}
		}
	}
	return nil
}
