package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const deviceCookie = "lan_transfer_device"

func (s *server) registerRoomRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/device", s.auth(s.identify))
	mux.Handle("GET /api/messages", s.withDevice(s.messages))
	mux.Handle("GET /api/limits", s.hostOnly(s.getLimits))
	mux.Handle("PUT /api/limits", s.hostOnly(s.putLimits))
	mux.Handle("GET /api/transfers/active", s.withDevice(s.activeUpload))
	mux.Handle("POST /api/transfers", s.withDevice(s.beginUpload))
	mux.Handle("GET /api/transfers/{id}", s.withDevice(s.uploadStatus))
	mux.Handle("PUT /api/transfers/{id}/files/{file}", s.withDevice(s.putChunk))
	mux.Handle("GET /api/transfers/{id}/files/{file}/checksums", s.withDevice(s.checksums))
	mux.Handle("POST /api/transfers/{id}/commit", s.withDevice(s.publish))
	mux.Handle("DELETE /api/transfers/{id}", s.withDevice(s.cancelUpload))
	mux.Handle("GET /api/messages/{id}/files/{file}", s.auth(s.downloadAttachment))
	mux.Handle("DELETE /api/messages/{id}", s.auth(s.removeMessage))
}

// Require both sides of the accepted connection to be loopback. Host already
// passed validHost and writing requests already passed the same-origin check.
func onHost(req *http.Request) bool {
	local, ok := req.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	if !ok || !local.IP.IsLoopback() {
		return false
	}
	remote, _, err := net.SplitHostPort(req.RemoteAddr)
	return err == nil && net.ParseIP(remote).IsLoopback()
}

func (s *server) hostOnly(next http.HandlerFunc) http.Handler {
	return s.auth(func(w http.ResponseWriter, req *http.Request) {
		if !s.canManage(req) {
			apiError(w, http.StatusForbidden, "请在运行互传程序的 Windows 主机上操作。")
			return
		}
		next(w, req)
	})
}

func (s *server) canManage(req *http.Request) bool {
	return onHost(req) && (s.room.group == nil || s.room.group.defaultAuthorized(req))
}

func (s *server) deviceMAC(value string) []byte {
	mac := hmac.New(sha256.New, []byte(s.room.limitOwner().secret))
	mac.Write([]byte("lan-transfer-device-v1:"))
	mac.Write([]byte(value))
	return mac.Sum(nil)
}

func (s *server) browserDevice(req *http.Request) (device, bool) {
	var d device
	cookie, err := req.Cookie(deviceCookie)
	if err != nil {
		return d, false
	}
	value, encodedMAC, ok := strings.Cut(cookie.Value, ".")
	if !ok || len(value) > 512 {
		return d, false
	}
	mac, err := base64.RawURLEncoding.Strict().DecodeString(encodedMAC)
	if err != nil || !hmac.Equal(mac, s.deviceMAC(value)) {
		return d, false
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || json.Unmarshal(data, &d) != nil || !validID(d.ID) {
		return device{}, false
	}
	return d, true
}

func (s *server) withDevice(next func(http.ResponseWriter, *http.Request, device)) http.Handler {
	return s.auth(func(w http.ResponseWriter, req *http.Request) {
		d, ok := s.browserDevice(req)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, apiProblem{Code: "device_required", Message: "请重新连接设备。"})
			return
		}
		next(w, req, d)
	})
}

func deviceLabel(req *http.Request) (string, string) {
	ua := req.UserAgent()
	switch {
	case onHost(req):
		return "host", "主机"
	case strings.Contains(ua, "iPhone"):
		return "iphone", "iPhone"
	case strings.Contains(ua, "iPad"):
		return "ipad", "iPad"
	case strings.Contains(ua, "Android"):
		return "android", "Android"
	case strings.Contains(ua, "Windows"):
		return "windows", "Windows 电脑"
	case strings.Contains(ua, "Macintosh"):
		return "mac", "Mac"
	default:
		return "browser", "浏览器"
	}
}

