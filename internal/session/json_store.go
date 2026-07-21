package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
)

type JSONStore struct {
	path string
	mu   sync.Mutex
}

type diskData struct {
	Version  int             `json:"version"`
	Sessions []ReviewSession `json:"sessions"`
}

func (d *diskData) UnmarshalJSON(b []byte) error {
	var wire struct {
		Version  int             `json:"version"`
		Sessions json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	d.Version = wire.Version
	if len(wire.Sessions) == 0 || string(wire.Sessions) == "null" {
		d.Sessions = []ReviewSession{}
		return nil
	}
	if wire.Sessions[0] == '[' {
		return json.Unmarshal(wire.Sessions, &d.Sessions)
	}
	var byID map[string]ReviewSession
	if err := json.Unmarshal(wire.Sessions, &byID); err != nil {
		return fmt.Errorf("sessions must be an array or object: %w", err)
	}
	d.Sessions = make([]ReviewSession, 0, len(byID))
	for id, value := range byID {
		if value.ReviewID == "" {
			value.ReviewID = id
		}
		d.Sessions = append(d.Sessions, value)
	}
	return nil
}

func (d diskData) MarshalJSON() ([]byte, error) {
	byID := make(map[string]ReviewSession, len(d.Sessions))
	for _, value := range d.Sessions {
		byID[value.ReviewID] = value
	}
	return json.Marshal(struct {
		Version  int                      `json:"version"`
		Sessions map[string]ReviewSession `json:"sessions"`
	}{Version: d.Version, Sessions: byID})
}

func NewJSONStore(path string) *JSONStore { return &JSONStore{path: path} }

func (s *JSONStore) Create(ctx context.Context, session ReviewSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.lockFile(true)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	d, err := s.load()
	if err != nil {
		return err
	}
	for _, existing := range d.Sessions {
		if existing.ReviewID == session.ReviewID {
			return fmt.Errorf("review %q already exists", session.ReviewID)
		}
	}
	d.Sessions = append(d.Sessions, session)
	return s.save(d)
}

func (s *JSONStore) Get(ctx context.Context, id string) (ReviewSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.lockFile(false)
	if err != nil {
		return ReviewSession{}, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return ReviewSession{}, err
	}
	d, err := s.load()
	if err != nil {
		return ReviewSession{}, err
	}
	for _, v := range d.Sessions {
		if v.ReviewID == id {
			return v, nil
		}
	}
	return ReviewSession{}, ErrNotFound
}

func (s *JSONStore) Update(ctx context.Context, session ReviewSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.lockFile(true)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	d, err := s.load()
	if err != nil {
		return err
	}
	for i := range d.Sessions {
		if d.Sessions[i].ReviewID == session.ReviewID {
			d.Sessions[i] = session
			return s.save(d)
		}
	}
	return ErrNotFound
}

func (s *JSONStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.lockFile(true)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	d, err := s.load()
	if err != nil {
		return err
	}
	for i := range d.Sessions {
		if d.Sessions[i].ReviewID == id {
			d.Sessions = append(d.Sessions[:i], d.Sessions[i+1:]...)
			return s.save(d)
		}
	}
	return ErrNotFound
}

func (s *JSONStore) List(ctx context.Context) ([]ReviewSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.lockFile(false)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	result := append([]ReviewSession(nil), d.Sessions...)
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}

func (s *JSONStore) load() (diskData, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return diskData{Version: 1, Sessions: []ReviewSession{}}, nil
	}
	if err != nil {
		return diskData{}, fmt.Errorf("read sessions: %w", err)
	}
	var d diskData
	if err := json.Unmarshal(b, &d); err != nil {
		return diskData{}, fmt.Errorf("parse sessions: %w", err)
	}
	if d.Version != 1 {
		return diskData{}, fmt.Errorf("unsupported sessions version %d", d.Version)
	}
	if d.Sessions == nil {
		d.Sessions = []ReviewSession{}
	}
	return d, nil
}

func (s *JSONStore) save(d diskData) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".sessions-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary sessions file: %w", err)
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		_ = f.Close()
		return fmt.Errorf("encode sessions: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync sessions: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close sessions: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace sessions: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(s.path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	ok = true
	return nil
}

func (s *JSONStore) Validate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.lockFile(false)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err = s.load()
	return err
}

func (s *JSONStore) LeaseDir() string {
	return filepath.Join(filepath.Dir(s.path), "leases")
}

func (s *JSONStore) lockFile(exclusive bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, fmt.Errorf("create data directory for lock: %w", err)
	}
	path := filepath.Join(filepath.Dir(s.path), ".sessions.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open sessions lock: %w", err)
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(file.Fd()), mode); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock sessions store: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
