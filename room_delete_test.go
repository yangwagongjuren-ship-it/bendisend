package main

import (
	"errors"
	"net/http"
	"os"
	"testing"
)

func TestDeleteRoomDuringUploadAndAfterRestart(t *testing.T) {
	root := t.TempDir()
	r, err := openRoom(root)
	if err != nil {
		t.Fatal(err)
	}
	s, g := serveRooms(t, r)
	host, guest := testClient(t), testClient(t)
	d := useChat(t, host, s.URL, r.secret)
	removed := testCreateRoom(t, host, s.URL, "待删除")
	other := testCreateRoom(t, host, s.URL, "继续使用")
	old := g.rooms[removed.ID]
	oldInvite := old.secret
	path := roomPrefix(removed.ID)
	draft := draftRequest{ID: testDraftID(t), Files: []messageFile{{Name: "preview.bin", Size: 6}}}
	if _, err := old.startTransfer(d, draft); err != nil {
		t.Fatal(err)
	}
	if _, err := old.addText("已发送"); err != nil {
		t.Fatal(err)
	}
	keep, err := g.rooms[other.ID].addText("保留这条")
	if err != nil {
		t.Fatal(err)
	}
	response(t, postJSON(t, guest, s.URL, path+"/api/join", "token", oldInvite), http.StatusNoContent, nil)
	response(t, postJSON(t, guest, s.URL, roomPrefix(other.ID)+"/api/join", "token", g.rooms[other.ID].secret), http.StatusNoContent, nil)
	response(t, chatRequest(t, guest, http.MethodPost, s.URL, roomPrefix(other.ID)+"/api/device", nil), http.StatusOK, nil)
	response(t, chatRequest(t, guest, http.MethodDelete, s.URL, "/api/rooms/"+removed.ID, map[string]string{"generation": removed.Generation}), http.StatusUnauthorized, nil)
	response(t, chatRequest(t, host, http.MethodDelete, s.URL, "/api/rooms/"+removed.ID, map[string]string{"generation": removed.Generation}), http.StatusNoContent, nil)
	if _, err := old.appendChunk(d.ID, draft.ID, 0, 0, []byte("abc123")); !errors.Is(err, errRoomDeleted) {
		t.Fatalf("stale upload could still write: %v", err)
	}
	if _, err := old.commitTransfer(d.ID, draft.ID); !errors.Is(err, errRoomDeleted) {
		t.Fatalf("stale upload could still publish: %v", err)
	}
	response(t, chatRequest(t, guest, http.MethodGet, s.URL, path+"/api/messages", nil), http.StatusGone, nil)
	response(t, chatRequest(t, guest, http.MethodGet, s.URL, path+"/", nil), http.StatusOK, nil)
	response(t, postJSON(t, guest, s.URL, path+"/api/join", "token", oldInvite), http.StatusGone, nil)
	var rooms struct{ Rooms []roomInfo }
	response(t, chatRequest(t, guest, http.MethodGet, s.URL, "/api/rooms", nil), http.StatusOK, &rooms)
	if len(rooms.Rooms) != 1 || rooms.Rooms[0].ID != other.ID {
		t.Fatalf("removed room still listed: %+v", rooms)
	}
	// Restart before the periodic cleanup. The still-present data must never reappear.
	if _, err := os.Stat(old.root); err != nil {
		t.Fatal(err)
	}
	s.Close()
	g.close()
	r.lock.Close()
	reopened, err := openRoom(root)
	if err != nil {
		t.Fatal(err)
	}
	newServer, group := serveRooms(t, reopened)
	if _, exists := group.rooms[removed.ID]; exists {
		t.Fatal("deleted room resurrected")
	}
	if _, err := os.Stat(old.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired data was not cleaned: %v", err)
	}
	var state roomSnapshot
	response(t, chatRequest(t, guest, http.MethodGet, newServer.URL, roomPrefix(other.ID)+"/api/messages", nil), http.StatusOK, &state)
	if len(state.Messages) != 1 || state.Messages[0].ID != keep.ID || state.PendingBytes != 0 {
		t.Fatalf("other room or quota changed: %+v", state)
	}
}

