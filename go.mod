module github.com/draftplane/draftplane

go 1.25.0

// The LOWEST toolchain this module may be built with, and a security floor
// rather than a language-feature one -- the `go` line above still decides the
// language version. go1.26.5 carries eight reachable Go standard-library
// advisories (GO-2026-6218, -6091, -6090, -6089, -6088, -5972, -5942, -5026),
// every one of them fixed in go1.26.6; zero of the 27 modules linked into a
// shipped binary carries an advisory at any stratum. Nothing pinned this
// before, and scripts/build-release.sh runs a bare `go build` against PATH,
// so a release would otherwise be built with whatever toolchain happens to be
// installed.
//
// IT IS HERE RATHER THAN ONLY IN THE RELEASE SCRIPT BECAUSE EVERY DOOR HAS TO
// INHERIT IT: `go build`, `go test`, `make check`, golangci-lint and the
// release script all read this file, and a floor stated only in the script
// would leave `make check` green on a toolchain the release refuses. The
// script states it too, but derives it from THIS line rather than repeating
// it, and checks the toolchain recorded in the artifact it just produced --
// which is the shipping question a source scan cannot answer.
// version/version_test.go pins that this line and its own floor agree.
toolchain go1.26.6

require (
	charm.land/bubbles/v2 v2.1.1
	charm.land/bubbletea/v2 v2.0.8
	charm.land/lipgloss/v2 v2.0.5
	github.com/charmbracelet/x/ansi v0.11.7
	github.com/charmbracelet/x/exp/teatest/v2 v2.0.0-20260713092006-0d683c34c74b
	github.com/clipperhouse/displaywidth v0.11.0
	github.com/fsnotify/fsnotify v1.10.1
	github.com/modelcontextprotocol/go-sdk v1.6.1
	github.com/yuin/goldmark v1.8.4
)

require (
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aymanbagabas/go-udiff v0.4.1 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260703014108-f5a850f9c2b7 // indirect
	github.com/charmbracelet/x/exp/golden v0.0.0-20251109135125-8916d276318f // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.0 // indirect
	github.com/mattn/go-runewidth v0.0.24 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
)
