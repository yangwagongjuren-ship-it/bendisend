package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxTextBytes = 16 << 10
	maxFileBytes = 100 << 20
	maxRoomItems = 200
	retention    = 7 * 24 * time.Hour
)

var (
	errRoomInUse = errors.New("room already open")
	errFull      = errors.New("room is full")
	errTooLarge  = errors.New("file is too large")
	errMissing   = errors.New("item is missing")
)

type item struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text,omitempty"`
	Name      string    `json:"name,omitempty"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"createdAt"`
}

type room struct {
	id         string
	group      *roomGroup
	policy     roomPolicy
	generation string
	deleted    atomic.Bool
	root       string
	itemsDir   string
	stagingDir string
	trashDir   string
	secret     string
	lock       *os.File
	now        func() time.Time
	bootID     string
	mu         *sync.Mutex
	items      map[string]item
	usedBytes  int64
	revision   uint64
	roomFiles
}

func openRoom(root string) (*room, error) {
	return openRoomAt(root, defaultRoomID, true)
}

func openNamedRoom(root, id string) (*room, error) {
	return openRoomAt(root, id, false)
}

func openRoomAt(root, id string, anchor bool) (*room, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	lock, err := lockRoom(filepath.Join(root, "room.lock"))
	if err != nil {
		return nil, fmt.Errorf("open room lock (check for another running instance): %w", err)
	}
	r := &room{
		root: root, id: id, generation: id, mu: &sync.Mutex{},
		itemsDir: filepath.Join(root, "items"), stagingDir: filepath.Join(root, "staging"),
		trashDir: filepath.Join(root, "trash"), lock: lock, now: time.Now, items: make(map[string]item),
	}
	if anchor {
		var catalog roomCatalog
		if err := readOptionalJSON(filepath.Join(root, catalogFile), &catalog); err != nil {
			lock.Close()
			return nil, err
		}
		r.deleted.Store(catalog.Default != "")
	}
	if err := r.load(root); err != nil {
		lock.Close()
		return nil, err
	}
	return r, nil
}

