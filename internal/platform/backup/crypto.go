package backup

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/scrypt"
)

const (
	backupAADPrefix = "upmo.backup.v1"
	streamMagic     = "UBK1"
	dekLen          = 32
	gcmNonceLen     = 12
	chunkSize       = 1 << 20 // 1 MiB
	scryptSaltLen   = 16
	scryptN         = 1 << 15
	scryptR         = 8
	scryptP         = 1

	sealModeKeyring    = "keyring"
	sealModePassphrase = "passphrase"
)

// Sealed holds an AEAD-sealed value from a Keyring.
type Sealed struct {
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
}

// Keyring seals and opens backup encryption keys and checks master key ID
// compatibility against the active environment keyring.
type Keyring interface {
	Ready() bool
	CurrentID() string
	HasKey(id string) bool
	Seal(ref, plaintext string) (Sealed, error)
	Open(ref string, s Sealed) (string, error)
}

func sealDEK(dek []byte, ring Keyring, passphrase string, aad []byte) (*SealedKeyInfo, error) {
	if len(dek) != dekLen {
		return nil, fmt.Errorf("backup: invalid data encryption key length %d", len(dek))
	}
	if len(aad) == 0 {
		aad = []byte(backupAADPrefix)
	}
	encoded := base64.StdEncoding.EncodeToString(dek)
	if passphrase != "" {
		salt := make([]byte, scryptSaltLen)
		if _, err := rand.Read(salt); err != nil {
			return nil, fmt.Errorf("backup: random salt: %w", err)
		}
		kek, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, dekLen)
		if err != nil {
			return nil, fmt.Errorf("backup: derive passphrase key: %w", err)
		}
		defer clear(kek)

		block, err := aes.NewCipher(kek)
		if err != nil {
			return nil, fmt.Errorf("backup: passphrase cipher: %w", err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("backup: passphrase gcm: %w", err)
		}
		nonce := make([]byte, aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return nil, fmt.Errorf("backup: random nonce: %w", err)
		}
		ct := aead.Seal(nil, nonce, []byte(encoded), aad)
		return &SealedKeyInfo{
			Mode:       sealModePassphrase,
			Salt:       base64.StdEncoding.EncodeToString(salt),
			Nonce:      base64.StdEncoding.EncodeToString(nonce),
			Ciphertext: base64.StdEncoding.EncodeToString(ct),
		}, nil
	}
	if ring == nil || !ring.Ready() {
		return nil, errors.New("backup: encryption requires UPMO_MASTER_KEY, --passphrase-env, or --no-encrypt")
	}
	sealed, err := ring.Seal(string(aad), encoded)
	if err != nil {
		return nil, fmt.Errorf("backup: seal archive key: %w", err)
	}
	return &SealedKeyInfo{
		Mode:       sealModeKeyring,
		KeyID:      sealed.KeyID,
		Nonce:      base64.StdEncoding.EncodeToString(sealed.Nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(sealed.Ciphertext),
	}, nil
}

func openDEK(info *SealedKeyInfo, ring Keyring, passphrase string, aad []byte) ([]byte, error) {
	if info == nil {
		return nil, errors.New("backup: encrypted archive is missing sealed_key metadata")
	}
	if len(aad) == 0 {
		aad = []byte(backupAADPrefix)
	}
	nonce, err := base64.StdEncoding.DecodeString(info.Nonce)
	if err != nil {
		return nil, errors.New("backup: invalid sealed key nonce")
	}
	ct, err := base64.StdEncoding.DecodeString(info.Ciphertext)
	if err != nil {
		return nil, errors.New("backup: invalid sealed key ciphertext")
	}
	var encoded string
	switch info.Mode {
	case sealModeKeyring:
		if ring == nil || !ring.Ready() {
			return nil, fmt.Errorf("backup: master key %q is required to unseal archive, but UPMO_MASTER_KEY is not configured", info.KeyID)
		}
		if info.KeyID != "" && !ring.HasKey(info.KeyID) {
			return nil, fmt.Errorf("backup: master key %q is not present in the active keyring (set UPMO_MASTER_KEY_ID or UPMO_MASTER_KEY_PREVIOUS_%s)", info.KeyID, info.KeyID)
		}
		plain, err := ring.Open(string(aad), Sealed{
			Ciphertext: ct,
			Nonce:      nonce,
			KeyID:      info.KeyID,
		})
		if err != nil {
			return nil, fmt.Errorf("backup: cannot unseal archive with master key %q: key mismatch or corrupted header", info.KeyID)
		}
		encoded = plain
	case sealModePassphrase:
		if passphrase == "" {
			return nil, errors.New("backup: archive is passphrase-encrypted; pass --passphrase-env")
		}
		salt, err := base64.StdEncoding.DecodeString(info.Salt)
		if err != nil || len(salt) != scryptSaltLen {
			return nil, errors.New("backup: invalid passphrase salt")
		}
		kek, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, dekLen)
		if err != nil {
			return nil, fmt.Errorf("backup: derive passphrase key: %w", err)
		}
		defer clear(kek)

		block, err := aes.NewCipher(kek)
		if err != nil {
			return nil, fmt.Errorf("backup: passphrase cipher: %w", err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("backup: passphrase gcm: %w", err)
		}
		if len(nonce) != aead.NonceSize() {
			return nil, errors.New("backup: invalid sealed key nonce length")
		}
		plain, err := aead.Open(nil, nonce, ct, aad)
		if err != nil {
			return nil, errors.New("backup: cannot decrypt archive key: wrong passphrase or corrupted header")
		}
		encoded = string(plain)
		clear(plain)
	default:
		return nil, fmt.Errorf("backup: unsupported sealed key mode %q", info.Mode)
	}
	dek, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(dek) != dekLen {
		clear(dek)
		return nil, errors.New("backup: decoded archive key has invalid length")
	}
	return dek, nil
}

func encryptStream(dst io.Writer, src io.Reader, dek, aad []byte) error {
	aead, err := newAESGCM(dek)
	if err != nil {
		return err
	}
	if len(aad) == 0 {
		aad = []byte(backupAADPrefix)
	}
	baseNonce := make([]byte, gcmNonceLen)
	if _, err := rand.Read(baseNonce); err != nil {
		return fmt.Errorf("backup: generate stream nonce: %w", err)
	}
	if _, err := dst.Write([]byte(streamMagic)); err != nil {
		return err
	}
	if _, err := dst.Write(baseNonce); err != nil {
		return err
	}

	br := bufio.NewReaderSize(src, chunkSize)
	buf := make([]byte, chunkSize)
	var idx uint64
	var lenBuf [4]byte
	for {
		n, readErr := io.ReadFull(br, buf)
		isFinal := readErr == io.EOF || readErr == io.ErrUnexpectedEOF
		if readErr != nil && !isFinal {
			return fmt.Errorf("backup: read plaintext stream: %w", readErr)
		}
		nonce := chunkNonce(baseNonce, idx)
		ct := aead.Seal(nil, nonce, buf[:n], chunkAAD(aad, idx, isFinal))
		if len(ct) > chunkSize+aead.Overhead()+1024 {
			return errors.New("backup: encrypted chunk exceeds maximum size")
		}
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ct))) // #nosec G115 -- bounded by chunkSize + GCM tag
		if _, err := dst.Write(lenBuf[:]); err != nil {
			return err
		}
		if _, err := dst.Write(ct); err != nil {
			return err
		}
		idx++
		if isFinal {
			break
		}
	}
	return nil
}

