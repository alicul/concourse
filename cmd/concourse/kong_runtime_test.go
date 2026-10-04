//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/concourse/concourse/v8/atc"
	"golang.org/x/crypto/ssh"
)

// This opt-in smoke test exercises the built Concourse executable, its real
// WorkerCommand, Houdini Garden server, Baggageclaim and healthcheck server.
// It requires Linux and root, but no PostgreSQL, containerd or external TSA.
func TestKongConcourseWorkerRuntime(t *testing.T) {
	binary := os.Getenv("CONCOURSE_KONG_TEST_BINARY")
	if binary == "" {
		t.Skip("set CONCOURSE_KONG_TEST_BINARY to the built cmd/concourse executable")
	}
	if os.Geteuid() != 0 {
		t.Skip("Concourse's Linux worker requires root")
	}
	port := func() int {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.Addr().(*net.TCPAddr).Port
	}
	gardenPort, baggageclaimPort, healthPort := port(), port(), port()
	dir := t.TempDir()
	resources := filepath.Join(dir, "resources")
	if err := os.Mkdir(resources, 0755); err != nil {
		t.Fatal(err)
	}
	key := kongTestKey(t)
	tsaAddress, tsaKey, registrations := kongTestTSA(t, key)
	config := kongTestConfig(t, fmt.Sprintf(`worker:
  work-dir: %q
  tsa-worker-private-key: %q
  tsa-host: [%q]
  tsa-public-key: %q
  name: from-file
  tag: [from-file]
  runtime: houdini
  resource-types: %q
  bind-port: %d
  baggageclaim-bind-port: %d
  baggageclaim-driver: naive
  baggageclaim-disable-user-namespaces: true
  baggageclaim-debug-bind-port: 0
  debug-bind-port: 0
  healthcheck-bind-ip: 127.0.0.1
  healthcheck-bind-port: %d
  connection-drain-timeout: 1s
`, dir, key, tsaAddress, tsaKey, resources, gardenPort, baggageclaimPort, healthPort))
	logname := filepath.Join(dir, "worker.log")
	log, err := os.Create(logname)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	command := exec.Command(binary, "--config", config, "worker", "--name", "kong-mvp")
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "CONCOURSE_") {
			command.Env = append(command.Env, variable)
		}
	}
	command.Env = append(command.Env, "CONCOURSE_NAME=from-env")
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	defer func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-exited:
			if err != nil {
				t.Errorf("worker exited: %v", err)
			}
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-exited
			t.Error("worker did not stop gracefully")
		}
		if t.Failed() {
			data, _ := os.ReadFile(logname)
			t.Logf("worker log:\n%s", data)
		}
	}()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	select {
	case worker := <-registrations:
		if worker.Name != "kong-mvp" || len(worker.Tags) != 1 || worker.Tags[0] != "from-file" {
			t.Fatalf("unexpected registration payload: %#v", worker)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("worker did not register with the test TSA")
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d", healthPort))
		if err == nil {
			var health struct {
				Garden       struct{ Healthy bool }
				Baggageclaim struct{ Healthy bool }
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&health)
			response.Body.Close()
			if decodeErr == nil && response.StatusCode == http.StatusOK && health.Garden.Healthy && health.Baggageclaim.Healthy {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not become healthy")
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, endpoint := range []string{
		fmt.Sprintf("http://127.0.0.1:%d/ping", gardenPort),
		fmt.Sprintf("http://127.0.0.1:%d/volumes", baggageclaimPort),
	} {
		response, err := client.Get(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s: %s", endpoint, response.Status)
		}
	}
	for _, name := range []string{"containers", "volumes"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || !info.IsDir() {
			t.Fatalf("worker did not use the configured work-dir for %s: %v", name, err)
		}
	}
}

// Minimal SSH fixture for the existing TSA client protocol. The worker's real
// code authenticates, requests reverse tunnels, sends its registration JSON,
// receives the registered event and sends a shutdown signal. No ATC/database
// behavior is simulated by this fixture.
func kongTestTSA(t *testing.T, workerKey string) (string, string, <-chan atc.Worker) {
	t.Helper()
	data, err := os.ReadFile(workerKey)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(key.Marshal(), signer.PublicKey().Marshal()) {
			return nil, fmt.Errorf("unexpected worker key")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	publicKey := filepath.Join(t.TempDir(), "tsa-key.pub")
	if err := os.WriteFile(publicKey, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	registrations := make(chan atc.Worker, 1)
	ctx, cancel := context.WithCancel(context.Background())
	var processes sync.WaitGroup
	processes.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			processes.Go(func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { conn.Close() })
				defer stop()
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer server.Close()
				processes.Go(func() {
					for request := range requests {
						request.Reply(true, nil)
					}
				})
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						incoming.Reject(ssh.UnknownChannelType, "sessions only")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						continue
					}
					processes.Go(func() {
						defer channel.Close()
						for request := range requests {
							request.Reply(true, nil)
							if request.Type == "exec" {
								var worker atc.Worker
								if json.NewDecoder(channel).Decode(&worker) != nil {
									return
								}
								select {
								case registrations <- worker:
								case <-ctx.Done():
									return
								}
								io.WriteString(channel, "{\"event\":\"registered\"}\n")
							}
							if request.Type == "signal" {
								channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
								return
							}
						}
					})
				}
			})
		}
	})
	t.Cleanup(func() {
		listener.Close()
		cancel()
		processes.Wait()
	})
	return listener.Addr().String(), publicKey, registrations
}
