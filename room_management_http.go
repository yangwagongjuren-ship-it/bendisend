package main

import (
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func (s *server) registerRoomManagement(mux *http.ServeMux) {
	mux.Handle("GET /api/room/retention", s.hostOnly(s.previewRetention))
	mux.Handle("PUT /api/room/retention", s.hostOnly(s.saveRetention))
	mux.Handle("GET /api/room/clear", s.hostOnly(s.previewClear))
	mux.Handle("POST /api/room/clear", s.hostOnly(s.clearMessages))
}

func (s *server) listRooms(w http.ResponseWriter, req *http.Request) {
	rooms, host, err := s.room.group.list(req)
	if err != nil {
		roomError(w, err)
		return
	}
	if len(rooms) == 0 {
		apiError(w, http.StatusUnauthorized, "请用邀请链接加入房间。")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Rooms     []roomInfo `json:"rooms"`
		CanCreate bool       `json:"canCreate"`
	}{rooms, host})
}

func (s *server) createRoom(w http.ResponseWriter, req *http.Request) {
	if !s.canManage(req) {
		s.denyRoomManagement(w, req)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, req, 512, &body) {
		return
	}
	policy := roomPolicy{Name: strings.TrimSpace(body.Name), Days: 7}
	if !policy.valid() {
		apiError(w, http.StatusBadRequest, "请输入 1 至 40 个字符作为房间名称。")
		return
	}
	room, err := s.room.group.create(policy)
	if err != nil {
		roomError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, room)
}

func (s *server) denyRoomManagement(w http.ResponseWriter, req *http.Request) {
	status := http.StatusUnauthorized
	if s.room.group.defaultAuthorized(req) {
		status = http.StatusForbidden
	}
	apiError(w, status, "请在运行互传程序的 Windows 主机上操作。")
}

func (s *server) deleteRoom(w http.ResponseWriter, req *http.Request) {
	if !s.canManage(req) {
		s.denyRoomManagement(w, req)
		return
	}
	id := req.PathValue("room")
	var body struct {
		Generation string `json:"generation"`
	}
	if !readJSON(w, req, 512, &body) {
		return
	}
	if (id != defaultRoomID && !validID(id)) || (body.Generation != defaultRoomID && !validID(body.Generation)) {
		apiError(w, http.StatusBadRequest, "房间编号或版本无效。")
		return
	}
	replacement, err := s.room.group.delete(req, id, body.Generation)
	if err != nil {
		roomError(w, err)
		return
	}
	if id == defaultRoomID {
		replacement.setDefaultCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) previewRetention(w http.ResponseWriter, req *http.Request) {
	days, err := strconv.Atoi(req.URL.Query().Get("days"))
	if err != nil || !validRetention(days) {
		apiError(w, http.StatusBadRequest, "请选择有效的保留时间。")
		return
	}
	r := s.room
	r.mu.Lock()
	if r.deleted.Load() {
		r.mu.Unlock()
		roomError(w, errRoomDeleted)
		return
	}
	count := r.retentionPreviewLocked(days, r.now())
	r.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]int{"expired": count})
}

func (s *server) saveRetention(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Days     *int `json:"retentionDays"`
		Expected int  `json:"expectedExpired"`
	}
	if !readJSON(w, req, 512, &body) {
		return
	}
	if body.Days == nil || !validRetention(*body.Days) || body.Expected < 0 || body.Expected > maxRoomItems {
		apiError(w, http.StatusBadRequest, "请选择有效的保留时间。")
		return
	}
	if err := s.room.setRetention(*body.Days, body.Expected); err != nil {
		if errors.Is(err, errPolicyChanged) {
			apiError(w, http.StatusConflict, "记录已变化，请重新查看将过期的条数。")
		} else if errors.Is(err, errExpiredInUse) {
			apiError(w, http.StatusConflict, "有过期的附件尚未释放，请等下载结束后重试。")
		} else {
			roomError(w, err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) previewClear(w http.ResponseWriter, _ *http.Request) {
	r := s.room
	r.mu.Lock()
	if r.deleted.Load() {
		r.mu.Unlock()
		roomError(w, errRoomDeleted)
		return
	}
	now := r.now()
	r.expireLocked(now)
	room := r.infoLocked()
	ids := make([]string, 0, len(r.items)+len(r.messages))
	for id, it := range r.items {
		if !r.expired(it.CreatedAt, now) {
			ids = append(ids, id)
		}
	}
	for id, m := range r.messages {
		if !r.expired(m.CreatedAt, now) {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	sort.Strings(ids)
	writeJSON(w, http.StatusOK, struct {
		Room roomInfo `json:"room"`
		IDs  []string `json:"ids"`
	}{room, ids})
}

func (s *server) clearMessages(w http.ResponseWriter, req *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !readJSON(w, req, 8<<10, &body) {
		return
	}
	seen := make(map[string]bool, len(body.IDs))
	if len(body.IDs) > maxRoomItems {
		apiError(w, http.StatusBadRequest, "本次清空的记录过多，请重新确认。")
		return
	}
	for _, id := range body.IDs {
		if !validID(id) || seen[id] {
			apiError(w, http.StatusBadRequest, "记录编号无效，请重新确认。")
			return
		}
		seen[id] = true
	}
	var deleted int
	failed := make([]string, 0)
	s.room.mu.Lock()
	if s.room.deleted.Load() {
		s.room.mu.Unlock()
		roomError(w, errRoomDeleted)
		return
	}
	for _, id := range body.IDs {
		err := s.room.deleteMessageByIDLocked(id)
		if err == nil {
			deleted++
		} else if !errors.Is(err, errMissing) {
			log.Printf("Clear message %s: %v", id, err)
			failed = append(failed, id)
		}
	}
	s.room.mu.Unlock()
	writeJSON(w, http.StatusOK, struct {
		Deleted int      `json:"deleted"`
		Failed  []string `json:"failed"`
	}{deleted, failed})
}
