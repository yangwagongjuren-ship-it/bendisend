package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const defaultRoomID = "default"
const membershipCookie = "lan_transfer_rooms"
const membershipDays = 30
const catalogFile = "room-catalog.json"

var errPolicyChanged = errors.New("retention preview has changed")
var errExpiredInUse = errors.New("expired records are still in use")

type roomPolicy struct {
	Name string `json:"name"`
	Days int    `json:"retentionDays"`
}

func validRetention(days int) bool { return days == 0 || days == 1 || days == 7 || days == 30 }

func (p roomPolicy) valid() bool {
	return p.Name != "" && utf8.RuneCountInString(p.Name) <= 40 && strings.IndexFunc(p.Name, unicode.IsControl) < 0 && validRetention(p.Days)
}

func (p roomPolicy) expired(created, now time.Time) bool {
	return p.Days != 0 && !now.Before(created.Add(time.Duration(p.Days)*24*time.Hour))
}

type roomInfo struct {
	ID         string `json:"id"`
	Generation string `json:"generation"`
	roomPolicy
}

type roomCatalog struct {
	Default string          `json:"default,omitempty"`
	Removed map[string]bool `json:"removed,omitempty"`
}

type roomGroup struct {
	primary        *server
	defaultServer  *server
	rooms          map[string]*room
	handlers       map[string]http.Handler
	dir            string
	defaultsDir    string
	membershipsDir string
	catalog        roomCatalog
}

func openRoomGroup(primary *server) (*roomGroup, error) {
	r := primary.room
	g := &roomGroup{
		primary: primary, rooms: make(map[string]*room), handlers: make(map[string]http.Handler),
		dir: filepath.Join(r.root, "rooms"), membershipsDir: filepath.Join(r.root, "memberships"),
		defaultsDir: filepath.Join(r.root, "default-rooms"),
	}
	if err := readOptionalJSON(filepath.Join(r.root, catalogFile), &g.catalog); err != nil {
		return nil, err
	}
	if g.catalog.Default != "" && !validID(g.catalog.Default) {
		return nil, errors.New("invalid default room generation")
	}
	for id := range g.catalog.Removed {
		if !validID(id) {
			return nil, errors.New("invalid retired room ID")
		}
	}
	for _, dir := range []string{g.dir, g.membershipsDir, g.defaultsDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	current := r
	if g.catalog.Default != "" {
		var err error
		current, err = openNamedRoom(filepath.Join(g.defaultsDir, g.catalog.Default), defaultRoomID)
		if err != nil {
			return nil, err
		}
		current.generation = g.catalog.Default
	}
	g.add(current)
	entries, err := os.ReadDir(g.dir)
	if err != nil {
		g.close()
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) || g.catalog.Removed[entry.Name()] {
			continue
		}
		child, err := openNamedRoom(filepath.Join(g.dir, entry.Name()), entry.Name())
		if err != nil {
			g.close()
			return nil, fmt.Errorf("open room %s: %w", entry.Name(), err)
		}
		g.add(child)
	}
	r.group = g
	g.cleanDeletedLocked()
	return g, nil
}

func (g *roomGroup) close() {
	for _, r := range g.rooms {
		if r != g.primary.room {
			r.lock.Close()
		}
	}
}

func (g *roomGroup) add(r *room) {
	r.group = g
	r.mu = g.primary.room.mu
	s := newServer(r, g.primary.port)
	if r.id == defaultRoomID {
		g.defaultServer = s
	}
	s.uploadSlots = g.primary.uploadSlots
	g.rooms[r.id] = r
	g.handlers[r.id] = s.roomHandler()
}

func (g *roomGroup) create(policy roomPolicy) (roomInfo, error) {
	id, err := randomID()
	if err != nil {
		return roomInfo{}, err
	}
	// Publish the named directory with its policy in place before opening it.
	temp, err := os.MkdirTemp(g.dir, ".new-room-")
	if err != nil {
		return roomInfo{}, err
	}
	defer os.RemoveAll(temp)
	if err := replaceJSON(filepath.Join(temp, "room.json"), policy); err != nil {
		return roomInfo{}, err
	}
	g.primary.room.mu.Lock()
	defer g.primary.room.mu.Unlock()
	dest := filepath.Join(g.dir, id)
	if err := os.Rename(temp, dest); err != nil {
		return roomInfo{}, err
	}
	r, err := openNamedRoom(dest, id)
	if err != nil {
		return roomInfo{}, errors.Join(err, moveToTrash(dest, filepath.Join(g.rooms[defaultRoomID].trashDir, "new-room-"+id)))
	}
	g.add(r)
	return r.infoLocked(), nil
}

func (g *roomGroup) dispatch(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("room")
	g.primary.room.mu.Lock()
	handler := g.handlers[id]
	removed := g.catalog.Removed[id]
	g.primary.room.mu.Unlock()
	if handler == nil {
		if removed {
			if req.URL.Path == "/r/"+id+"/" && req.Method == http.MethodGet {
				// Let the page explain the deletion and offer rooms this browser can still access.
				g.serveDefault(w, req, true)
				return
			}
			roomError(w, errRoomDeleted)
			return
		}
		http.NotFound(w, req)
		return
	}
	http.StripPrefix("/r/"+id, handler).ServeHTTP(w, req)
}

func (g *roomGroup) serveDefault(w http.ResponseWriter, req *http.Request, pageOnly bool) {
	g.primary.room.mu.Lock()
	handler := g.handlers[defaultRoomID]
	g.primary.room.mu.Unlock()
	if pageOnly {
		req = req.Clone(req.Context())
		req.URL.Path, req.URL.RawPath = "/", ""
	}
	handler.ServeHTTP(w, req)
}

