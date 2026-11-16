package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const stagingPrefix = ".upmo-restore-"

func archiveVolume(tw *tar.Writer, entries *[]EntryChecksum, prefix, rootDir string) (int, error) {
	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" {
		return 0, nil
	}
	info, err := os.Stat(rootDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("backup: configured volume directory %q does not exist", rootDir)
	}
	if err != nil {
		return 0, fmt.Errorf("backup: stat volume %q: %w", rootDir, err)
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("backup: volume %q is not a directory", rootDir)
	}

	resolvedRoot, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		return 0, fmt.Errorf("backup: resolve volume %q: %w", rootDir, err)
	}
	resolvedInfo, err := os.Stat(resolvedRoot)
	if err != nil {
		return 0, fmt.Errorf("backup: stat resolved volume %q: %w", resolvedRoot, err)
	}
	if !resolvedInfo.IsDir() {
		return 0, fmt.Errorf("backup: resolved volume %q is not a directory", resolvedRoot)
	}

	type item struct {
		rel  string
		full string
		dir  bool
		info fs.FileInfo
	}
	var items []item
	err = filepath.WalkDir(resolvedRoot, func(full string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(resolvedRoot, full)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), stagingPrefix) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup: symlink not supported in volume %q: %s", rootDir, rel)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !fi.Mode().IsRegular() {
			return fmt.Errorf("backup: unsupported file type in volume %q: %s", rootDir, rel)
		}
		items = append(items, item{
			rel:  filepath.ToSlash(rel),
			full: full,
			dir:  d.IsDir(),
			info: fi,
		})
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("backup: walk volume %q: %w", rootDir, err)
	}

	sort.Slice(items, func(i, j int) bool { return items[i].rel < items[j].rel })
	fileCount := 0
	for _, it := range items {
		archivePath := prefix + it.rel
		if it.dir {
			hdr := &tar.Header{
				Name:     archivePath + "/",
				Typeflag: tar.TypeDir,
				Mode:     int64(it.info.Mode().Perm()),
				ModTime:  it.info.ModTime().UTC(),
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return 0, fmt.Errorf("backup: write volume dir header %s: %w", archivePath, err)
			}
			continue
		}
		sum, size, err := writeVolumeFile(tw, archivePath, it.full)
		if err != nil {
			return 0, err
		}
		*entries = append(*entries, EntryChecksum{
			Path:   archivePath,
			SHA256: sum,
			Size:   size,
		})
		fileCount++
	}
	return fileCount, nil
}

