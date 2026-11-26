package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	qrcode "github.com/skip2/go-qrcode"
)

const cookieName = "lan_transfer_session"

//go:embed web/* web/hash/*.js
var webAssets embed.FS

type server struct {
	room        *room
	port        int
	credential  string
	uploadSlots chan struct{}
	instanceKey string
}

type lanAddress struct {
	IP    string `json:"ip"`
	Label string `json:"label"`
}

type invitation struct {
	IP    string `json:"ip"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

func newServer(r *room, port int) *server {
	mac := hmac.New(sha256.New, []byte(r.secret))
	mac.Write([]byte("lan-transfer-browser-session-v1"))
	return &server{
		room: r, port: port, credential: base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),
		uploadSlots: make(chan struct{}, 2),
	}
}

func (s *server) handler() http.Handler {
	var target http.Handler = s.roomHandler()
	if g := s.room.group; g != nil && g.primary == s {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/rooms", s.listRooms)
		mux.HandleFunc("GET /api/instance", s.proveInstance)
		mux.HandleFunc("POST /api/rooms", s.createRoom)
		mux.HandleFunc("DELETE /api/rooms/{room}", s.deleteRoom)
		mux.HandleFunc("/r/{room}/", g.dispatch)
		mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) { g.serveDefault(w, req, false) })
		target = mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !s.validHost(req) {
			apiError(w, http.StatusForbidden, "请从主机显示的局域网地址访问。")
			return
		}
		if req.Method != http.MethodGet && req.Method != http.MethodHead && req.Header.Get("Origin") != "http://"+req.Host {
			apiError(w, http.StatusForbidden, "请求来源与房间地址不一致。")
			return
		}
		target.ServeHTTP(w, req)
	})
}

func (s *server) roomHandler() http.Handler {
	assets, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", http.FileServerFS(assets))
	mux.Handle("GET /app.js", http.FileServerFS(assets))
	mux.Handle("GET /rooms.js", http.FileServerFS(assets))
	mux.Handle("GET /styles.css", http.FileServerFS(assets))
	mux.Handle("GET /hash/", http.FileServerFS(assets))
	mux.HandleFunc("POST /api/join", s.join)
	mux.Handle("GET /api/invites", s.auth(s.invites))
	mux.Handle("GET /api/qr", s.auth(s.qr))
	mux.Handle("GET /api/items", s.auth(s.listItems))
	mux.Handle("POST /api/items/text", s.auth(s.sendText))
	mux.Handle("POST /api/items/file", s.auth(s.sendFile))
	mux.Handle("GET /api/items/{id}/file", s.auth(s.download))
	mux.Handle("DELETE /api/items/{id}", s.auth(s.deleteItem))
	s.registerRoomRoutes(mux)
	s.registerRoomManagement(mux)
	return mux
}

func (s *server) validHost(req *http.Request) bool {
	local, ok := req.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	if !ok {
		return false
	}
	remoteHost, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return false
	}
	remote, err := netip.ParseAddr(remoteHost)
	if err != nil || (!remote.IsLoopback() && !isLAN(remote.Unmap())) {
		return false
	}
	localIP, ok := netip.AddrFromSlice(local.IP)
	if !ok || (!localIP.IsLoopback() && !isLAN(localIP.Unmap())) {
		return false
	}
	host, port, err := net.SplitHostPort(req.Host)
	if err != nil {
		host, port = req.Host, "80"
	}
	if port != strconv.Itoa(s.port) {
		return false
	}
	if host == "localhost" {
		return localIP.IsLoopback() && remote.IsLoopback()
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Is4() && ip == localIP.Unmap()
}

func isLAN(ip netip.Addr) bool {
	return ip.Is4() && (ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func (s *server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		generation := req.Header.Get("X-Room-Generation")
		if s.room.deleted.Load() || (generation != "" && generation != s.room.generation) {
			roomError(w, errRoomDeleted)
			return
		}
		allowed := s.legacySession(req)
		if s.room.id != defaultRoomID {
			allowed = s.room.group.canEnter(req, s.room.id)
		}
		if !allowed {
			apiError(w, http.StatusUnauthorized, "请用邀请链接加入房间。")
			return
		}
		next(w, req)
	})
}

func (s *server) legacySession(req *http.Request) bool {
	cookie, err := req.Cookie(cookieName)
	return err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(s.credential)) == 1
}

func (s *server) join(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !readJSON(w, req, 512, &body) {
		return
	}
	if s.room.deleted.Load() {
		roomError(w, errRoomDeleted)
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Token), []byte(s.room.secret)) != 1 {
		apiError(w, http.StatusForbidden, "邀请链接无效，请重新扫码。")
		return
	}
	if s.room.id != defaultRoomID {
		if err := s.room.group.join(w, req, s.room.id); err != nil {
			roomError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.setDefaultCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) setDefaultCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.credential, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 60 * 60,
	})
}

func lanAddresses() []lanAddress {
	interfaces, _ := net.Interfaces()
	addresses := make([]lanAddress, 0)
	seen := make(map[string]bool)
	for _, nic := range interfaces {
		if nic.Flags&net.FlagUp == 0 || nic.Flags&net.FlagLoopback != 0 {
			continue
		}
		ips, _ := nic.Addrs()
		for _, address := range ips {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil || !isLAN(prefix.Addr()) {
				continue
			}
			ip := prefix.Addr().String()
			if !seen[ip] {
				addresses = append(addresses, lanAddress{IP: ip, Label: nic.Name})
				seen[ip] = true
			}
		}
	}
	sort.Slice(addresses, func(i, j int) bool {
		a, b := addresses[i], addresses[j]
		if lanPriority(a) != lanPriority(b) {
			return lanPriority(a) < lanPriority(b)
		}
		if a.Label != b.Label {
			return a.Label < b.Label
		}
		return a.IP < b.IP
	})
	return addresses
}

func lanPriority(address lanAddress) int {
	if strings.HasPrefix(address.IP, "169.254.") {
		return 3
	}
	label := strings.ToLower(address.Label)
	if strings.Contains(label, "wi-fi") || strings.Contains(label, "wlan") || strings.Contains(label, "无线") {
		return 0
	}
	if strings.HasPrefix(label, "ethernet") || strings.HasPrefix(label, "以太网") {
		return 1
	}
	return 2
}

func (s *server) inviteURL(ip string) string {
	path := "/"
	if s.room.id != defaultRoomID {
		path = "/r/" + s.room.id + "/"
	}
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(s.port)) + path + "#" + s.room.secret
}

func (s *server) invites(w http.ResponseWriter, req *http.Request) {
	addresses := lanAddresses()
	result := make([]invitation, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, invitation{IP: address.IP, Label: address.Label, URL: s.inviteURL(address.IP)})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *server) qr(w http.ResponseWriter, req *http.Request) {
	ip := req.URL.Query().Get("ip")
	for _, address := range lanAddresses() {
		if ip == address.IP {
			png, err := qrcode.Encode(s.inviteURL(ip), qrcode.Medium, 260)
			if err != nil {
				storageError(w, err)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Write(png)
			return
		}
	}
	apiError(w, http.StatusNotFound, "这个局域网地址已不可用，请刷新页面。")
}

func (s *server) listItems(w http.ResponseWriter, req *http.Request) {
	items, used, limits, etag, unchanged := s.room.snapshot(req.Header.Get("If-None-Match"))
	if s.room.deleted.Load() {
		roomError(w, errRoomDeleted)
		return
	}
	w.Header().Set("ETag", etag)
	if unchanged {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items        []item `json:"items"`
		UsedBytes    int64  `json:"usedBytes"`
		MaxRoomBytes int64  `json:"maxRoomBytes"`
		MaxRoomItems int    `json:"maxRoomItems"`
		MaxTextBytes int    `json:"maxTextBytes"`
		MaxFileBytes int64  `json:"maxFileBytes"`
	}{items, used, limits.MaxRoomBytes, maxRoomItems, maxTextBytes, min(limits.MaxFileBytes, int64(maxFileBytes))})
}

func (s *server) sendText(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !readJSON(w, req, maxTextBytes*6+512, &body) {
		return
	}
	if len(body.Text) > maxTextBytes {
		apiError(w, http.StatusRequestEntityTooLarge, "每条文字最多 16 KiB。")
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		apiError(w, http.StatusBadRequest, "先输入或粘贴文字。")
		return
	}
	it, err := s.room.addText(body.Text)
	if err != nil {
		roomError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, it)
}

func (s *server) sendFile(w http.ResponseWriter, req *http.Request) {
	if req.Header.Get("Content-Type") != "application/octet-stream" {
		apiError(w, http.StatusUnsupportedMediaType, "上传格式无效。")
		return
	}
	name, err := parseFilename(req.Header.Get("X-Filename"))
	if err != nil {
		apiError(w, http.StatusBadRequest, "文件名无效。")
		return
	}
	if req.ContentLength > maxFileBytes {
		storageError(w, errTooLarge)
		return
	}
	select {
	case s.uploadSlots <- struct{}{}:
		defer func() { <-s.uploadSlots }()
	default:
		apiError(w, http.StatusTooManyRequests, "主机正在接收其他文件，稍后再试。")
		return
	}
	req.Body = http.MaxBytesReader(w, req.Body, maxFileBytes)
	it, err := s.room.addFile(name, req.Body, req.ContentLength)
	if err != nil {
		roomError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, it)
}

func parseFilename(header string) (string, error) {
	name, err := url.PathUnescape(header)
	if err != nil {
		return "", err
	}
	return cleanFilename(name)
}

func cleanFilename(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", errors.New("invalid file name")
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:<>"|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(strings.TrimSpace(name), ". ")
	if name == "" || len(name) > 1024 {
		return "", errors.New("invalid file name length")
	}
	return name, nil
}

func (s *server) download(w http.ResponseWriter, req *http.Request) {
	it, f, err := s.room.openFile(req.PathValue("id"))
	if err != nil {
		roomError(w, err)
		return
	}
	defer f.Close()
	serveAttachment(w, req, it.Name, it.CreatedAt, f)
}

func (s *server) deleteItem(w http.ResponseWriter, req *http.Request) {
	if err := s.room.remove(req.PathValue("id")); err != nil && !errors.Is(err, errMissing) {
		if errors.Is(err, errRoomDeleted) {
			roomError(w, err)
			return
		}
		log.Printf("Delete item: %v", err)
		apiError(w, http.StatusConflict, "暂时无法删除，若有设备在下载，请等下载结束后重试。")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func readJSON(w http.ResponseWriter, req *http.Request, limit int64, dest any) bool {
	mediaType, _, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		apiError(w, http.StatusUnsupportedMediaType, "请求格式无效。")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, limit))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(dest)
	if err == nil {
		err = decoder.Decode(new(json.RawMessage))
		if errors.Is(err, io.EOF) {
			return true
		}
	}
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		apiError(w, http.StatusRequestEntityTooLarge, "请求内容太长。")
	} else {
		apiError(w, http.StatusBadRequest, "请求内容无效。")
	}
	return false
}

func storageError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	switch {
	case errors.Is(err, errFull):
		apiError(w, http.StatusInsufficientStorage, "房间已满，请删除一些旧记录后重试。")
	case errors.Is(err, errTooLarge), errors.As(err, &maxErr):
		apiError(w, http.StatusRequestEntityTooLarge, "单个文件最多 100 MiB。")
	case errors.Is(err, errMissing):
		apiError(w, http.StatusNotFound, "这条记录已删除或过期。")
	case errors.Is(err, io.ErrUnexpectedEOF):
		apiError(w, http.StatusBadRequest, "上传中断，请重试。")
	default:
		log.Printf("Room operation: %v", err)
		apiError(w, http.StatusInternalServerError, "主机暂时无法保存或读取，请检查磁盘空间和程序窗口。")
	}
}

func apiError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("Write response: %v", err)
	}
}
