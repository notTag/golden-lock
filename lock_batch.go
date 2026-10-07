package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

type lockFailure struct {
	path string
	err  error
	code int
}

func reportLockFailures(failures []lockFailure) {
	for _, failure := range failures {
		fmt.Fprintf(os.Stderr, "%s lock: failed %q: %v\n", progName(), failure.path, failure.err)
	}
}

func lockFailureCode(failures []lockFailure) int {
	code := ExitWriteOK
	for _, failure := range failures {
		if failure.code > code {
			code = failure.code
		}
	}
	return code
}

type fileLockState struct {
	uid, gid  int
	mode      os.FileMode
	immutable bool
}

func captureLockState(f *os.File) (fileLockState, error) {
	info, err := f.Stat()
	if err != nil {
		return fileLockState{}, err
	}
	// Comma-ok, not a bare assertion: a panic here would unwind past the caller's
	// rollback, leaving a file frozen with nothing recorded in the manifest — the
	// one state lock must never reach, since unlock resolves its root FROM the
	// manifest. Matches the comma-ok form ReadManifest uses on the same call.
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileLockState{}, fmt.Errorf("cannot read ownership of %s: unexpected stat type %T", f.Name(), info.Sys())
	}
	immutable, err := immutableFD(int(f.Fd()))
	if err != nil && !immutableUnsupported(err) {
		return fileLockState{}, err
	}
	return fileLockState{int(st.Uid), int(st.Gid), info.Mode(), immutable}, nil
}

func restoreLockState(f *os.File, state fileLockState) error {
	if err := clearImmutable(f); err != nil {
		return err
	}
	// Chown can clear setuid/setgid, so restore the mode afterwards. Attempt all
	// remaining steps even if one fails, including re-protecting existing locks.
	ownerErr := f.Chown(state.uid, state.gid)
	modeErr := f.Chmod(state.mode)
	var flagErr error
	if state.immutable {
		_, flagErr = applyImmutable(f)
	}
	return errors.Join(ownerErr, modeErr, flagErr)
}

// Explicit dependencies let failure tests exercise the transaction without
// requiring privileged ownership changes or immutable flags on the test host.
type lockOps struct {
	capture   func(*os.File) (fileLockState, error)
	restore   func(*os.File, fileLockState) error
	freeze    func(*os.File) error
	hash      func(string, io.Reader) (string, error)
	immutable func(*os.File) (bool, error)
	publish   func(*Manifest) error
}

func defaultLockOps() lockOps {
	return lockOps{captureLockState, restoreLockState, LockFileFD, hashReader, applyImmutable, WriteManifestLocked}
}

// lockBatch resolves every target before changing any file. Independent errors
// do not discard successful locks: only those successes enter the manifest.
// Rollback always uses the pinned fd, including when the path has been renamed.
func lockBatch(m *Manifest, paths []string, ops lockOps) []lockFailure {
	type target struct {
		path, rel string
		f         *os.File
		before    fileLockState
		hash      string
		immutable bool
	}
	var targets []*target
	var failures []lockFailure
	for _, path := range paths {
		rel, err := NormalizePath(m.Root, path)
		if err != nil {
			failures = append(failures, lockFailure{path, err, ExitWriteArgs})
			continue
		}
		f, err := openWritableResolved(m.Root, rel, m.AbsPath(rel))
		if err != nil {
			code := ExitWriteIO
			if errors.Is(err, ErrSymlink) {
				code = ExitWriteArgs
			}
			failures = append(failures, lockFailure{path, err, code})
			continue
		}
		targets = append(targets, &target{path: path, rel: rel, f: f})
	}
	defer func() {
		for _, t := range targets {
			t.f.Close()
		}
	}()

	rollback := func(t *target) {
		if err := ops.restore(t.f, t.before); err != nil {
			failures = append(failures, lockFailure{t.path, fmt.Errorf("rollback failed; manual recovery required: %w", err), ExitWriteIO})
		}
	}
	var pending []*target
	for _, t := range targets {
		// Snapshot immediately before mutation, so hard-link aliases capture the
		// state left by an earlier successful target of the same inode.
		before, err := ops.capture(t.f)
		if err != nil {
			failures = append(failures, lockFailure{t.path, fmt.Errorf("read original state: %w", err), ExitWriteIO})
			continue
		}
		t.before = before
		err = ops.freeze(t.f)
		if err == nil {
			_, err = t.f.Seek(0, io.SeekStart)
		}
		if err == nil {
			t.hash, err = ops.hash(t.rel, t.f)
		}
		if err == nil {
			t.immutable, err = ops.immutable(t.f)
		}
		if err != nil {
			failures = append(failures, lockFailure{t.path, err, ExitWriteIO})
			rollback(t)
			continue
		}
		pending = append(pending, t)
	}
	if len(pending) == 0 {
		return failures
	}

	// Keep the original entries intact until publication succeeds.
	next := *m
	next.Entries = append([]Entry(nil), m.Entries...)
	for _, t := range pending {
		next.Upsert(t.rel, t.hash)
	}
	if err := ops.publish(&next); err != nil {
		failures = append(failures, lockFailure{m.Path, err, ExitWriteIO})
		if !errors.Is(err, ErrManifestPublished) {
			// Reverse order also restores hard-link aliases to their original state.
			for i := len(pending) - 1; i >= 0; i-- {
				t := pending[i]
				rollback(t)
				failures = append(failures, lockFailure{t.path, errors.New("not recorded: manifest publication failed"), ExitWriteIO})
			}
			return failures
		}
	}
	for _, t := range pending {
		if i := m.Find(t.rel); i >= 0 {
			if m.Entries[i].Hash == t.hash {
				fmt.Printf("note: %s already locked, unchanged\n", t.rel)
			} else {
				fmt.Printf("note: %s already locked; updating recorded hash to match current content\n", t.rel)
			}
		}
		if t.immutable {
			fmt.Printf("locked %s\n", t.rel)
		} else {
			fmt.Printf("locked %s  (warning: this filesystem does not support the immutable flag; tamper is detected by `verify` but not prevented)\n", t.rel)
		}
	}
	*m = next
	return failures
}
