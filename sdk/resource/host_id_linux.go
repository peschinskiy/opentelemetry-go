// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package resource

import (
	"os"
	"path/filepath"
)

var platformHostIDReader hostIDReader = &hostIDReaderLinux{
	readFile: readFile,
	fromContainer: containerProbe{
		readFile:     readFile,
		fileExists:   fileExists,
		getenv:       os.Getenv,
		getpid:       os.Getpid,
		evalSymlinks: filepath.EvalSymlinks,
	}.machineIDFromContainer,
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
