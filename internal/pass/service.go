package pass

import (
	"runtime"

	"github.com/mvanhorn/agent-tincan/internal/history"
)

// ServiceLabel is the launchd label of the pass service.
const ServiceLabel = "com.agenttincan.pass"

// systemdUnit is the systemd user unit name of the pass service.
const systemdUnit = "tincan-pass.service"

// launchdTemplate is the pass service's launchd agent. Placeholders:
// __TINCAN_BINARY__, __HOME__, __PATH__, each XML-escaped when filled.
const launchdTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!--
  Agent Tincan pass: runs "tincan pass serve" under launchd.
  "tincan pass install" writes this file into ~/Library/LaunchAgents with
  the real paths filled in. It does not load it; start it with:
    launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.agenttincan.pass.plist
  See docs/adapters/pass.md.
-->
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.agenttincan.pass</string>
  <key>ProgramArguments</key>
  <array>
    <string>__TINCAN_BINARY__</string>
    <string>pass</string>
    <string>serve</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>TINCAN_CONFIG</key>
    <string>__HOME__/.config/tincan/pass.json</string>
    <key>PATH</key>
    <string>__PATH__</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>10</integer>
  <key>ProcessType</key>
  <string>Background</string>
  <key>StandardOutPath</key>
  <string>__HOME__/Library/Logs/tincan-pass.log</string>
  <key>StandardErrorPath</key>
  <string>__HOME__/Library/Logs/tincan-pass.log</string>
</dict>
</plist>
`

// systemdTemplate is the Linux user unit. %h is systemd's home specifier.
const systemdTemplate = `[Unit]
Description=Agent Tincan pass
After=network-online.target

[Service]
ExecStart="__TINCAN_BINARY__" pass serve
Environment=TINCAN_CONFIG=%h/.config/tincan/pass.json
Environment="PATH=__PATH__"
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`

// InstallService writes the pass service definition, which runs
// tincan pass serve as the agent in ~/.config/tincan/pass.json. It
// never loads or starts it.
func InstallService(o history.ServiceOptions) (history.ServiceResult, error) {
	goos := o.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	return history.InstallServiceDef(o, history.ServiceDef{
		Label:       ServiceLabel,
		Unit:        systemdUnit,
		Launchd:     launchdTemplate,
		Systemd:     systemdTemplate,
		Unsupported: "no pass service definition for " + goos + "; run tincan pass serve under your own service manager",
	})
}
