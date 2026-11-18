package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/buildinfo"
)

// BackupOptions configures a backup archive creation run.
type BackupOptions struct {
	OutPath      string
	TempDir      string
	ArtifactsDir string
	KnowledgeDir string
	Keyring      Keyring
	Passphrase   string
	NoEncrypt    bool
}

// BackupResult summarizes a completed backup archive.
type BackupResult struct {
	OutPath        string
	MasterKeyID    string
	Encrypted      bool
	TablesDumped   int
	VolumeFiles    int
	SchemaVersions map[string]int64
}

// RestoreOptions configures a backup restore run.
type RestoreOptions struct {
	InPath            string
	TempDir           string
	ArtifactsDir      string
	KnowledgeDir      string
	Keyring           Keyring
	Passphrase        string
	RequireEncrypted  bool
	SupportedVersions map[string]int64
	PrepareSchemas    PrepareSchemasFunc
	MigrateForward    MigrateForwardFunc
}

// RestoreResult summarizes a completed restore and audit verification.
type RestoreResult struct {
	TablesRestored      int
	VolumeFilesRestored int
	// VolumesUnchanged holds the archive prefix name of each configured volume
	// left untouched because the archive has no entries for it.
	VolumesUnchanged  []string
	AuditHeadPosition int64
	AuditChainValid   bool
	MasterKeyID       string
}

// Backup exports a consistent read-only Postgres snapshot and the configured
// artifact and knowledge volumes into a verified atomic archive at opts.OutPath.
func Backup(ctx context.Context, pool *pgxpool.Pool, opts BackupOptions) (BackupResult, error) {
	if pool == nil {
		return BackupResult{}, errors.New("backup: database pool is required")
	}
	outPath := strings.TrimSpace(opts.OutPath)
	if outPath == "" {
		return BackupResult{}, errors.New("backup: --out path is required")
	}
	if opts.NoEncrypt && opts.Passphrase != "" {
		return BackupResult{}, errors.New("backup: --no-encrypt cannot be combined with a passphrase")
	}
	encrypt := !opts.NoEncrypt
	if encrypt && opts.Passphrase == "" && (opts.Keyring == nil || !opts.Keyring.Ready()) {
		return BackupResult{}, errors.New("backup: encryption requires UPMO_MASTER_KEY, --passphrase-env, or --no-encrypt")
	}
	if err := checkPrivileges(ctx, pool); err != nil {
		return BackupResult{}, err
	}

	tmpDir, err := createBackupTempDir(outPath, opts.TempDir)
	if err != nil {
		return BackupResult{}, err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	payloadPlainPath := filepath.Join(tmpDir, payloadPlainEntryName)
	meta, versions, innerCount, plainPayloadSHA, err := buildPayloadTarGz(ctx, pool, opts, tmpDir, payloadPlainPath)
	if err != nil {
		return BackupResult{}, err
	}

	masterKeyID := ""
	if encrypt && opts.Passphrase == "" && opts.Keyring != nil && opts.Keyring.Ready() {
		masterKeyID = opts.Keyring.CurrentID()
	}

	info := buildinfo.Info()
	manifest := Manifest{
		FormatVersion:  FormatVersion,
		BuildVersion:   info.Version,
		Commit:         info.Commit,
		BuildTime:      info.BuildTime,
		CreatedAt:      time.Now().UTC(),
		MasterKeyID:    masterKeyID,
		Encrypted:      encrypt,
		PayloadSHA256:  plainPayloadSHA,
		SchemaVersions: versions,
	}

	var (
		payloadEntryName = payloadPlainEntryName
		payloadSrcPath   = payloadPlainPath
	)
	if encrypt {
		dek := make([]byte, dekLen)
		if _, err := rand.Read(dek); err != nil {
			return BackupResult{}, fmt.Errorf("backup: generate archive key: %w", err)
		}
		defer clear(dek)

		aad := manifestAAD(manifest)
		sealedKey, err := sealDEK(dek, opts.Keyring, opts.Passphrase, aad)
		if err != nil {
			return BackupResult{}, err
		}
		manifest.SealedKey = sealedKey

		payloadEncPath := filepath.Join(tmpDir, payloadEncEntryName)
		if err := encryptFile(payloadEncPath, payloadPlainPath, dek, aad); err != nil {
			return BackupResult{}, err
		}
		payloadEntryName = payloadEncEntryName
		payloadSrcPath = payloadEncPath
	}

	payloadSHA, payloadSize, err := hashFile(payloadSrcPath)
	if err != nil {
		return BackupResult{}, err
	}
	manifest.Entries = []EntryChecksum{{
		Path:   payloadEntryName,
		SHA256: payloadSHA,
		Size:   payloadSize,
	}}

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return BackupResult{}, fmt.Errorf("backup: marshal manifest: %w", err)
	}

	if err := writeOuterArchiveAtomic(outPath, manifestBytes, payloadEntryName, payloadSrcPath, payloadSize); err != nil {
		return BackupResult{}, err
	}
	return BackupResult{
		OutPath:        outPath,
		MasterKeyID:    masterKeyID,
		Encrypted:      encrypt,
		TablesDumped:   len(meta.Tables),
		VolumeFiles:    innerCount,
		SchemaVersions: versions,
	}, nil
}

