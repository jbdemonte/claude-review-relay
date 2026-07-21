package session

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var ErrLeaseBusy = errors.New("review lease is already held")

type ReviewLease struct {
	file *os.File
}

func AcquireReviewLease(dir, reviewID string) (*ReviewLease, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create review lease directory: %w", err)
	}
	path := reviewLeasePath(dir, reviewID)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open review lease: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLeaseBusy
		}
		return nil, fmt.Errorf("acquire review lease: %w", err)
	}
	return &ReviewLease{file: file}, nil
}

func RemoveReviewLeaseFile(dir, reviewID string) error {
	err := os.Remove(reviewLeasePath(dir, reviewID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove review lease file: %w", err)
	}
	return nil
}

func (l *ReviewLease) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return fmt.Errorf("release review lease: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close review lease: %w", closeErr)
	}
	return nil
}

func reviewLeasePath(dir, reviewID string) string {
	name := fmt.Sprintf("%x.lock", sha256.Sum256([]byte(reviewID)))
	return filepath.Join(dir, name)
}
