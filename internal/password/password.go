package password

/*
#cgo LDFLAGS: -L/lib/x86_64-linux-gnu -l:libargon2.so.1
#include <stdlib.h>
#include <stdint.h>
int argon2id_hash_encoded(const uint32_t t_cost, const uint32_t m_cost, const uint32_t parallelism,
                          const void *pwd, const size_t pwdlen, const void *salt, const size_t saltlen,
                          const size_t hashlen, char *encoded, const size_t encodedlen);
int argon2id_verify(const char *encoded, const void *pwd, const size_t pwdlen);
*/
import "C"

import (
	"crypto/rand"
	"errors"
	"unsafe"
)

func Hash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return "", e
	}
	out := make([]byte, 256)
	p := C.CBytes([]byte(password))
	defer C.free(p)
	sp := C.CBytes(salt)
	defer C.free(sp)
	rc := C.argon2id_hash_encoded(3, 64*1024, 1, p, C.size_t(len(password)), sp, C.size_t(len(salt)), 32, (*C.char)(unsafe.Pointer(&out[0])), C.size_t(len(out)))
	if rc != 0 {
		return "", errors.New("argon2id hash failed")
	}
	n := 0
	for n < len(out) && out[n] != 0 {
		n++
	}
	return string(out[:n]), nil
}
func Verify(encoded, password string) bool {
	c := C.CString(encoded)
	defer C.free(unsafe.Pointer(c))
	p := C.CBytes([]byte(password))
	defer C.free(p)
	return C.argon2id_verify(c, p, C.size_t(len(password))) == 0
}
