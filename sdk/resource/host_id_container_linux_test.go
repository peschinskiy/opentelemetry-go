// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package resource

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// mountinfo lines as observed in real environments.
const (
	hostRootMount       = "26 1 253:1 / / rw,relatime shared:1 - ext4 /dev/root rw,discard\n"
	overlayRootMount    = "1618 1549 0:268 / / rw,relatime - overlay overlay rw,lowerdir=/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/1/fs\n"
	dockerHostnameMount = "1629 1618 253:17 /docker/containers/68f4a4e57fb1/hostname /etc/hostname rw,relatime - ext4 /dev/vdb1 rw\n"
	hostMachineIDMount  = "1628 1618 253:1 /etc/machine-id /etc/machine-id ro,relatime - ext4 /dev/root rw\n"
	outerMachineIDMount = "1700 1690 0:300 /etc/machine-id /etc/machine-id ro,relatime - overlay overlay rw\n"
	bootIDMount         = "1339 1558 0:284 /.lxc-boot-id /proc/sys/kernel/random/boot_id ro,nosuid,nodev,noexec - tmpfs none rw\n"
	escapedMount        = "40 26 253:2 / /mnt/my\\040disk rw - ext4 /dev/vdc rw\n"
)

type fakeEnv struct {
	files map[string]string // path -> content; presence means the file exists
	env   map[string]string
	pid   int
	links map[string]string // symlink path -> target
}

func (f fakeEnv) probe() containerProbe {
	return containerProbe{
		readFile: func(path string) (string, error) {
			if c, ok := f.files[path]; ok {
				return c, nil
			}
			return "", os.ErrNotExist
		},
		fileExists: func(path string) bool {
			_, ok := f.files[path]
			return ok
		},
		getenv: func(key string) string { return f.env[key] },
		getpid: func() int {
			if f.pid == 0 {
				return 4242
			}
			return f.pid
		},
		evalSymlinks: func(path string) (string, error) {
			if target, ok := f.links[path]; ok {
				return target, nil
			}
			if _, ok := f.files[path]; ok {
				return path, nil
			}
			return "", errors.New("not found")
		},
	}
}

func withFiles(mountinfo string, extra ...string) map[string]string {
	files := map[string]string{
		"/proc/self/mountinfo":       mountinfo,
		"/etc/machine-id":            expectedHostID + "\n",
		"/proc/sys/kernel/osrelease": "6.8.0-117-generic\n",
	}
	for _, path := range extra {
		files[path] = ""
	}
	return files
}

