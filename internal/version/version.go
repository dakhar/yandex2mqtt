// Package version reports the running build's version. It prefers a value
// injected at build time via -ldflags "-X .../internal/version.value=..."; if
// absent (a plain `go build`), it falls back to the VCS revision the toolchain
// embeds, and finally to "dev".
package version

import (
	"runtime/debug"
	"sync"
)

// value is set at build time via -ldflags. Leave empty for the fallbacks.
var value string

var (
	once   sync.Once
	cached string
)

// String returns the build version: the -ldflags value, else "<short-commit>[-dirty]"
// from the embedded VCS info, else "dev".
func String() string {
	once.Do(func() { cached = resolve() })
	return cached
}

func resolve() string {
	if value != "" {
		return value
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}
