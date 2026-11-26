// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package resource

import (
	"context"
	"errors"
	"fmt"
	"strings"

	semconv "go.opentelemetry.io/otel/semconv/v1.45.0"
)

type hostIDProvider func() (string, error)

var defaultHostIDProvider hostIDProvider = platformHostIDReader.read

var hostID = defaultHostIDProvider

type hostIDReader interface {
	read() (string, error)
}

type fileReader func(string) (string, error)

type commandExecutor func(string, ...string) (string, error)

// hostIDReaderBSD implements hostIDReader.
type hostIDReaderBSD struct {
	execCommand commandExecutor
	readFile    fileReader
}

// read attempts to read the machine-id from /etc/hostid.
// If not found it will execute: /bin/kenv -q smbios.system.uuid.
// If neither location yields an id an error will be returned.
func (r *hostIDReaderBSD) read() (string, error) {
	if result, err := r.readFile("/etc/hostid"); err == nil {
		return strings.TrimSpace(result), nil
	}

	if result, err := r.execCommand("/bin/kenv", "-q", "smbios.system.uuid"); err == nil {
		return strings.TrimSpace(result), nil
	}

	return "", errors.New("host id not found in: /etc/hostid or /bin/kenv")
}

// hostIDReaderDarwin implements hostIDReader.
type hostIDReaderDarwin struct {
	execCommand commandExecutor
}

// read executes `/usr/sbin/ioreg -rd1 -c "IOPlatformExpertDevice"` and parses host id
// from the IOPlatformUUID line. If the command fails or the uuid cannot be
// parsed an error will be returned.
func (r *hostIDReaderDarwin) read() (string, error) {
	result, err := r.execCommand("/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice")
	if err != nil {
		return "", err
	}

	for line := range strings.SplitSeq(result, "\n") {
		if strings.Contains(line, "IOPlatformUUID") {
			parts := strings.Split(line, " = ")
			if len(parts) == 2 {
				return strings.Trim(parts[1], "\""), nil
			}
			break
		}
	}

	return "", errors.New("could not parse IOPlatformUUID")
}

type hostIDReaderLinux struct {
	readFile fileReader
	// fromContainer reports whether the machine-id file at the given path was
	// provided by a container rather than by the host. It may be nil.
	fromContainer func(path string) bool
}

// read attempts to read the machine-id from /etc/machine-id followed by
// /var/lib/dbus/machine-id. Empty and "uninitialized" (see machine-id(5))
// values are skipped. If neither location yields an ID, or the ID was
// provided by a container, an error will be returned.
func (r *hostIDReaderLinux) read() (string, error) {
	for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		result, err := r.readFile(path)
		if err != nil {
			continue
		}
		id := strings.TrimSpace(result)
		if id == "" || id == "uninitialized" {
			continue
		}
		// A container's machine-id is baked into its image or generated at
		// startup, so it does not identify the host.
		if r.fromContainer != nil && r.fromContainer(path) {
			return "", fmt.Errorf("host id not used: %s is provided by a container", path)
		}
		return id, nil
	}

	return "", errors.New("host id not found in: /etc/machine-id or /var/lib/dbus/machine-id")
}

type hostIDDetector struct{}

// Detect returns a *Resource containing the platform specific host id.
func (hostIDDetector) Detect(context.Context) (*Resource, error) {
	hostID, err := hostID()
	if err != nil {
		return nil, err
	}

	return NewWithAttributes(
		semconv.SchemaURL,
		semconv.HostID(hostID),
	), nil
}
