package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"time"
)

func main() {
	port := flag.Int("port", 8765, "HTTP port (0 selects a free port)")
	dataDir := flag.String("data-dir", "", "room storage directory")
	noBrowser := flag.Bool("no-browser", false, "do not open the default browser")
	flag.Parse()
	automaticPort := true
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "port" {
			automaticPort = false
		}
	})
	if err := run(*port, automaticPort, *dataDir, *noBrowser); err != nil {
		log.Print(err)
		if !*noBrowser {
			showStartupError(err.Error())
		}
		os.Exit(1)
	}
}

func run(port int, automaticPort bool, dataDir string, noBrowser bool) error {
	if port < 0 || port > 65535 {
		return errors.New("端口必须在 0 到 65535 之间。")
	}
	if dataDir == "" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			var err error
			base, err = os.UserConfigDir()
			if err != nil {
				return err
			}
		}
		dataDir = filepath.Join(base, "LanTransfer")
	}
	listener, err := listenForRoom(port, automaticPort)
	if err != nil {
		return fmt.Errorf("无法监听端口 %d。可以更换 -port 参数，或双击程序自动选择端口：%w", port, err)
	}
	defer listener.Close()
	r, err := openRoom(dataDir)
	if err != nil {
		if errors.Is(err, errRoomInUse) {
			if existing, ok := runningRoomURL(dataDir); ok {
				fmt.Println("互传已在运行。")
				if !noBrowser {
					return openBrowser(existing)
				}
				return nil
			}
			return errors.New("这间共享收件箱已由另一个互传窗口打开。请先关闭正在运行的旧版或新版，再启动此程序。")
		}
		return fmt.Errorf("无法打开房间数据目录 %q：%w", dataDir, err)
	}
	defer r.lock.Close()
	app := newServer(r, listener.Addr().(*net.TCPAddr).Port)
	group, err := openRoomGroup(app)
	if err != nil {
		return fmt.Errorf("无法打开房间列表：%w", err)
	}
	defer group.close()
	if err := app.recordRunningInstance(); err != nil {
		log.Printf("Record running instance: %v", err)
	}
	server := &http.Server{
		Handler: app.handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Minute, WriteTimeout: 11 * time.Minute,
		IdleTimeout: 45 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		maintainRoom(ctx, server, r)
	}()
	activeDefault := group.defaultServer
	local := activeDefault.inviteURL("127.0.0.1")
	fmt.Println("BendiSend is running. Keep this window open. Press Ctrl+C to stop.")
	if port != 0 && port != app.port {
		fmt.Printf("Port %d is in use. Using port %d.\n", port, app.port)
	}
	fmt.Println("Host invite:", local)
	for _, address := range lanAddresses() {
		fmt.Printf("%s: %s\n", address.Label, activeDefault.inviteURL(address.IP))
	}
	if !noBrowser {
		if err := openBrowser(local); err != nil {
			log.Printf("Open the host invite above in a browser: %v", err)
		}
	}
	err = server.Serve(listener)
	stop()
	<-stopped
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func listenForRoom(port int, automatic bool) (net.Listener, error) {
	listener, err := net.Listen("tcp4", ":"+strconv.Itoa(port))
	if automatic && port != 0 && port < 65535 && addressInUse(err) {
		listener, err = net.Listen("tcp4", ":"+strconv.Itoa(port+1))
	}
	if automatic && port != 0 && addressInUse(err) {
		return net.Listen("tcp4", ":0")
	}
	return listener, err
}

func maintainRoom(ctx context.Context, srv *http.Server, r *room) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdown); err != nil {
				srv.Close()
			}
			return
		case <-ticker.C:
			r.mu.Lock()
			r.group.expireLocked()
			r.group.cleanTrashLocked()
			r.mu.Unlock()
		}
	}
}
