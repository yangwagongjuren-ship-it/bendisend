package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type draftRequest struct {
	ID    string        `json:"id"`
	Text  string        `json:"text"`
	Files []messageFile `json:"files"`
}

type transfer struct {
	draftRequest
	Sender    device    `json:"sender"`
	UpdatedAt time.Time `json:"updatedAt"`
	Offsets   []int64   `json:"-"`
}

type transferView struct {
	draftRequest
	Offsets   []int64   `json:"offsets,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	Published bool      `json:"published"`
}

func (t *transfer) view() transferView {
	return transferView{draftRequest: t.draftRequest, Offsets: slices.Clone(t.Offsets), UpdatedAt: t.UpdatedAt}
}

func publishedView(m message) transferView {
	return transferView{draftRequest: draftRequest{ID: m.ID, Text: m.Text, Files: m.Files}, Published: true, UpdatedAt: m.CreatedAt}
}

func validMessageContent(text string, files []messageFile) bool {
	if len(text) > maxTextBytes || len(files) > maxAttachments || (len(files) == 0 && strings.TrimSpace(text) == "") {
		return false
	}
	for _, file := range files {
		if file.Size < 0 || file.Size > maxByteSetting || file.Name == "" || len(file.Name) > 1024 {
			return false
		}
	}
	return true
}

func (t *transfer) uploadingBytes() int64 {
	total := int64(len(t.Text))
	for _, file := range t.Files {
		total += file.Size
	}
	return total
}

func (t *transfer) receivedBytes() int64 {
	var total int64
	for _, offset := range t.Offsets {
		total += offset
	}
	return total
}

func payloadPath(dir string, index int) string {
	return filepath.Join(dir, "file-"+strconv.Itoa(index))
}

func digestPath(dir string, index int) string {
	return payloadPath(dir, index) + ".sha256"
}

func (r *room) restoreTransfers() error {
	entries, err := os.ReadDir(r.uploadsDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		t, err := loadTransfer(filepath.Join(r.uploadsDir, entry.Name()), entry.Name())
		if err != nil {
			return fmt.Errorf("recover upload %s: %w", entry.Name(), err)
		}
		r.transfers[t.ID] = t
		r.reservedBytes += t.uploadingBytes()
		r.materializedBytes += t.receivedBytes()
		r.reservedItems++
	}
	return nil
}

func loadTransfer(dir, id string) (*transfer, error) {
	var t transfer
	if err := readJSONFile(filepath.Join(dir, "transfer.json"), &t); err != nil {
		return nil, err
	}
	if t.ID != id || !validID(t.Sender.ID) || t.UpdatedAt.IsZero() || !validMessageContent(t.Text, t.Files) {
		return nil, errors.New("invalid upload metadata")
	}
	t.Offsets = make([]int64, len(t.Files))
	for index, file := range t.Files {
		offset, last, err := recoverAttachment(dir, index, file.Size)
		if err != nil {
			return nil, err
		}
		t.Offsets[index] = offset
		if last.After(t.UpdatedAt) {
			t.UpdatedAt = last
		}
	}
	return &t, nil
}

func recoverAttachment(dir string, index int, size int64) (int64, time.Time, error) {
	journalPath, filePath := digestPath(dir, index), payloadPath(dir, index)
	journal, err := os.Stat(journalPath)
	if err != nil {
		return 0, time.Time{}, err
	}
	payload, err := os.Stat(filePath)
	if err != nil {
		return 0, time.Time{}, err
	}
	if !journal.Mode().IsRegular() || !payload.Mode().IsRegular() {
		return 0, time.Time{}, errors.New("invalid staged attachment")
	}
	// A journal entry follows a synced payload. Recover at the last whole
	// recorded chunk, discarding a payload or journal tail from a crash.
	chunks := min(journal.Size()/sha256.Size, (size+chunkSize-1)/chunkSize)
	if payload.Size() < size {
		chunks = min(chunks, payload.Size()/chunkSize)
	}
	offset := min(chunks*chunkSize, size)
	if journal.Size() != chunks*sha256.Size {
		if err := os.Truncate(journalPath, chunks*sha256.Size); err != nil {
			return 0, time.Time{}, err
		}
	}
	if payload.Size() != offset {
		if err := os.Truncate(filePath, offset); err != nil {
			return 0, time.Time{}, err
		}
	}
	return offset, journal.ModTime(), nil
}

func (r *room) startTransfer(d device, draft draftRequest) (transferView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleted.Load() {
		return transferView{}, errRoomDeleted
	}
	r.expireLocked(r.now())
	if m, exists := r.messages[draft.ID]; exists {
		if m.Sender.ID != d.ID || m.Text != draft.Text || !slices.Equal(m.Files, draft.Files) {
			return transferView{}, errIDConflict
		}
		return publishedView(m), nil
	}
	if _, exists := r.items[draft.ID]; exists {
		return transferView{}, errIDConflict
	}
	if t := r.transfers[draft.ID]; t != nil {
		if t.Sender.ID != d.ID || t.Text != draft.Text || !slices.Equal(t.Files, draft.Files) {
			return transferView{}, errIDConflict
		}
		return t.view(), nil
	}
	for _, t := range r.transfers {
		if t.Sender.ID == d.ID {
			return transferView{}, errDraftBusy
		}
	}
	for _, file := range draft.Files {
		if limit := r.hostLimitsLocked().MaxFileBytes; file.Size > limit {
			return transferView{}, &quotaProblem{kind: "file_limit", limit: limit, required: file.Size}
		}
	}
	t := &transfer{draftRequest: draft, Sender: d, UpdatedAt: r.now().UTC(), Offsets: make([]int64, len(draft.Files))}
	size := t.uploadingBytes()
	if err := r.reserveLocked(size, 1); err != nil {
		return transferView{}, err
	}
	if err := r.createTransferDir(t); err != nil {
		r.releaseLocked(size, 0, 1)
		return transferView{}, err
	}
	r.transfers[t.ID] = t
	return t.view(), nil
}

func (r *room) createTransferDir(t *transfer) error {
	temp, err := os.MkdirTemp(r.stagingDir, "upload-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	for index := range t.Files {
		if err := writeSynced(payloadPath(temp, index), nil); err != nil {
			return err
		}
		if err := writeSynced(digestPath(temp, index), nil); err != nil {
			return err
		}
	}
	if err := replaceJSON(filepath.Join(temp, "transfer.json"), t); err != nil {
		return err
	}
	return os.Rename(temp, filepath.Join(r.uploadsDir, t.ID))
}

func (r *room) findTransferLocked(owner, id string) (*transfer, error) {
	if r.deleted.Load() {
		return nil, errRoomDeleted
	}
	t := r.transfers[id]
	if t == nil || t.Sender.ID != owner || !r.now().Before(t.UpdatedAt.Add(uploadLifetime)) {
		return nil, errMissing
	}
	return t, nil
}

func (r *room) activeTransfer(owner string) *transferView {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked(r.now())
	for _, t := range r.transfers {
		if t.Sender.ID == owner && r.now().Before(t.UpdatedAt.Add(uploadLifetime)) {
			view := t.view()
			return &view
		}
	}
	return nil
}

func (r *room) transferStatus(owner, id string) (transferView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleted.Load() {
		return transferView{}, errRoomDeleted
	}
	if m, exists := r.messages[id]; exists && m.Sender.ID == owner && !r.expired(m.CreatedAt, r.now()) {
		return publishedView(m), nil
	}
	t, err := r.findTransferLocked(owner, id)
	if err != nil {
		return transferView{}, err
	}
	return t.view(), nil
}

type offsetConflict struct{ offset int64 }

func (e *offsetConflict) Error() string { return "upload offset conflict" }

func (r *room) appendChunk(owner, id string, fileIndex int, offset int64, body []byte) (int64, error) {
	digest := sha256.Sum256(body)
	r.mu.Lock()
	defer r.mu.Unlock()
	t, err := r.findTransferLocked(owner, id)
	if err != nil {
		return 0, err
	}
	if fileIndex < 0 || fileIndex >= len(t.Files) {
		return 0, errMissing
	}
	size := t.Files[fileIndex].Size
	if offset < 0 || offset >= size || offset%chunkSize != 0 || int64(len(body)) != min(int64(chunkSize), size-offset) {
		return 0, errChunkInvalid
	}
	current := t.Offsets[fileIndex]
	dir := filepath.Join(r.uploadsDir, id)
	journal := digestPath(dir, fileIndex)
	journalOffset := offset / chunkSize * sha256.Size
	if offset < current {
		previous, err := readDigests(journal, journalOffset, sha256.Size)
		if err != nil {
			return 0, err
		}
		if bytes.Equal(previous, digest[:]) {
			return current, nil
		}
		return 0, &offsetConflict{offset: current}
	}
	if offset != current {
		return 0, &offsetConflict{offset: current}
	}
	if err := r.checkDiskLocked(0); err != nil {
		return 0, err
	}
	if err := syncAt(payloadPath(dir, fileIndex), body, offset); err != nil {
		return 0, err
	}
	if err := syncAt(journal, digest[:], journalOffset); err != nil {
		return 0, err
	}
	current += int64(len(body))
	t.Offsets[fileIndex] = current
	t.UpdatedAt = r.now().UTC()
	r.materializedBytes += int64(len(body))
	return current, nil
}

func syncAt(path string, data []byte, offset int64) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteAt(data, offset)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

func readDigests(path string, offset int64, size int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data := make([]byte, size)
	if _, err := io.ReadFull(io.NewSectionReader(f, offset, int64(size)), data); err != nil {
		return nil, err
	}
	return data, nil
}

func (r *room) transferDigests(owner, id string, index int, start int64) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, err := r.findTransferLocked(owner, id)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(t.Files) {
		return nil, errMissing
	}
	chunks := (t.Offsets[index] + chunkSize - 1) / chunkSize
	if start < 0 || start > chunks {
		return nil, errChunkInvalid
	}
	count := int(min(chunks-start, 128))
	data, err := readDigests(digestPath(filepath.Join(r.uploadsDir, id), index), start*sha256.Size, count*sha256.Size)
	if err != nil {
		return nil, err
	}
	hashes := make([]string, count)
	for i := range hashes {
		hashes[i] = hex.EncodeToString(data[i*sha256.Size : (i+1)*sha256.Size])
	}
	return hashes, nil
}

func (r *room) commitTransfer(owner, id string) (message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleted.Load() {
		return message{}, errRoomDeleted
	}
	if m, exists := r.messages[id]; exists && m.Sender.ID == owner && !r.expired(m.CreatedAt, r.now()) {
		return m, nil
	}
	t, err := r.findTransferLocked(owner, id)
	if err != nil {
		return message{}, err
	}
	for index, file := range t.Files {
		if t.Offsets[index] != file.Size {
			return message{}, errIncomplete
		}
	}
	m := message{ID: id, Sender: t.Sender, Text: t.Text, Files: t.Files, CreatedAt: r.now().UTC()}
	dir := filepath.Join(r.uploadsDir, id)
	if err := replaceJSON(filepath.Join(dir, "message.json"), m); err != nil {
		return message{}, err
	}
	if err := os.Rename(dir, filepath.Join(r.messagesDir, id)); err != nil {
		return message{}, err
	}
	r.messages[id] = m
	r.usedBytes += m.size()
	r.releaseLocked(t.uploadingBytes(), t.receivedBytes(), 1)
	delete(r.transfers, id)
	r.revision++
	return m, nil
}

func (r *room) cancelTransfer(owner, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleted.Load() {
		return errRoomDeleted
	}
	if t := r.transfers[id]; t != nil && t.Sender.ID == owner {
		return r.dropTransferLocked(t)
	}
	if m, exists := r.messages[id]; exists && m.Sender.ID == owner {
		return errIDConflict
	}
	return nil
}

func (r *room) dropTransferLocked(t *transfer) error {
	if err := moveToTrash(filepath.Join(r.uploadsDir, t.ID), filepath.Join(r.trashDir, "upload-"+t.ID)); err != nil {
		return err
	}
	r.releaseLocked(t.uploadingBytes(), t.receivedBytes(), 1)
	delete(r.transfers, t.ID)
	return nil
}
