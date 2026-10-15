// Local-only SSH and echo fixture for the resource measurements in measure.py.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	dir := flag.String("dir", "", "temporary keys and configuration directory")
	flag.Parse()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	must(err)
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	must(err)
	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	must(err)
	clientSigner, err := ssh.NewSignerFromKey(clientKey)
	must(err)
	block, err := ssh.MarshalPrivateKey(clientKey, "")
	must(err)
	identity := filepath.Join(*dir, "identity")
	must(os.WriteFile(identity, pem.EncodeToMemory(block), 0600))
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	defer listener.Close()
	known := filepath.Join(*dir, "known_hosts")
	must(os.WriteFile(known, []byte(knownhosts.Line([]string{listener.Addr().String()}, hostSigner.PublicKey())+"\n"), 0600))
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	configPath := filepath.Join(*dir, "ssh_config")
	config := fmt.Sprintf("Host pf-bench\n HostName 127.0.0.1\n Port %s\n User fixture\n IdentityFile %q\n IdentitiesOnly yes\n UserKnownHostsFile %q\n GlobalKnownHostsFile /dev/null\n Compression no\n", port, identity, known)
	must(os.WriteFile(configPath, []byte(config), 0600))
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(key.Marshal(), clientSigner.PublicKey().Marshal()) {
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected key")
	}}
	serverConfig.AddHostKey(hostSigner)
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go serve(raw, serverConfig)
		}
	}()
	must(json.NewEncoder(os.Stdout).Encode(map[string]any{"ssh_config": configPath, "echo_port": echo.Addr().(*net.TCPAddr).Port}))
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
}
func serve(raw net.Conn, config *ssh.ServerConfig) {
	conn, channels, requests, err := ssh.NewServerConn(raw, config)
	if err != nil {
		raw.Close()
		return
	}
	defer conn.Close()
	go func() {
		for req := range requests {
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		}
	}()
	for request := range channels {
		if request.ChannelType() != "direct-tcpip" {
			_ = request.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		var target struct {
			Host       string
			Port       uint32
			OriginHost string
			OriginPort uint32
		}
		if ssh.Unmarshal(request.ExtraData(), &target) != nil {
			_ = request.Reject(ssh.ConnectionFailed, "bad target")
			continue
		}
		remote, err := net.Dial("tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
		if err != nil {
			_ = request.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		channel, requests, err := request.Accept()
		if err != nil {
			remote.Close()
			continue
		}
		go ssh.DiscardRequests(requests)
		go func() {
			defer remote.Close()
			defer channel.Close()
			done := make(chan struct{})
			go func() {
				_, _ = io.Copy(channel, remote)
				_ = channel.CloseWrite()
				close(done)
			}()
			_, _ = io.Copy(remote, channel)
			_ = remote.(*net.TCPConn).CloseWrite()
			<-done
		}()
	}
}
