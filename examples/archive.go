// Package examples supplies the starter projects bundled with the customer portal.
package examples

import (
	"archive/zip"
	"bytes"
	"embed"
)

// Keep the archive's source list explicit so documentation or local development
// files cannot accidentally become part of a downloadable project.
//
//go:embed node-website/package.json node-website/package-lock.json node-website/build.js node-website/server.js node-website/index.html
var sources embed.FS

// NodeWebsiteZIP returns a source archive ready for the portal's Node upload flow.
func NodeWebsiteZIP() ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, name := range []string{"package.json", "package-lock.json", "build.js", "server.js", "index.html"} {
		data, err := sources.ReadFile("node-website/" + name)
		if err != nil {
			return nil, err
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0644)
		file, err := archive.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := file.Write(data); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
