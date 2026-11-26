package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func serveRooms(t *testing.T, r *room) (*httptest.Server, *roomGroup) {
	t.Helper()
	s := httptest.NewUnstartedServer(nil)
	app := newServer(r, s.Listener.Addr().(*net.TCPAddr).Port)
	group, err := openRoomGroup(app)
	if err != nil {
		t.Fatal(err)
	}
	s.Config.Handler = app.handler()
	s.Start()
	t.Cleanup(func() { s.Close(); group.close(); r.lock.Close() })
	return s, group
}

func roomPrefix(id string) string { return "/r/" + id }

func testCreateRoom(t *testing.T, c *http.Client, origin, name string) roomInfo {
	t.Helper()
	var room roomInfo
	response(t, chatRequest(t, c, http.MethodPost, origin, "/api/rooms", map[string]string{"name": name}), http.StatusCreated, &room)
	return room
}

func TestRoomInvitationsAndLegacyData(t *testing.T) {
	root := t.TempDir()
	old, err := openRoom(root)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := old.addFile("旧文件.txt", http.NoBody, 0)
	if err != nil {
		t.Fatal(err)
	}
	limits := uploadLimits{MaxFileBytes: 5000, MaxRoomBytes: 10000}
	if err := old.setLimits(limits); err != nil {
		t.Fatal(err)
	}
	oldServer := testServer(t, old)
	host, guest := testClient(t), testClient(t)
	device := useChat(t, host, oldServer.URL, old.secret)
	draft := draftRequest{ID: testDraftID(t), Files: []messageFile{{Name: "pending.txt", Size: 9}}}
	response(t, chatRequest(t, host, http.MethodPost, oldServer.URL, "/api/transfers", draft), http.StatusOK, nil)
	oldServer.Close()
	if err := old.lock.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := openRoom(root)
	if err != nil {
		t.Fatal(err)
	}
	s, g := serveRooms(t, current)
	if old.secret != current.secret || current.limits != limits {
		t.Fatal("upgrading changed invite or limits")
	}
	if d := useChat(t, host, s.URL, old.secret); d != device {
		t.Fatal("device cookie changed across upgrade")
	}
	response(t, chatRequest(t, host, http.MethodGet, s.URL, "/api/messages/"+legacy.ID+"/files/0", nil), http.StatusOK, nil)
	var active transferView
	response(t, chatRequest(t, host, http.MethodGet, s.URL, "/api/transfers/active", nil), http.StatusOK, &active)
	if active.ID != draft.ID {
		t.Fatalf("old pending upload missing: %+v", active)
	}
	a := testCreateRoom(t, host, s.URL, "工作资料")
	b := testCreateRoom(t, host, s.URL, "临时传输")
	pa, pb := roomPrefix(a.ID), roomPrefix(b.ID)
	var invites []invitation
	response(t, chatRequest(t, host, http.MethodGet, s.URL, pa+"/api/invites", nil), http.StatusOK, &invites)
	for _, invite := range invites {
		parsed, err := url.Parse(invite.URL)
		if err != nil || parsed.Path != pa+"/" || parsed.Fragment != g.rooms[a.ID].secret {
			t.Fatalf("incorrect room invite route: %v", err)
		}
	}
	response(t, postJSON(t, guest, s.URL, pa+"/api/join", "token", g.rooms[a.ID].secret), http.StatusNoContent, nil)
	response(t, chatRequest(t, guest, http.MethodPost, s.URL, pa+"/api/device", nil), http.StatusOK, nil)
	var listed struct {
		Rooms     []roomInfo
		CanCreate bool
	}
	response(t, chatRequest(t, guest, http.MethodGet, s.URL, "/api/rooms", nil), http.StatusOK, &listed)
	if len(listed.Rooms) != 1 || listed.Rooms[0].ID != a.ID || listed.CanCreate {
		t.Fatalf("guest room list leaked other rooms or rights: %+v", listed)
	}
	for _, target := range []string{pb + "/api/messages", pb + "/api/qr", "/api/messages"} {
		response(t, chatRequest(t, guest, http.MethodGet, s.URL, target, nil), http.StatusUnauthorized, nil)
	}
	response(t, chatRequest(t, guest, http.MethodGet, s.URL, pa+"/api/room/clear", nil), http.StatusForbidden, nil)
	response(t, chatRequest(t, guest, http.MethodPost, s.URL, "/api/rooms", map[string]string{"name": "denied"}), http.StatusUnauthorized, nil)
	response(t, postJSON(t, guest, s.URL, pb+"/api/join", "token", g.rooms[a.ID].secret), http.StatusForbidden, nil)
	response(t, postJSON(t, guest, s.URL, pb+"/api/join", "token", g.rooms[b.ID].secret), http.StatusNoContent, nil)
	response(t, chatRequest(t, guest, http.MethodPost, s.URL, pb+"/api/device", nil), http.StatusOK, nil)
	response(t, chatRequest(t, guest, http.MethodGet, s.URL, "/api/rooms", nil), http.StatusOK, &listed)
	if len(listed.Rooms) != 2 {
		t.Fatalf("room membership did not accumulate: %+v", listed)
	}
	s.Close()
	g.close()
	current.lock.Close()
	reopened, err := openRoom(root)
	if err != nil {
		t.Fatal(err)
	}
	s2, g2 := serveRooms(t, reopened)
	response(t, chatRequest(t, guest, http.MethodGet, s2.URL, pa+"/api/messages", nil), http.StatusOK, nil)
	if g2.rooms[a.ID].secret != g.rooms[a.ID].secret {
		t.Fatal("new room invite changed after restart")
	}
}

