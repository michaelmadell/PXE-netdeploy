package ipxe

import (
	"fmt"
	"strings"
)

// Linux clients (ADR 0034): the gate in autoexec.ipxe, the profile menu the
// unlock endpoint answers with, the boot commands the start endpoint answers
// with, and a failure script. The agent endpoints are named by absolute path
// so they come over the scheme, host and port the script came from.

const (
	unlockPath = "/api/v1/agent/ipxe/unlock"
	startPath  = "/api/v1/agent/ipxe/start"
)

// GateOptions shape a Linux client's autoexec.ipxe.
type GateOptions struct {
	Arch string
	// Armed: a pending deployment runs after ArmedCountdown seconds unless
	// a key is pressed.
	Armed          bool
	ArmedCountdown int
	// PromptCountdown is how long a key may be pressed to open the
	// password prompt before the script returns to the firmware.
	PromptCountdown int
	// NoPassword: debug mode (bootPrompt.requirePassword false), a key goes
	// straight to the menu.
	NoPassword bool
	// PasswordSet: a boot prompt password is configured. Without one (and
	// not in debug mode) only armed deployments run.
	PasswordSet bool
}

// LinuxGate is autoexec.ipxe for a client whose profile is Linux.
func LinuxGate(o GateOptions) string {
	var b strings.Builder
	b.WriteString("#!ipxe\n# netdeploy (ADR 0034): generated for this client.\n")
	b.WriteString("echo netdeploy: iPXE ${version} from ${cwuri}\n")
	if o.NoPassword {
		b.WriteString("echo netdeploy: DEBUG MODE: the boot prompt password is turned off on this server.\n")
	}
	if o.Armed {
		fmt.Fprintf(&b, "echo netdeploy: a deployment is armed for this machine.\n")
		fmt.Fprintf(&b, "prompt --timeout %d Press any key within %d s for the menu, or wait to deploy... && goto menu ||\n", max(o.ArmedCountdown, 1)*1000, max(o.ArmedCountdown, 1))
		b.WriteString(paramsBlock(o.Arch, "param armed true"))
		fmt.Fprintf(&b, "chain --autofree %s##params || goto failed\n", startPath)
	} else {
		fmt.Fprintf(&b, "prompt --timeout %d Press any key within %d s to deploy this machine... || goto disk\n", max(o.PromptCountdown, 1)*1000, max(o.PromptCountdown, 1))
	}
	b.WriteString(":menu\n")
	switch {
	case o.NoPassword:
		b.WriteString(paramsBlock(o.Arch, ""))
		fmt.Fprintf(&b, "chain --autofree %s##params || goto failed\n", unlockPath)
	case !o.PasswordSet:
		b.WriteString("echo netdeploy: no boot prompt password is set on this server: only deployments armed there can run.\n")
		b.WriteString("sleep 10\ngoto disk\n")
	default:
		b.WriteString("echo netdeploy: enter the deployment password (the username is not used).\n")
		b.WriteString("login || goto disk\n")
		b.WriteString(paramsBlock(o.Arch, "param password ${password}"))
		b.WriteString("clear password\n")
		fmt.Fprintf(&b, "chain --autofree %s##params || goto failed\n", unlockPath)
	}
	b.WriteString(diskAndFailed)
	return b.String()
}

// paramsBlock starts a form with the client's MAC and architecture.
func paramsBlock(arch, extra string) string {
	s := "params\nparam mac ${netX/mac}\nparam arch " + arch + "\n"
	if extra != "" {
		s += extra + "\n"
	}
	return s
}

const diskAndFailed = `goto disk
:failed
echo netdeploy: that failed (${errno}); see the netdeployd log.
sleep 15
:disk
echo netdeploy: booting from the next device.
exit 1
`

// MenuItem is one Linux profile in the menu.
type MenuItem struct {
	ID, Name, Description string
}

// LinuxMenu is the unlock endpoint's answer: the Linux profiles, def
// preselected; choosing one posts to the start endpoint with token.
func LinuxMenu(arch string, items []MenuItem, def, token string) string {
	var b strings.Builder
	b.WriteString("#!ipxe\n# netdeploy (ADR 0034): the profile menu.\n")
	b.WriteString("menu netdeploy: deploy this machine with\n")
	for _, it := range items {
		label := Text(it.Name)
		if it.Description != "" {
			label += " - " + Text(it.Description)
		}
		fmt.Fprintf(&b, "item %s %s\n", menuID(it.ID), label)
	}
	b.WriteString("item --gap --\nitem disk Boot from the next device instead\n")
	d := "disk"
	for _, it := range items {
		if it.ID == def {
			d = menuID(def)
		}
	}
	fmt.Fprintf(&b, "choose --default %s target || goto disk\n", d)
	b.WriteString("iseq ${target} disk && goto disk ||\n")
	b.WriteString(paramsBlock(arch, "param unlockToken "+menuID(token)+"\nparam profileId ${target}"))
	fmt.Fprintf(&b, "chain --autofree %s##params || goto failed\n", startPath)
	b.WriteString(diskAndFailed)
	return b.String()
}

// Boot describes the commands that start a Linux installer.
type Boot struct {
	Title     string // shown before booting
	Shim      string // URLs: absolute paths or full URLs
	Kernel    string
	Initrd    string
	Cmdline   string
	ImageBase string // for the log line
}

// LinuxBoot is the start endpoint's answer: the distribution's shim through
// iPXE's shim command, then the kernel and initrd (Phase 7 spike, chain B).
func LinuxBoot(b Boot) string {
	return fmt.Sprintf(`#!ipxe
# netdeploy (ADR 0034): boot commands.
echo netdeploy: starting %s
shim %s || goto failed
kernel --name vmlinuz %s initrd=initrd %s || goto failed
initrd --name initrd %s || goto failed
boot || goto failed
%s`, Text(b.Title), b.Shim, b.Kernel, b.Cmdline, b.Initrd, diskAndFailed)
}

// Failure says why in an iPXE script and returns to the firmware. With
// retry set it goes back to arch's gate (autoexec.ipxe) instead, which
// times out to the firmware itself.
func Failure(arch, why string, retry bool) string {
	next := "goto disk\n"
	if retry {
		next = "sleep 5\nchain --autofree /content/boot/" + Dir + "/" + arch + "/" + Script + " || goto disk\n"
	}
	return "#!ipxe\n# netdeploy (ADR 0034)\necho netdeploy: " + Text(why) + "\nsleep 10\n" + next +
		":disk\necho netdeploy: booting from the next device.\nexit 1\n"
}

// Text makes s safe on an iPXE command line: one line, printable, no
// setting expansion ("${").
func Text(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return strings.ReplaceAll(s, "${", "$ {")
}

// menuID keeps an identifier to the characters iPXE takes in a menu item
// or setting value.
func menuID(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return -1
	}, s)
}
