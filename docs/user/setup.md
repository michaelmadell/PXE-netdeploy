# Setting up

Everything here is in **Settings**. Changes are checked and applied together
when you save; a port that's already taken is named with the program holding
it.

## The deployment network (Settings → Network)

Add a **deployment interface** for each network adapter clients boot from,
and choose how it answers DHCP:

| DHCP mode | Use it when | What netdeploy does |
|---|---|---|
| **Off** | Your DHCP server already hands out boot options (66/67) pointing here | Serves TFTP/HTTP only |
| **Proxy** | You have a DHCP server you don't want to change | Answers only network-booting clients with boot information; your DHCP server still gives out addresses |
| **Full** | An isolated deployment network with no other DHCP server | Hands out addresses from a pool and the boot information |

Only one DHCP server should answer on a network in full mode: a second one
can win the race and hand out its own addresses (netdeploy logs "another
DHCP server answered a network-boot client" when it sees that).

Services per interface:

- **TFTP**: for PXE firmware and the Windows boot manager. Needed for PXE.
- **HTTP**: boot files and images for the agent, UEFI HTTP Boot, and
  **required for Ubuntu**, whose installer downloads its ISO and settings
  over plain HTTP.
- **HTTPS**: content over TLS with netdeploy's internal CA (Settings →
  Admin → Content HTTPS). HTTP(S) Boot clients then get an `https://` boot
  URL, and the Windows agent only trusts this CA.

**Address clients use** is only needed when clients reach the server at a
different address than the adapter's (NAT, a relay). **Only answer these MAC
prefixes** limits which machines get a boot answer at all.

## Client types (Settings → Network)

Which firmware gets an answer: UEFI x64, UEFI ARM64, UEFI HTTP Boot, legacy
BIOS. HTTP Boot is needed for Ubuntu; PXE (UEFI x64) works for Windows.

## Boot prompt (Settings → Deployments)

What a machine does when it network boots with nothing armed for it:

- It shows a countdown (**Prompt countdown**, 30 s by default). With no key
  pressed it starts from its disk, so installed machines carry on as normal.
- A key opens the **password prompt**. The right **boot prompt password**
  shows the profiles to choose from: in WinPE for Windows profiles, in the
  iPXE menu for Linux profiles.
- An **armed** deployment instead runs after a short countdown
  (**Scheduled deployment countdown**, 10 s), which a key interrupts.
- **Default profile**: offered to machines that have no profile of their own.

Set a password before deploying. Turning **Require the password** off lets
anyone at a machine on the deployment network re-image it; use it only on a
bench.

## Deployment timeouts (Settings → Deployments)

- **Scheduled deployments expire** after 72 hours if the machine never network
  boots.
- **Running deployments time out** after 30 minutes without a report from
  the machine (the Windows agent or the Ubuntu installer), and are marked
  failed.

## Storage and backups (Settings → Storage & backups)

- **Free space to keep**: imports and builds stop before the disk fills.
- **Backups**: scheduled, incremental, to a folder or an SMB share, with the
  database, certificates and the stored files they need. Backups don't
  include the secrets key: export it once (see [Installing](install.md)).

## Admin (Settings → Admin)

Where the admin UI listens, its certificate (the generated one, or your
own), and content HTTPS. **Accounts** (in the menu) manages admin users and
API tokens for automation.

## Secrets (in the menu)

Passwords, domain join credentials, offline join blobs and product keys are
stored encrypted, never shown again after saving, and only decrypted for a
running deployment. Profiles and clients refer to them by name.

Next: [Deploying Windows 11](windows.md) or [Deploying Ubuntu](ubuntu.md).