func (g *roomGroup) defaultAuthorized(req *http.Request) bool {
	g.primary.room.mu.Lock()
	defer g.primary.room.mu.Unlock()
	return g.defaultServer.legacySession(req)
}

func (g *roomGroup) expireLocked() {
	for _, r := range g.rooms {
		r.expireLocked(r.now())
	}
}

func (g *roomGroup) cleanTrashLocked() {
	for _, r := range g.rooms {
		entries, err := os.ReadDir(r.trashDir)
		if err != nil {
			log.Printf("Read deleted data directory: %v", err)
			continue
		}
		for _, entry := range entries {
			if err := os.RemoveAll(filepath.Join(r.trashDir, entry.Name())); err != nil {
				log.Printf("Retry cleaning deleted data: %v", err)
			}
		}
	}
	g.cleanDeletedLocked()
}

func (r *room) limitOwner() *room {
	if r.group != nil {
		return r.group.primary.room
	}
	return r
}

func (r *room) hostLimitsLocked() uploadLimits { return r.limitOwner().limits }

func (r *room) totalsLocked() (used, pending, materialized int64) {
	if r.group == nil {
		return r.usedBytes, r.reservedBytes, r.materializedBytes
	}
	for _, child := range r.group.rooms {
		used += child.usedBytes
		pending += child.reservedBytes
		materialized += child.materializedBytes
	}
	return
}

func (r *room) expired(created, now time.Time) bool { return r.policy.expired(created, now) }

func (r *room) infoLocked() roomInfo {
	return roomInfo{ID: r.id, Generation: r.generation, roomPolicy: r.policy}
}

func (r *room) retentionPreviewLocked(days int, now time.Time) int {
	policy := roomPolicy{Days: days}
	count := 0
	for _, it := range r.items {
		if !r.expired(it.CreatedAt, now) && policy.expired(it.CreatedAt, now) {
			count++
		}
	}
	for _, m := range r.messages {
		if !r.expired(m.CreatedAt, now) && policy.expired(m.CreatedAt, now) {
			count++
		}
	}
	return count
}

func (r *room) setRetention(days, expected int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleted.Load() {
		return errRoomDeleted
	}
	now := r.now()
	if r.retentionPreviewLocked(days, now) != expected {
		return errPolicyChanged
	}
	policy := r.policy
	policy.Days = days
	// Sweep using the old policy first so extending it cannot revive an expired record.
	r.expireLocked(now)
	for _, it := range r.items {
		if r.expired(it.CreatedAt, now) && !policy.expired(it.CreatedAt, now) {
			return errExpiredInUse
		}
	}
	for _, m := range r.messages {
		if r.expired(m.CreatedAt, now) && !policy.expired(m.CreatedAt, now) {
			return errExpiredInUse
		}
	}
	if err := replaceJSON(filepath.Join(r.root, "room.json"), policy); err != nil {
		return err
	}
	r.policy = policy
	r.revision++
	r.expireLocked(now)
	return nil
}

func (g *roomGroup) membershipsLocked(req *http.Request) (string, map[string]time.Time, error) {
	cookie, err := req.Cookie(membershipCookie)
	if err != nil || !validID(cookie.Value) {
		return "", nil, nil
	}
	var rooms map[string]time.Time
	err = readJSONFile(filepath.Join(g.membershipsDir, cookie.Value+".json"), &rooms)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	return cookie.Value, rooms, err
}

func (g *roomGroup) join(w http.ResponseWriter, req *http.Request, roomID string) error {
	g.primary.room.mu.Lock()
	defer g.primary.room.mu.Unlock()
	if g.catalog.Removed[roomID] {
		return errRoomDeleted
	}
	id, rooms, err := g.membershipsLocked(req)
	if err != nil {
		return err
	}
	if id == "" || rooms == nil {
		id, err = randomID()
		if err != nil {
			return err
		}
		rooms = make(map[string]time.Time)
	}
	now := g.primary.room.now()
	for key, expires := range rooms {
		if !now.Before(expires) {
			delete(rooms, key)
		}
	}
	expires := now.Add(membershipDays * 24 * time.Hour)
	rooms[roomID] = expires
	if err := replaceJSON(filepath.Join(g.membershipsDir, id+".json"), rooms); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: membershipCookie, Value: id, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: membershipDays * 24 * 60 * 60})
	return nil
}

func (g *roomGroup) canEnter(req *http.Request, id string) bool {
	g.primary.room.mu.Lock()
	defer g.primary.room.mu.Unlock()
	if g.catalog.Removed[id] {
		return false
	}
	if onHost(req) && g.defaultServer.legacySession(req) {
		return true
	}
	_, rooms, err := g.membershipsLocked(req)
	return err == nil && g.primary.room.now().Before(rooms[id])
}

func (g *roomGroup) list(req *http.Request) ([]roomInfo, bool, error) {
	g.primary.room.mu.Lock()
	defer g.primary.room.mu.Unlock()
	defaultAccess := g.defaultServer.legacySession(req)
	host := defaultAccess && onHost(req)
	_, rooms, err := g.membershipsLocked(req)
	if err != nil {
		return nil, false, err
	}
	now := g.primary.room.now()
	result := make([]roomInfo, 0)
	for id, r := range g.rooms {
		if host || (id == defaultRoomID && defaultAccess) || now.Before(rooms[id]) {
			result = append(result, r.infoLocked())
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.ID == defaultRoomID || b.ID == defaultRoomID {
			return a.ID == defaultRoomID && b.ID != defaultRoomID
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	return result, host, nil
}
