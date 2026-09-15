// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package main

import "syscall"

const oReadNonblock = syscall.O_NONBLOCK