func createBackupTempDir(outPath, configuredTempDir string) (string, error) {
	base := strings.TrimSpace(configuredTempDir)
	if base == "" {
		base = filepath.Dir(outPath)
	}
	if err := os.MkdirAll(base, 0o750); err != nil {
		return "", fmt.Errorf("backup: create temp base dir %q: %w", base, err)
	}
	tmpDir, err := os.MkdirTemp(base, ".upmo-backup-*")
	if err != nil {
		return "", fmt.Errorf("backup: create temp dir: %w", err)
	}
	return tmpDir, nil
}

func createRestoreTempDir(inPath, configuredTempDir string) (string, error) {
	if base := strings.TrimSpace(configuredTempDir); base != "" {
		if err := os.MkdirAll(base, 0o750); err != nil {
			return "", fmt.Errorf("backup: create temp base dir %q: %w", base, err)
		}
		tmpDir, err := os.MkdirTemp(base, ".upmo-restore-*")
		if err != nil {
			return "", fmt.Errorf("backup: create temp dir: %w", err)
		}
		return tmpDir, nil
	}
	// Prefer staging alongside the input archive to avoid small /tmp filesystems;
	// fall back to os.TempDir if the input directory is read-only.
	if tmpDir, err := os.MkdirTemp(filepath.Dir(inPath), ".upmo-restore-*"); err == nil {
		return tmpDir, nil
	}
	tmpDir, err := os.MkdirTemp("", "upmo-restore-*")
	if err != nil {
		return "", fmt.Errorf("backup: create temp dir: %w", err)
	}
	return tmpDir, nil
}

