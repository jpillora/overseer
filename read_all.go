package upgrade

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"

	"strings"
)

//Similar to ioutil.ReadAll except it extracts binaries from
//the reader, whether the reader is a .zip .tar .tar.gz .gz or raw bytes
func ReadAll(path string, r io.Reader) ([]byte, error) {

	if strings.HasSuffix(path, ".gz") {
		gr, err := gzip.NewReader(r)
		if err != nil {
			return nil, err
		}
		r = gr
		path = strings.TrimSuffix(path, ".gz")
	}

	if strings.HasSuffix(path, ".tar") {
		tr := tar.NewReader(r)
		var fr io.Reader
		for {
			info, err := tr.Next()
			if err != nil {
				return nil, err
			}
			if os.FileMode(info.Mode)&0111 != 0 {
				log.Printf("found exec %s", info.Name)
				fr = tr
				break
			}
		}
		if fr == nil {
			return nil, fmt.Errorf("binary not found in tar archive")
		}
		r = fr

	} else if strings.HasSuffix(path, ".zip") {
		bin, err := ioutil.ReadAll(r)
		if err != nil {
			return nil, err
		}
		buff := bytes.NewReader(bin)
		zr, err := zip.NewReader(buff, int64(buff.Len()))
		if err != nil {
			return nil, err
		}

		var fr io.Reader
		for _, f := range zr.File {
			info := f.FileInfo()
			if info.Mode()&0111 != 0 {
				log.Printf("found exec %s", info.Name())
				fr, err = f.Open()
				if err != nil {
					return nil, err
				}
			}
		}
		if fr == nil {
			return nil, fmt.Errorf("binary not found in zip archive")
		}
		r = fr
	}

	return ioutil.ReadAll(r)
}
