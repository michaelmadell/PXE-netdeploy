package ipxe

import (
	"strings"
	"testing"
)

func TestLinuxGate(t *testing.T) {
	armed := LinuxGate(GateOptions{Arch: "x64", Armed: true, ArmedCountdown: 10, PromptCountdown: 30, PasswordSet: true})
	for _, want := range []string{"prompt --timeout 10000", "param armed true", "chain --autofree /api/v1/agent/ipxe/start##params", "login || goto disk", "param mac ${netX/mac}", "exit 1"} {
		if !strings.Contains(armed, want) {
			t.Errorf("armed script lacks %q:\n%s", want, armed)
		}
	}
	locked := LinuxGate(GateOptions{Arch: "arm64", PromptCountdown: 30, PasswordSet: true})
	if strings.Contains(locked, "param armed") || !strings.Contains(locked, "prompt --timeout 30000 Press any key within 30 s to deploy this machine... || goto disk") || !strings.Contains(locked, "param arch arm64") {
		t.Errorf("locked script:\n%s", locked)
	}
	debug := LinuxGate(GateOptions{Arch: "x64", PromptCountdown: 30, NoPassword: true})
	if strings.Contains(debug, "login") || !strings.Contains(debug, "DEBUG MODE") || !strings.Contains(debug, "/api/v1/agent/ipxe/unlock##params") {
		t.Errorf("debug script:\n%s", debug)
	}
	none := LinuxGate(GateOptions{Arch: "x64", PromptCountdown: 30})
	if strings.Contains(none, "login") || strings.Contains(none, "unlock") || !strings.Contains(none, "only deployments armed") {
		t.Errorf("no-password script:\n%s", none)
	}
}

func TestLinuxMenu(t *testing.T) {
	m := LinuxMenu("x64", []MenuItem{{ID: "01ABC", Name: "Ubuntu ${evil}\nline"}, {ID: "01DEF", Name: "Other", Description: "desc"}}, "01DEF", "tok/en+=")
	for _, want := range []string{"item 01ABC Ubuntu $ {evil} line", "item 01DEF Other - desc", "choose --default 01DEF target", "param unlockToken token", "param profileId ${target}"} {
		if !strings.Contains(m, want) {
			t.Errorf("menu lacks %q:\n%s", want, m)
		}
	}
	if !strings.Contains(LinuxMenu("x64", nil, "", "t"), "choose --default disk") {
		t.Error("empty menu doesn't default to the disk")
	}
}

func TestLinuxBootAndFailure(t *testing.T) {
	b := LinuxBoot(Boot{Title: "Ubuntu", Shim: "/content/linux/I/EFI/boot/bootx64.efi", Kernel: "/content/linux/I/casper/vmlinuz",
		Initrd: "/content/linux/I/casper/initrd", Cmdline: "ip=dhcp url=http://h/iso cloud-config-url=/dev/null"})
	for _, want := range []string{"shim /content/linux/I/EFI/boot/bootx64.efi || goto failed", "kernel --name vmlinuz /content/linux/I/casper/vmlinuz initrd=initrd ip=dhcp url=http://h/iso cloud-config-url=/dev/null", "initrd --name initrd /content/linux/I/casper/initrd", "boot || goto failed"} {
		if !strings.Contains(b, want) {
			t.Errorf("boot script lacks %q:\n%s", want, b)
		}
	}
	if f := Failure("x64", "Wrong password.", true); !strings.Contains(f, "chain --autofree /content/boot/ipxe/x64/autoexec.ipxe") {
		t.Errorf("retry failure:\n%s", f)
	}
}
