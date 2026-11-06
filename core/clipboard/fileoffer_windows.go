package clipboard

import (
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// FileItem describes an entry on the wire. Its position in the manifest is
// the item ID. Absolute paths remain in FileOffer on the originating endpoint.
type FileItem struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Directory bool   `json:"directory,omitempty"`
}

type FileOffer struct {
	items   []FileItem
	sources []fileSource
}

type fileSource struct {
	path string
	info fs.FileInfo
}

// NewFileOffer inventories the selected files without reading their contents.
func NewFileOffer(paths []string, maxBytes int64) (*FileOffer, error) {
	offer := &FileOffer{}
	names := make(map[string]bool)
	var total int64
	for _, root := range paths {
		root = filepath.Clean(root)
		if !filepath.IsAbs(root) || filepath.Dir(root) == root {
			return nil, fmt.Errorf("invalid clipboard root %q", root)
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !offerFileType(info) {
				return fmt.Errorf("unsupported clipboard entry %q", path)
			}
			name, err := filepath.Rel(filepath.Dir(root), path)
			if err != nil {
				return err
			}
			key := strings.ToUpper(name)
			if names[key] || len(offer.items) >= maxFileDropPaths {
				return fmt.Errorf("duplicate or excessive clipboard entries")
			}
			names[key] = true
			item := FileItem{Name: name, Directory: info.IsDir()}
			if !item.Directory {
				item.Size = info.Size()
				if item.Size < 0 || item.Size > maxBytes-total {
					return fmt.Errorf("clipboard transfer exceeds size limit")
				}
				total += item.Size
			}
			offer.items = append(offer.items, item)
			offer.sources = append(offer.sources, fileSource{path: path, info: info})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return offer, nil
}

func offerFileType(info fs.FileInfo) bool {
	attrs, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && attrs.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT == 0 &&
		(info.IsDir() || info.Mode().IsRegular())
}

func (offer *FileOffer) Items() []FileItem {
	return append([]FileItem(nil), offer.items...)
}

// Open verifies the opened handle against the original selection. Callers own
// the handle and must enforce the advertised length during transfer.
func (offer *FileOffer) Open(index int) (*os.File, error) {
	if index < 0 || index >= len(offer.items) || offer.items[index].Directory {
		return nil, fmt.Errorf("invalid clipboard file item")
	}
	source := offer.sources[index]
	file, err := os.Open(source.path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !offerFileType(info) || !os.SameFile(source.info, info) ||
		source.info.Size() != info.Size() || !source.info.ModTime().Equal(info.ModTime()) {
		file.Close()
		return nil, fmt.Errorf("clipboard file changed after copying")
	}
	return file, nil
}

// ValidateFileItems checks the untrusted manifest before it is given to the
// Windows Shell. Each directory must appear before its children.
func ValidateFileItems(items []FileItem, maxBytes int64) error {
	if len(items) == 0 || len(items) > maxFileDropPaths || maxBytes < 0 {
		return fmt.Errorf("invalid clipboard manifest size")
	}
	dirs := make(map[string]bool)
	seen := make(map[string]bool)
	for _, item := range items {
		wide, err := syscall.UTF16FromString(item.Name)
		if err != nil || len(wide) > 260 {
			return fmt.Errorf("invalid clipboard item name")
		}
		for _, component := range strings.Split(item.Name, "\\") {
			if component == "" || strings.ContainsAny(component, "/:*?<>|\"") ||
				strings.TrimRight(component, " .") != component || component == "." || component == ".." || !filepath.IsLocal(component) {
				return fmt.Errorf("invalid clipboard item path")
			}
			for _, r := range component {
				if r < 32 {
					return fmt.Errorf("invalid clipboard item path")
				}
			}
		}
		key := strings.ToUpper(item.Name)
		parent := filepath.Dir(key)
		if seen[key] || parent != "." && !dirs[parent] ||
			item.Size < 0 || item.Size > maxBytes || item.Directory && item.Size != 0 {
			return fmt.Errorf("invalid clipboard item metadata")
		}
		seen[key] = true
		dirs[key] = item.Directory
		maxBytes -= item.Size
	}
	return nil
}

// ClipboardManifest carries relative names only. The offer ID is scoped to a
// single peer and changes each time that peer advertises a new selection.
type ClipboardManifest struct {
	ID    string     `json:"id"`
	Items []FileItem `json:"items"`
}

func (m ClipboardManifest) Validate(limit int64) error {
	id, err := hex.DecodeString(m.ID)
	if err != nil || len(id) != 16 {
		return fmt.Errorf("檔案剪貼簿識別碼無效")
	}
	return ValidateFileItems(m.Items, limit)
}
