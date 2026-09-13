//go:build linux

package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// dataHome is $XDG_DATA_HOME, or ~/.local/share when unset, per the XDG base
// directory spec. The fallback icon and desktop entry are written under it.
func dataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

// dataDirs lists every directory a desktop searches for applications and
// icons: dataHome first, then $XDG_DATA_DIRS (default
// /usr/local/share:/usr/share). A distro package, a Flatpak export, and the
// install script each put their files in one of these.
func dataDirs() []string {
	var dirs []string
	if h := dataHome(); h != "" {
		dirs = append(dirs, h)
	}
	sys := os.Getenv("XDG_DATA_DIRS")
	if sys == "" {
		sys = "/usr/local/share:/usr/share"
	}
	for d := range strings.SplitSeq(sys, ":") {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// installedIcon returns the path of an AppID icon already present in the
// hicolor theme of any data dir, or "" when there is none.
func installedIcon() string {
	for _, d := range dataDirs() {
		matches, _ := filepath.Glob(filepath.Join(d, "icons", "hicolor", "*", "apps", AppID+".*"))
		if len(matches) > 0 {
			return matches[0]
		}
	}
	return ""
}

// installedDesktopEntry reports whether an AppID.desktop already exists in the
// applications dir of any data dir.
func installedDesktopEntry() bool {
	for _, d := range dataDirs() {
		if _, err := os.Stat(filepath.Join(d, "applications", AppID+".desktop")); err == nil {
			return true
		}
	}
	return false
}

// removeLegacy deletes a file that versions before the AppID rename wrote
// under ~/.local/share on every launch. Nothing resolves those names now.
func removeLegacy(rel ...string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	_ = os.Remove(filepath.Join(append([]string{home, ".local", "share"}, rel...)...))
}

// InstallIcon makes sure an AppID icon is available to the desktop and returns
// its absolute path (used for notifications), or "" if none could be found or
// written. An icon a package, a Flatpak, or the install script already put in
// a data dir wins. The embedded SVG is the fallback for installs that brought
// no icon of their own, such as `make install` or a plain `go build`. All
// operations are best-effort; errors are silently ignored.
func InstallIcon() string {
	removeLegacy("icons", "hicolor", "scalable", "apps", "vibez.svg")
	if p := installedIcon(); p != "" {
		return p
	}
	base := dataHome()
	if base == "" {
		return ""
	}
	dir := filepath.Join(base, "icons", "hicolor", "scalable", "apps")
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // XDG icon dir
		return ""
	}
	dst := filepath.Join(dir, AppID+".svg")
	if err := os.WriteFile(dst, Icon, 0o644); err != nil { //nolint:gosec // public icon file
		return ""
	}
	hicolor := filepath.Join(base, "icons", "hicolor")
	_ = exec.Command("gtk-update-icon-cache", "--force", "--ignore-theme-index", hicolor).Run() //nolint:gosec
	return dst
}

// InstallDesktopEntry makes sure an AppID.desktop exists somewhere the desktop
// looks, so MPRIS consumers can resolve the DesktopEntry property to an icon.
// One a package, a Flatpak, or the install script already provides wins,
// whatever it contains. The embedded fallback (NoDisplay=true, so invisible to
// app launchers) is written under dataHome only when nothing else is there,
// and once written is itself found on the next launch and left alone. All
// operations are best-effort; errors are silently ignored.
func InstallDesktopEntry() {
	removeLegacy("applications", "vibez.desktop")
	if installedDesktopEntry() {
		return
	}
	base := dataHome()
	if base == "" {
		return
	}
	dir := filepath.Join(base, "applications")
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // XDG applications dir
		return
	}
	if err := os.WriteFile(filepath.Join(dir, AppID+".desktop"), DesktopEntry, 0o644); err != nil { //nolint:gosec // public .desktop file
		return
	}
	_ = exec.Command("update-desktop-database", dir).Run() //nolint:gosec
}
