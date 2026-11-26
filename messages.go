package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

const (
	chunkSize       = 8 << 20
	maxAttachments  = 20
	maxByteSetting  = 1<<53 - 1 // Preserve byte counts in browser JSON numbers.
	defaultFileSize = 10 << 30
	defaultRoomSize = 50 << 30
	uploadLifetime  = 24 * time.Hour
	diskHeadroom    = 64 << 20
)

var (
	errDiskSpace    = errors.New("insufficient disk space")
	errDraftBusy    = errors.New("another upload is waiting on this device")
	errIDConflict   = errors.New("message ID is already in use")
	errIncomplete   = errors.New("upload is incomplete")
	errChunkInvalid = errors.New("invalid chunk size or position")
	errBadLimits    = errors.New("invalid upload limits")
)

type uploadLimits struct {
	MaxFileBytes int64 `json:"maxFileBytes"`
	MaxRoomBytes int64 `json:"maxRoomBytes"`
}

func (l uploadLimits) valid() bool {
	return l.MaxFileBytes > 0 && l.MaxFileBytes <= l.MaxRoomBytes && l.MaxRoomBytes <= maxByteSetting
}

type quotaProblem struct {
	kind     string
	limit    int64
	required int64
}

func (e *quotaProblem) Error() string { return e.kind }
func (e *quotaProblem) Is(target error) bool {
	return (e.kind == "file_limit" && target == errTooLarge) || (e.kind == "room_limit" && target == errFull)
}

type device struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type connectedDevice struct {
	device
	seen time.Time
}

type messageFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type message struct {
	ID        string        `json:"id"`
	Sender    device        `json:"sender"`
	Text      string        `json:"text,omitempty"`
	Files     []messageFile `json:"files,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
}

func (m message) size() int64 {
	size := int64(len(m.Text))
	for _, file := range m.Files {
		size += file.Size
	}
	return size
}

type roomFiles struct {
	messagesDir string
	uploadsDir  string
	limits      uploadLimits
	messages    map[string]message
	transfers   map[string]*transfer
	devices     map[string]connectedDevice
	deviceCount map[string]int64
	checkFree   func(string) (int64, error)

	reservedBytes     int64
	materializedBytes int64
	reservedItems     int
}

func (r *room) loadRoomFiles() error {
	r.messagesDir = filepath.Join(r.root, "messages")
	r.uploadsDir = filepath.Join(r.root, "uploads")
	r.limits = uploadLimits{MaxFileBytes: defaultFileSize, MaxRoomBytes: defaultRoomSize}
	r.messages = make(map[string]message)
	r.transfers = make(map[string]*transfer)
	r.devices = make(map[string]connectedDevice)
	r.deviceCount = make(map[string]int64)
	r.checkFree = freeBytes
	if err := readOptionalJSON(filepath.Join(r.root, "limits.json"), &r.limits); err != nil {
		return err
	}
	if !r.limits.valid() {
		return errBadLimits
	}
	if err := readOptionalJSON(filepath.Join(r.root, "devices.json"), &r.deviceCount); err != nil {
		return err
	}
	if r.deviceCount == nil {
		return errors.New("invalid device counters")
	}
	if r.deleted.Load() {
		return nil
	}
	for _, dir := range []string{r.messagesDir, r.uploadsDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(r.messagesDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || !validID(id) {
			continue
		}
		m, err := loadMessage(filepath.Join(r.messagesDir, id), id)
		if err != nil {
			return fmt.Errorf("load message %s: %w", id, err)
		}
		r.messages[id] = m
		r.usedBytes += m.size()
	}
	return r.restoreTransfers()
}

func loadMessage(dir, id string) (message, error) {
	var m message
	if err := readJSONFile(filepath.Join(dir, "message.json"), &m); err != nil {
		return m, err
	}
	if m.ID != id || m.CreatedAt.IsZero() || !validID(m.Sender.ID) || !validMessageContent(m.Text, m.Files) {
		return m, errors.New("invalid message metadata")
	}
	for index, file := range m.Files {
		info, err := os.Stat(payloadPath(dir, index))
		if err != nil {
			return m, err
		}
		if !info.Mode().IsRegular() || info.Size() != file.Size {
			return m, errors.New("invalid message attachment")
		}
	}
	return m, nil
}

func readJSONFile(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func readOptionalJSON(path string, value any) error {
	err := readJSONFile(path, value)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func replaceJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".partial-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (r *room) setLimits(limits uploadLimits) error {
	if !limits.valid() {
		return errBadLimits
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleted.Load() {
		return errRoomDeleted
	}
	owner := r.limitOwner()
	if err := replaceJSON(filepath.Join(owner.root, "limits.json"), limits); err != nil {
		return err
	}
	owner.limits = limits
	owner.revision++
	return nil
}

func (r *room) reserveLocked(size int64, count int) error {
	if r.deleted.Load() {
		return errRoomDeleted
	}
	if r.group == nil {
		r.expireLocked(r.now())
	} else {
		r.group.expireLocked()
	}
	if len(r.messages)+len(r.items)+r.reservedItems+count > maxRoomItems {
		return errFull
	}
	used, pending, _ := r.totalsLocked()
	needed := used + pending + size
	if limit := r.hostLimitsLocked().MaxRoomBytes; needed > limit {
		return &quotaProblem{kind: "room_limit", limit: limit, required: needed}
	}
	if err := r.checkDiskLocked(size); err != nil {
		return err
	}
	r.reservedBytes += size
	r.reservedItems += count
	return nil
}

func (r *room) checkDiskLocked(extra int64) error {
	free, err := r.checkFree(r.uploadsDir)
	if err != nil {
		return err
	}
	_, pending, materialized := r.totalsLocked()
	if free < diskHeadroom+pending-materialized+extra {
		return errDiskSpace
	}
	return nil
}

func (r *room) releaseLocked(size, materialized int64, count int) {
	r.reservedBytes -= size
	r.materializedBytes -= materialized
	r.reservedItems -= count
}

func (r *room) registerDevice(kind, label string) (device, error) {
	id, err := randomID()
	if err != nil {
		return device{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	count := r.deviceCount[kind] + 1
	r.deviceCount[kind] = count
	if err := replaceJSON(filepath.Join(r.root, "devices.json"), r.deviceCount); err != nil {
		r.deviceCount[kind]--
		return device{}, err
	}
	if kind != "host" || count > 1 {
		label += " " + strconv.FormatInt(count, 10)
	}
	d := device{ID: id, Name: label, Kind: kind}
	return d, nil
}

func (r *room) seeDeviceLocked(d device, now time.Time) {
	if _, exists := r.devices[d.ID]; !exists {
		if len(r.devices) >= 128 {
			var oldest connectedDevice
			for _, entry := range r.devices {
				if oldest.ID == "" || entry.seen.Before(oldest.seen) {
					oldest = entry
				}
			}
			delete(r.devices, oldest.ID)
		}
		r.revision++
	}
	r.devices[d.ID] = connectedDevice{device: d, seen: now}
}

type roomSnapshot struct {
	Room         roomInfo     `json:"room"`
	Messages     []message    `json:"messages"`
	Devices      []device     `json:"devices"`
	Self         device       `json:"self"`
	CanEdit      bool         `json:"canEditLimits"`
	Limits       uploadLimits `json:"limits"`
	UsedBytes    int64        `json:"usedBytes"`
	PendingBytes int64        `json:"pendingBytes"`
	MaxTextBytes int          `json:"maxTextBytes"`
	MaxFiles     int          `json:"maxFiles"`
}

func (r *room) timeline(d device, canEdit bool, ifNoneMatch string) (roomSnapshot, string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.expireLocked(now)
	r.seeDeviceLocked(d, now)
	for id, entry := range r.devices {
		if now.Sub(entry.seen) >= 2*time.Minute {
			delete(r.devices, id)
			r.revision++
		}
	}
	state := roomSnapshot{
		Room: r.infoLocked(), Self: d, CanEdit: canEdit, Limits: r.hostLimitsLocked(),
		MaxTextBytes: maxTextBytes, MaxFiles: maxAttachments,
		Messages: make([]message, 0, len(r.messages)+len(r.items)), Devices: make([]device, 0, len(r.devices)),
	}
	state.UsedBytes, state.PendingBytes, _ = r.totalsLocked()
	for _, m := range r.messages {
		if !r.expired(m.CreatedAt, now) {
			state.Messages = append(state.Messages, m)
		}
	}
	for _, it := range r.items {
		if !r.expired(it.CreatedAt, now) {
			m := message{ID: it.ID, CreatedAt: it.CreatedAt, Text: it.Text, Sender: device{Name: "旧记录", Kind: "legacy"}}
			if it.Kind == "file" {
				m.Files = []messageFile{{Name: it.Name, Size: it.Size}}
			}
			state.Messages = append(state.Messages, m)
		}
	}
	tag := fmt.Sprintf(`"%s-%s-%d-%d-%d-%d-%d-%t"`, r.bootID, d.ID, r.revision, r.limitOwner().revision, len(state.Messages), state.UsedBytes, state.PendingBytes, canEdit)
	if ifNoneMatch == tag {
		return roomSnapshot{}, tag, true
	}
	for _, entry := range r.devices {
		state.Devices = append(state.Devices, entry.device)
	}
	sort.Slice(state.Devices, func(i, j int) bool { return state.Devices[i].Name < state.Devices[j].Name })
	sort.Slice(state.Messages, func(i, j int) bool {
		a, b := state.Messages[i], state.Messages[j]
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID < b.ID
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
	return state, tag, false
}

func (r *room) openAttachment(id string, index int) (messageFile, time.Time, *os.File, error) {
	r.mu.Lock()
	if r.deleted.Load() {
		r.mu.Unlock()
		return messageFile{}, time.Time{}, nil, errRoomDeleted
	}
	m, exists := r.messages[id]
	if exists {
		defer r.mu.Unlock()
		if index < 0 || index >= len(m.Files) || r.expired(m.CreatedAt, r.now()) {
			return messageFile{}, time.Time{}, nil, errMissing
		}
		f, err := os.Open(payloadPath(filepath.Join(r.messagesDir, id), index))
		return m.Files[index], m.CreatedAt, f, err
	}
	r.mu.Unlock()
	if index != 0 {
		return messageFile{}, time.Time{}, nil, errMissing
	}
	it, f, err := r.openFile(id)
	return messageFile{Name: it.Name, Size: it.Size}, it.CreatedAt, f, err
}

func (r *room) deleteMessage(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.deleteMessageByIDLocked(id)
}

func (r *room) deleteMessageByIDLocked(id string) error {
	if r.deleted.Load() {
		return errRoomDeleted
	}
	if m, exists := r.messages[id]; exists {
		return r.deleteMessageLocked(m)
	}
	if _, exists := r.items[id]; exists {
		return r.removeLocked(id)
	}
	return errMissing
}

func moveToTrash(source, trash string) error {
	if err := os.Rename(source, trash); err != nil {
		return err
	}
	if err := os.RemoveAll(trash); err != nil {
		log.Printf("Clean deleted data: %v", err)
	}
	return nil
}

func (r *room) deleteMessageLocked(m message) error {
	if err := moveToTrash(filepath.Join(r.messagesDir, m.ID), filepath.Join(r.trashDir, "message-"+m.ID)); err != nil {
		return err
	}
	r.usedBytes -= m.size()
	delete(r.messages, m.ID)
	r.revision++
	return nil
}

func (r *room) expireRoomFilesLocked(now time.Time) {
	for _, m := range r.messages {
		if r.expired(m.CreatedAt, now) {
			if err := r.deleteMessageLocked(m); err != nil {
				log.Printf("Clean expired message: %v", err)
			}
		}
	}
	for _, t := range r.transfers {
		if !now.Before(t.UpdatedAt.Add(uploadLifetime)) {
			if err := r.dropTransferLocked(t); err != nil {
				log.Printf("Clean expired upload: %v", err)
			}
		}
	}
}
