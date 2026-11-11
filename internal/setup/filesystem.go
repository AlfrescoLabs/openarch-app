package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
)

// readersKey holds the readers of the filesystem source in .env.local.
const readersKey = "FILESYSTEM_READ_PRINCIPALS"

// maxReaderLength is the longest reader name.
const maxReaderLength = 100

// filesystemSource checks the filesystem flags of init and returns the
// .env.local values of the filesystem source, or nil without one. It makes
// c.Filesystem absolute. A reader is a user name: the RAG service does not
// look up groups for the filesystem source, so a group would match nobody. No
// reader means everyone, which needs --filesystem-everyone.
func filesystemSource(c *Config, readers []string, everyone bool) (map[string]string, error) {
	if c.Filesystem == "" {
		if len(readers) > 0 || everyone {
			return nil, output.Usage("--filesystem-reader and --filesystem-everyone need --filesystem", "")
		}
		return nil, nil
	}
	switch {
	case len(readers) > 0 && everyone:
		return nil, output.Usage("--filesystem-reader and --filesystem-everyone cannot be combined", "")
	case len(readers) == 0 && !everyone:
		return nil, output.Usage("--filesystem needs --filesystem-reader or --filesystem-everyone",
			"Name the users who can read the files, for example --filesystem-reader jim. With --filesystem-everyone, every user who can log in reads them.")
	}
	for _, r := range readers {
		bad := r == "" || len(r) > maxReaderLength || strings.ContainsAny(r, ",'\"") ||
			strings.ContainsFunc(r, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) })
		if bad {
			return nil, output.Usage(fmt.Sprintf("invalid --filesystem-reader %q", r), fmt.Sprintf("Use a user name of 1 to %d characters, with no space, comma or quote.", maxReaderLength))
		}
		if strings.HasPrefix(strings.ToUpper(r), "GROUP_") {
			return nil, output.Usage(fmt.Sprintf("--filesystem-reader %s is a group", r),
				"The RAG service does not look up groups for the filesystem source. Name each user instead.")
		}
	}
	dir, err := filepath.Abs(c.Filesystem)
	if err != nil {
		return nil, err
	}
	// The path goes into .env.local in single quotes.
	if strings.ContainsRune(dir, '\'') || strings.ContainsFunc(dir, unicode.IsControl) {
		return nil, output.Usage(fmt.Sprintf("invalid --filesystem %q", c.Filesystem), "Use a folder path with no single quote.")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, output.Usage(fmt.Sprintf("--filesystem %s is not a folder", c.Filesystem), "Create the folder first, or give the path of an existing one.")
	}
	c.Filesystem = dir
	// The folder is mounted read-only.
	return map[string]string{
		"CONNECTOR_HOST_PATH": "'" + dir + "'",
		readersKey:            strings.Join(readers, ","),
	}, nil
}