func TestMachineIDFromContainer(t *testing.T) {
	tests := []struct {
		name string
		env  fakeEnv
		path string
		want bool
	}{
		{
			name: "host",
			env:  fakeEnv{files: withFiles(hostRootMount)},
			want: false,
		},
		{
			name: "host with an overlay root and no other signal",
			env:  fakeEnv{files: withFiles(strings.Replace(overlayRootMount, "1618 1549", "26 1", 1))},
			want: false,
		},
		{
			name: "docker: machine-id baked into the image",
			env:  fakeEnv{files: withFiles(overlayRootMount+dockerHostnameMount, "/.dockerenv")},
			want: true,
		},
		{
			name: "kubernetes: runtime /etc/hostname mount only, hostPID",
			env:  fakeEnv{files: withFiles(overlayRootMount + dockerHostnameMount)},
			want: true,
		},
		{
			name: "docker: host machine-id bind-mounted",
			env:  fakeEnv{files: withFiles(overlayRootMount+dockerHostnameMount+hostMachineIDMount, "/.dockerenv")},
			want: false,
		},
		{
			name: "docker-in-docker: outer container's machine-id bind-mounted",
			env:  fakeEnv{files: withFiles(overlayRootMount+dockerHostnameMount+outerMachineIDMount, "/.dockerenv")},
			want: true,
		},
		{
			name: "kubernetes: KUBERNETES_SERVICE_HOST",
			env:  fakeEnv{files: withFiles(hostRootMount), env: map[string]string{"KUBERNETES_SERVICE_HOST": "10.96.0.1"}},
			want: true,
		},
		{
			name: "docker: /.dockerenv",
			env:  fakeEnv{files: withFiles(hostRootMount, "/.dockerenv")},
			want: true,
		},
		{
			name: "podman: /run/.containerenv",
			env:  fakeEnv{files: withFiles(hostRootMount, "/run/.containerenv")},
			want: true,
		},
		{
			name: "$container set for PID 1",
			env:  fakeEnv{files: withFiles(hostRootMount), env: map[string]string{"container": "podman"}, pid: 1},
			want: true,
		},
		{
			name: "$container set, not PID 1",
			env:  fakeEnv{files: withFiles(hostRootMount), env: map[string]string{"container": "oci"}},
			want: false,
		},
		{
			name: "container running systemd as init",
			env:  fakeEnv{files: withFiles(hostRootMount, "/run/systemd/container")},
			want: true,
		},
		{
			name: "lxc or nspawn: boot_id bind mount",
			env:  fakeEnv{files: withFiles(hostRootMount + bootIDMount)},
			want: true,
		},
		{
			name: "lxc: host machine-id bind-mounted",
			env:  fakeEnv{files: withFiles(hostRootMount + bootIDMount + hostMachineIDMount)},
			want: false,
		},
		{
			name: "openvz container",
			env:  fakeEnv{files: withFiles(hostRootMount, "/proc/vz")},
			want: true,
		},
		{
			name: "openvz host node",
			env:  fakeEnv{files: withFiles(hostRootMount, "/proc/vz", "/proc/bc")},
			want: false,
		},
		{
			name: "wsl2",
			env: fakeEnv{files: func() map[string]string {
				f := withFiles(hostRootMount)
				f["/proc/sys/kernel/osrelease"] = "5.15.167.4-microsoft-standard-WSL2\n"
				return f
			}()},
			want: true,
		},
		{
			name: "wsl1",
			env: fakeEnv{files: func() map[string]string {
				f := withFiles(hostRootMount)
				f["/proc/sys/kernel/osrelease"] = "4.4.0-19041-Microsoft\n"
				return f
			}()},
			want: true,
		},
		{
			name: "machine-id symlink resolved to a bind-mounted host file",
			env: fakeEnv{
				files: withFiles(overlayRootMount+dockerHostnameMount+hostMachineIDMount, "/var/lib/dbus/machine-id"),
				links: map[string]string{"/var/lib/dbus/machine-id": "/etc/machine-id"},
			},
			path: "/var/lib/dbus/machine-id",
			want: false,
		},
		{
			name: "mountinfo unreadable, no other signal",
			env:  fakeEnv{files: map[string]string{"/etc/machine-id": expectedHostID}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.path
			if path == "" {
				path = "/etc/machine-id"
			}
			assert.Equal(t, tt.want, tt.env.probe().machineIDFromContainer(path))
		})
	}
}

func TestMounts(t *testing.T) {
	p := fakeEnv{files: withFiles(hostRootMount + escapedMount + "malformed line\n")}.probe()
	assert.Equal(t, []mountInfo{
		{mountPoint: "/", fsType: "ext4"},
		{mountPoint: "/mnt/my disk", fsType: "ext4"},
	}, p.mounts())
}

func TestContainingMount(t *testing.T) {
	mounts := []mountInfo{
		{mountPoint: "/", fsType: "overlay"},
		{mountPoint: "/etc", fsType: "xfs"},
		{mountPoint: "/etc/hostname", fsType: "ext4"},
		{mountPoint: "/etcd", fsType: "tmpfs"},
	}
	tests := []struct {
		path string
		want string
	}{
		{"/etc/machine-id", "/etc"},
		{"/etc/hostname", "/etc/hostname"},
		{"/etcd/data", "/etcd"},
		{"/var/lib/dbus/machine-id", "/"},
	}
	for _, tt := range tests {
		m, ok := containingMount(mounts, tt.path)
		assert.True(t, ok, tt.path)
		assert.Equal(t, tt.want, m.mountPoint, tt.path)
	}
}
