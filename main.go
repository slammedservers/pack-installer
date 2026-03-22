package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	provider := flag.String("provider", "", "Pack provider (e.g. curseforge)")
	game := flag.String("game", "", "Game identifier (e.g. minecraft)")
	packID := flag.String("pack-id", "", "Provider pack ID")
	versionID := flag.String("version-id", "", "Provider version ID")
	downloadURL := flag.String("download-url", "", "Direct download URL for the server pack")
	directory := flag.String("directory", "/mnt/server", "Server files directory")
	flag.Parse()

	log("[SlammedUtils] Pack Installer")
	log("  Game:        %s", *game)
	log("  Provider:    %s", *provider)
	log("  Pack ID:     %s", *packID)
	log("  Version ID:  %s", *versionID)
	log("  Directory:   %s", *directory)

	if *downloadURL == "" {
		fatal("--download-url is required")
	}

	if err := os.MkdirAll(*directory, 0755); err != nil {
		fatal("Failed to create directory %s: %v", *directory, err)
	}

	// Step 1: Clean old modpack files
	log("[SlammedUtils] Cleaning previous modpack files...")
	cleanModpackFiles(*directory, *game)

	// Step 2: Download
	zipPath := filepath.Join(*directory, "pack_download.zip")
	log("[SlammedUtils] Downloading pack...")
	log("  URL: %s", *downloadURL)
	if err := downloadFile(*downloadURL, zipPath); err != nil {
		fatal("Download failed: %v", err)
	}

	info, _ := os.Stat(zipPath)
	if info != nil {
		log("[SlammedUtils] Downloaded %.2f MB", float64(info.Size())/(1024*1024))
	}

	// Step 3: Extract
	log("[SlammedUtils] Extracting pack...")
	if err := extractZip(zipPath, *directory); err != nil {
		fatal("Extraction failed: %v", err)
	}
	os.Remove(zipPath)

	// Step 4: Handle nested directory
	flattenIfNested(*directory)

	// Step 5: List files
	log("[SlammedUtils] Server files:")
	entries, _ := os.ReadDir(*directory)
	for _, e := range entries {
		info, _ := e.Info()
		if info != nil {
			log("  %s (%d bytes)", e.Name(), info.Size())
		} else {
			log("  %s", e.Name())
		}
	}

	// Step 6: Game-specific post-install
	switch *game {
	case "minecraft":
		postInstallMinecraft(*directory)
	default:
		log("[SlammedUtils] No post-install for game: %s", *game)
	}

	log("[SlammedUtils] Installation finished successfully.")
}

func cleanModpackFiles(dir, game string) {
	switch game {
	case "minecraft":
		// Remove known modpack directories and files
		remove := []string{
			"mods", "coremods", "libraries",
			".fabric", ".forge", ".neoforge",
			"server.jar", "unix_args.txt", "user_jvm_args.txt",
		}
		for _, name := range remove {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err == nil {
				os.RemoveAll(p)
				log("  Removed: %s", name)
			}
		}
	}
}

func downloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer out.Close()

	written, err := io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	if written == 0 {
		return fmt.Errorf("downloaded 0 bytes")
	}

	return nil
}

func extractZip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	log("  Archive contains %d entries", len(r.File))

	for _, f := range r.File {
		target := filepath.Join(destDir, f.Name)

		// Prevent zip slip
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) && filepath.Clean(target) != filepath.Clean(destDir) {
			log("  Skipping unsafe path: %s", f.Name)
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", f.Name, err)
		}

		outFile, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return fmt.Errorf("create %s: %w", f.Name, err)
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return fmt.Errorf("open entry %s: %w", f.Name, err)
		}

		_, err = io.Copy(outFile, rc)
		rc.Close()
		outFile.Close()
		if err != nil {
			return fmt.Errorf("extract %s: %w", f.Name, err)
		}
	}

	return nil
}

func flattenIfNested(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	// Count directories (ignore files)
	var dirs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e)
		}
	}

	// If there's exactly one directory and no server.jar or startserver.sh, flatten it
	if len(dirs) != 1 {
		return
	}

	for _, name := range []string{"server.jar", "startserver.sh", "start.sh"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return
		}
	}

	subdir := filepath.Join(dir, dirs[0].Name())
	log("[SlammedUtils] Flattening nested directory: %s", dirs[0].Name())

	subEntries, err := os.ReadDir(subdir)
	if err != nil {
		return
	}

	for _, e := range subEntries {
		src := filepath.Join(subdir, e.Name())
		dst := filepath.Join(dir, e.Name())
		if err := os.Rename(src, dst); err != nil {
			log("  Warning: could not move %s: %v", e.Name(), err)
		}
	}

	os.Remove(subdir)
}

func postInstallMinecraft(dir string) {
	log("[SlammedUtils] Running Minecraft post-install...")

	// Find and run Forge/NeoForge installers
	patterns := []string{
		"forge-*-installer.jar",
		"neoforge-*-installer.jar",
	}

	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, jar := range matches {
			log("[SlammedUtils] Running installer: %s", filepath.Base(jar))
			cmd := exec.Command("java", "-jar", jar, "--installServer")
			cmd.Dir = dir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				log("[SlammedUtils] Warning: installer failed: %v", err)
			} else {
				os.Remove(jar)
			}
		}
	}

	// Make shell scripts executable
	shFiles, _ := filepath.Glob(filepath.Join(dir, "*.sh"))
	for _, sh := range shFiles {
		os.Chmod(sh, 0755)
	}

	log("[SlammedUtils] Minecraft post-install complete.")
}

func log(format string, args ...interface{}) {
	fmt.Printf(format+"\n", args...)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "[SlammedUtils] FATAL: "+format+"\n", args...)
	os.Exit(1)
}
