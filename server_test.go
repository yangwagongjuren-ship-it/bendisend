package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T, r *room) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(nil)
	s.Config.Handler = newServer(r, s.Listener.Addr().(*net.TCPAddr).Port).handler()
	s.Start()
	t.Cleanup(s.Close)
	return s
}

func testClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

func request(t *testing.T, client *http.Client, method, origin, path string, body io.Reader, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, origin+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", origin)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func response(t *testing.T, res *http.Response, code int, value any) []byte {
	t.Helper()
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != code {
		t.Fatalf("%s %s: got %d, want %d: %s", res.Request.Method, res.Request.URL.Path, res.StatusCode, code, body)
	}
	if value != nil {
		if err := json.Unmarshal(body, value); err != nil {
			t.Fatal(err)
		}
	}
	return body
}

func postJSON(t *testing.T, client *http.Client, origin, path, key, value string) *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]string{key: value})
	if err != nil {
		t.Fatal(err)
	}
	return request(t, client, http.MethodPost, origin, path, bytes.NewReader(body), map[string]string{"Content-Type": "application/json"})
}

func readItems(t *testing.T, client *http.Client, origin string) ([]item, string) {
	t.Helper()
	res := request(t, client, http.MethodGet, origin, "/api/items", nil, nil)
	var result struct{ Items []item }
	response(t, res, http.StatusOK, &result)
	return result.Items, res.Header.Get("ETag")
}