func TestClearUsesSnapshotAndReleasesSharedQuota(t *testing.T) {
	r, err := openRoom(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, g := serveRooms(t, r)
	host := testClient(t)
	d := useChat(t, host, s.URL, r.secret)
	a := testCreateRoom(t, host, s.URL, "A")
	b := testCreateRoom(t, host, s.URL, "B")
	ra, rb := g.rooms[a.ID], g.rooms[b.ID]
	if err := r.setLimits(uploadLimits{MaxFileBytes: 10, MaxRoomBytes: 12}); err != nil {
		t.Fatal(err)
	}
	first, err := ra.addText("1111")
	if err != nil {
		t.Fatal(err)
	}
	other, err := rb.addText("2222")
	if err != nil {
		t.Fatal(err)
	}
	pa := roomPrefix(a.ID)
	var preview struct{ IDs []string }
	response(t, chatRequest(t, host, http.MethodGet, s.URL, pa+"/api/room/clear", nil), http.StatusOK, &preview)
	if len(preview.IDs) != 1 || preview.IDs[0] != first.ID {
		t.Fatalf("incorrect clear snapshot: %+v", preview)
	}
	newer, err := ra.addText("n")
	if err != nil {
		t.Fatal(err)
	}
	draft := draftRequest{ID: testDraftID(t), Files: []messageFile{{Name: "later", Size: 2}}}
	if _, err := ra.startTransfer(d, draft); err != nil {
		t.Fatal(err)
	}
	if _, err := rb.startTransfer(d, draft); !errors.Is(err, errFull) {
		t.Fatalf("room B bypassed shared quota: %v", err)
	}
	var result struct {
		Deleted int
		Failed  []string
	}
	response(t, chatRequest(t, host, http.MethodPost, s.URL, pa+"/api/room/clear", map[string][]string{"ids": append(preview.IDs, other.ID)}), http.StatusOK, &result)
	if result.Deleted != 1 || len(result.Failed) != 0 {
		t.Fatalf("incorrect clear result: %+v", result)
	}
	if _, exists := ra.items[newer.ID]; !exists {
		t.Fatal("clear removed a newer record")
	}
	if _, exists := rb.items[other.ID]; !exists {
		t.Fatal("clear removed another room's record")
	}
	if ra.activeTransfer(d.ID) == nil {
		t.Fatal("clear removed an in-flight upload")
	}
	if _, err := rb.startTransfer(d, draft); err != nil {
		t.Fatalf("deleted files did not release capacity: %v", err)
	}
	if err := rb.cancelTransfer(d.ID, draft.ID); err != nil {
		t.Fatal(err)
	}
	if err := ra.cancelTransfer(d.ID, draft.ID); err != nil {
		t.Fatal(err)
	}
	claims := make(chan error, 2)
	var wg sync.WaitGroup
	for _, target := range []*room{ra, rb} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := target.startTransfer(d, draftRequest{ID: draft.ID, Files: []messageFile{{Name: "parallel", Size: 5}}})
			claims <- err
		}()
	}
	wg.Wait()
	close(claims)
	winners := 0
	for err := range claims {
		if err == nil {
			winners++
		} else if !errors.Is(err, errFull) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("shared quota admitted %d of two competing transfers", winners)
	}
}

func TestRetentionPoliciesSurviveRestart(t *testing.T) {
	r, err := openRoom(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, g := serveRooms(t, r)
	host := testClient(t)
	useChat(t, host, s.URL, r.secret)
	short, long := testCreateRoom(t, host, s.URL, "短期"), testCreateRoom(t, host, s.URL, "长期")
	a, b := g.rooms[short.ID], g.rooms[long.ID]
	oldTime := time.Now().Add(-2 * 24 * time.Hour)
	for _, room := range []*room{a, b} {
		room.now = func() time.Time { return oldTime }
		if _, err := room.addText("两天前"); err != nil {
			t.Fatal(err)
		}
		room.now = time.Now
	}
	path := roomPrefix(short.ID) + "/api/room/retention"
	var preview struct{ Expired int }
	response(t, chatRequest(t, host, http.MethodGet, s.URL, path+"?days=1", nil), http.StatusOK, &preview)
	if preview.Expired != 1 {
		t.Fatalf("wrong expiration preview: %+v", preview)
	}
	response(t, chatRequest(t, host, http.MethodPut, s.URL, path, map[string]int{"retentionDays": 1, "expectedExpired": 0}), http.StatusConflict, nil)
	response(t, chatRequest(t, host, http.MethodPut, s.URL, path, map[string]int{"retentionDays": 1, "expectedExpired": 1}), http.StatusNoContent, nil)
	if len(a.items) != 0 || len(b.items) != 1 {
		t.Fatal("retention affected wrong room")
	}
	response(t, chatRequest(t, host, http.MethodPut, s.URL, roomPrefix(long.ID)+"/api/room/retention", map[string]int{"retentionDays": 0, "expectedExpired": 0}), http.StatusNoContent, nil)
	for _, room := range []*room{a, b} {
		room.now = func() time.Time { return time.Now().Add(-40 * 24 * time.Hour) }
		if _, err := room.addText("重启后检查"); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	g.close()
	for _, id := range []string{short.ID, long.ID} {
		reopened, err := openNamedRoom(filepath.Join(g.dir, id), id)
		if err != nil {
			t.Fatal(err)
		}
		if (id == short.ID && (reopened.policy.Days != 1 || len(reopened.items) != 0)) || (id == long.ID && (reopened.policy.Days != 0 || len(reopened.items) != 2)) {
			t.Fatalf("retention after restart: days=%d, items=%d", reopened.policy.Days, len(reopened.items))
		}
		reopened.lock.Close()
	}
}
