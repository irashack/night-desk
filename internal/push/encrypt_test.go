package push

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"strings"
	"testing"
)

// RFC 8291 Appendix A, including the RFC 8188 header and final delimiter.
const (
	uaPublic   = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	authSecret = "BTBZMqHH6r4Tts7J_aSIgg"
)

func decoded(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestRFC8291AppendixA(t *testing.T) {
	sender, err := ecdh.P256().NewPrivateKey(decoded(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	receiver, auth, err := receiverKeys(uaPublic, authSecret)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sealRecord(sender, receiver, auth, decoded(t, "DGv6ra1nlYgDCS1FRnbzlw"), []byte("When I grow up, I want to be a watermelon"))
	want := decoded(t, "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("RFC 8291 ciphertext mismatch", err)
	}
}
func TestValidKeys(t *testing.T) {
	if !ValidKeys(uaPublic, authSecret) || !ValidKeys(uaPublic+"=", authSecret+"==") {
		t.Fatal("valid keys rejected")
	}
	invalidPoint := append([]byte{4}, make([]byte, 64)...)
	for _, pair := range [][2]string{{"", authSecret}, {uaPublic, ""}, {base64.RawURLEncoding.EncodeToString(invalidPoint), authSecret}, {uaPublic, authSecret + "="}, {uaPublic, authSecret + "A"}, {uaPublic, authSecret + "\n"}, {strings.Replace(uaPublic, "_", "/", 1), authSecret}} {
		if ValidKeys(pair[0], pair[1]) {
			t.Fatal("invalid keys accepted")
		}
	}
}