func writeVolumeFile(tw *tar.Writer, archivePath, fullPath string) (string, int64, error) {
	f, err := os.Open(fullPath) //nolint:gosec // fullPath comes from WalkDir under resolved volume root
	if err != nil {
		return "", 0, fmt.Errorf("backup: open volume file %s: %w", archivePath, err)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return "", 0, fmt.Errorf("backup: stat volume file %s: %w", archivePath, err)
	}
	if !fi.Mode().IsRegular() {
		return "", 0, fmt.Errorf("backup: unsupported file type in volume %s", archivePath)
	}
	size := fi.Size()

	hdr := &tar.Header{
		Name:     archivePath,
		Typeflag: tar.TypeReg,
		Size:     size,
		Mode:     int64(fi.Mode().Perm()),
		ModTime:  fi.ModTime().UTC(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return "", 0, fmt.Errorf("backup: write volume file header %s: %w", archivePath, err)
	}

	h := sha256.New()
	mw := io.MultiWriter(tw, h)
	n, err := io.CopyN(mw, f, size)
	if err != nil {
		return "", 0, fmt.Errorf("backup: write volume file %s: %w", archivePath, err)
	}
	var extra [1]byte
	if extraN, err := f.Read(extra[:]); extraN > 0 || (err != nil && !errors.Is(err, io.EOF)) {
		return "", 0, fmt.Errorf("backup: volume file %s modified during backup", archivePath)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

type dirMode struct {
	path string
	mode fs.FileMode
}

type stagedVolume struct {
	prefix        string
	rootDir       string
	stagingDir    string
	stagingAbs    string
	backupDir     string
	dirModes      []dirMode
	filesRestored int
	movedToBackup []string
	movedToLive   []string
}

func stageVolumes(artDir, knowDir, payloadTarPath string, hasArt, hasKnow bool) ([]*stagedVolume, int, error) {
	specs := []struct {
		prefix     string
		rootDir    string
		hasEntries bool
	}{
		{prefix: artifactsVolumePrefix, rootDir: strings.TrimSpace(artDir), hasEntries: hasArt},
		{prefix: knowledgeVolumePrefix, rootDir: strings.TrimSpace(knowDir), hasEntries: hasKnow},
	}

	var vols []*stagedVolume
	cleanupOnErr := true
	defer func() {
		if cleanupOnErr {
			cleanupStagedVolumes(vols)
		}
	}()

	for _, spec := range specs {
		if spec.rootDir == "" {
			if spec.hasEntries {
				return nil, 0, fmt.Errorf("backup: archive contains volume files under %s, but target directory is not configured", spec.prefix)
			}
			continue
		}
		if err := os.MkdirAll(spec.rootDir, 0o750); err != nil {
			return nil, 0, fmt.Errorf("backup: create volume dir %q: %w", spec.rootDir, err)
		}
		info, err := os.Stat(spec.rootDir)
		if err != nil {
			return nil, 0, fmt.Errorf("backup: stat volume dir %q: %w", spec.rootDir, err)
		}
		if !info.IsDir() {
			return nil, 0, fmt.Errorf("backup: volume path %q is not a directory", spec.rootDir)
		}

		stagingDir, err := os.MkdirTemp(spec.rootDir, stagingPrefix+"stage-*")
		if err != nil {
			return nil, 0, fmt.Errorf("backup: create staging dir in %q: %w", spec.rootDir, err)
		}
		backupDir, err := os.MkdirTemp(spec.rootDir, stagingPrefix+"old-*")
		if err != nil {
			_ = os.RemoveAll(stagingDir)
			return nil, 0, fmt.Errorf("backup: create backup swap dir in %q: %w", spec.rootDir, err)
		}
		stagingAbs, err := filepath.EvalSymlinks(stagingDir)
		if err != nil {
			stagingAbs, err = filepath.Abs(stagingDir)
			if err != nil {
				_ = os.RemoveAll(stagingDir)
				_ = os.RemoveAll(backupDir)
				return nil, 0, fmt.Errorf("backup: resolve staging dir: %w", err)
			}
		}

		vols = append(vols, &stagedVolume{
			prefix:     spec.prefix,
			rootDir:    spec.rootDir,
			stagingDir: stagingDir,
			stagingAbs: stagingAbs,
			backupDir:  backupDir,
		})
	}

	if len(vols) == 0 {
		cleanupOnErr = false
		return nil, 0, nil
	}

	totalFiles, err := extractVolumesToStaging(payloadTarPath, vols)
	if err != nil {
		return nil, 0, err
	}

	// Apply recorded directory permissions bottom-up after all files are written.
	for _, v := range vols {
		sort.Slice(v.dirModes, func(i, j int) bool { return len(v.dirModes[i].path) > len(v.dirModes[j].path) })
		for _, dm := range v.dirModes {
			if err := os.Chmod(dm.path, dm.mode); err != nil {
				return nil, 0, fmt.Errorf("backup: chmod volume dir: %w", err)
			}
		}
	}

	cleanupOnErr = false
	return vols, totalFiles, nil
}

func cleanupStagedVolumes(vols []*stagedVolume) {
	for _, v := range vols {
		if v.stagingDir != "" {
			_ = os.RemoveAll(v.stagingDir)
		}
		if v.backupDir != "" {
			_ = os.RemoveAll(v.backupDir)
		}
	}
}

func commitVolumeSwaps(vols []*stagedVolume, renameFn func(oldpath, newpath string) error) error {
	if len(vols) == 0 {
		return nil
	}
	if renameFn == nil {
		renameFn = os.Rename
	}

	for i, v := range vols {
		existing, err := os.ReadDir(v.rootDir)
		if err != nil {
			rollbackVolumeSwaps(vols[:i+1], renameFn)
			return fmt.Errorf("backup: list volume dir %q: %w", v.rootDir, err)
		}
		stagingBase := filepath.Base(v.stagingDir)
		backupBase := filepath.Base(v.backupDir)
		for _, entry := range existing {
			name := entry.Name()
			if name == stagingBase || name == backupBase {
				continue
			}
			src := filepath.Join(v.rootDir, name)
			dst := filepath.Join(v.backupDir, name)
			if err := renameFn(src, dst); err != nil {
				rollbackVolumeSwaps(vols[:i+1], renameFn)
				return fmt.Errorf("backup: move existing volume entry %s to backup dir: %w", name, err)
			}
			v.movedToBackup = append(v.movedToBackup, name)
		}

		staged, err := os.ReadDir(v.stagingDir)
		if err != nil {
			rollbackVolumeSwaps(vols[:i+1], renameFn)
			return fmt.Errorf("backup: list staging dir: %w", err)
		}
		for _, entry := range staged {
			name := entry.Name()
			src := filepath.Join(v.stagingDir, name)
			dst := filepath.Join(v.rootDir, name)
			if err := renameFn(src, dst); err != nil {
				rollbackVolumeSwaps(vols[:i+1], renameFn)
				return fmt.Errorf("backup: move staged volume entry %s: %w", name, err)
			}
			v.movedToLive = append(v.movedToLive, name)
		}
	}

	// Both swaps succeeded; only now remove old live backup contents and empty staging dirs.
	for _, v := range vols {
		_ = os.RemoveAll(v.backupDir)
		_ = os.RemoveAll(v.stagingDir)
	}
	return nil
}

func rollbackVolumeSwaps(vols []*stagedVolume, renameFn func(oldpath, newpath string) error) {
	for i := len(vols) - 1; i >= 0; i-- {
		v := vols[i]
		for j := len(v.movedToLive) - 1; j >= 0; j-- {
			name := v.movedToLive[j]
			_ = renameFn(filepath.Join(v.rootDir, name), filepath.Join(v.stagingDir, name))
		}
		for j := len(v.movedToBackup) - 1; j >= 0; j-- {
			name := v.movedToBackup[j]
			_ = renameFn(filepath.Join(v.backupDir, name), filepath.Join(v.rootDir, name))
		}
		// Only remove backupDir if empty; never delete staged files on rename failure.
		_ = os.Remove(v.backupDir)
	}
}

func restoreVolumes(artDir, knowDir, payloadTarPath string, hasArt, hasKnow bool, renameFn func(oldpath, newpath string) error) (int, error) {
	vols, totalFiles, err := stageVolumes(artDir, knowDir, payloadTarPath, hasArt, hasKnow)
	if err != nil {
		return 0, err
	}
	if err := commitVolumeSwaps(vols, renameFn); err != nil {
		return 0, err
	}
	return totalFiles, nil
}

func extractVolumesToStaging(payloadTarPath string, vols []*stagedVolume) (int, error) {
	f, err := os.Open(payloadTarPath) //nolint:gosec // temp file created by Restore
	if err != nil {
		return 0, fmt.Errorf("backup: open payload archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, fmt.Errorf("backup: open payload gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	totalRestored := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("backup: read payload tar: %w", err)
		}
		clean, err := cleanArchivePath(hdr.Name)
		if err != nil {
			return 0, err
		}
		var targetVol *stagedVolume
		for _, v := range vols {
			if strings.HasPrefix(clean, v.prefix) {
				targetVol = v
				break
			}
		}
		if targetVol == nil {
			continue
		}

		rel := strings.TrimPrefix(clean, targetVol.prefix)
		rel = strings.TrimSuffix(rel, "/")
		if rel == "" || strings.ContainsRune(rel, '\x00') || path.Clean(rel) != rel || strings.HasPrefix(rel, "..") {
			return 0, fmt.Errorf("backup: unsafe volume path %q", hdr.Name)
		}

		target, err := confinedJoin(targetVol.stagingAbs, rel)
		if err != nil {
			return 0, err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return 0, fmt.Errorf("backup: mkdir volume %s: %w", rel, err)
			}
			if hdr.Mode > 0 {
				targetVol.dirModes = append(targetVol.dirModes, dirMode{
					path: target,
					mode: fs.FileMode(hdr.Mode) & fs.ModePerm, // #nosec G115 -- masked to permission bits
				})
			}
		case tar.TypeReg:
			if hdr.Size < 0 {
				return 0, fmt.Errorf("backup: negative file size for %s", rel)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return 0, fmt.Errorf("backup: mkdir parent %s: %w", rel, err)
			}
			if err := extractRegularFile(target, tr, hdr); err != nil {
				return 0, fmt.Errorf("backup: extract %s: %w", rel, err)
			}
			targetVol.filesRestored++
			totalRestored++
		default:
			return 0, fmt.Errorf("backup: unsupported archive entry type %d for %s", hdr.Typeflag, hdr.Name)
		}
	}
	return totalRestored, nil
}

func extractRegularFile(target string, r io.Reader, hdr *tar.Header) error {
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // target verified via confinedJoin
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		_ = out.Close()
		if cleanup {
			_ = os.Remove(target)
		}
	}()

	if _, err := io.CopyN(out, r, hdr.Size); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	mode := fs.FileMode(hdr.Mode) & fs.ModePerm // #nosec G115 -- masked to permission bits
	if mode == 0 {
		mode = 0o600
	}
	if err := out.Chmod(mode); err != nil {
		return err
	}
	cleanup = false
	return out.Close()
}

func confinedJoin(rootAbs, slashRel string) (string, error) {
	joined := filepath.Join(rootAbs, filepath.FromSlash(slashRel))
	abs, err := filepath.Abs(joined)
	if err != nil {
		return "", fmt.Errorf("backup: resolve path %q: %w", slashRel, err)
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("backup: path escapes volume root: %q", slashRel)
	}
	return abs, nil
}