func decryptStream(dst io.Writer, src io.Reader, dek, aad []byte) error {
	aead, err := newAESGCM(dek)
	if err != nil {
		return err
	}
	if len(aad) == 0 {
		aad = []byte(backupAADPrefix)
	}
	var hdr [len(streamMagic) + gcmNonceLen]byte
	if _, err := io.ReadFull(src, hdr[:]); err != nil {
		return fmt.Errorf("backup: read encrypted stream header: %w", err)
	}
	if !bytes.Equal(hdr[:len(streamMagic)], []byte(streamMagic)) {
		return errors.New("backup: invalid encrypted stream magic")
	}
	baseNonce := hdr[len(streamMagic):]
	maxCT := chunkSize + aead.Overhead()
	var lenBuf [4]byte
	var idx uint64
	for {
		if _, err := io.ReadFull(src, lenBuf[:]); err != nil {
			return fmt.Errorf("backup: truncated encrypted stream at chunk %d: %w", idx, err)
		}
		ctLen := int(binary.BigEndian.Uint32(lenBuf[:]))
		if ctLen < aead.Overhead() || ctLen > maxCT {
			return fmt.Errorf("backup: invalid encrypted chunk length %d", ctLen)
		}
		ct := make([]byte, ctLen)
		if _, err := io.ReadFull(src, ct); err != nil {
			return fmt.Errorf("backup: truncated encrypted chunk %d: %w", idx, err)
		}

		// Try non-final first; if this is the final chunk, open with final=true.
		plain, err := aead.Open(nil, chunkNonce(baseNonce, idx), ct, chunkAAD(aad, idx, false))
		isFinal := false
		if err != nil {
			plain, err = aead.Open(nil, chunkNonce(baseNonce, idx), ct, chunkAAD(aad, idx, true))
			if err != nil {
				return fmt.Errorf("backup: decrypt failed at chunk %d: tampered data or wrong key", idx)
			}
			isFinal = true
		}
		if len(plain) > 0 {
			if _, err := dst.Write(plain); err != nil {
				clear(plain)
				return err
			}
		}
		clear(plain)
		idx++
		if isFinal {
			var extra [1]byte
			n, err := src.Read(extra[:])
			if n > 0 || !errors.Is(err, io.EOF) {
				return errors.New("backup: unexpected trailing data after encrypted stream")
			}
			return nil
		}
	}
}

func newAESGCM(dek []byte) (cipher.AEAD, error) {
	if len(dek) != dekLen {
		return nil, fmt.Errorf("backup: invalid data encryption key length %d", len(dek))
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("backup: aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("backup: gcm: %w", err)
	}
	return aead, nil
}

func chunkNonce(baseNonce []byte, idx uint64) []byte {
	out := make([]byte, gcmNonceLen)
	copy(out, baseNonce)
	var tail [8]byte
	binary.BigEndian.PutUint64(tail[:], idx)
	for i := 0; i < 8; i++ {
		out[4+i] ^= tail[i]
	}
	return out
}

func chunkAAD(manifestAAD []byte, idx uint64, isFinal bool) []byte {
	aad := make([]byte, len(manifestAAD)+1+8+1)
	copy(aad, manifestAAD)
	aad[len(manifestAAD)] = 0x00
	binary.BigEndian.PutUint64(aad[len(manifestAAD)+1:], idx)
	if isFinal {
		aad[len(aad)-1] = 1
	}
	return aad
}
