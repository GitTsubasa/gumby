package opencc

// #cgo LDFLAGS: -L${SRCDIR}/opencc -lopencc
/*
#include <stdlib.h>
#include <opencc/src/opencc.h>
*/
import "C"
import (
	"errors"
	"runtime"
	"unsafe"
)

type Converter struct {
	ptr C.opencc_t
}

func New(configFile string) (*Converter, error) {
	configFileCStr := C.CString(configFile)
	defer C.free(unsafe.Pointer(configFileCStr))

	c := C.opencc_open(configFileCStr)

	if c == nil {
		return nil, errors.New(C.GoString(C.opencc_error()))
	}
	conv := &Converter{c}
	runtime.SetFinalizer(conv, func(c *Converter) { c.Close() })
	return conv, nil
}

func (c *Converter) Close() error {
	if result := C.opencc_close(c.ptr); result != 0 {
		return errors.New(C.GoString(C.opencc_error()))
	}
	runtime.SetFinalizer(c, nil)
	return nil
}

func (c *Converter) Convert(s string) (string, error) {
	sCStr := C.CString(s)
	defer C.free(unsafe.Pointer(sCStr))

	rCStr := C.opencc_convert_utf8(c.ptr, sCStr, C.ulong(len(s)))
	if rCStr == nil {
		return "", errors.New(C.GoString(C.opencc_error()))
	}

	r := C.GoString(rCStr)
	C.opencc_convert_utf8_free(rCStr)
	return r, nil
}
