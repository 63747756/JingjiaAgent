//go:build !linux

package main

import "errors"

func lock(string) (func(), error) { return nil, errors.New("installation requires Linux") }
