// Copyright 2026 The GoGPU Authors
// SPDX-License-Identifier: MIT

//go:build !rtwip

// Command ray-scene is a port of Rust wgpu's ray_scene example. It is written
// against the public ray tracing API planned in docs/RAY-TRACING-PLAN.md and
// only builds with -tags rtwip until that API exists (plan task 3.9 removes
// this stub). The OBJ loader in obj.go builds and is tested without the tag.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "ray-scene: the public ray tracing API is not implemented yet; see docs/RAY-TRACING-PLAN.md")
	os.Exit(1)
}
