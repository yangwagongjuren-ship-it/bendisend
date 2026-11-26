package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func chatRequest(t *testing.T, c *http.Client, method, origin, path string, value any) *http.Response {
	t.Helper()
	var data []byte
	var err error
	if value != nil {
		data, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	return request(t, c, method, origin, path, bytes.NewReader(data), map[string]string{"Content-Type": "application/json"})
}

func useChat(t *testing.T, c *http.Client, origin, secret string) device {
	t.Helper()
	response(t, postJSON(t, c, origin, "/api/join", "token", secret), http.StatusNoContent, nil)
	var d device
	response(t, chatRequest(t, c, http.MethodPost, origin, "/api/device", nil), http.StatusOK, &d)
	return d
}

func testDraftID(t *testing.T) string {
	t.Helper()
	id, err := randomID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func sendPart(t *testing.T, c *http.Client, origin, id string, offset int64, data []byte) *http.Response {
	t.Helper()
	return request(t, c, http.MethodPut, origin, "/api/transfers/"+id+"/files/0", bytes.NewReader(data), map[string]string{
		"Upload-Offset": strconv.FormatInt(offset, 10), "Content-Type": "application/octet-stream",
	})
}

func TestChatResumeAndPublish(t *testing.T) {
	dir := t.TempDir()
	r, err := openRoom(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.lock.Close() })
	if _, err := r.addText("旧版记录"); err != nil {
		t.Fatal(err)
	}
	s := testServer(t, r)
	a, b := testClient(t), testClient(t)
	for _, module := range []string{"sha2.js", "_md.js", "_u64.js", "utils.js"} {
		response(t, chatRequest(t, a, http.MethodGet, s.URL, "/hash/"+module, nil), http.StatusOK, nil)
	}
	from, other := useChat(t, a, s.URL, r.secret), useChat(t, b, s.URL, r.secret)
	if from.ID == other.ID || from.Name == other.Name {
		t.Fatal("independent browsers shared a device identity")
	}
	id := testDraftID(t)
	draft := draftRequest{ID: id, Text: "附件已发\n请查收", Files: []messageFile{{Name: "会议资料.bin", Size: chunkSize + 3}}}
	response(t, chatRequest(t, a, http.MethodPost, s.URL, "/api/transfers", draft), http.StatusOK, nil)
	response(t, chatRequest(t, b, http.MethodGet, s.URL, "/api/transfers/"+id, nil), http.StatusNotFound, nil)
	response(t, chatRequest(t, a, http.MethodPost, s.URL, "/api/transfers/"+id+"/commit", nil), http.StatusConflict, nil)
	part := bytes.Repeat([]byte{41, 32, 23, 14}, chunkSize/4)
	var ack struct{ Offset int64 }
	response(t, sendPart(t, a, s.URL, id, 0, part), http.StatusOK, &ack)
	response(t, sendPart(t, a, s.URL, id, 0, part), http.StatusOK, &ack)
	if ack.Offset != chunkSize {
		t.Fatalf("duplicate upload advanced position to %d", ack.Offset)
	}
	var before roomSnapshot
	response(t, chatRequest(t, b, http.MethodGet, s.URL, "/api/messages", nil), http.StatusOK, &before)
	if len(before.Messages) != 1 || before.Messages[0].Sender.Name != "旧记录" {
		t.Fatalf("pending upload was exposed or old history was missing: %+v", before.Messages)
	}
	if err := syncAt(payloadPath(filepath.Join(r.uploadsDir, id), 0), []byte("unacknowledged tail"), chunkSize); err != nil {
		t.Fatal(err)
	}
	if err := syncAt(digestPath(filepath.Join(r.uploadsDir, id), 0), []byte("partial record"), sha256.Size); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := r.lock.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = openRoom(dir)
	if err != nil {
		t.Fatal(err)
	}
	s = testServer(t, r)
	if again := useChat(t, a, s.URL, r.secret); again != from {
		t.Fatalf("device identity changed after restart: %+v", again)
	}
	var restored transferView
	response(t, chatRequest(t, a, http.MethodGet, s.URL, "/api/transfers/active", nil), http.StatusOK, &restored)
	if restored.ID != id || len(restored.Offsets) != 1 || restored.Offsets[0] != chunkSize || restored.Text != draft.Text {
		t.Fatalf("bad restored upload: %+v", restored)
	}
	var hashes []string
	response(t, chatRequest(t, a, http.MethodGet, s.URL, "/api/transfers/"+id+"/files/0/checksums?from=0", nil), http.StatusOK, &hashes)
	checksum := sha256.Sum256(part)
	if len(hashes) != 1 || hashes[0] != hex.EncodeToString(checksum[:]) {
		t.Fatalf("wrong checksums: %v", hashes)
	}
	response(t, sendPart(t, a, s.URL, id, chunkSize, []byte("end")), http.StatusOK, &ack)
	for i := 0; i < 2; i++ {
		response(t, chatRequest(t, a, http.MethodPost, s.URL, "/api/transfers/"+id+"/commit", nil), http.StatusOK, nil)
	}
	var after roomSnapshot
	response(t, chatRequest(t, b, http.MethodGet, s.URL, "/api/messages", nil), http.StatusOK, &after)
	if len(after.Messages) != 2 || after.Messages[1].Text != draft.Text || after.Messages[1].Sender != from || len(after.Messages[1].Files) != 1 {
		t.Fatalf("message group did not publish atomically: %+v", after.Messages)
	}
	path := "/api/messages/" + id + "/files/0"
	data := response(t, chatRequest(t, b, http.MethodGet, s.URL, path, nil), http.StatusOK, nil)
	if sha256.Sum256(data) != sha256.Sum256(append(part, []byte("end")...)) {
		t.Fatal("download does not match uploaded bytes")
	}
	rangeData := response(t, request(t, b, http.MethodGet, s.URL, path, nil, map[string]string{
		"Range": "bytes=" + strconv.Itoa(chunkSize) + "-",
	}), http.StatusPartialContent, nil)
	if string(rangeData) != "end" {
		t.Fatal("file range does not match uploaded tail")
	}
}

func TestHostLimitsAndQuota(t *testing.T) {
	r, err := openRoom(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.lock.Close() })
	s := testServer(t, r)
	c := testClient(t)
	limits := uploadLimits{MaxFileBytes: chunkSize, MaxRoomBytes: 2 * chunkSize}
	response(t, chatRequest(t, c, http.MethodPut, s.URL, "/api/limits", limits), http.StatusUnauthorized, nil)
	d := useChat(t, c, s.URL, r.secret)
	response(t, chatRequest(t, c, http.MethodPut, s.URL, "/api/limits", limits), http.StatusOK, nil)
	var saved uploadLimits
	if err := readJSONFile(filepath.Join(r.root, "limits.json"), &saved); err != nil || saved != limits {
		t.Fatalf("limits were not saved: %+v, %v", saved, err)
	}
	remote := httptest.NewRequest(http.MethodPut, "http://192.168.1.20:"+strconv.Itoa(s.Listener.Addr().(*net.TCPAddr).Port)+"/api/limits", strings.NewReader(`{"maxFileBytes":100,"maxRoomBytes":100}`))
	remote.RemoteAddr = "192.168.1.21:50000"
	remote.Header.Set("Origin", "http://"+remote.Host)
	remote.Header.Set("Content-Type", "application/json")
	remote.Header.Set("X-Forwarded-For", "127.0.0.1")
	remote = remote.WithContext(context.WithValue(remote.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("192.168.1.20")}))
	app := newServer(r, s.Listener.Addr().(*net.TCPAddr).Port)
	remote.AddCookie(&http.Cookie{Name: cookieName, Value: app.credential})
	reply := httptest.NewRecorder()
	app.handler().ServeHTTP(reply, remote)
	if reply.Code != http.StatusForbidden {
		t.Fatalf("LAN request changed limits: %d", reply.Code)
	}
	var problem apiProblem
	tooLarge := draftRequest{ID: testDraftID(t), Files: []messageFile{{Name: "too-big.bin", Size: chunkSize + 1}}}
	response(t, chatRequest(t, c, http.MethodPost, s.URL, "/api/transfers", tooLarge), http.StatusRequestEntityTooLarge, &problem)
	if problem.Code != "file_limit" || problem.Limit != chunkSize || problem.Required != chunkSize+1 {
		t.Fatalf("bad limit notification: %+v", problem)
	}

	first := draftRequest{ID: testDraftID(t), Text: "a", Files: []messageFile{{Name: "first.bin", Size: chunkSize}}}
	second := draftRequest{ID: testDraftID(t), Text: "b", Files: []messageFile{{Name: "second.bin", Size: chunkSize}}}
	other := device{ID: testDraftID(t), Name: "Phone", Kind: "iphone"}
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for i, draft := range []draftRequest{first, second} {
		owner := []device{d, other}[i]
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := r.startTransfer(owner, draft)
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	accepted := 0
	for err := range errs {
		if err == nil {
			accepted++
		} else if !errors.Is(err, errFull) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d concurrent uploads with space for one", accepted)
	}
	if err := r.cancelTransfer(d.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.cancelTransfer(other.ID, second.ID); err != nil || r.reservedBytes != 0 {
		t.Fatalf("cancellation did not release reservations: %v, %d", err, r.reservedBytes)
	}
	r.checkFree = func(string) (int64, error) { return diskHeadroom, nil }
	response(t, chatRequest(t, c, http.MethodPost, s.URL, "/api/transfers", first), http.StatusInsufficientStorage, &problem)
	if problem.Code != "disk_space" {
		t.Fatalf("expected disk space error, got %+v", problem)
	}
}

func TestLowerLimitsKeepReservationsAndExpiryReleasesThem(t *testing.T) {
	r, err := openRoom(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.lock.Close()
	now := time.Now()
	r.now = func() time.Time { return now }
	d := device{ID: testDraftID(t), Name: "主机", Kind: "host"}
	first := draftRequest{ID: testDraftID(t), Files: []messageFile{{Name: "a", Size: 2}}}
	if _, err := r.startTransfer(d, first); err != nil {
		t.Fatal(err)
	}
	if err := r.setLimits(uploadLimits{MaxFileBytes: 1, MaxRoomBytes: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.appendChunk(d.ID, first.ID, 0, 0, []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.commitTransfer(d.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.setLimits(uploadLimits{MaxFileBytes: 10, MaxRoomBytes: 10}); err != nil {
		t.Fatal(err)
	}
	second := draftRequest{ID: testDraftID(t), Files: []messageFile{{Name: "b", Size: 8}}}
	if _, err := r.startTransfer(d, second); err != nil {
		t.Fatal(err)
	}
	now = now.Add(uploadLifetime)
	if transfer := r.activeTransfer(d.ID); transfer != nil || r.reservedBytes != 0 {
		t.Fatal("expired upload still occupied capacity")
	}
	if _, err := os.Stat(filepath.Join(r.uploadsDir, second.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired data directory remains: %v", err)
	}
}
