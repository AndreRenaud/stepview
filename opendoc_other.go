//go:build !darwin || !cgo

package main

// watchOpenDocuments returns nil: only macOS sends files to an already
// running application, and receiving them needs cgo.
func watchOpenDocuments() <-chan string {
	return nil
}
