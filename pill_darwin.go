package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>

void tailtray_pill(const char *title, int on);
*/
import "C"

import "unsafe"

// pill replaces the top-level menu item with this title by a row with an
// on/off switch, which macOS menus otherwise draw as a checkmark.
func pill(title string, on bool) {
	t := C.CString(title)
	defer C.free(unsafe.Pointer(t))
	var v C.int
	if on {
		v = 1
	}
	C.tailtray_pill(t, v)
}
