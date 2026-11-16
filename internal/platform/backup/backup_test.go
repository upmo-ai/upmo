package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type stubKeyring struct {
	current string
	keys    map[string][]byte
}

func newStubKeyring(current string, keys map[string][]byte) *stubKeyring {
	return &stubKeyring{current: current, keys: keys}
}

func (s *stubKeyring) Ready() bool           { return s != nil && len(s.keys[s.current]) == 32 }
func (s *stubKeyring) CurrentID() string     { return s.current }
func (s *stubKeyring) HasKey(id string) bool { return len(s.keys[id]) == 32 }

func (s *stubKeyring) Seal(ref, plaintext string) (Sealed, error) {
	raw := s.keys[s.current]
	if len(raw) != 32 {
		return Sealed{}, errors.New("no current key")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return Sealed{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Sealed{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, err
	}
	return Sealed{
		Ciphertext: aead.Seal(nil, nonce, []byte(plaintext), []byte(ref)),
		Nonce:      nonce,
		KeyID:      s.current,
	}, nil
}

func (s *stubKeyring) Open(ref string, sealed Sealed) (string, error) {
	raw := s.keys[sealed.KeyID]
	if len(raw) != 32 {
		return "", errors.New("unknown key")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := aead.Open(nil, sealed.Nonce, sealed.Ciphertext, []byte(ref))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func TestStreamEncryptDecrypt(t *testing.T) {
	dek := bytes.Repeat([]byte{0x42}, dekLen)
	other := bytes.Repeat([]byte{0x99}, dekLen)
	aad := []byte("canonical-manifest-aad-v1")

	cases := []struct {
		name string
		data []byte
	}{
		{name: "empty", data: nil},
		{name: "small", data: []byte("hello upmo backup stream")},
		{name: "multi_chunk", data: bytes.Repeat([]byte("A"), chunkSize+137)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var enc bytes.Buffer
			if err := encryptStream(&enc, bytes.NewReader(tc.data), dek, aad); err != nil {
				t.Fatalf("encryptStream: %v", err)
			}
			var dec bytes.Buffer
			if err := decryptStream(&dec, bytes.NewReader(enc.Bytes()), dek, aad); err != nil {
				t.Fatalf("decryptStream: %v", err)
			}
			if !bytes.Equal(dec.Bytes(), tc.data) {
				t.Fatalf("decrypted length %d, want %d", dec.Len(), len(tc.data))
			}

			// Mismatched AAD must fail.
			var badAAD bytes.Buffer
			if err := decryptStream(&badAAD, bytes.NewReader(enc.Bytes()), dek, []byte("tampered-aad")); err == nil {
				t.Fatal("expected error with mismatched AAD")
			}

			// Wrong key must fail.
			var bad bytes.Buffer
			if err := decryptStream(&bad, bytes.NewReader(enc.Bytes()), other, aad); err == nil {
				t.Fatal("expected error with wrong DEK")
			}

			// Corrupt byte must fail.
			corrupted := append([]byte(nil), enc.Bytes()...)
			corrupted[len(corrupted)-5] ^= 0xff
			var badCorrupt bytes.Buffer
			if err := decryptStream(&badCorrupt, bytes.NewReader(corrupted), dek, aad); err == nil {
				t.Fatal("expected error on corrupted stream")
			}

			// Truncated stream must fail.
			if len(enc.Bytes()) > 20 {
				var badTrunc bytes.Buffer
				if err := decryptStream(&badTrunc, bytes.NewReader(enc.Bytes()[:len(enc.Bytes())-10]), dek, aad); err == nil {
					t.Fatal("expected error on truncated stream")
				}
			}

			// Trailing bytes must fail.
			withTrailing := append(append([]byte(nil), enc.Bytes()...), 0x00)
			var badTrail bytes.Buffer
			if err := decryptStream(&badTrail, bytes.NewReader(withTrailing), dek, aad); err == nil {
				t.Fatal("expected error on trailing bytes")
			}
		})
	}
}

func TestSealOpenDEK(t *testing.T) {
	k1 := bytes.Repeat([]byte{0x11}, 32)
	k2 := bytes.Repeat([]byte{0x22}, 32)
	ring1 := newStubKeyring("k1", map[string][]byte{"k1": k1})
	ringRotated := newStubKeyring("k2", map[string][]byte{"k2": k2, "k1": k1})
	ringWrongID := newStubKeyring("k2", map[string][]byte{"k2": k2})
	ringWrongBytes := newStubKeyring("k1", map[string][]byte{"k1": k2})

	aad := []byte("manifest-aad-1")
	dek := bytes.Repeat([]byte{0x55}, dekLen)
	sealed, err := sealDEK(dek, ring1, "", aad)
	if err != nil {
		t.Fatalf("sealDEK keyring: %v", err)
	}
	if sealed.KeyID != "k1" || sealed.Mode != sealModeKeyring {
		t.Fatalf("unexpected sealed header: %+v", sealed)
	}

	got, err := openDEK(sealed, ringRotated, "", aad)
	if err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("openDEK rotated: got %x, err %v", got, err)
	}
	if _, err := openDEK(sealed, ringRotated, "", []byte("tampered-aad")); err == nil {
		t.Fatal("expected error when AAD differs in keyring mode")
	}

	if _, err := openDEK(sealed, ringWrongID, "", aad); err == nil || !strings.Contains(err.Error(), "k1") {
		t.Fatalf("expected missing k1 error, got %v", err)
	}
	if _, err := openDEK(sealed, ringWrongBytes, "", aad); err == nil {
		t.Fatal("expected error when key bytes differ")
	}

	// Passphrase mode.
	passSealed, err := sealDEK(dek, nil, "correct-horse-battery", aad)
	if err != nil {
		t.Fatalf("sealDEK passphrase: %v", err)
	}
	gotPass, err := openDEK(passSealed, nil, "correct-horse-battery", aad)
	if err != nil || !bytes.Equal(gotPass, dek) {
		t.Fatalf("openDEK passphrase: err=%v", err)
	}
	if _, err := openDEK(passSealed, nil, "correct-horse-battery", []byte("tampered-aad")); err == nil {
		t.Fatal("expected error when AAD differs in passphrase mode")
	}
	if _, err := openDEK(passSealed, nil, "wrong-passphrase", aad); err == nil {
		t.Fatal("expected error on wrong passphrase")
	}
	if _, err := openDEK(passSealed, nil, "", aad); err == nil {
		t.Fatal("expected error when passphrase is empty")
	}
}

func TestArchiveAndRestoreVolumeAndSymlinkRoot(t *testing.T) {
	tmp := t.TempDir()
	realArt := filepath.Join(tmp, "real-artifacts")
	if err := os.MkdirAll(filepath.Join(realArt, "task-1", "empty-sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	fileA := filepath.Join(realArt, "task-1", "report.txt")
	if err := os.WriteFile(fileA, []byte("artifact body"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Symlinked volume root must resolve cleanly via EvalSymlinks.
	srcArtLink := filepath.Join(tmp, "src-artifacts-link")
	if err := os.Symlink(realArt, srcArtLink); err != nil {
		t.Fatal(err)
	}

	tarPath := filepath.Join(tmp, "payload.tar.gz")
	f, err := os.Create(tarPath) //nolint:gosec // test temp path
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	var entries []EntryChecksum
	count, err := archiveVolume(tw, &entries, artifactsVolumePrefix, srcArtLink)
	if err != nil {
		t.Fatalf("archiveVolume(symlink root): %v", err)
	}
	if count != 1 || len(entries) != 1 {
		t.Fatalf("count=%d len(entries)=%d, want 1", count, len(entries))
	}
	if entries[0].SHA256 != sha256Hex([]byte("artifact body")) || entries[0].Size != int64(len("artifact body")) {
		t.Fatalf("single-pass hash mismatch: %+v", entries[0])
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	dstArt := filepath.Join(tmp, "dst-artifacts")
	if err := os.MkdirAll(dstArt, 0o750); err != nil {
		t.Fatal(err)
	}
	staleFile := filepath.Join(dstArt, "stale.txt")
	if err := os.WriteFile(staleFile, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	restoredCount, err := restoreVolumes(dstArt, "", tarPath, true, false, nil)
	if err != nil {
		t.Fatalf("restoreVolumes: %v", err)
	}
	if restoredCount != 1 {
		t.Fatalf("restoredCount=%d, want 1", restoredCount)
	}
	if _, err := os.Stat(staleFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale file should be gone, err=%v", err)
	}
	gotBody, err := os.ReadFile(filepath.Join(dstArt, "task-1", "report.txt")) //nolint:gosec // test temp path
	if err != nil || string(gotBody) != "artifact body" {
		t.Fatalf("restored body=%q err=%v", gotBody, err)
	}
	if info, err := os.Stat(filepath.Join(dstArt, "task-1", "empty-sub")); err != nil || !info.IsDir() {
		t.Fatalf("empty subdir missing: err=%v", err)
	}

	// Non-existent configured volume dir must return a clear error.
	var sinkMissing bytes.Buffer
	gzMissing := gzip.NewWriter(&sinkMissing)
	twMissing := tar.NewWriter(gzMissing)
	var eMissing []EntryChecksum
	if _, err := archiveVolume(twMissing, &eMissing, artifactsVolumePrefix, filepath.Join(tmp, "does-not-exist")); err == nil {
		t.Fatal("expected error for non-existent configured volume directory")
	}

	// Missing target dir when archive has files must fail.
	if _, err := restoreVolumes("", "", tarPath, true, false, nil); err == nil {
		t.Fatal("expected error when ArtifactsDir is empty but archive has files")
	}

	// Symlink inside source volume must be rejected.
	if err := os.Symlink(fileA, filepath.Join(realArt, "escape")); err != nil {
		t.Fatal(err)
	}
	var sink bytes.Buffer
	gz2 := gzip.NewWriter(&sink)
	tw2 := tar.NewWriter(gz2)
	var e2 []EntryChecksum
	if _, err := archiveVolume(tw2, &e2, artifactsVolumePrefix, srcArtLink); err == nil {
		t.Fatal("expected symlink inside volume to be rejected")
	}
}

func TestRestoreVolumesRollbackOnSecondVolumeFailure(t *testing.T) {
	tmp := t.TempDir()
	srcArt := filepath.Join(tmp, "src-art")
	srcKnow := filepath.Join(tmp, "src-know")
	if err := os.MkdirAll(srcArt, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(srcKnow, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcArt, "new-art.txt"), []byte("new-art"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcKnow, "new-know.txt"), []byte("new-know"), 0o600); err != nil {
		t.Fatal(err)
	}

	tarPath := filepath.Join(tmp, "payload.tar.gz")
	f, err := os.Create(tarPath) //nolint:gosec // test temp path
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	var entries []EntryChecksum
	if _, err := archiveVolume(tw, &entries, artifactsVolumePrefix, srcArt); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveVolume(tw, &entries, knowledgeVolumePrefix, srcKnow); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	dstArt := filepath.Join(tmp, "dst-art")
	dstKnow := filepath.Join(tmp, "dst-know")
	if err := os.MkdirAll(dstArt, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstKnow, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstArt, "orig-art.txt"), []byte("orig-art"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstKnow, "orig-know.txt"), []byte("orig-know"), 0o600); err != nil {
		t.Fatal(err)
	}

	injectedErr := errors.New("simulated second-volume rename failure")
	failOnSecondKnow := func(oldpath, newpath string) error {
		if strings.Contains(oldpath, "dst-know") && strings.HasSuffix(oldpath, "new-know.txt") {
			return injectedErr
		}
		return os.Rename(oldpath, newpath)
	}

	if _, err := restoreVolumes(dstArt, dstKnow, tarPath, true, true, failOnSecondKnow); !errors.Is(err, injectedErr) {
		t.Fatalf("expected injected error, got %v", err)
	}

	// Both live directories must retain their original contents and contain none of the new files.
	if got, err := os.ReadFile(filepath.Join(dstArt, "orig-art.txt")); err != nil || string(got) != "orig-art" { //nolint:gosec // test temp path
		t.Fatalf("first volume was not rolled back: got=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dstArt, "new-art.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new-art.txt survived rollback: err=%v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dstKnow, "orig-know.txt")); err != nil || string(got) != "orig-know" { //nolint:gosec // test temp path
		t.Fatalf("second volume was not rolled back: got=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dstKnow, "new-know.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new-know.txt survived rollback: err=%v", err)
	}

	// Staged files must NOT have been deleted on rename failure.
	artEntries, err := os.ReadDir(dstArt)
	if err != nil {
		t.Fatal(err)
	}
	var stageDir string
	for _, e := range artEntries {
		if strings.HasPrefix(e.Name(), stagingPrefix+"stage-") {
			stageDir = filepath.Join(dstArt, e.Name())
		}
	}
	if stageDir == "" {
		t.Fatal("expected staged directory to be preserved on rename failure")
	}
	if _, err := os.Stat(filepath.Join(stageDir, "new-art.txt")); err != nil {
		t.Fatalf("staged file was deleted on rename failure: %v", err)
	}
}

func TestVerifyAndStageRejectsTamperingAndTraversal(t *testing.T) {
	tmp := t.TempDir()
	innerPath := filepath.Join(tmp, "inner.tar.gz")
	meta := DBMeta{
		Schemas:        []string{"audit"},
		SchemaVersions: map[string]int64{"audit": 1},
		Tables:         []TableMeta{{Schema: "audit", Name: "head", Kind: "r", Columns: []string{"one"}}},
	}
	metaBytes, _ := json.Marshal(meta)
	tableBytes := []byte("t\n")
	metaSum := sha256Hex(metaBytes)
	tableSum := sha256Hex(tableBytes)
	pmBytes, _ := json.Marshal(PayloadManifest{
		Entries: []EntryChecksum{
			{Path: dbMetaEntryName, SHA256: metaSum, Size: int64(len(metaBytes))},
			{Path: dbTablesPrefix + "audit.head.copy", SHA256: tableSum, Size: int64(len(tableBytes))},
		},
	})

	writeTestTarGz(t, innerPath, []testTarEntry{
		{name: dbMetaEntryName, body: metaBytes, typeflag: tar.TypeReg},
		{name: dbTablesPrefix + "audit.head.copy", body: tableBytes, typeflag: tar.TypeReg},
		{name: payloadManifestEntryName, body: pmBytes, typeflag: tar.TypeReg},
	})

	innerRaw, err := os.ReadFile(innerPath) //nolint:gosec // test temp path
	if err != nil {
		t.Fatal(err)
	}
	plainSHA := sha256Hex(innerRaw)

	// Build an encrypted archive with manifest AAD binding.
	ring := newStubKeyring("k1", map[string][]byte{"k1": bytes.Repeat([]byte{1}, 32)})
	dek := bytes.Repeat([]byte{0x77}, dekLen)
	encManifest := Manifest{
		FormatVersion:  FormatVersion,
		MasterKeyID:    "k1",
		Encrypted:      true,
		PayloadSHA256:  plainSHA,
		SchemaVersions: map[string]int64{"audit": 1},
	}
	aad := manifestAAD(encManifest)
	sealedKey, err := sealDEK(dek, ring, "", aad)
	if err != nil {
		t.Fatal(err)
	}
	encManifest.SealedKey = sealedKey
	encPath := filepath.Join(tmp, "inner.tar.gz.enc")
	if err := encryptFile(encPath, innerPath, dek, aad); err != nil {
		t.Fatal(err)
	}
	encSHA, encSize, err := hashFile(encPath)
	if err != nil {
		t.Fatal(err)
	}
	encManifest.Entries = []EntryChecksum{{Path: payloadEncEntryName, SHA256: encSHA, Size: encSize}}
	encManifestBytes, _ := json.Marshal(encManifest)
	encOuter := filepath.Join(tmp, "enc-backup.tar.gz")
	if err := writeOuterArchiveAtomic(encOuter, encManifestBytes, payloadEncEntryName, encPath, encSize); err != nil {
		t.Fatalf("writeOuterArchiveAtomic(enc): %v", err)
	}

	fi, err := os.Stat(encOuter)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("archive perm = %o, want 0600", fi.Mode().Perm())
	}

	supported := map[string]int64{"audit": 2}
	if _, err := verifyAndStageArchive(encOuter, t.TempDir(), RestoreOptions{Keyring: ring, SupportedVersions: supported}); err != nil {
		t.Fatalf("valid encrypted archive failed verification: %v", err)
	}

	// Tampered schema_versions in manifest.json -> AEAD rejects before decrypting/mutating.
	tamperedVer := encManifest
	tamperedVer.SchemaVersions = map[string]int64{"audit": 2}
	tamperedVerBytes, _ := json.Marshal(tamperedVer)
	tamperedVerOuter := filepath.Join(tmp, "tampered-ver.tar.gz")
	if err := writeOuterArchiveAtomic(tamperedVerOuter, tamperedVerBytes, payloadEncEntryName, encPath, encSize); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyAndStageArchive(tamperedVerOuter, t.TempDir(), RestoreOptions{Keyring: ring, SupportedVersions: supported}); err == nil {
		t.Fatal("expected tampered schema_versions in manifest.json to fail AEAD verification")
	}

	// Tampered master_key_id in manifest.json -> rejected.
	tamperedKey := encManifest
	tamperedKey.MasterKeyID = "k2"
	tamperedKeyBytes, _ := json.Marshal(tamperedKey)
	tamperedKeyOuter := filepath.Join(tmp, "tampered-key.tar.gz")
	if err := writeOuterArchiveAtomic(tamperedKeyOuter, tamperedKeyBytes, payloadEncEntryName, encPath, encSize); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyAndStageArchive(tamperedKeyOuter, t.TempDir(), RestoreOptions{Keyring: ring, SupportedVersions: supported}); err == nil {
		t.Fatal("expected tampered master_key_id in manifest.json to be rejected")
	}

	// Tampered encrypted: false on encrypted archive -> rejected.
	tamperedPlain := encManifest
	tamperedPlain.Encrypted = false
	tamperedPlainBytes, _ := json.Marshal(tamperedPlain)
	tamperedPlainOuter := filepath.Join(tmp, "tampered-plain.tar.gz")
	if err := writeOuterArchiveAtomic(tamperedPlainOuter, tamperedPlainBytes, payloadEncEntryName, encPath, encSize); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyAndStageArchive(tamperedPlainOuter, t.TempDir(), RestoreOptions{Keyring: ring, SupportedVersions: supported}); err == nil {
		t.Fatal("expected tampered encrypted=false in manifest.json to be rejected")
	}

	// Unencrypted archive + --require-encrypted -> rejected.
	outerPath := filepath.Join(tmp, "backup.tar.gz")
	manifestBytes, _ := json.Marshal(Manifest{
		FormatVersion:  FormatVersion,
		Encrypted:      false,
		PayloadSHA256:  plainSHA,
		SchemaVersions: map[string]int64{"audit": 1},
		Entries: []EntryChecksum{{
			Path:   payloadPlainEntryName,
			SHA256: plainSHA,
			Size:   int64(len(innerRaw)),
		}},
	})
	if err := writeOuterArchiveAtomic(outerPath, manifestBytes, payloadPlainEntryName, innerPath, int64(len(innerRaw))); err != nil {
		t.Fatalf("writeOuterArchiveAtomic: %v", err)
	}
	if _, err := verifyAndStageArchive(outerPath, t.TempDir(), RestoreOptions{
		RequireEncrypted:  true,
		SupportedVersions: supported,
	}); err == nil {
		t.Fatal("expected --require-encrypted to reject unencrypted archive")
	}

	// Future schema_version above binary head -> rejected before touching anything.
	if _, err := verifyAndStageArchive(encOuter, t.TempDir(), RestoreOptions{
		Keyring:           ring,
		SupportedVersions: map[string]int64{"audit": 0},
	}); err == nil {
		t.Fatal("expected future schema version above supported version to be rejected")
	}

	// Corrupt checksum in outer manifest -> fail.
	badManifestBytes, _ := json.Marshal(Manifest{
		FormatVersion:  FormatVersion,
		MasterKeyID:    "k1",
		Encrypted:      false,
		SchemaVersions: map[string]int64{"audit": 1},
		Entries: []EntryChecksum{{
			Path:   payloadPlainEntryName,
			SHA256: strings.Repeat("0", 64),
			Size:   int64(len(innerRaw)),
		}},
	})
	badOuter := filepath.Join(tmp, "bad-checksum.tar.gz")
	if err := writeOuterArchiveAtomic(badOuter, badManifestBytes, payloadPlainEntryName, innerPath, int64(len(innerRaw))); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyAndStageArchive(badOuter, t.TempDir(), RestoreOptions{Keyring: ring, SupportedVersions: supported}); err == nil {
		t.Fatal("expected outer checksum mismatch error")
	}

	// Path traversal entry in inner payload -> fail.
	travInner := filepath.Join(tmp, "trav-inner.tar.gz")
	writeTestTarGz(t, travInner, []testTarEntry{
		{name: "../escape.txt", body: []byte("x"), typeflag: tar.TypeReg},
	})
	travRaw, _ := os.ReadFile(travInner) //nolint:gosec // test temp path
	travManifest, _ := json.Marshal(Manifest{
		FormatVersion: FormatVersion,
		Entries:       []EntryChecksum{{Path: payloadPlainEntryName, SHA256: sha256Hex(travRaw), Size: int64(len(travRaw))}},
	})
	travOuter := filepath.Join(tmp, "trav.tar.gz")
	if err := writeOuterArchiveAtomic(travOuter, travManifest, payloadPlainEntryName, travInner, int64(len(travRaw))); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyAndStageArchive(travOuter, t.TempDir(), RestoreOptions{SupportedVersions: supported}); err == nil {
		t.Fatal("expected traversal path error")
	}
}

func TestTopoSortTablesAndPartitionBounds(t *testing.T) {
	tables := []TableMeta{
		{Schema: "tasks", Name: "task_events"},
		{Schema: "tasks", Name: "tasks"},
		{Schema: "identity", Name: "users"},
		{Schema: "identity", Name: "roles"},
	}
	fks := []ForeignKeyMeta{
		{FromSchema: "identity", FromTable: "users", ToSchema: "identity", ToTable: "roles"},
		{FromSchema: "tasks", FromTable: "task_events", ToSchema: "tasks", ToTable: "tasks"},
		{FromSchema: "tasks", FromTable: "tasks", ToSchema: "tasks", ToTable: "tasks"}, // self-FK
	}
	sorted := topoSortTables(tables, fks)
	names := make([]string, len(sorted))
	for i, tbl := range sorted {
		names[i] = tbl.Schema + "." + tbl.Name
	}
	if slices.Index(names, "identity.roles") > slices.Index(names, "identity.users") {
		t.Fatalf("roles must precede users: %v", names)
	}
	if slices.Index(names, "tasks.tasks") > slices.Index(names, "tasks.task_events") {
		t.Fatalf("tasks must precede task_events: %v", names)
	}

	if got := validatedPartitionBound("DEFAULT"); got != "DEFAULT" {
		t.Fatalf("DEFAULT got %q", got)
	}
	goodRange := "FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00')"
	if got := validatedPartitionBound(goodRange); got != goodRange {
		t.Fatalf("range got %q, want %q", got, goodRange)
	}
	if got := validatedPartitionBound("FOR VALUES FROM ('2026-11-01'); DROP TABLE audit.events; --') TO ('2026-12-01')"); got != "" {
		t.Fatalf("expected SQL injection bound to be rejected, got %q", got)
	}
}

type testTarEntry struct {
	name     string
	body     []byte
	typeflag byte
}

func writeTestTarGz(t *testing.T, path string, entries []testTarEntry) {
	t.Helper()
	f, err := os.Create(path) //nolint:gosec // test temp path
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Size:     int64(len(e.body)),
			Mode:     0o600,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