func TestRoomTransferAndRestart(t *testing.T) {
	dir := t.TempDir()
	r, err := openRoom(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.lock.Close()
	s := testServer(t, r)
	a, b := testClient(t), testClient(t)
	response(t, request(t, b, http.MethodGet, s.URL, "/api/items", nil, nil), http.StatusUnauthorized, nil)
	response(t, postJSON(t, b, s.URL, "/api/join", "token", "wrong"), http.StatusForbidden, nil)
	joined := postJSON(t, a, s.URL, "/api/join", "token", r.secret)
	response(t, joined, http.StatusNoContent, nil)
	cookies := joined.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Value == r.secret {
		t.Fatal("unexpected room cookie")
	}
	response(t, request(t, a, http.MethodDelete, s.URL, "/api/items/missing", nil, map[string]string{"Origin": "http://other.example"}), http.StatusForbidden, nil)
	_, originalTag := readItems(t, a, s.URL)
	response(t, request(t, a, http.MethodGet, s.URL, "/api/items", nil, map[string]string{"If-None-Match": originalTag}), http.StatusNotModified, nil)

	text := "中文第一行\nhttps://example.test/链接\n第三行 <script>"
	var message item
	response(t, postJSON(t, a, s.URL, "/api/items/text", "text", text), http.StatusCreated, &message)
	name := "照片 01 + 02.png"
	headers := map[string]string{"Content-Type": "application/octet-stream", "X-Filename": url.PathEscape(name)}
	payloads := [][]byte{{0, 0xff, 0xef, 8}, []byte("same name, different bytes\n")}
	files := make([]item, len(payloads))
	for i, data := range payloads {
		response(t, request(t, a, http.MethodPost, s.URL, "/api/items/file", bytes.NewReader(data), headers), http.StatusCreated, &files[i])
	}
	if files[0].ID == files[1].ID {
		t.Fatal("same-name files received the same ID")
	}
	response(t, request(t, b, http.MethodGet, s.URL, "/api/items/"+files[0].ID+"/file", nil, nil), http.StatusUnauthorized, nil)
	response(t, postJSON(t, b, s.URL, "/api/join", "token", r.secret), http.StatusNoContent, nil)
	items, tag := readItems(t, b, s.URL)
	if len(items) != 3 || items[2].Text != text || tag == originalTag {
		t.Fatalf("unexpected history on late join: %+v", items)
	}
	for i, file := range files {
		down := request(t, b, http.MethodGet, s.URL, "/api/items/"+file.ID+"/file", nil, nil)
		got := response(t, down, http.StatusOK, nil)
		_, params, err := mime.ParseMediaType(down.Header.Get("Content-Disposition"))
		if err != nil || params["filename"] != name || sha256.Sum256(got) != sha256.Sum256(payloads[i]) {
			t.Fatalf("download did not match upload %d", i)
		}
	}
	s.Close()
	if err := r.lock.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openRoom(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.lock.Close()
	if reopened.secret != r.secret {
		t.Fatal("invitation changed after restart")
	}
	s2 := testServer(t, reopened)
	response(t, postJSON(t, b, s2.URL, "/api/join", "token", r.secret), http.StatusNoContent, nil)
	items, newTag := readItems(t, b, s2.URL)
	if len(items) != 3 || newTag == tag {
		t.Fatalf("wrong history after restart: %+v", items)
	}
	response(t, request(t, b, http.MethodDelete, s2.URL, "/api/items/"+files[0].ID, nil, nil), http.StatusNoContent, nil)
	response(t, request(t, b, http.MethodGet, s2.URL, "/api/items/"+files[0].ID+"/file", nil, nil), http.StatusNotFound, nil)
	items, _ = readItems(t, b, s2.URL)
	if len(items) != 2 {
		t.Fatalf("wrong history after delete: %+v", items)
	}
}

type zeroBytes struct{}

func (zeroBytes) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestInterruptedAndOversizedUpload(t *testing.T) {
	r, err := openRoom(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.lock.Close()
	s := testServer(t, r)
	client := testClient(t)
	response(t, postJSON(t, client, s.URL, "/api/join", "token", r.secret), http.StatusNoContent, nil)
	response(t, postJSON(t, client, s.URL, "/api/items/text", "text", strings.Repeat("字", maxTextBytes)), http.StatusRequestEntityTooLarge, nil)
	response(t, request(t, client, http.MethodPost, s.URL, "/api/items/file", io.LimitReader(zeroBytes{}, maxFileBytes+1), map[string]string{
		"Content-Type": "application/octet-stream", "X-Filename": "oversized.bin",
	}), http.StatusRequestEntityTooLarge, nil)

	address, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTCP("tcp", nil, s.Listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	cookie := client.Jar.Cookies(address)[0]
	if _, err := fmt.Fprintf(conn, "POST /api/items/file HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nCookie: %s\r\nContent-Type: application/octet-stream\r\nX-Filename: interrupted.bin\r\nContent-Length: 64\r\nConnection: close\r\n\r\npartial", address.Host, s.URL, cookie.String()); err != nil {
		t.Fatal(err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatal(err)
	}
	items, _ := readItems(t, client, s.URL)
	staged, err := os.ReadDir(r.stagingDir)
	if err != nil || len(items) != 0 || len(staged) != 0 {
		t.Fatalf("incomplete data survived: %d items, %d staged, %v", len(items), len(staged), err)
	}
	var empty item
	response(t, request(t, client, http.MethodPost, s.URL, "/api/items/file", strings.NewReader(""), map[string]string{
		"Content-Type": "application/octet-stream", "X-Filename": "empty.txt",
	}), http.StatusCreated, &empty)
	response(t, request(t, client, http.MethodGet, s.URL, "/api/items/"+empty.ID+"/file", nil, nil), http.StatusOK, nil)
}

func TestExpirationAndRoomLimit(t *testing.T) {
	dir := t.TempDir()
	r, err := openRoom(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.lock.Close()
	competing, err := openRoom(dir)
	if err == nil {
		competing.lock.Close()
		t.Fatal("room allowed concurrent writers")
	}
	now := time.Now()
	r.now = func() time.Time { return now }
	for i := 0; i < maxRoomItems; i++ {
		if _, err := r.addText("a"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.addText("overflow"); !errors.Is(err, errFull) {
		t.Fatalf("item limit: %v", err)
	}
	now = now.Add(retention)
	items, used, _, _, _ := r.snapshot("")
	if len(items) != 0 || used != 0 {
		t.Fatalf("expired history was retained: %d items, %d bytes", len(items), used)
	}
	r.usedBytes = r.limits.MaxRoomBytes - 1
	if _, err := r.addText("xx"); !errors.Is(err, errFull) {
		t.Fatalf("byte limit: %v", err)
	}
}