func buildPayloadTarGz(ctx context.Context, pool *pgxpool.Pool, opts BackupOptions, tmpDir, outPath string) (DBMeta, map[string]int64, int, string, error) {
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // temp file created in private tmpDir
	if err != nil {
		return DBMeta{}, nil, 0, "", fmt.Errorf("backup: create payload file: %w", err)
	}
	cleanup := true
	defer func() {
		_ = f.Close()
		if cleanup {
			_ = os.Remove(outPath)
		}
	}()

	h := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(f, h))
	tw := tar.NewWriter(gz)

	var entries []EntryChecksum
	meta, versions, err := dumpDB(ctx, pool, tw, &entries, tmpDir)
	if err != nil {
		return DBMeta{}, nil, 0, "", err
	}
	artCount, err := archiveVolume(tw, &entries, artifactsVolumePrefix, opts.ArtifactsDir)
	if err != nil {
		return DBMeta{}, nil, 0, "", err
	}
	knowCount, err := archiveVolume(tw, &entries, knowledgeVolumePrefix, opts.KnowledgeDir)
	if err != nil {
		return DBMeta{}, nil, 0, "", err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	pmBytes, err := json.MarshalIndent(PayloadManifest{Entries: entries}, "", "  ")
	if err != nil {
		return DBMeta{}, nil, 0, "", fmt.Errorf("backup: marshal payload manifest: %w", err)
	}
	if _, _, err := writeBytesToTar(tw, payloadManifestEntryName, pmBytes); err != nil {
		return DBMeta{}, nil, 0, "", err
	}

	if err := tw.Close(); err != nil {
		return DBMeta{}, nil, 0, "", fmt.Errorf("backup: close payload tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return DBMeta{}, nil, 0, "", fmt.Errorf("backup: close payload gzip: %w", err)
	}
	if err := f.Close(); err != nil {
		return DBMeta{}, nil, 0, "", fmt.Errorf("backup: close payload file: %w", err)
	}
	cleanup = false
	return meta, versions, artCount + knowCount, hex.EncodeToString(h.Sum(nil)), nil
}

func encryptFile(dstPath, srcPath string, dek, aad []byte) error {
	src, err := os.Open(srcPath) //nolint:gosec // temp file in private tmpDir
	if err != nil {
		return fmt.Errorf("backup: open plaintext payload: %w", err)
	}
	defer func() { _ = src.Close() }()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // temp file in private tmpDir
	if err != nil {
		return fmt.Errorf("backup: create encrypted payload: %w", err)
	}
	cleanup := true
	defer func() {
		_ = dst.Close()
		if cleanup {
			_ = os.Remove(dstPath)
		}
	}()

	if err := encryptStream(dst, src, dek, aad); err != nil {
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func decryptFile(dstPath, srcPath string, dek, aad []byte) error {
	src, err := os.Open(srcPath) //nolint:gosec // temp file in private tmpDir
	if err != nil {
		return fmt.Errorf("backup: open encrypted payload: %w", err)
	}
	defer func() { _ = src.Close() }()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // temp file in private tmpDir
	if err != nil {
		return fmt.Errorf("backup: create decrypted payload: %w", err)
	}
	cleanup := true
	defer func() {
		_ = dst.Close()
		if cleanup {
			_ = os.Remove(dstPath)
		}
	}()

	if err := decryptStream(dst, src, dek, aad); err != nil {
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func hashFile(filePath string) (string, int64, error) {
	f, err := os.Open(filePath) //nolint:gosec // private temp file
	if err != nil {
		return "", 0, fmt.Errorf("backup: open file for hashing: %w", err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("backup: hash file: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func writeOuterArchiveAtomic(outPath string, manifestBytes []byte, payloadName, payloadPath string, payloadSize int64) error {
	parent := filepath.Dir(outPath)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return fmt.Errorf("backup: create output directory: %w", err)
	}
	tmp, err := os.CreateTemp(parent, ".backup-*.tmp")
	if err != nil {
		return fmt.Errorf("backup: create temp output archive: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)

	if _, _, err := writeBytesToTar(tw, manifestEntryName, manifestBytes); err != nil {
		return err
	}

	pf, err := os.Open(payloadPath) //nolint:gosec // private temp file
	if err != nil {
		return fmt.Errorf("backup: open payload for outer archive: %w", err)
	}
	defer func() { _ = pf.Close() }()

	if err := tw.WriteHeader(&tar.Header{
		Name:     payloadName,
		Typeflag: tar.TypeReg,
		Size:     payloadSize,
		Mode:     0o600,
	}); err != nil {
		return fmt.Errorf("backup: write payload tar header: %w", err)
	}
	if _, err := io.CopyN(tw, pf, payloadSize); err != nil {
		return fmt.Errorf("backup: write payload to outer archive: %w", err)
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("backup: close outer tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("backup: close outer gzip: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("backup: sync temp archive: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("backup: chmod temp archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("backup: close temp archive: %w", err)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		return fmt.Errorf("backup: rename archive to %s: %w", outPath, err)
	}
	cleanup = false
	return nil
}

// Restore verifies the archive structure, checksums, schema versions, and key
// compatibility before mutating Postgres or volume directories, stages both
// volumes, rehearses the database restore (including forward migrations) in a
// throwaway staging database, then restores database state and swaps both
// volumes atomically. Volumes the archive has no entries for are left
// untouched. A failure in the staging rehearsal or before any live change
// leaves the database and volumes as they were; a live failure returns an
// empty target to empty, and leaves a different-head target with no restored
// data (after its schemas are dropped) and its volumes untouched; after the
// live data commits, a migration failure swaps the staged volumes in and the
// error says to run `brain migrate` before starting brain.
func Restore(ctx context.Context, pool *pgxpool.Pool, opts RestoreOptions) (RestoreResult, error) {
	if pool == nil {
		return RestoreResult{}, errors.New("backup: database pool is required")
	}
	inPath := strings.TrimSpace(opts.InPath)
	if inPath == "" {
		return RestoreResult{}, errors.New("backup: --in path is required")
	}
	if err := checkPrivileges(ctx, pool); err != nil {
		return RestoreResult{}, err
	}

	tmpDir, err := createRestoreTempDir(inPath, opts.TempDir)
	if err != nil {
		return RestoreResult{}, err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	verified, err := verifyAndStageArchive(inPath, tmpDir, opts)
	if err != nil {
		return RestoreResult{}, err
	}
	if len(verified.manifest.SchemaVersions) > 0 && opts.SupportedVersions == nil {
		return RestoreResult{}, errors.New("backup: SupportedVersions map is required for restore")
	}

	stagedVols, unchanged, filesRestored, err := stageVolumes(
		opts.ArtifactsDir,
		opts.KnowledgeDir,
		verified.payloadTarPath,
		verified.hasArtifacts,
		verified.hasKnowledge,
	)
	if err != nil {
		return RestoreResult{}, err
	}

	tablesRestored, headPos, err := restoreDB(
		ctx,
		pool,
		verified.meta,
		verified.manifest,
		verified.payloadTarPath,
		verified.tableSizes,
		opts.PrepareSchemas,
		opts.MigrateForward,
	)
	if err != nil {
		if errors.Is(err, errMigrateAfterCommit) {
			// The archive data is committed; database and files must stay in step,
			// so swap the staged volumes in instead of dropping them.
			if swapErr := commitVolumeSwaps(stagedVols, nil); swapErr != nil {
				return RestoreResult{}, fmt.Errorf("backup: %w (swapping restored volumes in also failed: %v)", err, swapErr)
			}
			return RestoreResult{}, err
		}
		cleanupStagedVolumes(stagedVols)
		return RestoreResult{}, err
	}

	if err := commitVolumeSwaps(stagedVols, nil); err != nil {
		return RestoreResult{}, err
	}

	return RestoreResult{
		TablesRestored:      tablesRestored,
		VolumeFilesRestored: filesRestored,
		VolumesUnchanged:    unchanged,
		AuditHeadPosition:   headPos,
		AuditChainValid:     true,
		MasterKeyID:         verified.manifest.MasterKeyID,
	}, nil
}

type verifiedArchive struct {
	manifest       Manifest
	meta           DBMeta
	payloadTarPath string
	tableSizes     map[string]int64
	hasArtifacts   bool
	hasKnowledge   bool
}

func verifyAndStageArchive(inPath, tmpDir string, opts RestoreOptions) (verifiedArchive, error) {
	f, err := os.Open(inPath) //nolint:gosec // CLI operator input path
	if err != nil {
		return verifiedArchive{}, fmt.Errorf("backup: open archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return verifiedArchive{}, fmt.Errorf("backup: read archive gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	outerPayloadPath := filepath.Join(tmpDir, "outer-payload")

	var (
		hasManifest    bool
		manifest       Manifest
		payloadEntry   string
		payloadSHA     string
		payloadSize    int64
		hasPayloadFile bool
	)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return verifiedArchive{}, fmt.Errorf("backup: read archive tar: %w", err)
		}
		clean, err := cleanArchivePath(hdr.Name)
		if err != nil {
			return verifiedArchive{}, err
		}
		if hdr.Typeflag != tar.TypeReg {
			return verifiedArchive{}, fmt.Errorf("backup: unexpected non-regular entry %q in outer archive", clean)
		}
		switch clean {
		case manifestEntryName:
			if hasManifest {
				return verifiedArchive{}, fmt.Errorf("backup: duplicate %s in archive", manifestEntryName)
			}
			manifest, err = readManifest(tr, hdr.Size)
			if err != nil {
				return verifiedArchive{}, err
			}
			hasManifest = true
		case payloadEncEntryName, payloadPlainEntryName:
			if hasPayloadFile {
				return verifiedArchive{}, errors.New("backup: duplicate payload entry in archive")
			}
			if hdr.Size < 0 {
				return verifiedArchive{}, fmt.Errorf("backup: negative payload size %d", hdr.Size)
			}
			sha, n, err := writeStreamToTempFile(outerPayloadPath, tr, hdr.Size)
			if err != nil {
				return verifiedArchive{}, err
			}
			payloadEntry = clean
			payloadSHA = sha
			payloadSize = n
			hasPayloadFile = true
		default:
			return verifiedArchive{}, fmt.Errorf("backup: unexpected entry %q in archive", clean)
		}
	}
	if err := drainGzipEOF(gz); err != nil {
		return verifiedArchive{}, err
	}

	if !hasManifest {
		return verifiedArchive{}, fmt.Errorf("backup: %s missing from archive", manifestEntryName)
	}
	if manifest.FormatVersion != FormatVersion {
		return verifiedArchive{}, fmt.Errorf("backup: unsupported format_version %d", manifest.FormatVersion)
	}
	if opts.RequireEncrypted && !manifest.Encrypted {
		return verifiedArchive{}, errors.New("backup: archive is not encrypted (--require-encrypted)")
	}
	if !manifest.Encrypted && manifest.SealedKey != nil {
		return verifiedArchive{}, errors.New("backup: unencrypted archive unexpectedly carries sealed_key")
	}
	if !hasPayloadFile {
		return verifiedArchive{}, errors.New("backup: payload missing from archive")
	}
	wantPayload := payloadPlainEntryName
	if manifest.Encrypted {
		wantPayload = payloadEncEntryName
	}
	if payloadEntry != wantPayload {
		return verifiedArchive{}, fmt.Errorf("backup: manifest expects %s, archive holds %s", wantPayload, payloadEntry)
	}
	if len(manifest.Entries) != 1 ||
		manifest.Entries[0].Path != payloadEntry ||
		manifest.Entries[0].Size != payloadSize ||
		manifest.Entries[0].SHA256 != payloadSHA {
		return verifiedArchive{}, errors.New("backup: archive checksum verification failed")
	}

	if err := validateManifestSchemaVersions(manifest.SchemaVersions, opts.SupportedVersions); err != nil {
		return verifiedArchive{}, err
	}
	if err := checkMasterKeyIDs([]string{manifest.MasterKeyID}, opts.Keyring); err != nil {
		return verifiedArchive{}, err
	}

	payloadTarPath := outerPayloadPath
	if manifest.Encrypted {
		aad := manifestAAD(manifest)
		dek, err := openDEK(manifest.SealedKey, opts.Keyring, opts.Passphrase, aad)
		if err != nil {
			return verifiedArchive{}, err
		}
		defer clear(dek)

		payloadTarPath = filepath.Join(tmpDir, payloadPlainEntryName)
		if err := decryptFile(payloadTarPath, outerPayloadPath, dek, aad); err != nil {
			return verifiedArchive{}, err
		}
	}

	if manifest.PayloadSHA256 != "" {
		gotPlainSHA, _, err := hashFile(payloadTarPath)
		if err != nil {
			return verifiedArchive{}, err
		}
		if gotPlainSHA != manifest.PayloadSHA256 {
			return verifiedArchive{}, errors.New("backup: decrypted payload digest mismatch against manifest")
		}
	}

	return verifyInnerPayload(manifest, payloadTarPath, opts)
}

func validateManifestSchemaVersions(versions, supported map[string]int64) error {
	allowed := knownSchemaSet()
	for schema, ver := range versions {
		if !allowed[schema] {
			return fmt.Errorf("backup: unknown schema %q in manifest", schema)
		}
		if ver < 0 {
			return fmt.Errorf("backup: invalid negative schema version %d for %q in manifest", ver, schema)
		}
		if supported != nil {
			maxVer, ok := supported[schema]
			if !ok {
				return fmt.Errorf("backup: schema %q is not supported by this binary", schema)
			}
			if ver > maxVer {
				return fmt.Errorf("backup: schema %q version %d exceeds supported version %d (backup is from a newer release)", schema, ver, maxVer)
			}
		}
	}
	return nil
}

func checkMasterKeyIDs(ids []string, ring Keyring) error {
	for _, id := range ids {
		if id == "" {
			continue
		}
		if ring == nil || !ring.Ready() {
			return fmt.Errorf("backup: archive requires master key %q, but UPMO_MASTER_KEY is not configured", id)
		}
		if !ring.HasKey(id) {
			return fmt.Errorf("backup: master key %q is not present in the active keyring (current key is %q; configure UPMO_MASTER_KEY_ID or UPMO_MASTER_KEY_PREVIOUS_%s)", id, ring.CurrentID(), id)
		}
	}
	return nil
}

func writeStreamToTempFile(dstPath string, r io.Reader, size int64) (string, int64, error) {
	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // temp file in private tmpDir
	if err != nil {
		return "", 0, fmt.Errorf("backup: create staged file: %w", err)
	}
	cleanup := true
	defer func() {
		_ = out.Close()
		if cleanup {
			_ = os.Remove(dstPath)
		}
	}()

	h := sha256.New()
	mw := io.MultiWriter(out, h)
	n, err := io.CopyN(mw, r, size)
	if err != nil {
		return "", 0, fmt.Errorf("backup: read archive payload: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", 0, fmt.Errorf("backup: close staged file: %w", err)
	}
	cleanup = false
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func verifyInnerPayload(manifest Manifest, payloadTarPath string, opts RestoreOptions) (verifiedArchive, error) {
	f, err := os.Open(payloadTarPath) //nolint:gosec // staged temp file in private tmpDir
	if err != nil {
		return verifiedArchive{}, fmt.Errorf("backup: open payload archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return verifiedArchive{}, fmt.Errorf("backup: read payload gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	actualFiles := make(map[string]EntryChecksum)
	var (
		hasPayloadManifest bool
		payloadManifest    PayloadManifest
		hasMeta            bool
		meta               DBMeta
		hasArtifacts       bool
		hasKnowledge       bool
	)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return verifiedArchive{}, fmt.Errorf("backup: read payload tar: %w", err)
		}
		clean, err := cleanArchivePath(hdr.Name)
		if err != nil {
			return verifiedArchive{}, err
		}
		if volumeEntry(clean, artifactsVolumePrefix) {
			hasArtifacts = true
		}
		if volumeEntry(clean, knowledgeVolumePrefix) {
			hasKnowledge = true
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return verifiedArchive{}, fmt.Errorf("backup: unsupported entry type %d for %s", hdr.Typeflag, clean)
		}
		if hdr.Size < 0 {
			return verifiedArchive{}, fmt.Errorf("backup: negative entry size for %s", clean)
		}

		if clean == payloadManifestEntryName {
			if hasPayloadManifest {
				return verifiedArchive{}, fmt.Errorf("backup: duplicate %s", payloadManifestEntryName)
			}
			raw, err := readBoundedJSON(tr, hdr.Size, payloadManifestEntryName)
			if err != nil {
				return verifiedArchive{}, err
			}
			if err := json.Unmarshal(raw, &payloadManifest); err != nil {
				return verifiedArchive{}, fmt.Errorf("backup: parse %s: %w", payloadManifestEntryName, err)
			}
			hasPayloadManifest = true
			continue
		}

		if _, dup := actualFiles[clean]; dup {
			return verifiedArchive{}, fmt.Errorf("backup: duplicate entry %s in payload", clean)
		}

		h := sha256.New()
		var n int64
		if clean == dbMetaEntryName {
			if hdr.Size > maxMetadataBytes {
				return verifiedArchive{}, fmt.Errorf("backup: %s exceeds %d bytes", dbMetaEntryName, maxMetadataBytes)
			}
			raw, err := readBoundedJSON(io.TeeReader(tr, h), hdr.Size, dbMetaEntryName)
			if err != nil {
				return verifiedArchive{}, err
			}
			if err := json.Unmarshal(raw, &meta); err != nil {
				return verifiedArchive{}, fmt.Errorf("backup: parse %s: %w", dbMetaEntryName, err)
			}
			n = int64(len(raw))
			hasMeta = true
		} else {
			n, err = io.CopyN(h, tr, hdr.Size)
			if err != nil {
				return verifiedArchive{}, fmt.Errorf("backup: verify payload entry %s: %w", clean, err)
			}
		}
		actualFiles[clean] = EntryChecksum{
			Path:   clean,
			SHA256: hex.EncodeToString(h.Sum(nil)),
			Size:   n,
		}
	}
	if err := drainGzipEOF(gz); err != nil {
		return verifiedArchive{}, err
	}

	if !hasPayloadManifest {
		return verifiedArchive{}, fmt.Errorf("backup: %s missing from payload", payloadManifestEntryName)
	}
	if !hasMeta {
		return verifiedArchive{}, fmt.Errorf("backup: %s missing from payload", dbMetaEntryName)
	}
	if err := validateDBMeta(meta); err != nil {
		return verifiedArchive{}, err
	}
	if !maps.Equal(manifest.SchemaVersions, meta.SchemaVersions) {
		return verifiedArchive{}, errors.New("backup: manifest schema_versions does not match db metadata")
	}
	if err := checkMasterKeyIDs(meta.KeyIDs, opts.Keyring); err != nil {
		return verifiedArchive{}, err
	}
	if len(payloadManifest.Entries) != len(actualFiles) {
		return verifiedArchive{}, fmt.Errorf("backup: payload entry count mismatch (manifest %d, archive %d)", len(payloadManifest.Entries), len(actualFiles))
	}

	tableSizes := make(map[string]int64, len(meta.Tables))
	for _, want := range payloadManifest.Entries {
		got, ok := actualFiles[want.Path]
		if !ok || got.Size != want.Size || got.SHA256 != want.SHA256 {
			return verifiedArchive{}, fmt.Errorf("backup: checksum mismatch for %s", want.Path)
		}
		if strings.HasPrefix(want.Path, dbTablesPrefix) {
			tableSizes[want.Path] = want.Size
		}
	}
	for _, tbl := range meta.Tables {
		if len(tbl.Columns) == 0 {
			continue
		}
		entryPath := fmt.Sprintf("%s%s.%s.copy", dbTablesPrefix, tbl.Schema, tbl.Name)
		if _, ok := tableSizes[entryPath]; !ok {
			return verifiedArchive{}, fmt.Errorf("backup: missing table dump %s", entryPath)
		}
	}
	if hasArtifacts && strings.TrimSpace(opts.ArtifactsDir) == "" {
		return verifiedArchive{}, errors.New("backup: archive contains artifacts volume files, but UPMO_ARTIFACTS_DIR is not set")
	}
	if hasKnowledge && strings.TrimSpace(opts.KnowledgeDir) == "" {
		return verifiedArchive{}, errors.New("backup: archive contains knowledge volume files, but UPMO_KNOWLEDGE_DIR is not set")
	}

	return verifiedArchive{
		manifest:       manifest,
		meta:           meta,
		payloadTarPath: payloadTarPath,
		tableSizes:     tableSizes,
		hasArtifacts:   hasArtifacts,
		hasKnowledge:   hasKnowledge,
	}, nil
}

func readBoundedJSON(r io.Reader, size int64, name string) ([]byte, error) {
	if size < 0 || size > maxMetadataBytes {
		return nil, fmt.Errorf("backup: %s size %d out of range", name, size)
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxMetadataBytes+1))
	if err != nil {
		return nil, fmt.Errorf("backup: read %s: %w", name, err)
	}
	if int64(len(raw)) != size {
		return nil, fmt.Errorf("backup: truncated %s", name)
	}
	return raw, nil
}

func drainGzipEOF(gz *gzip.Reader) error {
	var buf [4096]byte
	var drained int64
	for {
		n, err := gz.Read(buf[:])
		for i := 0; i < n; i++ {
			if buf[i] != 0 {
				return errors.New("backup: unexpected non-zero trailing data in tar stream")
			}
		}
		drained += int64(n)
		if drained > 1<<20 {
			return errors.New("backup: excessive trailing padding in tar stream")
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("backup: gzip integrity check failed: %w", err)
		}
	}
}
