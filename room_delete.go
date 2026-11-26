package main

import (
	"errors"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
)

var errRoomDeleted = errors.New("room deleted")
var errRoomGeneration = errors.New("room generation changed")

type roomReader struct {
	room *room
	body io.Reader
}

func (r roomReader) Read(p []byte) (int, error) {
	if r.room.deleted.Load() {
		return 0, errRoomDeleted
	}
	return r.body.Read(p)
}

func (g *roomGroup) delete(req *http.Request, id, generation string) (*server, error) {
	g.primary.room.mu.Lock()
	defer g.primary.room.mu.Unlock()
	if !onHost(req) || !g.defaultServer.legacySession(req) {
		return nil, errRoomGeneration
	}
	r := g.rooms[id]
	if r == nil {
		return nil, errRoomDeleted
	}
	if r.generation != generation {
		return nil, errRoomGeneration
	}

	// The catalog is the commit point. Never retire a room until that update is saved.
	next := roomCatalog{Default: g.catalog.Default, Removed: maps.Clone(g.catalog.Removed)}
	var replacement *room
	if id == defaultRoomID {
		key, err := randomID()
		if err != nil {
			return nil, err
		}
		next.Default = key
		replacement, err = openNamedRoom(filepath.Join(g.defaultsDir, key), defaultRoomID)
		if err != nil {
			return nil, err
		}
		replacement.generation = key
	} else {
		if next.Removed == nil {
			next.Removed = make(map[string]bool)
		}
		next.Removed[id] = true
	}
	if err := replaceJSON(filepath.Join(g.primary.room.root, catalogFile), next); err != nil {
		if replacement != nil {
			replacement.lock.Close()
		}
		return nil, err
	}
	g.catalog = next
	r.deleted.Store(true)
	clear(r.items)
	clear(r.messages)
	clear(r.transfers)
	clear(r.devices)
	delete(g.rooms, id)
	delete(g.handlers, id)
	if r != g.primary.room {
		r.lock.Close()
	}
	if replacement != nil {
		g.add(replacement)
	}
	return g.defaultServer, nil
}

func (g *roomGroup) cleanDeletedLocked() {
	for id := range g.catalog.Removed {
		removeRetiredPath(filepath.Join(g.dir, id))
	}
	entries, err := os.ReadDir(g.defaultsDir)
	if err != nil {
		log.Printf("Read default room generations: %v", err)
	} else {
		for _, entry := range entries {
			if entry.IsDir() && validID(entry.Name()) && entry.Name() != g.catalog.Default {
				removeRetiredPath(filepath.Join(g.defaultsDir, entry.Name()))
			}
		}
	}
	if g.catalog.Default != "" {
		// Only retire room payloads. The installation lock, device identities,
		// memberships and quota settings still belong to the original root.
		for _, name := range []string{"items", "messages", "uploads", "trash"} {
			removeRetiredPath(filepath.Join(g.primary.room.root, name))
		}
		entries, err := os.ReadDir(g.primary.room.stagingDir)
		if err == nil {
			for _, entry := range entries {
				removeRetiredPath(filepath.Join(g.primary.room.stagingDir, entry.Name()))
			}
		}
	}
}

func removeRetiredPath(path string) {
	if err := os.RemoveAll(path); err != nil {
		log.Printf("Retry deleted room cleanup: %v", err)
	}
}
