package imgsrc

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/builder/dockerignore"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/fileutils"
)

type archiveOptions struct {
	sourcePath string
	exclusions []string
	compressed bool
	additions  map[string][]byte
}

func archiveDirectory(options archiveOptions) (io.ReadCloser, error) {
	opts := &archive.TarOptions{
		ExcludePatterns: options.exclusions,
	}
	if options.compressed && len(options.additions) == 0 {
		opts.Compression = archive.Gzip
	}

	tmp, err := archive.TarWithOptions(options.sourcePath, opts)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(tmp)
	var size int64 = 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		size += hdr.Size
	}
	err = tmp.Close()
	if err != nil {
		return nil, err
	}
	fmt.Printf("Your build context is %s\n\n", ReadableBytes(size))

	r, err := archive.TarWithOptions(options.sourcePath, opts)
	if err != nil {
		return nil, err
	}

	if options.additions != nil {
		mods := map[string]archive.TarModifierFunc{}
		for name, contents := range options.additions {
			mods[name] = func(path string, header *tar.Header, content io.Reader) (*tar.Header, []byte, error) {
				newHeader := &tar.Header{
					Name: name,
					Size: int64(len(contents)),
				}

				return newHeader, contents, nil
			}
		}

		r = archive.ReplaceFileTarWrapper(r, mods)
	}

	return r, nil
}

func readDockerignore(workingDir string) ([]string, error) {
	file, err := os.Open(filepath.Join(workingDir, ".dockerignore"))
	if os.IsNotExist(err) {
		return []string{}, nil
	} else if err != nil {
		return nil, err
	}
	defer file.Close()

	return parseDockerignore(file)
}

func parseDockerignore(r io.Reader) ([]string, error) {
	excludes, err := dockerignore.ReadAll(r)
	if err != nil {
		return nil, err
	}

	if match, _ := fileutils.Matches("fly.toml", excludes); !match {
		excludes = append(excludes, "fly.toml")
	}

	if match, _ := fileutils.Matches(".dockerignore", excludes); match {
		excludes = append(excludes, "!.dockerignore")
	}

	if match, _ := fileutils.Matches("Dockerfile", excludes); match {
		excludes = append(excludes, "![Dd]ockerfile")
	}

	if match, _ := fileutils.Matches("dockerfile", excludes); match {
		excludes = append(excludes, "![Dd]ockerfile")
	}

	return excludes, nil
}

func isPathInRoot(target, rootDir string) bool {
	rootDir, _ = filepath.Abs(rootDir)
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootDir, target)
	}

	rel, err := filepath.Rel(rootDir, target)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(filepath.ToSlash(rel), "../")
}

func ReadableBytes(b int64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB",
		float64(b)/float64(div), "kMGTPE"[exp])
}
