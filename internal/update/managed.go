package update

import (
	"path/filepath"
	"strings"
)

// Managed reports whether the binary at path was installed by a package
// manager, and the command that updates it there. Such installs are
// never replaced by `shiplino update`.
func Managed(path string) (manager, command string) {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		path = p
	}
	p := strings.ToLower(filepath.ToSlash(path))
	switch {
	case strings.Contains(p, "/cellar/") || strings.HasPrefix(p, "/opt/homebrew/") || strings.Contains(p, "/linuxbrew/"):
		return "Homebrew", "brew upgrade shiplino"
	case strings.Contains(p, "/scoop/apps/") || strings.Contains(p, "/scoop/shims/"):
		return "Scoop", "scoop update shiplino"
	case strings.Contains(p, "/winget/packages/") || strings.Contains(p, "/microsoft/winget/"):
		return "winget", "winget upgrade shiplino"
	case strings.HasPrefix(p, "/nix/store/"):
		return "Nix", "update it through Nix"
	case strings.HasPrefix(p, "/snap/"):
		return "Snap", "snap refresh shiplino"
	}
	return "", ""
}
