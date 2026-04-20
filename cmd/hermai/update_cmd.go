package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	appversion "github.com/hermai-ai/hermai-cli/internal/version"
	"github.com/spf13/cobra"
)

const (
	githubReleasesLatest = "https://api.github.com/repos/hermai-ai/hermai-cli/releases/latest"
	githubReleaseTagURL  = "https://api.github.com/repos/hermai-ai/hermai-cli/releases/tags/%s"
)

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type releaseInfo struct {
	TagName string         `json:"tag_name"`
	Name    string         `json:"name"`
	Assets  []releaseAsset `json:"assets"`
}

func newUpdateCmd() *cobra.Command {
	var (
		checkOnly   bool
		force       bool
		assumeYes   bool
		targetTag   string
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the hermai binary to the latest GitHub release",
		Long: `Check GitHub for a newer hermai release and atomically replace the
currently-running binary with it. Verifies the SHA-256 checksum against
the release's checksums.txt before swapping.

Requires write permission on the binary's directory. If hermai lives in
/usr/local/bin or another root-owned path, re-run under sudo.`,
		Example: `  hermai update              # prompt, then update to latest
  hermai update --check      # just report whether an update is available
  hermai update --yes        # non-interactive update
  hermai update --version v0.5.0   # install a specific tag (up- or downgrade)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()

			current := strings.TrimPrefix(appversion.Version, "v")

			rel, err := fetchRelease(ctx, targetTag)
			if err != nil {
				return fmt.Errorf("fetch release info: %w", err)
			}
			latest := strings.TrimPrefix(rel.TagName, "v")

			fmt.Fprintf(out, "current: %s\nlatest:  %s\n", valueOr(current, "dev"), latest)

			if !force && !checkOnly && targetTag == "" && current != "dev" && current == latest {
				fmt.Fprintln(out, "Already on the latest version.")
				return nil
			}
			if checkOnly {
				if current == latest && current != "dev" {
					fmt.Fprintln(out, "Up to date.")
				} else {
					fmt.Fprintf(out, "Update available: %s → %s\n", valueOr(current, "dev"), latest)
				}
				return nil
			}

			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate current binary: %w", err)
			}
			if resolved, err := filepath.EvalSymlinks(exe); err == nil {
				exe = resolved
			}
			if err := checkWritable(exe); err != nil {
				return err
			}

			if !assumeYes {
				fmt.Fprintf(out, "\nReplace %s with release %s? [y/N] ", exe, rel.TagName)
				if !confirmYes(cmd.InOrStdin()) {
					fmt.Fprintln(out, "aborted.")
					return nil
				}
			}

			assetName, archiveExt := assetNameFor(latest)
			asset := findAsset(rel.Assets, assetName)
			if asset == nil {
				return fmt.Errorf("no asset named %q in release %s — this platform (%s/%s) may not have a prebuilt binary", assetName, rel.TagName, runtime.GOOS, runtime.GOARCH)
			}

			expectedSHA, err := fetchChecksum(ctx, rel.Assets, assetName)
			if err != nil {
				return fmt.Errorf("fetch checksums: %w", err)
			}

			fmt.Fprintf(out, "Downloading %s (%s)...\n", asset.Name, humanBytes(asset.Size))
			tmpArchive, err := downloadToTemp(ctx, asset.BrowserDownloadURL)
			if err != nil {
				return fmt.Errorf("download: %w", err)
			}
			defer os.Remove(tmpArchive)

			if err := verifySHA256(tmpArchive, expectedSHA); err != nil {
				return fmt.Errorf("checksum mismatch: %w", err)
			}
			fmt.Fprintln(out, "Checksum verified.")

			newBin, err := extractBinary(tmpArchive, archiveExt, filepath.Dir(exe))
			if err != nil {
				return fmt.Errorf("extract binary: %w", err)
			}

			if err := swapBinary(newBin, exe); err != nil {
				os.Remove(newBin)
				return fmt.Errorf("replace binary: %w", err)
			}

			fmt.Fprintf(out, "Updated to %s.\n", rel.TagName)
			return nil
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "Only report whether an update is available — don't download or replace")
	cmd.Flags().BoolVar(&force, "force", false, "Reinstall even if already on the target version")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Skip the confirmation prompt")
	cmd.Flags().StringVar(&targetTag, "version", "", "Install a specific release tag (e.g. v0.5.0) instead of latest")

	return cmd
}

func fetchRelease(ctx context.Context, tag string) (*releaseInfo, error) {
	url := githubReleasesLatest
	if tag != "" {
		if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
		url = fmt.Sprintf(githubReleaseTagURL, tag)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", appversion.UserAgent())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("release not found (tag %q)", tag)
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("GitHub API rate-limited — try again in ~an hour, or set GITHUB_TOKEN")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
	}
	var rel releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// assetNameFor returns the archive filename that goreleaser produces
// for the current GOOS/GOARCH plus the archive extension.
func assetNameFor(version string) (name, ext string) {
	ext = ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("hermai_%s_%s_%s%s", version, runtime.GOOS, runtime.GOARCH, ext), ext
}

func findAsset(assets []releaseAsset, name string) *releaseAsset {
	for i := range assets {
		if assets[i].Name == name {
			return &assets[i]
		}
	}
	return nil
}

// fetchChecksum pulls checksums.txt from the release's asset list and
// returns the hex SHA-256 recorded for the named archive.
func fetchChecksum(ctx context.Context, assets []releaseAsset, archiveName string) (string, error) {
	sums := findAsset(assets, "checksums.txt")
	if sums == nil {
		return "", errors.New("checksums.txt not in release assets")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sums.BrowserDownloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", appversion.UserAgent())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksums.txt HTTP %d", resp.StatusCode)
	}
	scan := bufio.NewScanner(resp.Body)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == archiveName {
			return strings.ToLower(fields[0]), nil
		}
	}
	if err := scan.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no checksum entry for %s", archiveName)
}

func downloadToTemp(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", appversion.UserAgent())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	f, err := os.CreateTemp("", "hermai-update-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func verifySHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("expected %s, got %s", want, got)
	}
	return nil
}

// extractBinary pulls the "hermai" binary out of the archive and writes
// it to a temp file next to the target binary directory (so the final
// rename stays on the same filesystem). Returns the path to the new
// binary.
func extractBinary(archivePath, ext, dstDir string) (string, error) {
	binName := "hermai"
	if runtime.GOOS == "windows" {
		binName = "hermai.exe"
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	out, err := os.CreateTemp(dstDir, "hermai.new.*")
	if err != nil {
		return "", err
	}
	outPath := out.Name()
	cleanup := func() {
		out.Close()
		os.Remove(outPath)
	}

	switch ext {
	case ".tar.gz":
		gz, err := gzip.NewReader(f)
		if err != nil {
			cleanup()
			return "", err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				cleanup()
				return "", fmt.Errorf("binary %q not found in archive", binName)
			}
			if err != nil {
				cleanup()
				return "", err
			}
			if filepath.Base(hdr.Name) == binName && hdr.Typeflag == tar.TypeReg {
				if _, err := io.Copy(out, tr); err != nil {
					cleanup()
					return "", err
				}
				goto done
			}
		}
	case ".zip":
		info, err := f.Stat()
		if err != nil {
			cleanup()
			return "", err
		}
		zr, err := zip.NewReader(f, info.Size())
		if err != nil {
			cleanup()
			return "", err
		}
		for _, ze := range zr.File {
			if filepath.Base(ze.Name) != binName {
				continue
			}
			rc, err := ze.Open()
			if err != nil {
				cleanup()
				return "", err
			}
			if _, err := io.Copy(out, rc); err != nil {
				rc.Close()
				cleanup()
				return "", err
			}
			rc.Close()
			goto done
		}
		cleanup()
		return "", fmt.Errorf("binary %q not found in archive", binName)
	default:
		cleanup()
		return "", fmt.Errorf("unsupported archive extension %q", ext)
	}

done:
	if err := out.Chmod(0o755); err != nil {
		cleanup()
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(outPath)
		return "", err
	}
	return outPath, nil
}

// swapBinary replaces the current binary with the new one. On unix a
// plain rename works even while the old binary is executing. On
// Windows we have to move the old binary aside first because a running
// .exe cannot be overwritten.
func swapBinary(newPath, targetPath string) error {
	if runtime.GOOS == "windows" {
		backup := targetPath + ".old"
		_ = os.Remove(backup)
		if err := os.Rename(targetPath, backup); err != nil {
			return err
		}
		if err := os.Rename(newPath, targetPath); err != nil {
			_ = os.Rename(backup, targetPath) // try to restore
			return err
		}
		return nil
	}
	return os.Rename(newPath, targetPath)
}

func checkWritable(binaryPath string) error {
	dir := filepath.Dir(binaryPath)
	testFile, err := os.CreateTemp(dir, ".hermai-write-test-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (try sudo): %w", dir, err)
	}
	name := testFile.Name()
	testFile.Close()
	os.Remove(name)
	return nil
}

func confirmYes(r io.Reader) bool {
	s := bufio.NewScanner(r)
	if !s.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(s.Text()))
	return answer == "y" || answer == "yes"
}

func valueOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}
