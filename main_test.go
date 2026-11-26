package main

import (
	"errors"
	"net"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestOccupiedPort(t *testing.T) {
	busy, err := net.Listen("tcp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	if explicit, err := listenForRoom(port, false); err == nil {
		explicit.Close()
		t.Fatal("explicit port silently changed")
	} else if !addressInUse(err) {
		t.Fatal(err)
	}
	alternative, err := listenForRoom(port, true)
	if err != nil {
		t.Fatal(err)
	}
	defer alternative.Close()
	if alternative.Addr().(*net.TCPAddr).Port == port {
		t.Fatal("automatic mode reused the occupied port")
	}
}

func TestRunningRoomRequiresInstanceProof(t *testing.T) {
	r, err := openRoom(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.lock.Close()
	s := httptest.NewUnstartedServer(nil)
	app := newServer(r, s.Listener.Addr().(*net.TCPAddr).Port)
	g, err := openRoomGroup(app)
	if err != nil {
		t.Fatal(err)
	}
	defer g.close()
	if err := app.recordRunningInstance(); err != nil {
		t.Fatal(err)
	}
	s.Config.Handler = app.handler()
	s.Start()
	defer s.Close()
	if got, ok := runningRoomURL(r.root); !ok || got != s.URL+"/#"+r.secret {
		t.Fatal("running room did not verify")
	}
	key, err := randomID()
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceJSON(filepath.Join(r.root, instanceFile), runningInstance{app.port, key}); err != nil {
		t.Fatal(err)
	}
	if _, ok := runningRoomURL(r.root); ok {
		t.Fatal("stale instance descriptor was accepted")
	}
}

func TestRoomAlreadyOpen(t *testing.T) {
	dir := t.TempDir()
	r, err := openRoom(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.lock.Close()
	if second, err := openRoom(dir); err == nil {
		second.lock.Close()
		t.Fatal("opened the same room twice")
	} else if !errors.Is(err, errRoomInUse) {
		t.Fatalf("expected the room-in-use error, got %v", err)
	}
}
