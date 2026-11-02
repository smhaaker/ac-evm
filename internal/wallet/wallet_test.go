package wallet

import "testing"

// PBKDF2 test vector from the Web3 Secret Storage spec.
const specVector = `{"crypto":{"cipher":"aes-128-ctr","cipherparams":{"iv":"6087dab2f9fdbbfaddc31a909735c1e6"},
"ciphertext":"5318b4d5bcd28de64ee5559e671353e16f075ecae9f99c7a79a38af5f869aa46","kdf":"pbkdf2",
"kdfparams":{"c":262144,"dklen":32,"prf":"hmac-sha256","salt":"ae3cd4e7013836a3df6bd7241b12db061dbe2c6785853cce422d148a624ce0bd"},
"mac":"517ead924a9d0dc3124507e3393d175ce3ff7c1e96529c6c555ce9e51205e9b2"},"id":"3198bc9c-6672-5ab3-d995-4942343ae5b6","version":3}`

func TestKeystore(t *testing.T) {
	k, err := Decrypt([]byte(specVector), "testpassword")
	if err != nil {
		t.Fatal(err)
	}
	if got := k.priv.Key.String(); got != "7a28b5ba57c53603b0b07b56bba752f7784bf506fa95edc395f5cf6c7514fe9d" {
		t.Fatalf("priv = %s", got)
	}
	if got := k.Address().Hex(); got != "0x008AeEda4D805471dF9b2A5B0f38A0C3bCBA786b" {
		t.Fatalf("address = %s", got)
	}
	if _, err := Decrypt([]byte(specVector), "wrong"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}

	enc, err := k.Encrypt("pw")
	if err != nil {
		t.Fatal(err)
	}
	if back, err := Decrypt(enc, "pw"); err != nil || back.Address() != k.Address() {
		t.Fatalf("round trip: %v", err)
	}
}
