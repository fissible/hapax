// Package publish atomically publishes rewritten document bytes.
package publish

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var (
	// ErrExists reports that Create's destination was already occupied.
	ErrExists = errors.New("publication destination exists")
	// ErrAliasesInput reports that Create's destination names its input file.
	ErrAliasesInput = errors.New("publication destination aliases input")
)

type deps struct {
	EvalSymlinks func(string) (string, error)
	Stat         func(string) (os.FileInfo, error)
	CreateTemp   func(string, string) (*os.File, error)
	Write        func(*os.File, []byte) (int, error)
	Chmod        func(*os.File, os.FileMode) error
	Sync         func(*os.File) error
	Close        func(*os.File) error
	Link         func(string, string) error
	Rename       func(string, string) error
	Remove       func(string) error
	OpenDir      func(string) (*os.File, error)
}

func realDeps() deps {
	return deps{
		EvalSymlinks: filepath.EvalSymlinks,
		Stat:         os.Stat,
		CreateTemp:   os.CreateTemp,
		Write: func(file *os.File, content []byte) (int, error) {
			return file.Write(content)
		},
		Chmod: func(file *os.File, mode os.FileMode) error {
			return file.Chmod(mode)
		},
		Sync: func(file *os.File) error {
			return file.Sync()
		},
		Close: func(file *os.File) error {
			return file.Close()
		},
		Link:    os.Link,
		Rename:  os.Rename,
		Remove:  os.Remove,
		OpenDir: os.Open,
	}
}

func Create(source, destination string, content []byte) error {
	return create(realDeps(), source, destination, content)
}

func Replace(source string, content []byte) error {
	return replace(realDeps(), source, content)
}

func create(d deps, source, destination string, content []byte) error {
	if source == "" || destination == "" {
		return errors.New("publication path is empty")
	}

	input, info, err := resolveRegular(d, source)
	if err != nil {
		return err
	}
	parent, err := d.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return err
	}
	target := filepath.Join(parent, filepath.Base(destination))
	if target == input {
		return ErrAliasesInput
	}
	if destinationInfo, statErr := d.Stat(target); statErr == nil {
		if os.SameFile(info, destinationInfo) {
			return ErrAliasesInput
		}
		return ErrExists
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	return stageAndPublish(d, parent, target, content, info.Mode().Perm(), d.Link, true)
}

func replace(d deps, source string, content []byte) error {
	if source == "" {
		return errors.New("publication path is empty")
	}

	target, info, err := resolveRegular(d, source)
	if err != nil {
		return err
	}
	return stageAndPublish(d, filepath.Dir(target), target, content, info.Mode().Perm(), d.Rename, false)
}

func resolveRegular(d deps, path string) (string, os.FileInfo, error) {
	resolved, err := d.EvalSymlinks(path)
	if err != nil {
		return "", nil, err
	}
	info, err := d.Stat(resolved)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("publication source %q is not a regular file", path)
	}
	return resolved, info, nil
}

func stageAndPublish(d deps, directory, destination string, content []byte, mode os.FileMode, publish func(string, string) error, linked bool) (err error) {
	staged, err := d.CreateTemp(directory, ".hapax-*")
	if err != nil {
		return err
	}
	staging := staged.Name()
	closed := false
	published := false
	defer func() {
		// Closing releases the descriptor; removing only unlinks its name. Do
		// this first on every exit, including failures before the explicit close.
		if !closed {
			closed = true
			if closeErr := d.Close(staged); err == nil {
				err = closeErr
			}
		}
		if !published || linked {
			if cleanupErr := d.Remove(staging); err == nil {
				err = cleanupErr
			}
		}
	}()

	if n, writeErr := d.Write(staged, content); writeErr != nil {
		return writeErr
	} else if n != len(content) {
		return io.ErrShortWrite
	}
	if err := d.Chmod(staged, mode); err != nil {
		return err
	}
	if err := d.Sync(staged); err != nil {
		return err
	}
	closed = true
	if err := d.Close(staged); err != nil {
		return err
	}
	if err := publish(staging, destination); err != nil {
		if linked && errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %v", ErrExists, err)
		}
		return err
	}
	published = true

	directoryFile, err := d.OpenDir(directory)
	if err != nil {
		return err
	}
	if err := d.Sync(directoryFile); err != nil {
		_ = d.Close(directoryFile)
		return err
	}
	return d.Close(directoryFile)
}