func TestDefaultResetRevokesInviteAndPreservesOtherRoom(t *testing.T) {
	root := t.TempDir()
	r, err := openRoom(root)
	if err != nil {
		t.Fatal(err)
	}
	s, g := serveRooms(t, r)
	host, stale, invited := testClient(t), testClient(t), testClient(t)
	oldSecret := r.secret
	d := useChat(t, host, s.URL, oldSecret)
	useChat(t, stale, s.URL, oldSecret)
	other := testCreateRoom(t, host, s.URL, "保留")
	path := roomPrefix(other.ID)
	response(t, postJSON(t, invited, s.URL, path+"/api/join", "token", g.rooms[other.ID].secret), http.StatusNoContent, nil)
	response(t, chatRequest(t, invited, http.MethodPost, s.URL, path+"/api/device", nil), http.StatusOK, nil)
	kept, err := g.rooms[other.ID].addText("完整保留")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.addText("删除旧默认记录"); err != nil {
		t.Fatal(err)
	}
	if err := r.setRetention(30, 0); err != nil {
		t.Fatal(err)
	}
	limits := uploadLimits{MaxFileBytes: 150, MaxRoomBytes: 300}
	if err := r.setLimits(limits); err != nil {
		t.Fatal(err)
	}
	draft := draftRequest{ID: testDraftID(t), Files: []messageFile{{Name: "pending", Size: 60}}}
	if _, err := r.startTransfer(d, draft); err != nil {
		t.Fatal(err)
	}
	deletePath := "/api/rooms/default"
	command := map[string]string{"generation": r.generation}
	response(t, chatRequest(t, host, http.MethodDelete, s.URL, deletePath, command), http.StatusNoContent, nil)
	response(t, chatRequest(t, host, http.MethodDelete, s.URL, deletePath, command), http.StatusConflict, nil)
	response(t, chatRequest(t, stale, http.MethodGet, s.URL, "/api/messages", nil), http.StatusUnauthorized, nil)
	response(t, request(t, stale, http.MethodGet, s.URL, "/api/messages", nil, map[string]string{"X-Room-Generation": r.generation}), http.StatusGone, nil)
	response(t, postJSON(t, stale, s.URL, "/api/join", "token", oldSecret), http.StatusForbidden, nil)
	response(t, chatRequest(t, invited, http.MethodGet, s.URL, path+"/api/messages", nil), http.StatusOK, nil)
	var state roomSnapshot
	response(t, chatRequest(t, host, http.MethodGet, s.URL, "/api/messages", nil), http.StatusOK, &state)
	if state.Room.Generation == r.generation || state.Room.Days != 7 || state.Limits != limits || len(state.Messages) != 0 || state.PendingBytes != 0 || state.Self != d {
		t.Fatalf("reset did not result in an empty new default room with existing settings: %+v", state)
	}
	generation, newSecret := state.Room.Generation, g.defaultServer.room.secret
	s.Close()
	g.close()
	r.lock.Close()
	reopened, err := openRoom(root)
	if err != nil {
		t.Fatal(err)
	}
	newServer, group := serveRooms(t, reopened)
	if group.defaultServer.room.generation != generation || group.defaultServer.room.secret != newSecret || newSecret == oldSecret {
		t.Fatal("default room reset was not durable")
	}
	response(t, chatRequest(t, host, http.MethodGet, newServer.URL, "/api/messages", nil), http.StatusOK, &state)
	if len(state.Messages) != 0 {
		t.Fatal("old default room data resurrected")
	}
	response(t, chatRequest(t, invited, http.MethodGet, newServer.URL, path+"/api/messages", nil), http.StatusOK, &state)
	if len(state.Messages) != 1 || state.Messages[0].ID != kept.ID {
		t.Fatal("other room was altered by reset")
	}
	response(t, postJSON(t, stale, newServer.URL, "/api/join", "token", oldSecret), http.StatusForbidden, nil)
}
