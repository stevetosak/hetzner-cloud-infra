// Package remote provides an SSH client used to run commands and write
// files on cluster nodes.
package remote

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
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
}

// Dial connects to host:22 as user, authenticating via the given private
// key file (PEM, unencrypted or agent-backed) or, if keyPath is empty,
// via the running SSH agent (SSH_AUTH_SOCK).
func Dial(host, user, keyPath string) (*Client, error) {
	auth, err := authMethod(keyPath)
	if err != nil {
		return nil, fmt.Errorf("configuring auth for %s: %w", host, err)
	}

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // cluster nodes are ephemeral and re-provisioned; known_hosts churns every apply
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(host, "22")
	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", addr, err)
	}

	return &Client{conn: conn, host: host}, nil
}

func authMethod(keyPath string) (ssh.AuthMethod, error) {
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
		runErr = session.Run(cmd)
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

	cmd := fmt.Sprintf("cat > %s && chmod %s %s", shellQuote(path), strconv.FormatInt(int64(mode.Perm()), 8), shellQuote(path))

	done := make(chan error, 1)
	go func() {
		done <- session.Run(cmd)
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

func shellQuote(s string) string {
	return "'" + string(bytes.ReplaceAll([]byte(s), []byte("'"), []byte(`'\''`))) + "'"
}

func trimTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
