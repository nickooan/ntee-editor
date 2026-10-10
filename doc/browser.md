# internal/browser

**Introduction**

Opens a URL in the user's default browser. The Ctrl+R op overlay uses it when a link in a command's streamed output is clicked. Links come from arbitrary command output, so the package is deliberately narrow: it only ever opens absolute `http://` or `https://` URLs.

**Architecture**

One file, no state. It shells out to the platform's opener: `open` on macOS, `xdg-open` on Linux. The URL is passed as a single argv entry and never goes through a shell. Every run has a hard deadline, the same rule `clipboard` and `gitcmd` follow, so a misbehaving handler can't hold a goroutine forever. The app calls it from a `tea.Cmd` and never from `Update`.

**Functions**

### browser.go

- `Open` — checks the link with `net/url`. The scheme must be `http` or `https` and there must be a host; `file:`, `javascript:`, custom schemes and bare hosts are refused. It then runs the platform opener with a 5-second timeout.

*Plus: `command` — picks the opener for a GOOS (split out so the choice is unit-testable).*
