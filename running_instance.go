package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const instanceFile = "running-instance.json"

type runningInstance struct {
	Port int    `json:"port"`
	Key  string `json:"key"`
}

func (s *server) recordRunningInstance() error {
	key, err := randomID()
	if err != nil {
		return err
	}
	if err := replaceJSON(filepath.Join(s.room.root, instanceFile), runningInstance{s.port, key}); err != nil {
		return err
	}
	s.instanceKey = key
	return nil
}

func instanceProof(key, challenge string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("lan-transfer-instance:" + challenge))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *server) proveInstance(w http.ResponseWriter, req *http.Request) {
	challenge := req.URL.Query().Get("challenge")
	if !onHost(req) || s.instanceKey == "" || !validID(challenge) {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, instanceProof(s.instanceKey, challenge))
}

func runningRoomURL(root string) (string, bool) {
	var instance runningInstance
	if err := readJSONFile(filepath.Join(root, instanceFile), &instance); err != nil ||
		instance.Port < 1 || instance.Port > 65535 || !validID(instance.Key) {
		return "", false
	}
	base := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(instance.Port))
	challenge, err := randomID()
	if err != nil {
		return "", false
	}
	client := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true}, Timeout: time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Get(base + "/api/instance?challenge=" + challenge)
	if err != nil {
		return "", false
	}
	defer response.Body.Close()
	proof, err := io.ReadAll(io.LimitReader(response.Body, sha256.Size*2+1))
	if err != nil || response.StatusCode != http.StatusOK || !hmac.Equal(proof, []byte(instanceProof(instance.Key, challenge))) {
		return "", false
	}
	var catalog roomCatalog
	if err := readOptionalJSON(filepath.Join(root, catalogFile), &catalog); err != nil {
		return "", false
	}
	if catalog.Default != "" {
		if !validID(catalog.Default) {
			return "", false
		}
		root = filepath.Join(root, "default-rooms", catalog.Default)
	}
	key, err := os.ReadFile(filepath.Join(root, "invite.key"))
	if err != nil || len(key) != 43 {
		return "", false
	}
	return base + "/#" + string(key), true
}
