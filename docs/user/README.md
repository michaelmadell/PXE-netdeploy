# User guide

How to install netdeploy, set up a deployment network, and deploy Windows 11
and Ubuntu machines. For how it works inside, see the
[architecture](../architecture.md) and the [decision records](../adr/README.md).

1. [Installing](install.md): the Windows installer or the Ubuntu package,
   first sign-in, upgrading and removing.
2. [Setting up](setup.md): the deployment network (DHCP modes, TFTP, HTTP,
   HTTPS), client types, the boot prompt, timeouts and backups.
3. [Deploying Windows 11](windows.md): images, drivers, boot images,
   templates, profiles and clients.
4. [Deploying Ubuntu](ubuntu.md): Linux images, automatic and interactive
   installs, autoinstall templates.
5. [Troubleshooting](troubleshooting.md): where the logs are and the usual
   problems.

## Concepts in one minute

- **Images** are imported ISOs: Windows install media, or a Linux installer.
- **Boot images** (Windows only) are WinPE builds from an image, with your
  drivers and netdeploy's agent; machines boot them to be deployed.
- **Profiles** say what a machine becomes: for Windows a boot image, an
  edition, accounts and drivers; for Ubuntu an installer image and its
  install settings.
- **Clients** are machines, by MAC address, found when they network boot or
  added by hand. A client can have its own profile; the **default profile**
  covers the rest.
- **Deployments** install a profile on a client: **armed** from the UI (it
  installs on the next network boot after a short countdown), or started at
  the machine's **boot prompt** with the password. Without either, a machine
  that network boots just carries on to its disk, so installed machines are
  never re-imaged by accident.