func (s *server) identify(w http.ResponseWriter, req *http.Request) {
	d, ok := s.browserDevice(req)
	if !ok {
		kind, label := deviceLabel(req)
		var err error
		d, err = s.room.limitOwner().registerDevice(kind, label)
		if err != nil {
			roomError(w, err)
			return
		}
		data, _ := json.Marshal(d)
		value := base64.RawURLEncoding.EncodeToString(data)
		http.SetCookie(w, &http.Cookie{
			Name: deviceCookie, Value: value + "." + base64.RawURLEncoding.EncodeToString(s.deviceMAC(value)),
			Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 365 * 24 * 60 * 60,
		})
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) messages(w http.ResponseWriter, req *http.Request, d device) {
	state, tag, unchanged := s.room.timeline(d, s.canManage(req), req.Header.Get("If-None-Match"))
	if s.room.deleted.Load() {
		roomError(w, errRoomDeleted)
		return
	}
	w.Header().Set("ETag", tag)
	if unchanged {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *server) getLimits(w http.ResponseWriter, _ *http.Request) {
	r := s.room
	r.mu.Lock()
	limits := r.hostLimitsLocked()
	used, pending, _ := r.totalsLocked()
	r.mu.Unlock()
	disk, err := r.checkFree(r.uploadsDir)
	if err != nil {
		roomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		uploadLimits
		Used    int64 `json:"usedBytes"`
		Pending int64 `json:"pendingBytes"`
		Free    int64 `json:"freeBytes"`
	}{limits, used, pending, min(disk, int64(maxByteSetting))})
}

func (s *server) putLimits(w http.ResponseWriter, req *http.Request) {
	var l uploadLimits
	if !readJSON(w, req, 512, &l) {
		return
	}
	if err := s.room.setLimits(l); err != nil {
		roomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *server) activeUpload(w http.ResponseWriter, _ *http.Request, d device) {
	active := s.room.activeTransfer(d.ID)
	if s.room.deleted.Load() {
		roomError(w, errRoomDeleted)
		return
	}
	writeJSON(w, http.StatusOK, active)
}

func (s *server) beginUpload(w http.ResponseWriter, req *http.Request, d device) {
	var draft draftRequest
	if !readJSON(w, req, 256<<10, &draft) {
		return
	}
	if !validID(draft.ID) || !validMessageContent(draft.Text, draft.Files) {
		apiError(w, http.StatusBadRequest, "待发内容无效，检查文字长度和附件数量后重试。")
		return
	}
	for i := range draft.Files {
		name, err := cleanFilename(draft.Files[i].Name)
		if err != nil {
			apiError(w, http.StatusBadRequest, "文件名无效。")
			return
		}
		draft.Files[i].Name = name
	}
	view, err := s.room.startTransfer(d, draft)
	if err != nil {
		roomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *server) uploadStatus(w http.ResponseWriter, req *http.Request, d device) {
	view, err := s.room.transferStatus(d.ID, req.PathValue("id"))
	if err != nil {
		roomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *server) putChunk(w http.ResponseWriter, req *http.Request, d device) {
	index, indexErr := strconv.Atoi(req.PathValue("file"))
	offset, offsetErr := strconv.ParseInt(req.Header.Get("Upload-Offset"), 10, 64)
	if indexErr != nil || offsetErr != nil || offset < 0 || offset%chunkSize != 0 || req.Header.Get("Content-Type") != "application/octet-stream" {
		roomError(w, errChunkInvalid)
		return
	}
	if req.ContentLength > chunkSize {
		apiError(w, http.StatusRequestEntityTooLarge, "单次上传分片最多 8 MiB。")
		return
	}
	select {
	case s.uploadSlots <- struct{}{}:
		defer func() { <-s.uploadSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		apiError(w, http.StatusTooManyRequests, "主机正在收文件，稍后继续。")
		return
	}
	data, err := io.ReadAll(roomReader{s.room, http.MaxBytesReader(w, req.Body, chunkSize)})
	if err != nil {
		if errors.Is(err, errRoomDeleted) {
			roomError(w, err)
			return
		}
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			apiError(w, http.StatusRequestEntityTooLarge, "单次上传分片最多 8 MiB。")
		} else {
			apiError(w, http.StatusBadRequest, "分片没有完整收到，请重试。")
		}
		return
	}
	next, err := s.room.appendChunk(d.ID, req.PathValue("id"), index, offset, data)
	if err != nil {
		roomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"offset": next})
}

func (s *server) checksums(w http.ResponseWriter, req *http.Request, d device) {
	index, indexErr := strconv.Atoi(req.PathValue("file"))
	start, startErr := strconv.ParseInt(req.URL.Query().Get("from"), 10, 64)
	if indexErr != nil || startErr != nil {
		roomError(w, errChunkInvalid)
		return
	}
	hashes, err := s.room.transferDigests(d.ID, req.PathValue("id"), index, start)
	if err != nil {
		roomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hashes)
}

func (s *server) publish(w http.ResponseWriter, req *http.Request, d device) {
	m, err := s.room.commitTransfer(d.ID, req.PathValue("id"))
	if err != nil {
		roomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *server) cancelUpload(w http.ResponseWriter, req *http.Request, d device) {
	if err := s.room.cancelTransfer(d.ID, req.PathValue("id")); err != nil {
		roomError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) downloadAttachment(w http.ResponseWriter, req *http.Request) {
	index, err := strconv.Atoi(req.PathValue("file"))
	if err != nil {
		roomError(w, errMissing)
		return
	}
	file, created, f, err := s.room.openAttachment(req.PathValue("id"), index)
	if err != nil {
		roomError(w, err)
		return
	}
	defer f.Close()
	serveAttachment(w, req, file.Name, created, f)
}

func serveAttachment(w http.ResponseWriter, req *http.Request, name string, created time.Time, file io.ReadSeeker) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	writer := &downloadWriter{ResponseWriter: w, control: http.NewResponseController(w)}
	writer.refreshDeadline()
	http.ServeContent(writer, req, name, created, file)
}

type downloadWriter struct {
	http.ResponseWriter
	control *http.ResponseController
	next    time.Time
}

func (w *downloadWriter) refreshDeadline() {
	now := time.Now()
	w.control.SetWriteDeadline(now.Add(2 * time.Minute))
	w.next = now.Add(15 * time.Second)
}

func (w *downloadWriter) Write(p []byte) (int, error) {
	if time.Now().After(w.next) {
		w.refreshDeadline()
	}
	return w.ResponseWriter.Write(p)
}

func (s *server) removeMessage(w http.ResponseWriter, req *http.Request) {
	if err := s.room.deleteMessage(req.PathValue("id")); err != nil && !errors.Is(err, errMissing) {
		if errors.Is(err, errRoomDeleted) {
			roomError(w, err)
			return
		}
		log.Printf("Delete message: %v", err)
		apiError(w, http.StatusConflict, "暂时无法删除，若有设备在下载，请稍后重试。")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type apiProblem struct {
	Code     string `json:"code,omitempty"`
	Message  string `json:"message"`
	Limit    int64  `json:"limit,omitempty"`
	Required int64  `json:"required,omitempty"`
	Offset   *int64 `json:"offset,omitempty"`
}

func roomError(w http.ResponseWriter, err error) {
	var quota *quotaProblem
	var offset *offsetConflict
	switch {
	case errors.Is(err, errRoomDeleted):
		writeJSON(w, http.StatusGone, apiProblem{Code: "room_deleted", Message: "房间已删除。"})
	case errors.Is(err, errRoomGeneration):
		apiError(w, http.StatusConflict, "房间已变化，请重新打开后再操作。")
	case errors.As(err, &quota):
		status, message := http.StatusInsufficientStorage, "已达到所有房间共享总额。"
		if quota.kind == "file_limit" {
			status, message = http.StatusRequestEntityTooLarge, "已达到单文件上限。"
		}
		writeJSON(w, status, apiProblem{Code: quota.kind, Message: message, Limit: quota.limit, Required: quota.required})
	case errors.Is(err, errDiskSpace):
		writeJSON(w, http.StatusInsufficientStorage, apiProblem{Code: "disk_space", Message: "主机磁盘空间不足，请释放空间后重试。"})
	case errors.Is(err, errBadLimits):
		apiError(w, http.StatusBadRequest, "上限须大于零，单文件上限须小于或等于房间总额。")
	case errors.As(err, &offset):
		writeJSON(w, http.StatusConflict, apiProblem{Code: "offset_conflict", Message: "上传进度已变化，已确认的分片会保留。", Offset: &offset.offset})
	case errors.Is(err, errChunkInvalid):
		apiError(w, http.StatusBadRequest, "分片长度或位置无效。")
	case errors.Is(err, errIncomplete):
		apiError(w, http.StatusConflict, "附件尚未全部上传。")
	case errors.Is(err, errDraftBusy):
		writeJSON(w, http.StatusConflict, apiProblem{Code: "draft_busy", Message: "这台设备已有一条待续传消息。请在原页面继续或取消后重新发送。"})
	case errors.Is(err, errIDConflict):
		apiError(w, http.StatusConflict, "该传输已提交或编号已被使用，请查看记录。")
	default:
		storageError(w, err)
	}
}
