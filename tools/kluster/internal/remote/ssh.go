// Package remote provides an SSH client used to run commands and write
// files on cluster nodes. Harvested from feat/kluster-cli (PR #10) with its
// tests; the change is that every dial verifies the host key.
package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// syncBuffer is a mutex-guarded bytes.Buffer. golang.org/x/crypto/ssh
// copies a session's stdout and stderr on two separate internal goroutines;
// pointing both at a single plain bytes.Buffer is a data race even for a
// command that never gets canceled, since those two goroutines write to it
// concurrently for the command's entire lifetime.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// Client wraps a single SSH connection to a host.
type Client struct {
	conn *ssh.Client
	host string
	sudo bool
}

// Config is how to reach a host. HostKeyCallback is required: there is no
// way to dial with host key checking switched off (ADR 0009).
type Config struct {
	User            string
	Auth            ssh.AuthMethod
	HostKeyCallback ssh.HostKeyCallback
	// Sudo runs every command through `sudo -n`, for a login that is not
	// root. -n fails instead of asking for a password nobody can type.
	Sudo bool
}

// Dial connects to addr (host or host:port) and verifies its host key.
func Dial(ctx context.Context, addr string, cfg Config) (*Client, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	return DialVia(ctx, d.DialContext, addr, cfg)
}

// DialFunc opens a network connection, as net.Dialer.DialContext does.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// DialVia connects to addr through dial and verifies its host key. kluster
// uses it to reach a host through its own WireGuard tunnel.
func DialVia(ctx context.Context, dial DialFunc, addr string, cfg Config) (*Client, error) {
	if cfg.HostKeyCallback == nil {
		return nil, errors.New("dialing without a host key callback")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "22")
	}
	host, _, _ := net.SplitHostPort(addr)

	nc, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(deadline)
	}
	conn, chans, reqs, err := ssh.NewClientConn(nc, addr, &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{cfg.Auth},
		HostKeyCallback: cfg.HostKeyCallback,
		// kluster pins only ed25519 keys. The library asks for ECDSA and
		// RSA first, so a host that also has those keys, such as the
		// hand-built live Control Plane, would show one kluster never pinned.
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           10 * time.Second,
	})
	if err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("ssh handshake with %s: %w", addr, err)
	}
	_ = nc.SetDeadline(time.Time{})
	return &Client{conn: ssh.NewClient(conn, chans, reqs), host: host, sudo: cfg.Sudo}, nil
}

// DialWait dials until the host answers or ctx ends, for a server that is
// still booting. A host key mismatch is never retried: it means the machine
// answering is not the one kluster created.
func DialWait(ctx context.Context, addr string, cfg Config, every time.Duration) (*Client, error) {
	for {
		c, err := Dial(ctx, addr, cfg)
		if err == nil {
			return c, nil
		}
		if IsHostKeyError(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%s did not answer: %w (last error: %v)", addr, ctx.Err(), err)
		case <-time.After(every):
		}
	}
}

// IsHostKeyError reports whether err is a refused host key.
func IsHostKeyError(err error) bool {
	var ke *knownhosts.KeyError
	var mm *HostKeyMismatch
	return errors.As(err, &ke) || errors.As(err, &mm)
}

// HostKeyMismatch is returned by FixedHostKey for any other key.
type HostKeyMismatch struct{ Host string }

func (e *HostKeyMismatch) Error() string { return "host key mismatch for " + e.Host }

// FixedHostKey accepts exactly key, and reports any other as a
// HostKeyMismatch so that DialWait does not retry it.
func FixedHostKey(key ssh.PublicKey) ssh.HostKeyCallback {
	return func(hostname string, _ net.Addr, k ssh.PublicKey) error {
		if !bytes.Equal(k.Marshal(), key.Marshal()) {
			return &HostKeyMismatch{Host: hostname}
		}
		return nil
	}
}

// Auth is the identity file if one is given, otherwise the ssh-agent.
func Auth(keyPath string) (ssh.AuthMethod, error) {
	if keyPath == "" {
		sock := os.Getenv("SSH_AUTH_SOCK")
		if sock == "" {
			return nil, fmt.Errorf("no key path given and SSH_AUTH_SOCK is not set")
		}
		conn, err := net.Dial("unix", sock)
		if err != nil {
			return nil, fmt.Errorf("connecting to ssh-agent at %s: %w", sock, err)
		}
		return ssh.PublicKeysCallback(agent.NewClient(conn).Signers), nil
	}

	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("reading key %s: %w", keyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("parsing key %s: %w", keyPath, err)
	}
	return ssh.PublicKeys(signer), nil
}

// Run executes cmd on the remote host and returns combined stdout+stderr,
// trimmed of a trailing newline. It returns an error if the remote command
// exits non-zero.
func (c *Client) Run(ctx context.Context, cmd string) (string, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("opening session to %s: %w", c.host, err)
	}
	defer session.Close()

	done := make(chan struct{})
	var out syncBuffer
	var runErr error
	session.Stdout = &out
	session.Stderr = &out

	go func() {
		runErr = session.Run(c.wrap(cmd))
		close(done)
	}()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		// Wait for the goroutine (and the session's own internal
		// stdout/stderr copy goroutines, which it joins before returning)
		// to fully stop before reading final output.
		<-done
		return out.String(), ctx.Err()
	case <-done:
	}

	output := trimTrailingNewline(out.String())
	if runErr != nil {
		return output, fmt.Errorf("running %q on %s: %w (output: %s)", cmd, c.host, runErr, output)
	}
	return output, nil
}

// WriteFile writes content to path on the remote host with the given mode,
// via an SFTP-free `cat > path` pipe over its own session — this fixes the
// bash pipeline's habit of using a bare shell redirect on a wrapper
// function's own stdout, which bypassed dry-run and error handling.
func (c *Client) WriteFile(ctx context.Context, path, content string, mode os.FileMode) error {
	session, err := c.conn.NewSession()
	if err != nil {
		return fmt.Errorf("opening session to %s: %w", c.host, err)
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("opening stdin pipe to %s: %w", c.host, err)
	}

	// umask first, so the file is never readable by others, not even
	// between the write and the chmod.
	cmd := fmt.Sprintf("umask 077 && cat > %s && chmod %s %s", shellQuote(path), strconv.FormatInt(int64(mode.Perm()), 8), shellQuote(path))

	done := make(chan error, 1)
	go func() {
		done <- session.Run(c.wrap(cmd))
	}()

	if _, err := stdin.Write([]byte(content)); err != nil {
		return fmt.Errorf("writing content to %s on %s: %w", path, c.host, err)
	}
	if err := stdin.Close(); err != nil {
		return fmt.Errorf("closing stdin to %s: %w", c.host, err)
	}

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("writing %s on %s: %w", path, c.host, err)
		}
	}
	return nil
}

// ReadFile reads the content of path from the remote host.
func (c *Client) ReadFile(ctx context.Context, path string) (string, error) {
	return c.Run(ctx, fmt.Sprintf("cat %s", shellQuote(path)))
}

// Close closes the underlying SSH connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// wrap runs cmd as root through sudo when the login is not root. The
// command keeps its own shell, so pipes and && stay inside sudo.
func (c *Client) wrap(cmd string) string {
	if !c.sudo {
		return cmd
	}
	return "sudo -n sh -c " + shellQuote(cmd)
}

func shellQuote(s string) string {
	return "'" + string(bytes.ReplaceAll([]byte(s), []byte("'"), []byte(`'\''`))) + "'"
}

func trimTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
