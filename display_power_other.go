//go:build !windows || !amd64

package main

// captureDisplayRequest 在非Windows x64平台不申请显示电源，保留原平台采集行为。
func captureDisplayRequest() (func(), error) {
	return func() {}, nil
}
