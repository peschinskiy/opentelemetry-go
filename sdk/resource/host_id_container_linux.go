// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package resource

import (
	"bufio"
	"path/filepath"
	"strings"
)

// containerProbe decides whether a machine-id file was provided by a
// container rather than by the host, using only unprivileged reads.
//
// A container's machine-id is either baked into its image or generated at
// startup, so it is neither unique (every container of an image shares it)
// nor repeatable (other observers on the host report a different value).
type containerProbe struct {
	readFile     fileReader
	fileExists   func(string) bool
	getenv       func(string) string
	getpid       func() int
	evalSymlinks func(string) (string, error)
}

type mountInfo struct {
	mountPoint string
	fsType     string
}

// machineIDFromContainer reports whether the machine-id file at path comes
// from a container, and must therefore not be used as host.id.
func (p containerProbe) machineIDFromContainer(path string) bool {
	mounts := p.mounts()

	resolved := path
	if r, err := p.evalSymlinks(path); err == nil {
		resolved = r
	}

	// The machine-id file is itself a mount point: it was explicitly provided
	// (e.g. the host's file bind-mounted into the container), unless it was
	// bind-mounted from another container's image layers (container-in-container).
	if m, ok := containingMount(mounts, resolved); ok && m.mountPoint == resolved {
		return isContainerFS(m.fsType)
	}

	return p.inContainer(mounts)
}

// inContainer reports whether any in-container signal applies. The checks
// follow systemd's detect_container() where possible, and only use files that
// any user can read.
func (p containerProbe) inContainer(mounts []mountInfo) bool {
	// Kubernetes (CRI-O, containerd), Docker, Podman: the runtime generates
	// the per-container hostname and bind-mounts it over /etc/hostname.
	if isMountPoint(mounts, "/etc/hostname") {
		return true
	}
	// Kubernetes: injected into every pod.
	if p.getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	// Docker and Podman marker files.
	if p.fileExists("/.dockerenv") || p.fileExists("/run/.containerenv") {
		return true
	}
	// Podman, LXC, systemd-nspawn: $container is set for PID 1 only. Like
	// systemd, trust it only when this process is PID 1.
	if p.getpid() == 1 && p.getenv("container") != "" {
		return true
	}
	// Containers running systemd as init: systemd copies $container here.
	if p.fileExists("/run/systemd/container") {
		return true
	}
	// systemd-nspawn and LXC (also Incus, Proxmox) bind-mount a fake boot_id,
	// whatever the container's init is.
	if isMountPoint(mounts, "/proc/sys/kernel/random/boot_id") {
		return true
	}
	// OpenVZ: /proc/vz exists inside and outside of the container, /proc/bc
	// only outside of it.
	if p.fileExists("/proc/vz") && !p.fileExists("/proc/bc") {
		return true
	}
	// WSL runs distributions as containers inside a shared utility VM.
	if osRelease, err := p.readFile("/proc/sys/kernel/osrelease"); err == nil &&
		(strings.Contains(osRelease, "Microsoft") || strings.Contains(osRelease, "WSL")) {
		return true
	}
	return false
}

// mounts parses /proc/self/mountinfo, see proc_pid_mountinfo(5). It returns
// nil if the file cannot be read.
func (p containerProbe) mounts() []mountInfo {
	content, err := p.readFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}

	var mounts []mountInfo
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// The optional fields end with a "-" separator, followed by the
		// file system type.
		for i := 5; i < len(fields)-1; i++ {
			if fields[i] == "-" {
				mounts = append(mounts, mountInfo{
					mountPoint: unescapeMountPath(fields[4]),
					fsType:     fields[i+1],
				})
				break
			}
		}
	}
	return mounts
}

var mountPathUnescaper = strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)

func unescapeMountPath(path string) string {
	return mountPathUnescaper.Replace(path)
}

// containingMount returns the mount that path lives on: the last mount whose
// mount point is the longest prefix of path.
func containingMount(mounts []mountInfo, path string) (mountInfo, bool) {
	path = filepath.Clean(path)
	var best mountInfo
	found := false
	for _, m := range mounts {
		prefix := strings.TrimSuffix(m.mountPoint, "/") + "/"
		if m.mountPoint == path || m.mountPoint == "/" || strings.HasPrefix(path, prefix) {
			if !found || len(m.mountPoint) >= len(best.mountPoint) {
				best, found = m, true
			}
		}
	}
	return best, found
}

func isMountPoint(mounts []mountInfo, path string) bool {
	m, ok := containingMount(mounts, path)
	return ok && m.mountPoint == path
}

// isContainerFS reports the file systems container runtimes use for image layers.
func isContainerFS(fsType string) bool {
	switch fsType {
	case "overlay", "aufs", "fuse.fuse-overlayfs":
		return true
	}
	return false
}
