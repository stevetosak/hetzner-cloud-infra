package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func execCommand(cmd string) *exec.Cmd {
	return exec.Command("/bin/sh", "-c", cmd)
}

// testServer starts a minimal in-process SSH server that accepts any
// password auth and, for each session, execs the requested command using
// the local shell. It returns the listener address and the host public key.
func testServer(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("creating signer: %v", err)
	}

	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			return nil, nil // accept anything
		},
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			nConn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleConn(t, nConn, config)
		}
	}()

	return listener.Addr().String(), signer.PublicKey()
}

func handleConn(t *testing.T, nConn net.Conn, config *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(nConn, config)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go handleSession(channel, requests)
	}
}

func handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for req := range requests {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		// exec payload is a length-prefixed string; skip the 4-byte length.
		cmd := string(req.Payload[4:])
		if req.WantReply {
			_ = req.Reply(true, nil)
		}

		exitCode := runShellCommand(cmd, channel, channel, channel)
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(exitCode)}))
		return
	}
}

// runShellCommand emulates just enough shell behavior for the two commands
// remote.Client issues: "cat > path && chmod N path" and generic commands.
// It shells out to /bin/sh so real semantics (redirects, &&) apply.
func runShellCommand(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := execCommand(cmd)
	c.Stdin = stdin
	c.Stdout = stdout
	c.Stderr = stderr
	if err := c.Run(); err != nil {
		if exitErr, ok := err.(interface{ ExitCode() int }); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

func TestClient_Run(t *testing.T) {
	addr, key := testServer(t)
	client := dialForTest(t, addr, key)
	defer client.Close()

	out, err := client.Run(context.Background(), "echo hello")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if out != "hello" {
		t.Errorf("Run() output = %q, want %q", out, "hello")
	}
}

func TestClient_Run_NonZeroExit(t *testing.T) {
	addr, key := testServer(t)
	client := dialForTest(t, addr, key)
	defer client.Close()

	_, err := client.Run(context.Background(), "exit 3")
	if err == nil {
		t.Fatal("Run() expected error for non-zero exit, got nil")
	}
}

func TestClient_WriteFile(t *testing.T) {
	addr, key := testServer(t)
	client := dialForTest(t, addr, key)
	defer client.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "test-write.txt")

	err := client.WriteFile(context.Background(), path, "hello world\n", 0o600)
	if err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(data) != "hello world\n" {
		t.Errorf("file content = %q, want %q", string(data), "hello world\n")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestClient_ReadFile(t *testing.T) {
	addr, key := testServer(t)
	client := dialForTest(t, addr, key)
	defer client.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "test-read.txt")
	if err := os.WriteFile(path, []byte("some content"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	out, err := client.ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if out != "some content" {
		t.Errorf("ReadFile() = %q, want %q", out, "some content")
	}
}

func dialForTest(t *testing.T, addr string, key ssh.PublicKey) *Client {
	t.Helper()
	c, err := Dial(context.Background(), addr, Config{
		User:            "test",
		Auth:            ssh.Password("anything"),
		HostKeyCallback: FixedHostKey(key),
	})
	if err != nil {
		t.Fatalf("dialing test server: %v", err)
	}
	return c
}

// A host answering with any key but the pinned one is refused at once, and
// DialWait does not retry it as if the server were still booting.
func TestDialRefusesAnotherHostKey(t *testing.T) {
	addr, _ := testServer(t)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := DialWait(ctx, addr, Config{
		User:            "test",
		Auth:            ssh.Password("anything"),
		HostKeyCallback: FixedHostKey(otherSigner.PublicKey()),
	}, time.Second)
	var mm *HostKeyMismatch
	if !errors.As(err, &mm) {
		t.Fatalf("got %v, want a host key mismatch", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("a host key mismatch was retried")
	}
}

func TestDialRequiresAHostKeyCallback(t *testing.T) {
	if _, err := Dial(context.Background(), "127.0.0.1:1", Config{User: "x", Auth: ssh.Password("x")}); err == nil {
		t.Fatal("dialed without host key checking")
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/etc/wireguard/wg0.conf": `'/etc/wireguard/wg0.conf'`,
		"it's":                    `'it'\''s'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTrimTrailingNewline(t *testing.T) {
	if got := trimTrailingNewline("hello\n"); got != "hello" {
		t.Errorf("trimTrailingNewline(%q) = %q, want %q", "hello\n", got, "hello")
	}
	if got := trimTrailingNewline("hello\r\n"); got != "hello" {
		t.Errorf("trimTrailingNewline with CRLF = %q, want %q", got, "hello")
	}
	if got := trimTrailingNewline("no-newline"); got != "no-newline" {
		t.Errorf("trimTrailingNewline no-op = %q, want %q", got, "no-newline")
	}
}
