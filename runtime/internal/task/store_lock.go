package task

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

type storeProcessLock struct {
	file        *os.File
	registryKey string
	releaseOnce sync.Once
	releaseErr  error
}

var liveStoreLocks = struct {
	sync.Mutex
	paths map[string]struct{}
}{paths: make(map[string]struct{})}

func acquireStoreProcessLock(databasePath string) (*storeProcessLock, error) {
	lockPath := databasePath + ".lock"
	registryKey := canonicalStoreLockPath(lockPath)

	liveStoreLocks.Lock()
	if _, exists := liveStoreLocks.paths[registryKey]; exists {
		liveStoreLocks.Unlock()
		return nil, ErrStoreInUse
	}
	liveStoreLocks.paths[registryKey] = struct{}{}
	liveStoreLocks.Unlock()

	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		forgetStoreLock(registryKey)
		return nil, WrapError(CodeTaskConflict, err)
	}
	if err := tryPlatformStoreLock(file); err != nil {
		_ = file.Close()
		forgetStoreLock(registryKey)
		if isPlatformStoreLockContention(err) {
			return nil, WrapError(CodeStoreInUse, err)
		}
		return nil, WrapError(CodeTaskConflict, err)
	}
	return &storeProcessLock{file: file, registryKey: registryKey}, nil
}

func (l *storeProcessLock) release() error {
	if l == nil {
		return nil
	}
	l.releaseOnce.Do(func() {
		var unlockErr, closeErr error
		if l.file != nil {
			unlockErr = unlockPlatformStoreLock(l.file)
			closeErr = l.file.Close()
		}
		forgetStoreLock(l.registryKey)
		l.releaseErr = errors.Join(unlockErr, closeErr)
	})
	return l.releaseErr
}

func canonicalStoreLockPath(path string) string {
	cleaned := filepath.Clean(path)
	if resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(cleaned)); err == nil {
		cleaned = filepath.Join(resolvedParent, filepath.Base(cleaned))
	}
	if runtime.GOOS == "windows" {
		cleaned = strings.ToLower(cleaned)
	}
	return cleaned
}

func forgetStoreLock(key string) {
	liveStoreLocks.Lock()
	delete(liveStoreLocks.paths, key)
	liveStoreLocks.Unlock()
}