func (r *room) load(root string) error {
	r.policy = roomPolicy{Name: "默认房间", Days: 7}
	if r.deleted.Load() {
		if err := os.MkdirAll(r.stagingDir, 0700); err != nil {
			return err
		}
		if err := os.MkdirAll(r.trashDir, 0700); err != nil {
			return err
		}
		var err error
		r.secret, err = loadSecret(filepath.Join(root, "invite.key"), r.stagingDir)
		if err != nil {
			return err
		}
		return r.loadRoomFiles()
	}
	var policyErr error
	if r.id == defaultRoomID {
		policyErr = readOptionalJSON(filepath.Join(root, "room.json"), &r.policy)
	} else {
		policyErr = readJSONFile(filepath.Join(root, "room.json"), &r.policy)
	}
	if policyErr != nil {
		return policyErr
	}
	if !r.policy.valid() {
		return errors.New("invalid room policy")
	}
	for _, dir := range []string{r.stagingDir, r.trashDir} {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	for _, dir := range []string{r.itemsDir, r.stagingDir, r.trashDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	var err error
	if r.secret, err = loadSecret(filepath.Join(root, "invite.key"), r.stagingDir); err != nil {
		return err
	}
	if r.bootID, err = randomID(); err != nil {
		return err
	}
	entries, err := os.ReadDir(r.itemsDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		it, err := loadItem(filepath.Join(r.itemsDir, entry.Name()), entry.Name())
		if err != nil {
			return fmt.Errorf("read item %s: %w", entry.Name(), err)
		}
		r.items[it.ID] = it
		r.usedBytes += it.Size
	}
	if err := r.loadRoomFiles(); err != nil {
		return err
	}
	r.expireLocked(r.now())
	return nil
}

func loadItem(dir, id string) (item, error) {
	var it item
	data, err := os.ReadFile(filepath.Join(dir, "item.json"))
	if err != nil {
		return it, err
	}
	if err := json.Unmarshal(data, &it); err != nil {
		return it, err
	}
	if it.ID != id || it.CreatedAt.IsZero() || it.Size < 0 {
		return it, errors.New("invalid item metadata")
	}
	switch it.Kind {
	case "text":
		if int64(len(it.Text)) != it.Size || it.Size > maxTextBytes {
			return it, errors.New("invalid text length")
		}
	case "file":
		file, err := os.Stat(filepath.Join(dir, "payload"))
		if err != nil {
			return it, err
		}
		if !file.Mode().IsRegular() || it.Size > maxFileBytes || file.Size() != it.Size || it.Name == "" {
			return it, errors.New("invalid file payload")
		}
	default:
		return it, errors.New("invalid item type")
	}
	return it, nil
}

func loadSecret(path, staging string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		secret := string(data)
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(secret)
		if err != nil || len(decoded) != 32 {
			return "", errors.New("invalid invite.key")
		}
		return secret, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(key)
	temp := filepath.Join(staging, "invite.key")
	if err := writeSynced(temp, []byte(secret)); err != nil {
		return "", err
	}
	return secret, os.Rename(temp, path)
}

func randomID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func validID(id string) bool {
	if len(id) != 32 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	return errors.Join(writeErr, f.Close())
}

func (r *room) addText(text string) (item, error) {
	return r.add(item{Kind: "text", Text: text, Size: int64(len(text))}, nil, int64(len(text)))
}

func (r *room) addFile(name string, body io.Reader, length int64) (item, error) {
	return r.add(item{Kind: "file", Name: name}, body, length)
}

func (r *room) add(it item, body io.Reader, reserved int64) (item, error) {
	r.mu.Lock()
	if body != nil {
		limit := min(r.hostLimitsLocked().MaxFileBytes, int64(maxFileBytes))
		if reserved < 0 {
			reserved = limit
		}
		if reserved > limit {
			r.mu.Unlock()
			return item{}, &quotaProblem{kind: "file_limit", limit: limit, required: reserved}
		}
	}
	err := r.reserveLocked(reserved, 1)
	r.mu.Unlock()
	if err != nil {
		return item{}, err
	}
	defer func() {
		r.mu.Lock()
		r.releaseLocked(reserved, 0, 1)
		r.mu.Unlock()
	}()
	id, err := randomID()
	if err != nil {
		return item{}, err
	}
	it.ID = id
	staged := filepath.Join(r.stagingDir, id)
	if err := os.Mkdir(staged, 0700); err != nil {
		return item{}, err
	}
	defer os.RemoveAll(staged)
	if body != nil {
		it.Size, err = writePayload(filepath.Join(staged, "payload"), roomReader{r, body}, reserved)
		if err != nil {
			return item{}, err
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleted.Load() {
		return item{}, errRoomDeleted
	}
	now := r.now().UTC()
	r.expireLocked(now)
	it.CreatedAt = now
	metadata, err := json.Marshal(it)
	if err != nil {
		return item{}, err
	}
	if err := writeSynced(filepath.Join(staged, "item.json"), metadata); err != nil {
		return item{}, err
	}
	if err := os.Rename(staged, filepath.Join(r.itemsDir, id)); err != nil {
		return item{}, err
	}
	r.items[id] = it
	r.usedBytes += it.Size
	r.revision++
	return it, nil
}

func writePayload(path string, body io.Reader, limit int64) (int64, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(body, limit+1))
	if n > limit {
		err = errTooLarge
	}
	if err == nil {
		err = f.Sync()
	}
	return n, errors.Join(err, f.Close())
}

func (r *room) snapshot(ifNoneMatch string) (items []item, usedBytes int64, limits uploadLimits, etag string, unchanged bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.expireLocked(now)
	// Include pending expirations in the tag if an open download is holding a file.
	expired := 0
	for _, it := range r.items {
		if r.expired(it.CreatedAt, now) {
			expired++
		}
	}
	etag = `"` + r.bootID + `-` + strconv.FormatUint(r.revision, 10) + `-` + strconv.Itoa(expired) + `"`
	if ifNoneMatch == etag {
		return nil, 0, uploadLimits{}, etag, true
	}
	items = make([]item, 0, len(r.items)-expired)
	for _, it := range r.items {
		if !r.expired(it.CreatedAt, now) {
			items = append(items, it)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	used, _, _ := r.totalsLocked()
	return items, used, r.hostLimitsLocked(), etag, false
}

func (r *room) openFile(id string) (item, *os.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleted.Load() {
		return item{}, nil, errRoomDeleted
	}
	it, ok := r.items[id]
	if !ok || it.Kind != "file" || r.expired(it.CreatedAt, r.now()) {
		return item{}, nil, errMissing
	}
	f, err := os.Open(filepath.Join(r.itemsDir, id, "payload"))
	return it, f, err
}

func (r *room) remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleted.Load() {
		return errRoomDeleted
	}
	if _, ok := r.items[id]; !ok {
		return errMissing
	}
	return r.removeLocked(id)
}

func (r *room) removeLocked(id string) error {
	trash := filepath.Join(r.trashDir, id)
	if err := os.Rename(filepath.Join(r.itemsDir, id), trash); err != nil {
		return err
	}
	r.usedBytes -= r.items[id].Size
	delete(r.items, id)
	r.revision++
	if err := os.RemoveAll(trash); err != nil {
		log.Printf("Clean deleted item: %v", err)
	}
	return nil
}

func (r *room) expireLocked(now time.Time) {
	for id, it := range r.items {
		if r.expired(it.CreatedAt, now) {
			if err := r.removeLocked(id); err != nil {
				log.Printf("Clean expired item: %v", err)
			}
		}
	}
	r.expireRoomFilesLocked(now)
}
