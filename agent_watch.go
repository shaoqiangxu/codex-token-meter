package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Notifications only wake the existing checkpoint reader. They are not usage
// events. Overflow falls back to discovery; nothing is acknowledged or dropped
// from the persistent spool. Memory is bounded even during a directory import.
type sourceWatcher struct {
	mu      sync.Mutex
	watcher *fsnotify.Watcher
	pending map[string]bool
	rescan  bool
	wake    chan struct{}
	homes   []string
}

func watchSources(homes []string) (*sourceWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	s := &sourceWatcher{watcher: w, pending: map[string]bool{}, wake: make(chan struct{}, 1), homes: append([]string(nil), homes...)}
	for _, home := range homes {
		// Watch the home too: sessions may be created after Meter starts.
		if err = w.Add(home); err != nil {
			w.Close()
			return nil, err
		}
		for _, directory := range []string{"sessions", "archived_sessions"} {
			if err = s.addTree(filepath.Join(home, directory)); err != nil && !os.IsNotExist(err) {
				w.Close()
				return nil, err
			}
		}
	}
	go func() {
		for {
			select {
			case event, ok := <-w.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) == 0 {
					continue
				}
				if !inLogTree(event.Name, s.homes) {
					continue
				}
				if st, err := os.Stat(event.Name); err == nil && st.IsDir() {
					// Only Codex log trees, never arbitrary home subdirectories.
					p := filepath.ToSlash(event.Name) + "/"
					if strings.Contains(p, "/sessions/") || strings.Contains(p, "/archived_sessions/") {
						if s.addTree(event.Name) != nil {
							s.overflow()
						}
					}
				} else if strings.HasSuffix(strings.ToLower(event.Name), ".jsonl") {
					s.add(event.Name)
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
				s.overflow()
			}
		}
	}()
	return s, nil
}

func inLogTree(path string, homes []string) bool {
	for _, home := range homes {
		for _, directory := range []string{"sessions", "archived_sessions"} {
			rel, err := filepath.Rel(filepath.Join(home, directory), path)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				return true
			}
		}
	}
	return false
}
func (s *sourceWatcher) addTree(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return s.watcher.Add(path)
		}
		if strings.HasSuffix(strings.ToLower(path), ".jsonl") {
			s.add(path)
		}
		return nil
	})
}
func (s *sourceWatcher) add(path string) {
	s.mu.Lock()
	if len(s.pending) < 1024 {
		s.pending[path] = true
	} else {
		s.rescan = true
	}
	s.mu.Unlock()
	wakeAgent(s.wake)
}
func (s *sourceWatcher) overflow() { s.mu.Lock(); s.rescan = true; s.mu.Unlock(); wakeAgent(s.wake) }
func (s *sourceWatcher) drain() (map[string]bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, r := s.pending, s.rescan
	s.pending = map[string]bool{}
	s.rescan = false
	return p, r
}
