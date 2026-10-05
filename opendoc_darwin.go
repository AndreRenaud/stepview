//go:build cgo

package main

/*
#cgo LDFLAGS: -framework Cocoa
void stepviewInstallOpenHandler(void);
*/
import "C"

var openDocs = make(chan modelSource, 16)

// watchOpenDocuments returns a channel of the files Finder asks the app to
// open. It must be called before the UI starts: a file double-clicked to
// launch the app arrives while the application is starting up.
func watchOpenDocuments() <-chan modelSource {
	C.stepviewInstallOpenHandler()
	return openDocs
}

//export stepviewOpenDocument
func stepviewOpenDocument(path *C.char) {
	// This runs on the main thread inside the event loop, so never block.
	select {
	case openDocs <- fileSource(C.GoString(path)):
	default:
	}
}
