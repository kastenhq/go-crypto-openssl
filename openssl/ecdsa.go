// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build linux && !android
// +build linux,!android

package openssl

// #include "goopenssl.h"
import "C"
import (
	"encoding/asn1"
	"errors"
	"math/big"
	"runtime"
	"unsafe"
)

type ecdsaSignature struct {
	R, S *big.Int
}

type PrivateKeyECDSA struct {
	key *C.EC_KEY
}

func (k *PrivateKeyECDSA) finalize() {
	C.go_openssl_EC_KEY_free(k.key)
}

type PublicKeyECDSA struct {
	key *C.EC_KEY
}

func (k *PublicKeyECDSA) finalize() {
	C.go_openssl_EC_KEY_free(k.key)
}

var errUnknownCurve = errors.New("openssl: unknown elliptic curve")
var errUnsupportedCurve = errors.New("openssl: unsupported elliptic curve")

func curveNID(curve string) (C.int, error) {
	switch curve {
	case "P-224":
		return C.NID_secp224r1, nil
	case "P-256":
		return C.NID_X9_62_prime256v1, nil
	case "P-384":
		return C.NID_secp384r1, nil
	case "P-521":
		return C.NID_secp521r1, nil
	}
	return 0, errUnknownCurve
}

func NewPublicKeyECDSA(curve string, X, Y *big.Int) (*PublicKeyECDSA, error) {
	key, err := newECKey(curve, X, Y)
	if err != nil {
		return nil, err
	}
	k := &PublicKeyECDSA{key}
	// Note: Because of the finalizer, any time k.key is passed to cgo,
	// that call must be followed by a call to runtime.KeepAlive(k),
	// to make sure k is not collected (and finalized) before the cgo
	// call returns.
	runtime.SetFinalizer(k, (*PublicKeyECDSA).finalize)
	return k, nil
}

func newECKey(curve string, X, Y *big.Int) (*C.EC_KEY, error) {
	nid, err := curveNID(curve)
	if err != nil {
		return nil, err
	}
	key := C.go_openssl_EC_KEY_new_by_curve_name(nid)
	if key == nil {
		return nil, newOpenSSLError("EC_KEY_new_by_curve_name failed")
	}
	group := C.go_openssl_EC_KEY_get0_group(key)
	pt := C.go_openssl_EC_POINT_new(group)
	if pt == nil {
		C.go_openssl_EC_KEY_free(key)
		return nil, newOpenSSLError("EC_POINT_new failed")
	}
	bx := bigToBN(X)
	by := bigToBN(Y)
	ok := bx != nil && by != nil && C.go_openssl_EC_POINT_set_affine_coordinates_GFp(group, pt, bx, by, nil) != 0 &&
		C.go_openssl_EC_KEY_set_public_key(key, pt) != 0
	if bx != nil {
		C.go_openssl_BN_free(bx)
	}
	if by != nil {
		C.go_openssl_BN_free(by)
	}
	C.go_openssl_EC_POINT_free(pt)
	if !ok {
		C.go_openssl_EC_KEY_free(key)
		return nil, newOpenSSLError("EC_POINT_free failed")
	}
	return key, nil
}

func NewPrivateKeyECDSA(curve string, X, Y *big.Int, D *big.Int) (*PrivateKeyECDSA, error) {
	key, err := newECKey(curve, X, Y)
	if err != nil {
		return nil, err
	}
	bd := bigToBN(D)
	ok := bd != nil && C.go_openssl_EC_KEY_set_private_key(key, bd) != 0
	if bd != nil {
		C.go_openssl_BN_free(bd)
	}
	if !ok {
		C.go_openssl_EC_KEY_free(key)
		return nil, newOpenSSLError("EC_KEY_set_private_key failed")
	}
	k := &PrivateKeyECDSA{key}
	// Note: Because of the finalizer, any time k.key is passed to cgo,
	// that call must be followed by a call to runtime.KeepAlive(k),
	// to make sure k is not collected (and finalized) before the cgo
	// call returns.
	runtime.SetFinalizer(k, (*PrivateKeyECDSA).finalize)
	return k, nil
}

func SignECDSA(priv *PrivateKeyECDSA, hash []byte) (r, s *big.Int, err error) {
	// We could use ECDSA_do_sign instead but would need to convert
	// the resulting BIGNUMs to *big.Int form. If we're going to do a
	// conversion, converting the ASN.1 form is more convenient and
	// likely not much more expensive.
	sig, err := SignMarshalECDSA(priv, hash)
	if err != nil {
		return nil, nil, err
	}
	var esig ecdsaSignature
	if _, err := asn1.Unmarshal(sig, &esig); err != nil {
		return nil, nil, err
	}
	return esig.R, esig.S, nil
}

func SignMarshalECDSA(priv *PrivateKeyECDSA, hash []byte) ([]byte, error) {
	size := C.go_openssl_ECDSA_size(priv.key)
	sig := make([]byte, size)
	var sigLen C.uint
	if C.go_openssl_ECDSA_sign(0, base(hash), C.size_t(len(hash)), (*C.uint8_t)(unsafe.Pointer(&sig[0])), &sigLen, priv.key) == 0 {
		return nil, newOpenSSLError("ECDSA_sign failed")
	}
	runtime.KeepAlive(priv)
	return sig[:sigLen], nil
}

func VerifyECDSA(pub *PublicKeyECDSA, hash []byte, r, s *big.Int) bool {
	// We could use ECDSA_do_verify instead but would need to convert
	// r and s to BIGNUM form. If we're going to do a conversion, marshaling
	// to ASN.1 is more convenient and likely not much more expensive.
	sig, err := asn1.Marshal(ecdsaSignature{r, s})
	if err != nil {
		return false
	}
	ok := C.go_openssl_ECDSA_verify(0, base(hash), C.size_t(len(hash)), (*C.uint8_t)(unsafe.Pointer(&sig[0])), C.uint(len(sig)), pub.key) > 0
	runtime.KeepAlive(pub)
	return ok
}

func GenerateKeyECDSA(curve string) (X, Y, D *big.Int, err error) {
	nid, err := curveNID(curve)
	if err != nil {
		return nil, nil, nil, err
	}
	key := C.go_openssl_EC_KEY_new_by_curve_name(nid)
	if key == nil {
		return nil, nil, nil, newOpenSSLError("EC_KEY_new_by_curve_name failed")
	}
	defer C.go_openssl_EC_KEY_free(key)
	if C.go_openssl_EC_KEY_generate_key(key) == 0 {
		return nil, nil, nil, newOpenSSLError("EC_KEY_generate_key failed")
	}
	group := C.go_openssl_EC_KEY_get0_group(key)
	pt := C.go_openssl_EC_KEY_get0_public_key(key)
	bd := C.go_openssl_EC_KEY_get0_private_key(key)
	if pt == nil || bd == nil {
		return nil, nil, nil, newOpenSSLError("EC_KEY_get0_private_key failed")
	}
	bx := C.go_openssl_BN_new()
	if bx == nil {
		return nil, nil, nil, newOpenSSLError("BN_new failed")
	}
	defer C.go_openssl_BN_free(bx)
	by := C.go_openssl_BN_new()
	if by == nil {
		return nil, nil, nil, newOpenSSLError("BN_new failed")
	}
	defer C.go_openssl_BN_free(by)
	if C.go_openssl_EC_POINT_get_affine_coordinates_GFp(group, pt, bx, by, nil) == 0 {
		return nil, nil, nil, newOpenSSLError("EC_POINT_get_affine_coordinates_GFp failed")
	}
	return bnToBig(bx), bnToBig(by), bnToBig(bd), nil
}