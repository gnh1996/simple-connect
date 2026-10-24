package testutil

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"runtime"

	pkgsftp "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTPEnv 测试环境：地址、本地根目录与远程根路径。
// Root 是本地文件系统路径（预置/校验文件用）；RemoteRoot 是 SFTP 客户端可见的
// POSIX 远程路径（Windows 下为 /C:/...，Linux 下与 Root 相同）。
type SFTPEnv struct {
	Addr       string
	Root       string
	RemoteRoot string
}

// TB 测试/基准共用最小接口：*testing.T 与 *testing.B 均满足，
// 使内存服务器既可用于 go test 也可用于 go test -bench。
type TB interface {
	Helper()
	TempDir() string
	Cleanup(func())
	Fatal(...any)
}

// StartSFTP 启动内存测试 SSH/SFTP 服务器，远程文件系统限定在临时目录
func StartSFTP(tb TB) SFTPEnv {
	tb.Helper()
	root := tb.TempDir()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		tb.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "tester" && string(pass) == "secret" {
				return nil, nil
			}
			return nil, errors.New("认证失败")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveConn(conn, cfg, root)
		}
	}()
	return SFTPEnv{Addr: ln.Addr().String(), Root: root, RemoteRoot: remoteRoot(root)}
}

// remoteRoot 返回测试服务器根目录对应的 POSIX 远程路径。
// pkg/sftp 的服务器要求远程路径使用 POSIX 形式；Windows 下把 C:\x 表示为
// /C:/x 才能被正确映射回本地路径（直接传 C:\x 会被当成相对路径再拼工作目录）。
func remoteRoot(root string) string {
	if runtime.GOOS == "windows" {
		return "/" + filepath.ToSlash(root)
	}
	return root
}

// Remote 把本地绝对路径转换为该测试服务器可见的远程 POSIX 路径。
// 仅用于 SFTP 客户端调用；本地文件操作仍应使用 Root 下的 filepath 路径。
func (e SFTPEnv) Remote(localPath string) string {
	if runtime.GOOS != "windows" {
		return localPath
	}
	if !filepath.IsAbs(localPath) {
		return filepath.ToSlash(localPath)
	}
	return "/" + filepath.ToSlash(localPath)
}

func serveConn(conn net.Conn, cfg *ssh.ServerConfig, root string) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "不支持的通道")
			continue
		}
		ch, requests, err := newChan.Accept()
		if err != nil {
			continue
		}
		go func(ch ssh.Channel, requests <-chan *ssh.Request) {
			defer ch.Close()
			for req := range requests {
				if req.Type == "subsystem" && len(req.Payload) >= 4 &&
					string(req.Payload[4:]) == "sftp" {
					if req.WantReply {
						_ = req.Reply(true, nil)
					}
					server, err := pkgsftp.NewServer(ch,
						pkgsftp.WithServerWorkingDirectory(root))
					if err != nil {
						return
					}
					_ = server.Serve()
					return
				}
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
			}
		}(ch, requests)
	}
}

// SplitHostPort 从测试地址拆分主机与端口
func SplitHostPort(addr string) (host string, port int) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0
	}
	var n int
	fmt.Sscanf(p, "%d", &n)
	return h, n
}
