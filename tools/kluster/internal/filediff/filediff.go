// Package filediff shows an edit to an existing file as a unified diff, the
// way Plan Mode previews every change kluster makes to a host or the laptop
// (ADR 0009). Lines that hold a secret are redacted before diffing.
package filediff

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// secretLine matches a line whose value is a secret: a WireGuard private or
// preshared key, a kubeconfig's embedded key or token, a bearer token.
var secretLine = regexp.MustCompile(`^(\s*(?:PrivateKey|PresharedKey)\s*=\s*|\s*(?:client-key-data|token|password)\s*:\s*)(\S.*)$`)

// Redact replaces the value of every secret line with a short digest. Two
// different secrets still show as a change; neither is shown.
func Redact(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if m := secretLine.FindStringSubmatch(l); m != nil {
			lines[i] = m[1] + digest(m[2])
		}
	}
	return strings.Join(lines, "\n")
}

func digest(v string) string {
	sum := sha256.Sum256([]byte(v))
	return fmt.Sprintf("<redacted sha256:%x>", sum[:4])
}

// Unified returns the redacted unified diff of path from old to new, or ""
// when they are equal.
func Unified(path, old, new string) string {
	if old == new {
		return ""
	}
	return udiff.Unified(path+" (now)", path+" (after)", Redact(old), Redact(new))
}
