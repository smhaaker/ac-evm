package wallet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/smhaaker/ac-evm/internal/keccak"
)

// Web3 Secret Storage v3 with pbkdf2 (scrypt would need x/crypto).
const pbkdf2Iter = 600_000

type keystore struct {
	Address string `json:"address"`
	Crypto  struct {
		Cipher       string `json:"cipher"`
		Ciphertext   string `json:"ciphertext"`
		CipherParams struct {
			IV string `json:"iv"`
		} `json:"cipherparams"`
		KDF       string `json:"kdf"`
		KDFParams struct {
			C     int    `json:"c"`
			DKLen int    `json:"dklen"`
			PRF   string `json:"prf"`
			Salt  string `json:"salt"`
		} `json:"kdfparams"`
		MAC string `json:"mac"`
	} `json:"crypto"`
	ID      string `json:"id"`
	Version int    `json:"version"`
}

func (k *Key) Encrypt(pass string) ([]byte, error) {
	salt, iv, id := make([]byte, 32), make([]byte, 16), make([]byte, 16)
	for _, b := range [][]byte{salt, iv, id} {
		rand.Read(b)
	}
	id[6], id[8] = id[6]&0x0f|0x40, id[8]&0x3f|0x80 // uuid v4

	dk, err := pbkdf2.Key(sha256.New, pass, salt, pbkdf2Iter, 32)
	if err != nil {
		return nil, err
	}
	ct, err := ctr(dk[:16], iv, k.priv.Serialize())
	if err != nil {
		return nil, err
	}
	mac := keccak.Sum256(append(dk[16:32:32], ct...))

	var ks keystore
	ks.Address = strings.TrimPrefix(strings.ToLower(k.Address().Hex()), "0x")
	ks.Crypto.Cipher = "aes-128-ctr"
	ks.Crypto.Ciphertext = hex.EncodeToString(ct)
	ks.Crypto.CipherParams.IV = hex.EncodeToString(iv)
	ks.Crypto.KDF = "pbkdf2"
	ks.Crypto.KDFParams.C = pbkdf2Iter
	ks.Crypto.KDFParams.DKLen = 32
	ks.Crypto.KDFParams.PRF = "hmac-sha256"
	ks.Crypto.KDFParams.Salt = hex.EncodeToString(salt)
	ks.Crypto.MAC = hex.EncodeToString(mac[:])
	h := hex.EncodeToString(id)
	ks.ID = fmt.Sprintf("%s-%s-%s-%s-%s", h[:8], h[8:12], h[12:16], h[16:20], h[20:])
	ks.Version = 3
	return json.MarshalIndent(ks, "", "  ")
}

func Decrypt(data []byte, pass string) (*Key, error) {
	var ks keystore
	if err := json.Unmarshal(data, &ks); err != nil {
		return nil, err
	}
	c := ks.Crypto
	if ks.Version != 3 || c.Cipher != "aes-128-ctr" {
		return nil, errors.New("wallet: unsupported keystore")
	}
	if c.KDF != "pbkdf2" || c.KDFParams.PRF != "hmac-sha256" || c.KDFParams.DKLen != 32 {
		return nil, errors.New("wallet: only pbkdf2 keystores are supported; import the private key instead")
	}
	salt, err1 := hex.DecodeString(c.KDFParams.Salt)
	iv, err2 := hex.DecodeString(c.CipherParams.IV)
	ct, err3 := hex.DecodeString(c.Ciphertext)
	mac, err4 := hex.DecodeString(c.MAC)
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return nil, err
	}
	dk, err := pbkdf2.Key(sha256.New, pass, salt, c.KDFParams.C, 32)
	if err != nil {
		return nil, err
	}
	want := keccak.Sum256(append(dk[16:32:32], ct...))
	if subtle.ConstantTimeCompare(want[:], mac) != 1 {
		return nil, errors.New("wallet: wrong passphrase")
	}
	priv, err := ctr(dk[:16], iv, ct)
	if err != nil {
		return nil, err
	}
	return FromHex(hex.EncodeToString(priv))
}

func ctr(key, iv, in []byte) ([]byte, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(in))
	cipher.NewCTR(b, iv).XORKeyStream(out, in)
	return out, nil
}
