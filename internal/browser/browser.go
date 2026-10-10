// Package browser opens http(s) URLs in the user's default browser on macOS
// and Linux.
package browser

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"runtime"
	"time"
)

// openTimeout bounds the opener run: `open` returns at once, but a misbehaving
// xdg-open handler must not hold a goroutine forever (the clipboard rule).
const openTimeout = 5 * time.Second

// Open launches link in the default browser. Only absolute http and https
// URLs are accepted: links come from arbitrary command output, which must
// never be able to trigger file: or custom-scheme handlers.
func Open(link string) error {
	parsed, err := url.Parse(link)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("not an http(s) link: " + link)
	}
	name, args, ok := command(runtime.GOOS, link)
	if !ok {
		return errors.New("opening links is not supported on " + runtime.GOOS)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return errors.New(name + " not found")
	}
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, args...)
	command.WaitDelay = time.Second
	return command.Run()
}

// command picks the platform's opener; the URL is a single argv entry, never
// passed through a shell.
func command(goos, link string) (name string, args []string, ok bool) {
	switch goos {
	case "darwin":
		return "open", []string{link}, true
	case "linux":
		return "xdg-open", []string{link}, true
	}
	return "", nil, false
}
