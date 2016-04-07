// Adapted from http://blog.ralch.com/tutorial/golang-working-with-tar-and-gzip/

package targz

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func Pack(srcDir, destDir string) error {
	if err := tarIt(srcDir, destDir); err != nil {
		return err
	}
	filename := filepath.Base(srcDir)
	tarName := filepath.Join(destDir, fmt.Sprintf("%s.tar", filename))
	if err := gzipIt(tarName, destDir); err != nil {
		return err
	}
	return nil
}

func Unpack(srcFile, destDir string) error {
	if err := ungzip(srcFile, destDir); err != nil {
		return err
	}
	tarName := strings.TrimSuffix(filepath.Base(srcFile), filepath.Ext(srcFile))
	tarPath := filepath.Join(destDir, tarName)
	if err := untar(tarPath, destDir); err != nil {
		return err
	}
	return nil
}

func gzipIt(srcFile, destDir string) error {
	reader, err := os.Open(srcFile)
	if err != nil {
		return err
	}

	filename := filepath.Base(srcFile)
	destDir = filepath.Join(destDir, fmt.Sprintf("%s.gz", filename))
	writer, err := os.Create(destDir)
	if err != nil {
		return err
	}
	defer writer.Close()

	archiver := gzip.NewWriter(writer)
	archiver.Name = filename
	defer archiver.Close()

	_, err = io.Copy(archiver, reader)
	return err
}

func ungzip(srcFile, destDir string) error {
	reader, err := os.Open(srcFile)
	if err != nil {
		return err
	}
	defer reader.Close()

	archive, err := gzip.NewReader(reader)
	if err != nil {
		return err
	}
	defer archive.Close()

	destDir = filepath.Join(destDir, archive.Name)
	writer, err := os.Create(destDir)
	if err != nil {
		return err
	}

	defer writer.Close()

	_, err = io.Copy(writer, archive)
	return err
}

func tarIt(srcDir, destDir string) error {
	filename := filepath.Base(srcDir)
	destDir = filepath.Join(destDir, fmt.Sprintf("%s.tar", filename))
	tarfile, err := os.Create(destDir)
	if err != nil {
		return err
	}
	defer tarfile.Close()

	tarball := tar.NewWriter(tarfile)
	defer tarball.Close()

	_, err = os.Stat(srcDir)
	if err != nil {
		return nil
	}

	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == destDir {
			return nil
		}

		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}

		header.Name = filepath.Join(strings.TrimPrefix(path, srcDir))

		if err := tarball.WriteHeader(header); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(tarball, file)
		return err
	}

	return filepath.Walk(srcDir, walkFn)
}

func untar(tarball, target string) error {
	reader, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}

		path := filepath.Join(target, header.Name)
		info := header.FileInfo()
		if info.IsDir() {
			if err = os.MkdirAll(path, info.Mode()); err != nil {
				return err
			}
			continue
		}

		file, err:= os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			return err
		}
		defer file.Close()
		_, err =io.Copy(file, tarReader)
		if err != nil {
			return err
		}
	}

	return nil
}
