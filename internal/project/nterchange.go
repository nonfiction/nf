package project

import (
	"fmt"
	"path"
	"strings"
)

// nterchange uses the repository's existing app/db Compose services.
// Media paths are data, not part of a code release.
type Nterchange struct {
	Media []string `json:"media,omitempty"`
}

func (n *Nterchange) MediaPaths() []string {
	if len(n.Media) == 0 {
		return []string{"public_html/upload"}
	}
	return n.Media
}

func (n *Nterchange) Validate() error {
	for i, media := range n.MediaPaths() {
		if err := ValidateRelativePath(media); err != nil {
			return fmt.Errorf("nf.json nterchange.media: %w", err)
		}
		if path.Clean(media) != media || !strings.HasPrefix(media, "public_html/") {
			return fmt.Errorf("nf.json nterchange.media must contain clean paths below public_html")
		}
		for _, part := range strings.Split(media, "/") {
			if strings.HasPrefix(part, ".") {
				return fmt.Errorf("nf.json nterchange.media must not include hidden paths")
			}
		}
		for _, previous := range n.MediaPaths()[:i] {
			if media == previous || strings.HasPrefix(media, previous+"/") || strings.HasPrefix(previous, media+"/") {
				return fmt.Errorf("nf.json nterchange.media paths must not overlap")
			}
		}
	}
	return nil
}
